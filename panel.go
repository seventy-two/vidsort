//go:build windows

package main

import (
	"fmt"
	"time"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"

	"github.com/seventy-two/vidsort/review"
)

// panelHeight is the height of the controls under the video (before DPI scaling).
const panelHeight = 142

const wmMouseWheel co.WM = 0x020a

// target is a clickable part of the panel.
type target int

const (
	tNone target = iota
	tPlay
	tSeek
	tMute
	tVolume
	tKeep
	tDelete
	tSkip
	tUndo
)

// panelLayout is where everything in the panel goes, for a given width.
type panelLayout struct {
	play, posText, seek, durText, mute, volume win.RECT
	info, hint, status                         win.RECT
	buttons                                    []button
}

type button struct {
	t     target
	label string
	rc    win.RECT
}

func rect(x, y, w, h int) win.RECT {
	return win.RECT{Left: int32(x), Top: int32(y), Right: int32(x + w), Bottom: int32(y + h)}
}

// layoutPanel places the controls in rows: transport (play, times, seek
// bar, volume), the file's details, the decision buttons, then the status.
func layoutPanel(width int) panelLayout {
	d := ui.DpiX
	w := width
	var l panelLayout

	y, h := ui.DpiY(10), ui.DpiY(26)
	l.play = rect(d(8), y, d(30), h)
	l.posText = rect(d(42), y, d(62), h)
	l.volume = rect(w-d(112), y, d(100), h)
	l.mute = rect(w-d(146), y, d(30), h)
	l.durText = rect(w-d(220), y, d(62), h)
	l.seek = rect(d(114), y, w-d(220)-d(10)-d(114), h)

	l.info = rect(d(12), ui.DpiY(44), w-d(24), ui.DpiY(20))

	y, h = ui.DpiY(72), ui.DpiY(34)
	x := d(12)
	for _, b := range []struct {
		t     target
		label string
		w     int
	}{
		{tKeep, "Keep  (K)", 120}, {tDelete, "Delete  (Del)", 120},
		{tSkip, "Skip  (N)", 104}, {tUndo, "Undo keep  (Ctrl+Z)", 160},
	} {
		l.buttons = append(l.buttons, button{b.t, b.label, rect(x, y, d(b.w), h)})
		x += d(b.w) + d(8)
	}
	l.hint = rect(x+d(8), y, w-x-d(20), h)

	l.status = rect(d(12), ui.DpiY(114), w-d(24), ui.DpiY(20))
	return l
}

func inside(pt win.POINT, rc win.RECT) bool {
	return pt.X >= rc.Left && pt.X < rc.Right && pt.Y >= rc.Top && pt.Y < rc.Bottom
}

// fraction is how far along rc the point is, from 0 to 1.
func fraction(pt win.POINT, rc win.RECT) float64 {
	f := float64(pt.X-rc.Left) / float64(rc.Right-rc.Left)
	return min(max(f, 0), 1)
}

// panel is the dark, self-drawn area under the video.
type panel struct {
	a     *app
	ctl   *ui.Control
	fonts uiFonts

	hover, pressed, drag target
	dragFrac             float64 // seek or volume position while dragging
	tracking             bool    // asked for WM_MOUSELEAVE
	lastSeek             time.Time
}

