//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/seventy-two/vidsort/av1"
	"github.com/seventy-two/vidsort/review"
	"github.com/seventy-two/vidsort/scan"
)

// job is one queued AV1 conversion. They run one at a time, as they all
// share the GPU.
//
// The goroutine running it never calls into the UI (windigo's UiThread
// waits for the UI thread, which would deadlock with stopJobs). It records
// progress and the result here instead, and the UI's tick picks them up.
type job struct {
	v     scan.Video
	info  av1.Info
	level av1.Level

	cancel  context.CancelFunc
	stopped chan struct{} // closed once ffmpeg has exited and cleaned up

	mu       sync.Mutex
	done     float64 // 0–1
	note     string  // time left
	finished bool
	res      av1.Result
	err      error
}

func (j *job) progress() (done float64, note string, finished bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done, j.note, j.finished
}

// convert asks how to convert the current video and, if a level is
// picked, takes it out of review and queues it.
func (a *app) convert() {
	v, ok := a.Current()
	if !ok {
		return
	}
	if a.tools.FFmpeg == "" {
		a.message("Can't find ffmpeg: set \"ffmpeg\" in config.json to the full path of ffmpeg.exe")
		return
	}
	// Take the keyboard back from mpv's window (another process), or the
	// dialog's 1–3 keys would go to mpv and seek instead.
	a.list.Focus()
	est, info, ok := a.chooseConversion(v)
	if !ok {
		return
	}
	if cur, ok := a.Current(); !ok || cur.Path != v.Path {
		return // moved on while the dialog was open (a key pressed in mpv)
	}
	a.q.Take()
	a.fillList()
	a.showCurrent()
	a.jobs = append(a.jobs, &job{v: v, info: info, level: est.Level})
	a.message(fmt.Sprintf("Queued %s for AV1 (%s, about %s)", v.Name(), est.Name, review.Size(est.Size)))
	if len(a.jobs) == 1 {
		a.startJob()
	}
}

// startJob runs the first queued conversion.
func (a *app) startJob() {
	j := a.jobs[0]
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel, j.stopped = cancel, make(chan struct{})
	go func() {
		defer close(j.stopped)
		res, err := a.tools.Convert(ctx, j.info, j.v.Path, j.level.CQ, func(done float64, note string) {
			j.mu.Lock()
			j.done, j.note = done, note
			j.mu.Unlock()
		})
		j.mu.Lock()
		j.finished, j.res, j.err = true, res, err
		j.mu.Unlock()
	}()
	a.panel.redraw()
}

// checkJobs runs on the UI's tick: it finishes the current job when its
// goroutine has, and reports whether there's progress to draw.
func (a *app) checkJobs() bool {
	if len(a.jobs) == 0 {
		return false
	}
	if _, _, finished := a.jobs[0].progress(); finished {
		a.jobFinished()
	}
	return true
}

func (a *app) jobFinished() {
	j := a.jobs[0]
	a.jobs = a.jobs[1:]
	res, err := j.res, j.err
	switch {
	case errors.Is(err, context.Canceled):
	case errors.Is(err, av1.ErrNotSmaller):
		a.message(j.v.Name() + ": " + err.Error())
	case err != nil:
		a.message("Converting " + j.v.Name() + " failed: " + firstLine(err.Error()))
	default:
		now := scan.Video{Path: res.Path, Size: res.NewSize, ModTime: j.v.ModTime}
		if st, err := os.Stat(res.Path); err == nil {
			now.ModTime = st.ModTime()
		}
		if err := a.st.Converted(j.v, now, time.Now()); err != nil {
			a.message("Converted, but couldn't save: " + err.Error())
		} else {
			a.message(fmt.Sprintf("Converted %s: %s → %s, saved %s", j.v.Name(),
				review.Size(res.OldSize), review.Size(res.NewSize), review.Size(res.OldSize-res.NewSize)))
		}
	}
	if len(a.jobs) > 0 {
		a.startJob()
	}
	a.panel.redraw()
}

// jobStatus is the panel's line about conversions, empty if there are none.
func (a *app) jobStatus() (string, float64) {
	if len(a.jobs) == 0 {
		return "", 0
	}
	j := a.jobs[0]
	done, note, _ := j.progress()
	s := fmt.Sprintf("AV1 (%s): %s  ·  %.0f%%", j.level.Name, j.v.Name(), done*100)
	if note != "" {
		s += "  ·  " + note
	}
	if n := len(a.jobs) - 1; n > 0 {
		s += fmt.Sprintf("  ·  %d more queued", n)
	}
	return s, done
}

// stopJobs cancels the running conversion and waits for ffmpeg to exit
// and its partial file to go, so nothing is left half-written.
func (a *app) stopJobs() {
	if len(a.jobs) == 0 || a.jobs[0].cancel == nil {
		return
	}
	j := a.jobs[0]
	j.cancel()
	select {
	case <-j.stopped:
	case <-time.After(10 * time.Second):
	}
	a.jobs = nil
}
