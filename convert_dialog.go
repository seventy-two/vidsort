//go:build windows

package main

import (
	"context"
	"fmt"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"

	"github.com/seventy-two/vidsort/av1"
	"github.com/seventy-two/vidsort/review"
	"github.com/seventy-two/vidsort/scan"
)

// worthIt is the smallest saving worth converting for: below it an option
// is marked as not worth it, and at zero or less it can't be chosen.
const worthIt = 0.10

const optCancel = -1 // hover/choice index for the Cancel button

// convertDialog estimates what converting one video to AV1 would save at
// each quality level, and lets the user pick one (or cancel).
type convertDialog struct {
	a     *app
	v     scan.Video
	wnd   *ui.Modal
	fonts uiFonts

	info   av1.Info
	probed bool
	note   string  // what's happening, or what went wrong
	failed bool    // note is an error
	done   float64 // estimate progress, 0–1
	est    []av1.Estimate
	hover  int // option under the mouse, optCancel, or -2 for none
	chosen int // option picked, or optCancel
	cancel context.CancelFunc
	closed bool
}

// chooseConversion shows the dialog for v. It returns the estimate picked
// and the file's details, or ok false if cancelled.
func (a *app) chooseConversion(v scan.Video) (est av1.Estimate, info av1.Info, ok bool) {
	d := &convertDialog{a: a, v: v, hover: -2, chosen: optCancel, note: "Reading the file…"}
	d.wnd = ui.NewModal(a.wnd, ui.OptsModal().
		Title("Convert to AV1").
		Size(ui.Dpi(560, 344)).
		ClassBrush(solidBrush(colBg)).
		// No dialog keyboard handling: it would swallow the 1–3 and Esc
		// keys before they reach the key catcher below.
		ProcessDlgMsgs(false))

	// windigo gives the keyboard focus to a window's first child control,
	// and with none nothing has focus and keys arrive as system keys. So an
	// invisible control, just outside the client area, takes the focus and
	// the keys.
	keys := ui.NewControl(d.wnd, ui.OptsControl().
		Position(-10, -10).Size(1, 1).
		ExStyle(co.WS_EX_LEFT))
	d.wnd.On().WmCreate(func(_ ui.WmCreate) int {
		darkTitleBar(d.wnd.Hwnd())
		d.fonts = newFonts()
		ctx, cancel := context.WithCancel(context.Background())
		d.cancel = cancel
		go d.estimate(ctx)
		return 0
	})
	d.wnd.On().WmDestroy(func() {
		d.closed = true
		d.cancel() // stops any sampling still running
		d.fonts.release()
	})
	d.wnd.On().WmEraseBkgnd(func(ui.WmEraseBkgnd) int { return 1 })
	d.wnd.On().WmPaint(func() { paintBuffered(d.wnd.Hwnd(), d.fonts.text, d.draw) })
	d.wnd.On().WmMouseMove(func(m ui.WmMouse) {
		if h := d.hit(m.Pos()); h != d.hover {
			d.hover = h
			d.redraw()
		}
	})
	d.wnd.On().WmLButtonUp(func(m ui.WmMouse) { d.pick(d.hit(m.Pos())) })
	keys.On().WmKeyDown(func(k ui.WmKey) {
		switch vk := k.VirtualKeyCode(); {
		case vk == co.VK_ESCAPE:
			d.pick(optCancel)
		case vk >= '1' && vk < co.VK('1'+len(av1.Levels)):
			d.pick(int(vk - '1'))
		}
	})

	d.wnd.ShowModal()
	if d.chosen < 0 {
		return av1.Estimate{}, av1.Info{}, false
	}
	return d.est[d.chosen], d.info, true
}

// estimate probes the file and samples it, off the UI thread.
func (d *convertDialog) estimate(ctx context.Context) {
	tools := d.a.tools
	info, err := tools.Probe(ctx, d.v.Path)
	d.ui(func() {
		d.info, d.probed = info, err == nil
		if err == nil {
			d.note = "Starting…"
		}
	})
	switch {
	case err != nil:
		d.fail(err)
		return
	case info.VideoCodec == "av1":
		d.ui(func() { d.note, d.failed = "This file is already AV1.", true })
		return
	}

	est, err := tools.Estimate(ctx, info, d.v.Path, func(done float64, note string) {
		d.ui(func() { d.done, d.note = done, note })
	})
	if err != nil {
		if ctx.Err() == nil {
			d.fail(err)
		}
		return
	}
	d.ui(func() {
		d.est, d.done = est, 1
		samples := "3 short samples"
		if len(av1.SamplePoints(info.Duration)) == 1 {
			samples = "a short sample"
		}
		d.note = "Estimated by encoding " + samples + " on the GPU. Time is rough."
	})
}

func (d *convertDialog) fail(err error) {
	d.ui(func() { d.note, d.failed = "Couldn't estimate: "+firstLine(err.Error()), true })
}

// ui runs f on the UI thread and redraws, unless the dialog has closed.
func (d *convertDialog) ui(f func()) {
	d.wnd.UiThread(func() {
		if !d.closed {
			f()
			d.redraw()
		}
	})
}

func (d *convertDialog) redraw() { d.wnd.Hwnd().InvalidateRect(nil, false) }

