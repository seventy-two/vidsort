package review

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seventy-two/vidsort/scan"
)

func paths(q *Queue) []string {
	var out []string
	for _, v := range q.Items {
		out = append(out, v.Path)
	}
	return out
}

func cur(q *Queue) string {
	v, _ := q.Current()
	return v.Path
}

func sample() *Queue {
	return New([]scan.Video{
		{Path: "c", Size: 10},
		{Path: "a", Size: 30},
		{Path: "b", Size: 20},
	}, scan.BySize)
}

func TestNewSortsLargestFirst(t *testing.T) {
	q := sample()
	assert.Equal(t, []string{"a", "b", "c"}, paths(q))
	assert.Equal(t, "a", cur(q))
	assert.Equal(t, int64(60), q.TotalSize())
}

func TestKeepDeleteAndUndo(t *testing.T) {
	q := sample()

	v, ok := q.Keep()
	assert.True(t, ok)
	assert.Equal(t, "a", v.Path)
	assert.Equal(t, "b", cur(q))

	q.Skip() // now on c
	v, _ = q.Take()
	assert.Equal(t, "c", v.Path)
	assert.Equal(t, "b", cur(q), "deleting the last item moves back to the new last")

	v, ok = q.Undo()
	assert.True(t, ok)
	assert.Equal(t, "a", v.Path)
	assert.Equal(t, []string{"a", "b"}, paths(q))
	assert.Equal(t, "a", cur(q), "undo makes the restored video current")

	_, ok = q.Undo()
	assert.False(t, ok, "deletes can't be undone")
}

func TestEmptyQueue(t *testing.T) {
	q := New(nil, scan.BySize)
	_, ok := q.Current()
	assert.False(t, ok)
	_, ok = q.Keep()
	assert.False(t, ok)
	q.Skip()

	q = New([]scan.Video{{Path: "x"}}, scan.BySize)
	q.Take()
	_, ok = q.Current()
	assert.False(t, ok)
}

func TestSkipWraps(t *testing.T) {
	q := sample()
	q.Select(2)
	q.Skip()
	assert.Equal(t, "a", cur(q))
	q.Select(9) // out of range: ignored
	assert.Equal(t, "a", cur(q))
}

func TestResortKeepsCurrentAndUndo(t *testing.T) {
	q := sample()
	q.Keep()
	q.Select(1) // c
	q.Resort(scan.ByName)
	assert.Equal(t, []string{"b", "c"}, paths(q))
	assert.Equal(t, "c", cur(q))

	v, ok := q.Undo()
	assert.True(t, ok)
	assert.Equal(t, "a", v.Path)
	assert.Equal(t, []string{"a", "b", "c"}, paths(q), "undo puts the video back in the new order")
	assert.Equal(t, "a", cur(q))
}

func TestSize(t *testing.T) {
	assert.Equal(t, "512 B", Size(512))
	assert.Equal(t, "1.5 KB", Size(1536))
	assert.Equal(t, "4.2 GB", Size(4509715660))
	assert.Equal(t, "700 MB", Size(700*1024*1024))
	assert.Equal(t, "2048 TB", Size(2048*1024*1024*1024*1024))
}

func TestClock(t *testing.T) {
	assert.Equal(t, "0:00", Clock(-1))
	assert.Equal(t, "1:05", Clock(65.9))
	assert.Equal(t, "1:01:01", Clock(3661))
}
