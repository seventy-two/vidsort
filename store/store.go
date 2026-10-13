// Package store remembers which videos the user chose to keep, so they
// don't come up again, logs what was deleted, and remembers the folders
// last looked through.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seventy-two/vidsort/scan"
)

// Kept is the saved record of one kept file. A file only counts as kept
// while its size is unchanged.
type Kept struct {
	Size int64     `json:"size"`
	At   time.Time `json:"at"`
}

// Store is the decisions file plus the deletion log beside it.
type Store struct {
	path string
	Kept map[string]Kept `json:"kept"` // keyed by lower-case path
	// Freed is the total size of everything deleted so far.
	Freed int64 `json:"freed"`
	// Folders are the folders picked last time, where the picker starts.
	Folders []string `json:"folders,omitempty"`
}

// Open loads dir/decisions.json, starting empty if it doesn't exist yet.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "decisions.json"), Kept: map[string]Kept{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.path, err)
	}
	if s.Kept == nil {
		s.Kept = map[string]Kept{}
	}
	return s, nil
}

// key folds case, since Windows paths are case-insensitive.
func key(path string) string { return strings.ToLower(path) }

// IsKept reports whether v was kept before and hasn't changed size since.
func (s *Store) IsKept(v scan.Video) bool {
	k, ok := s.Kept[key(v.Path)]
	return ok && k.Size == v.Size
}

// Keep records v as kept and saves.
func (s *Store) Keep(v scan.Video, now time.Time) error {
	s.Kept[key(v.Path)] = Kept{Size: v.Size, At: now}
	return s.save()
}

// Unkeep forgets that v was kept (undo) and saves.
func (s *Store) Unkeep(v scan.Video) error {
	delete(s.Kept, key(v.Path))
	return s.save()
}

// Deleted adds v to the running total and the deletion log, and saves.
func (s *Store) Deleted(v scan.Video, now time.Time) error {
	s.Freed += v.Size
	f, err := os.OpenFile(filepath.Join(filepath.Dir(s.path), "deleted.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s\t%d\t%s\r\n", now.Format(time.RFC3339), v.Size, v.Path)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return s.save()
}

// SetFolders remembers the folders being looked through and saves.
func (s *Store) SetFolders(folders []string) error {
	s.Folders = folders
	return s.save()
}

// save writes the file atomically: a temporary file, then a rename.
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
