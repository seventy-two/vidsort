package scan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, path string, size int, mod time.Time) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, size), 0o644))
	require.NoError(t, os.Chtimes(path, mod, mod))
}

func names(vs []Video) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Name())
	}
	return out
}

func TestIsVideo(t *testing.T) {
	assert.True(t, IsVideo("a.mp4"))
	assert.True(t, IsVideo(`D:\x\Holiday.MKV`))
	assert.False(t, IsVideo("notes.txt"))
	assert.False(t, IsVideo("mp4"))
}

func TestWalk(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	write(t, filepath.Join(a, "big.mkv"), 300, day)
	write(t, filepath.Join(a, "sub", "small.MP4"), 10, day)
	write(t, filepath.Join(a, "readme.txt"), 999, day)
	write(t, filepath.Join(a, "$RECYCLE.BIN", "gone.mp4"), 999, day)
	write(t, filepath.Join(b, "System Volume Information", "x.avi"), 999, day)
	write(t, filepath.Join(b, "mid.avi"), 100, day)

	var counts []int
	vs := Walk([]string{a, b, filepath.Join(a, "missing")}, nil, func(n int) { counts = append(counts, n) })
	Sort(vs, BySize)

	assert.Equal(t, []string{"big.mkv", "mid.avi", "small.MP4"}, names(vs))
	assert.Equal(t, int64(300), vs[0].Size)
	assert.Empty(t, counts, "progress is only reported every 500 files")
}

func TestWalkReportsErrors(t *testing.T) {
	var bad []string
	Walk([]string{filepath.Join(t.TempDir(), "missing")}, func(p string, err error) { bad = append(bad, p) }, nil)
	assert.Len(t, bad, 1)
}

func TestSort(t *testing.T) {
	d1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	vs := []Video{
		{Path: filepath.Join("b", "zed.mp4"), Size: 5, ModTime: d1},
		{Path: filepath.Join("a", "Apple.mp4"), Size: 50, ModTime: d1.Add(time.Hour)},
		{Path: filepath.Join("a", "mango.mp4"), Size: 5, ModTime: d1.Add(-time.Hour)},
	}

	Sort(vs, BySize)
	assert.Equal(t, []string{"Apple.mp4", "mango.mp4", "zed.mp4"}, names(vs), "ties sort by path")

	Sort(vs, ByName)
	assert.Equal(t, []string{"Apple.mp4", "mango.mp4", "zed.mp4"}, names(vs))

	Sort(vs, ByDate)
	assert.Equal(t, []string{"Apple.mp4", "zed.mp4", "mango.mp4"}, names(vs))

	Sort(vs, ByFolder)
	assert.Equal(t, []string{"Apple.mp4", "mango.mp4", "zed.mp4"}, names(vs))
}
