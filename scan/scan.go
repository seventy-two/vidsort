// Package scan finds video files under a set of folders and sorts them.
package scan

import (
	"cmp"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Video is one file found by Walk.
type Video struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// Name returns the file's base name.
func (v Video) Name() string { return filepath.Base(v.Path) }

// Dir returns the folder holding the file.
func (v Video) Dir() string { return filepath.Dir(v.Path) }

// Extensions are the file types treated as video, lower case with the dot.
var Extensions = map[string]bool{
	".3gp": true, ".asf": true, ".avi": true, ".divx": true, ".f4v": true,
	".flv": true, ".m2ts": true, ".m4v": true, ".mkv": true, ".mov": true,
	".mp4": true, ".mpeg": true, ".mpg": true, ".mts": true, ".ogv": true,
	".rm": true, ".rmvb": true, ".ts": true, ".vob": true, ".webm": true,
	".wmv": true,
}

// IsVideo reports whether name has a video extension.
func IsVideo(name string) bool {
	return Extensions[strings.ToLower(filepath.Ext(name))]
}

// skipDirs are Windows system folders that never hold the user's videos.
var skipDirs = map[string]bool{
	"$recycle.bin":              true,
	"system volume information": true,
	"windowsapps":               true,
	"$windows.~bt":              true,
	"$windows.~ws":              true,
}

// Walk returns every video under roots. Folders it can't read are skipped;
// their errors are passed to onErr (which may be nil). progress, if not nil,
// is called with the running count every 500 files.
func Walk(roots []string, onErr func(path string, err error), progress func(found int)) []Video {
	var out []Video
	for _, root := range roots {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if onErr != nil {
					onErr(path, err)
				}
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if path != root && skipDirs[strings.ToLower(d.Name())] {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !IsVideo(d.Name()) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				if onErr != nil {
					onErr(path, err)
				}
				return nil
			}
			out = append(out, Video{Path: path, Size: info.Size(), ModTime: info.ModTime()})
			if progress != nil && len(out)%500 == 0 {
				progress(len(out))
			}
			return nil
		})
	}
	return out
}

// Order is how a list of videos is sorted.
type Order int

const (
	BySize   Order = iota // largest first
	ByName                // A to Z, ignoring case
	ByDate                // newest first
	ByFolder              // by folder, then name
)

// Sort sorts vs in place. Ties fall back to the path so the order is stable.
func Sort(vs []Video, o Order) {
	slices.SortStableFunc(vs, func(a, b Video) int {
		var c int
		switch o {
		case BySize:
			c = cmp.Compare(b.Size, a.Size)
		case ByName:
			c = cmp.Compare(strings.ToLower(a.Name()), strings.ToLower(b.Name()))
		case ByDate:
			c = b.ModTime.Compare(a.ModTime)
		case ByFolder:
			c = cmp.Compare(strings.ToLower(a.Dir()), strings.ToLower(b.Dir()))
			if c == 0 {
				c = cmp.Compare(strings.ToLower(a.Name()), strings.ToLower(b.Name()))
			}
		}
		if c == 0 {
			c = cmp.Compare(a.Path, b.Path)
		}
		return c
	})
}