func newPanel(a *app) *panel {
	p := &panel{a: a}
	p.ctl = ui.NewControl(a.wnd, ui.OptsControl().
		Position(0, ui.DpiY(860-panelHeight)).Size(ui.Dpi(966, panelHeight)).
		ExStyle(co.WS_EX_LEFT). // no border
		Layout(ui.LAY_RESIZE_MOVE))

	p.ctl.On().WmCreate(func(_ ui.WmCreate) int { p.fonts = newFonts(); return 0 })
	p.ctl.On().WmDestroy(func() { p.fonts.release() })
	p.ctl.On().WmEraseBkgnd(func(ui.WmEraseBkgnd) int { return 1 }) // all drawn in WmPaint
	p.ctl.On().WmPaint(p.paint)
	p.ctl.On().WmMouseMove(p.mouseMove)
	p.ctl.On().WmMouseLeave(func() {
		p.tracking = false
		p.setHover(tNone)
	})
	p.ctl.On().WmLButtonDown(p.buttonDown)
	p.ctl.On().WmLButtonUp(p.buttonUp)
	p.ctl.On().Wm(wmMouseWheel, func(m ui.Wm) uintptr {
		if delta := int16(m.WParam.HiWord()); delta > 0 {
			p.a.addVolume(5)
		} else if delta < 0 {
			p.a.addVolume(-5)
		}
		return 0
	})
	return p
}

func (p *panel) redraw() { p.ctl.Hwnd().InvalidateRect(nil, false) }

func (p *panel) layout() panelLayout {
	rc, _ := p.ctl.Hwnd().GetClientRect()
	return layoutPanel(int(rc.Right))
}

func (p *panel) hit(pt win.POINT) target {
	l := p.layout()
	switch {
	case inside(pt, l.play):
		return tPlay
	case inside(pt, l.seek):
		return tSeek
	case inside(pt, l.mute):
		return tMute
	case inside(pt, l.volume):
		return tVolume
	}
	for _, b := range l.buttons {
		if inside(pt, b.rc) {
			return b.t
		}
	}
	return tNone
}

func (p *panel) setHover(t target) {
	if t != p.hover {
		p.hover = t
		p.redraw()
	}
}

func (p *panel) mouseMove(m ui.WmMouse) {
	if !p.tracking {
		tme := win.TRACKMOUSEEVENT{DwFlags: co.TME_LEAVE, HwndTrack: p.ctl.Hwnd()}
		tme.SetCbSize()
		win.TrackMouseEvent(&tme)
		p.tracking = true
	}
	pt := m.Pos()
	switch p.drag {
	case tSeek:
		p.dragFrac = fraction(pt, p.layout().seek)
		// Keyframe seeks while dragging, at most every 50ms so they don't queue up.
		if time.Since(p.lastSeek) > 50*time.Millisecond {
			p.lastSeek = time.Now()
			p.a.seekFraction(p.dragFrac, false)
		}
		p.redraw()
	case tVolume:
		p.dragFrac = fraction(pt, p.layout().volume)
		p.a.setVolume(p.dragFrac * 100)
		p.redraw()
	}
	p.setHover(p.hit(pt))
}

func (p *panel) buttonDown(m ui.WmMouse) {
	pt := m.Pos()
	t := p.hit(pt)
	if !p.enabled(t) {
		return
	}
	p.pressed = t
	p.ctl.Hwnd().SetCapture()
	switch t {
	case tSeek:
		p.drag = tSeek
		p.dragFrac = fraction(pt, p.layout().seek)
		p.lastSeek = time.Now()
		p.a.seekFraction(p.dragFrac, false)
	case tVolume:
		p.drag = tVolume
		p.dragFrac = fraction(pt, p.layout().volume)
		p.a.setVolume(p.dragFrac * 100)
	}
	p.redraw()
}

func (p *panel) buttonUp(m ui.WmMouse) {
	if p.pressed == tNone {
		return
	}
	win.ReleaseCapture()
	pt := m.Pos()
	if p.drag == tSeek {
		p.a.seekFraction(fraction(pt, p.layout().seek), true) // exact, where it was let go
	}
	clicked := p.pressed
	p.drag, p.pressed = tNone, tNone
	if p.hit(pt) == clicked {
		switch clicked {
		case tPlay:
			p.a.togglePause()
		case tMute:
			p.a.toggleMute()
		case tKeep:
			p.a.keep()
		case tDelete:
			p.a.delete()
		case tSkip:
			p.a.skip()
		case tUndo:
			p.a.undo()
		}
	}
	p.redraw()
}

