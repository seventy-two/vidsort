package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, Config{MPV: "mpv.exe", FFmpeg: "ffmpeg.exe"}, c)
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte("\xef\xbb\xbf"+`{"mpv": "C:\\mpv\\mpv.exe", "minSizeMB": 50}`), 0o644))
	c, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, Config{MPV: `C:\mpv\mpv.exe`, MinSizeMB: 50, FFmpeg: "ffmpeg.exe"}, c)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{`), 0o644))
	_, err = Load(dir)
	assert.Error(t, err)
}
