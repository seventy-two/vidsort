// Package review holds the list of videos still to decide on and the
// position in it, separate from the window so it can be tested.
package review

import (
	"fmt"
	"slices"

	"github.com/seventy-two/vidsort/scan"
)

// Queue is the videos left to review, in display order, and the current one.
type Queue struct {
	Items []scan.Video
	Cur   int // index into Items; 0 when Items is empty
	Order scan.Order

	undo []scan.Video // kept videos, most recent last
}

// New sorts videos by order and starts at the first.
func New(videos []scan.Video, order scan.Order) *Queue {
	q := &Queue{Items: videos, Order: order}
	scan.Sort(q.Items, order)
	return q
}

// Current returns the video under review, if any are left.
func (q *Queue) Current() (scan.Video, bool) {
	if q.Cur < 0 || q.Cur >= len(q.Items) {
		return scan.Video{}, false
	}
	return q.Items[q.Cur], true
}

// Select makes index i current, if it's in range.
func (q *Queue) Select(i int) {
	if i >= 0 && i < len(q.Items) {
		q.Cur = i
	}
}

// Skip moves on without deciding, wrapping round to the start.
func (q *Queue) Skip() {
	if len(q.Items) > 0 {
		q.Cur = (q.Cur + 1) % len(q.Items)
	}
}

// remove takes the current video out of the list; the next one becomes
// current (or the new last one, at the end).
func (q *Queue) remove() (scan.Video, bool) {
	v, ok := q.Current()
	if !ok {
		return v, false
	}
	q.Items = slices.Delete(q.Items, q.Cur, q.Cur+1)
	if q.Cur >= len(q.Items) && q.Cur > 0 {
		q.Cur = len(q.Items) - 1
	}
	return v, true
}

// Keep removes the current video and remembers it for Undo.
func (q *Queue) Keep() (scan.Video, bool) {
	v, ok := q.remove()
	if ok {
		q.undo = append(q.undo, v)
	}
	return v, ok
}

// Take removes the current video for good, because it's being deleted or
// converted. It can't be undone.
func (q *Queue) Take() (scan.Video, bool) {
	return q.remove()
}

// Undo puts the most recently kept video back in its place in the current
// order and makes it current. Only keeps can be undone; deleted files are gone.
func (q *Queue) Undo() (scan.Video, bool) {
	if len(q.undo) == 0 {
		return scan.Video{}, false
	}
	v := q.undo[len(q.undo)-1]
	q.undo = q.undo[:len(q.undo)-1]
	q.Items = append(q.Items, v)
	q.Resort(q.Order)
	q.Cur = q.index(v.Path)
	return v, true
}

func (q *Queue) index(path string) int {
	return slices.IndexFunc(q.Items, func(v scan.Video) bool { return v.Path == path })
}

// CanUndo reports whether there's a keep to undo.
func (q *Queue) CanUndo() bool { return len(q.undo) > 0 }

// Resort changes the order, keeping the same video current.
func (q *Queue) Resort(order scan.Order) {
	cur, ok := q.Current()
	q.Order = order
	scan.Sort(q.Items, order)
	q.Cur = 0
	if ok {
		q.Cur = q.index(cur.Path)
	}
}

// TotalSize is the combined size of the videos left.
func (q *Queue) TotalSize() int64 {
	var n int64
	for _, v := range q.Items {
		n += v.Size
	}
	return n
}

// Size formats a byte count in binary units, as Explorer does: "4.2 GB".
func Size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		f /= unit
		if f < unit || u == "TB" {
			if f < 10 {
				return fmt.Sprintf("%.1f %s", f, u)
			}
			return fmt.Sprintf("%.0f %s", f, u)
		}
	}
	panic("unreachable")
}

// Clock formats seconds as m:ss, or h:mm:ss from an hour up.
func Clock(secs float64) string {
	if secs < 0 {
		secs = 0
	}
	s := int(secs)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