// enabled reports whether t can be used right now.
func (p *panel) enabled(t target) bool {
	_, ok := p.a.Current()
	switch t {
	case tNone:
		return false
	case tUndo:
		return p.a.q != nil && p.a.q.CanUndo()
	case tMute, tVolume:
		return true
	default:
		return ok
	}
}

// paint draws into an off-screen bitmap and copies it over in one go, so
// the panel never flickers.
func (p *panel) paint() {
	hwnd := p.ctl.Hwnd()
	var ps win.PAINTSTRUCT
	hdc, _ := hwnd.BeginPaint(&ps)
	defer hwnd.EndPaint(&ps)

	rc, _ := hwnd.GetClientRect()
	size := win.SIZE{Cx: rc.Right, Cy: rc.Bottom}
	mem, _ := hdc.CreateCompatibleDC()
	bmp, _ := hdc.CreateCompatibleBitmap(int(size.Cx), int(size.Cy))
	oldBmp, _ := mem.SelectObjectBmp(bmp)
	oldFont, _ := mem.SelectObjectFont(p.fonts.text)

	p.draw(mem, rc)
	hdc.BitBlt(win.POINT{}, size, mem, win.POINT{}, co.ROP_SRCCOPY)

	mem.SelectObjectFont(oldFont)
	mem.SelectObjectBmp(oldBmp)
	bmp.DeleteObject()
	mem.DeleteDC()
}

func (p *panel) draw(hdc win.HDC, rc win.RECT) {
	a := p.a
	l := layoutPanel(int(rc.Right))
	fill(hdc, rc, colBg)
	hdc.SetBkMode(co.BKMODE_TRANSPARENT)

	st := a.playerState()
	hot := func(t target) bool { return p.hover == t || p.drag == t }
	textCol := func(t target) win.COLORREF {
		switch {
		case !p.enabled(t):
			return colDisabled
		case hot(t):
			return colText
		default:
			return colDim
		}
	}

	// Transport row.
	icon := "" // play
	if a.playing != "" && !st.paused {
		icon = "" // pause
	}
	text(hdc, p.fonts.icons, icon, l.play, textCol(tPlay), co.DT_CENTER)

	pos := st.pos
	seekFrac := 0.0
	if st.dur > 0 {
		seekFrac = pos / st.dur
	}
	if p.drag == tSeek {
		seekFrac = p.dragFrac
		pos = seekFrac * st.dur
	}
	if a.playing != "" {
		text(hdc, p.fonts.text, review.Clock(pos), l.posText, colText, co.DT_RIGHT)
		text(hdc, p.fonts.text, review.Clock(st.dur), l.durText, colDim, co.DT_LEFT)
	}
	bar(hdc, l.seek, seekFrac, hot(tSeek) && p.enabled(tSeek))

	speaker := "" // volume
	if st.muted {
		speaker = "" // mute
	}
	text(hdc, p.fonts.icons, speaker, l.mute, textCol(tMute), co.DT_CENTER)
	vol := st.volume / 100
	if p.drag == tVolume {
		vol = p.dragFrac
	}
	if st.muted {
		vol = 0
	}
	bar(hdc, l.volume, vol, hot(tVolume))

	// The file's details.
	info := "Looking for videos…"
	if v, ok := a.Current(); ok {
		info = fmt.Sprintf("%s  ·  %s  ·  %s", review.Size(v.Size), v.ModTime.Format("2 Jan 2006"), v.Path)
	} else if a.q != nil {
		info = "Nothing left to review."
	}
	text(hdc, p.fonts.bold, info, l.info, colText, co.DT_LEFT|co.DT_PATH_ELLIPSIS)

	// Buttons.
	for _, b := range l.buttons {
		bg, fg := colButton, colText
		switch {
		case !p.enabled(b.t):
			fg = colDisabled
		case b.t == tKeep && hot(b.t):
			bg = colKeepHot
		case b.t == tKeep:
			bg = colKeep
		case b.t == tDelete && hot(b.t):
			bg = colDeleteHot
		case b.t == tDelete:
			bg = colDelete
		case hot(b.t):
			bg = colButtonHot
		}
		if p.pressed == b.t && p.hover == b.t {
			fg = colDim
		}
		roundRect(hdc, b.rc, bg, ui.DpiX(6))
		text(hdc, p.fonts.bold, b.label, b.rc, fg, co.DT_CENTER)
	}
	text(hdc, p.fonts.text, "Space pause · ←/→ 10s · 1–9 jump · [ ] speed · - = volume · M mute",
		l.hint, colDim, co.DT_LEFT)

	// Status line.
	status := ""
	if a.q != nil {
		status = fmt.Sprintf("%d left · %s     Freed %s", len(a.q.Items), review.Size(a.q.TotalSize()), review.Size(a.st.Freed))
	}
	if a.msg != "" {
		if status != "" {
			status += "     "
		}
		status += a.msg
	}
	text(hdc, p.fonts.text, status, l.status, colDim, co.DT_LEFT|co.DT_END_ELLIPSIS)
}

