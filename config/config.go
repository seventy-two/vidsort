// Package config reads vidsort's settings file.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Config is %APPDATA%\vidsort\config.json. Command-line arguments override it.
type Config struct {
	// MPV is the path to mpv.exe; a bare name is looked up on PATH.
	MPV string `json:"mpv"`
	// MinSizeMB hides files smaller than this many megabytes.
	MinSizeMB int64 `json:"minSizeMB"`
	// FFmpeg is the path to ffmpeg.exe, used to convert to AV1; ffprobe.exe
	// is expected beside it. A bare name is looked up on PATH.
	FFmpeg string `json:"ffmpeg"`
}

// Load reads dir/config.json. A missing file gives the defaults.
func Load(dir string) (Config, error) {
	c := Config{MPV: "mpv.exe", FFmpeg: "ffmpeg.exe"}
	path := filepath.Join(dir, "config.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	// Notepad and PowerShell 5 may start the file with a UTF-8 byte order mark.
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("reading %s: %w", path, err)
	}
	return c, nil
}