// pick closes the dialog with option i chosen, if it can be.
func (d *convertDialog) pick(i int) {
	if i != optCancel && !d.choosable(i) {
		return
	}
	d.chosen = i
	d.wnd.Hwnd().SendMessage(co.WM_CLOSE, 0, 0)
}

func (d *convertDialog) choosable(i int) bool {
	return i >= 0 && i < len(d.est) && d.est[i].Saving(d.v.Size) > 0
}

// dialogLayout is where things go in the dialog.
type dialogLayout struct {
	name, details, note, progress win.RECT
	options                       []win.RECT
	cancel                        win.RECT
}

func (d *convertDialog) layout() dialogLayout {
	rc, _ := d.wnd.Hwnd().GetClientRect()
	w := int(rc.Right)
	m := ui.DpiX(16)
	l := dialogLayout{
		name:     rect(m, ui.DpiY(14), w-2*m, ui.DpiY(22)),
		details:  rect(m, ui.DpiY(38), w-2*m, ui.DpiY(20)),
		note:     rect(m, ui.DpiY(64), w-2*m, ui.DpiY(20)),
		progress: rect(m, ui.DpiY(88), w-2*m, ui.DpiY(8)),
		cancel:   rect(w-m-ui.DpiX(110), ui.DpiY(298), ui.DpiX(110), ui.DpiY(32)),
	}
	for n := range av1.Levels {
		l.options = append(l.options, rect(m, ui.DpiY(104+n*62), w-2*m, ui.DpiY(56)))
	}
	return l
}

func (d *convertDialog) hit(pt win.POINT) int {
	l := d.layout()
	if inside(pt, l.cancel) {
		return optCancel
	}
	for n, rc := range l.options {
		if inside(pt, rc) {
			return n
		}
	}
	return -2
}

func (d *convertDialog) draw(hdc win.HDC, rc win.RECT) {
	l := d.layout()
	fill(hdc, rc, colBg)
	hdc.SetBkMode(co.BKMODE_TRANSPARENT)
	pad := int32(ui.DpiX(14))

	text(hdc, d.fonts.bold, d.v.Name(), l.name, colText, co.DT_LEFT|co.DT_END_ELLIPSIS)
	details := review.Size(d.v.Size)
	if d.probed {
		details += "  ·  " + d.info.Describe() + "  ·  " + review.Clock(d.info.Duration)
	}
	text(hdc, d.fonts.text, details, l.details, colDim, co.DT_LEFT|co.DT_END_ELLIPSIS)
	noteCol := colDim
	if d.failed {
		noteCol = colDeleteHot
	}
	text(hdc, d.fonts.text, d.note, l.note, noteCol, co.DT_LEFT|co.DT_END_ELLIPSIS)
	if d.est == nil && !d.failed {
		bar(hdc, l.progress, d.done, false)
	}

	for n, lvl := range av1.Levels {
		box := l.options[n]
		ok := d.choosable(n)
		bg := colButton
		if ok && d.hover == n {
			bg = colButtonHot
		}
		roundRect(hdc, box, bg, ui.DpiX(6))

		left, right := box, box
		left.Left += pad
		right.Right -= pad
		top, bottom := halves(left)
		nameCol := colText
		if d.est != nil && !ok {
			nameCol = colDisabled
		}
		text(hdc, d.fonts.bold, fmt.Sprintf("%d   %s", n+1, lvl.Name), top, nameCol, co.DT_LEFT)
		text(hdc, d.fonts.text, fmt.Sprintf("     CQ %d", lvl.CQ), bottom, colDim, co.DT_LEFT)

		if d.est == nil {
			continue
		}
		e := d.est[n]
		saving := e.Saving(d.v.Size)
		top, bottom = halves(right)
		sizeCol, line := colText, ""
		switch {
		case saving <= 0:
			sizeCol = colDeleteHot
			line = fmt.Sprintf("would grow by %s: no use", review.Size(e.Size-d.v.Size))
		case saving < worthIt:
			sizeCol = colDim
			line = fmt.Sprintf("saves only %s (%.0f%%): not worth it", review.Size(d.v.Size-e.Size), saving*100)
		default:
			line = fmt.Sprintf("saves %s (%.0f%%)  ·  about %s", review.Size(d.v.Size-e.Size), saving*100, av1.FormatDuration(e.Time))
		}
		text(hdc, d.fonts.bold, "≈ "+review.Size(e.Size), top, sizeCol, co.DT_RIGHT)
		text(hdc, d.fonts.text, line, bottom, colDim, co.DT_RIGHT)
	}

	bg := colButton
	if d.hover == optCancel {
		bg = colButtonHot
	}
	roundRect(hdc, l.cancel, bg, ui.DpiX(6))
	text(hdc, d.fonts.bold, "Cancel  (Esc)", l.cancel, colText, co.DT_CENTER)
	hint := l.cancel
	hint.Left, hint.Right = l.options[0].Left, l.cancel.Left-pad
	wrapText(hdc, d.fonts.text, "Converts in the background. The original is replaced only once the new file checks out.",
		hint, colDim)
}

// halves splits rc into top and bottom halves, a little in from the edges.
func halves(rc win.RECT) (top, bottom win.RECT) {
	mid := (rc.Top + rc.Bottom) / 2
	inset := int32(ui.DpiY(5))
	top, bottom = rc, rc
	top.Top, top.Bottom = rc.Top+inset, mid
	bottom.Top, bottom.Bottom = mid, rc.Bottom-inset
	return top, bottom
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
