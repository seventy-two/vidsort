package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seventy-two/vidsort/scan"
)

func TestKeepPersists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vidsort")
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	v := scan.Video{Path: `D:\Films\Big.mkv`, Size: 1000}

	s, err := Open(dir)
	require.NoError(t, err)
	assert.False(t, s.IsKept(v))
	require.NoError(t, s.Keep(v, now))

	s, err = Open(dir)
	require.NoError(t, err)
	assert.True(t, s.IsKept(v))
	assert.True(t, s.IsKept(scan.Video{Path: `d:\films\big.MKV`, Size: 1000}), "paths match without case")
	assert.False(t, s.IsKept(scan.Video{Path: v.Path, Size: 999}), "a changed file comes up again")

	require.NoError(t, s.Unkeep(v))
	s, err = Open(dir)
	require.NoError(t, err)
	assert.False(t, s.IsKept(v))
}

func TestDeleted(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	s, err := Open(dir)
	require.NoError(t, err)
	require.NoError(t, s.Deleted(scan.Video{Path: `E:\a.mp4`, Size: 300}, now))
	require.NoError(t, s.Deleted(scan.Video{Path: `E:\b.mp4`, Size: 200}, now))

	s, err = Open(dir)
	require.NoError(t, err)
	assert.Equal(t, int64(500), s.Freed)
	log, err := os.ReadFile(filepath.Join(dir, "deleted.log"))
	require.NoError(t, err)
	assert.Equal(t, "2026-10-13T12:00:00Z\t300\tE:\\a.mp4\r\n2026-10-13T12:00:00Z\t200\tE:\\b.mp4\r\n", string(log))
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "decisions.json"), []byte("{nope"), 0o644))
	_, err := Open(dir)
	assert.Error(t, err)
}