// bar draws a thin track filled to frac, thicker with a knob when hot.
func bar(hdc win.HDC, rc win.RECT, frac float64, hot bool) {
	thick := int32(ui.DpiY(4))
	if hot {
		thick = int32(ui.DpiY(6))
	}
	mid := (rc.Top + rc.Bottom) / 2
	track := win.RECT{Left: rc.Left, Top: mid - thick/2, Right: rc.Right, Bottom: mid - thick/2 + thick}
	roundRect(hdc, track, colTrack, int(thick))
	x := rc.Left + int32(frac*float64(rc.Right-rc.Left))
	if x > rc.Left {
		filled := track
		filled.Right = x
		roundRect(hdc, filled, colAccent, int(thick))
	}
	if hot {
		r := int32(ui.DpiY(7))
		ellipse(hdc, win.RECT{Left: x - r, Top: mid - r, Right: x + r, Bottom: mid + r}, colText)
	}
}

func fill(hdc win.HDC, rc win.RECT, c win.COLORREF) {
	b := solidBrush(c)
	hdc.FillRect(&rc, b)
	b.DeleteObject()
}

// withBrush selects a solid brush and no outline, runs f, then cleans up.
func withBrush(hdc win.HDC, c win.COLORREF, f func()) {
	b := solidBrush(c)
	nullPen, _ := win.GetStockObject(co.STOCK_NULL_PEN)
	oldPen, _ := hdc.SelectObjectPen(win.HPEN(nullPen))
	oldBrush, _ := hdc.SelectObjectBrush(b)
	f()
	hdc.SelectObjectBrush(oldBrush)
	hdc.SelectObjectPen(oldPen)
	b.DeleteObject()
}

func roundRect(hdc win.HDC, rc win.RECT, c win.COLORREF, radius int) {
	// With no outline GDI leaves off the last row and column, hence the +1s.
	rc.Right++
	rc.Bottom++
	withBrush(hdc, c, func() { hdc.RoundRect(rc, win.SIZE{Cx: int32(radius), Cy: int32(radius)}) })
}

func ellipse(hdc win.HDC, rc win.RECT, c win.COLORREF) {
	rc.Right++
	rc.Bottom++
	withBrush(hdc, c, func() { hdc.Ellipse(rc) })
}

func text(hdc win.HDC, font win.HFONT, s string, rc win.RECT, c win.COLORREF, flags co.DT) {
	old, _ := hdc.SelectObjectFont(font)
	hdc.SetTextColor(c)
	hdc.DrawText(s, &rc, flags|co.DT_SINGLELINE|co.DT_VCENTER|co.DT_NOPREFIX)
	hdc.SelectObjectFont(old)
}
