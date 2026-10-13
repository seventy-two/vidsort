//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"

	"github.com/seventy-two/vidsort/config"
	"github.com/seventy-two/vidsort/mpv"
	"github.com/seventy-two/vidsort/review"
	"github.com/seventy-two/vidsort/scan"
	"github.com/seventy-two/vidsort/store"
)

// Command IDs for the keyboard shortcuts (accelerators).
const (
	cmdKeep uint16 = iota + 1000
	cmdDelete
	cmdSkip
	cmdUndo
	cmdPause
	cmdBack
	cmdForward
	cmdSlower
	cmdFaster
	cmdJump // cmdJump+1 to cmdJump+9 jump to 10% to 90%
)

const (
	timerClock = 1
	seekStep   = 10 // seconds moved by the arrow keys
	// decisionGap is the shortest time between two keep/delete key presses;
	// anything quicker, such as a held-down Delete key, is ignored.
	decisionGap = 400 * time.Millisecond
	// columns, also the sort order a click on each gives
	colSize, colDate, colName, colFolder = 0, 1, 2, 3
)

var columnOrder = map[int]scan.Order{
	colSize: scan.BySize, colDate: scan.ByDate, colName: scan.ByName, colFolder: scan.ByFolder,
}

type app struct {
	cfg     config.Config
	mpvPath string
	st      *store.Store
	q       *review.Queue // nil until the scan finishes

	wnd                                  *ui.Main
	video                                *ui.Control
	list                                 *ui.ListView
	info                                 *ui.Static
	btnKeep, btnDelete, btnSkip, btnUndo *ui.Button
	status                               *ui.StatusBar

	player  *mpv.Player
	mpvCmds chan func(*mpv.Player) // run in order, off the UI thread
	playing string                 // path loaded in mpv
	syncing bool                   // set while the code itself changes the list selection
	closing bool
	lastKey time.Time // last keep/delete press, for decisionGap

	clockMu  sync.Mutex
	pos, dur float64 // playback position and length, from mpv's events
}

func newApp(cfg config.Config, mpvPath string, st *store.Store) *app {
	a := &app{cfg: cfg, mpvPath: mpvPath, st: st, mpvCmds: make(chan func(*mpv.Player), 32)}

	accel, _ := win.CreateAcceleratorTable(a.shortcuts()...)
	a.wnd = ui.NewMain(ui.OptsMain().
		Title("vidsort").
		Size(ui.Dpi(1400, 860)).
		Style(co.WS_CAPTION | co.WS_SYSMENU | co.WS_CLIPCHILDREN | co.WS_BORDER | co.WS_VISIBLE |
			co.WS_MINIMIZEBOX | co.WS_MAXIMIZEBOX | co.WS_SIZEBOX).
		AccelTable(accel))

	black, _ := win.GetStockObject(co.STOCK_BLACK_BRUSH)
	a.video = ui.NewControl(a.wnd, ui.OptsControl().
		Position(0, 0).Size(ui.Dpi(966, 750)).
		ClassBrush(win.HBRUSH(black)).
		Layout(ui.LAY_RESIZE_RESIZE))

	a.list = ui.NewListView(a.wnd, ui.OptsListView().
		Position(ui.Dpi(970, 0)).Size(ui.Dpi(430, 838)).
		// Owner data: the list asks for each visible row's text, so tens of
		// thousands of files cost nothing to show or re-sort.
		CtrlStyle(co.LVS_REPORT|co.LVS_SHOWSELALWAYS|co.LVS_SINGLESEL|co.LVS_OWNERDATA).
		CtrlExStyle(co.LVS_EX_FULLROWSELECT|co.LVS_EX_DOUBLEBUFFER).
		Column("Size", ui.DpiX(70)).
		Column("Modified", ui.DpiX(80)).
		Column("Name", ui.DpiX(170)).
		Column("Folder", ui.DpiX(200)).
		Layout(ui.LAY_MOVE_RESIZE))

	a.info = ui.NewStatic(a.wnd, ui.OptsStatic().
		Position(ui.Dpi(10, 758)).Size(ui.Dpi(950, 20)).
		CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX|co.SS_ENDELLIPSIS).
		Text("Looking for videos…").
		Layout(ui.LAY_RESIZE_MOVE))

	button := func(text string, x, w int) *ui.Button {
		return ui.NewButton(a.wnd, ui.OptsButton().
			Text(text).Position(ui.Dpi(x, 784)).Width(ui.DpiX(w)).Height(ui.DpiY(32)).
			Layout(ui.LAY_HOLD_MOVE))
	}
	a.btnKeep = button("Keep  (K)", 10, 110)
	a.btnDelete = button("Delete  (Del)", 126, 110)
	a.btnSkip = button("Skip  (N)", 242, 110)
	a.btnUndo = button("Undo keep  (Ctrl+Z)", 358, 140)

	ui.NewStatic(a.wnd, ui.OptsStatic().
		Position(ui.Dpi(512, 792)).Size(ui.Dpi(450, 20)).
		CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX|co.SS_ENDELLIPSIS).
		Text("Space pause · ←/→ 10s · 1–9 jump to 10–90% · [ ] speed · click a column to sort").
		Layout(ui.LAY_RESIZE_MOVE))

	a.status = ui.NewStatusBar(a.wnd, ui.OptsStatusBar().
		FixedPart(ui.DpiX(190), "").
		FixedPart(ui.DpiX(130), "").
		FixedPart(ui.DpiX(110), "").
		FlexPart(1, ""))

	a.events()
	return a
}

// shortcuts are the window's keyboard shortcuts. They work wherever focus
// is in the window; mpv gets matching bindings for when it has focus.
func (a *app) shortcuts() []win.ACCEL {
	key := func(k co.VK, cmd uint16) win.ACCEL { return win.ACCEL{FVirt: co.ACCELF_VIRTKEY, Key: k, Cmd: cmd} }
	list := []win.ACCEL{
		key('K', cmdKeep),
		key(co.VK_DELETE, cmdDelete),
		key('N', cmdSkip),
		{FVirt: co.ACCELF_VIRTKEY | co.ACCELF_CONTROL, Key: 'Z', Cmd: cmdUndo},
		key(co.VK_SPACE, cmdPause),
		key(co.VK_LEFT, cmdBack),
		key(co.VK_RIGHT, cmdForward),
		key(co.VK_OEM_4, cmdSlower), // [
		key(co.VK_OEM_6, cmdFaster), // ]
	}
	for i := uint16(1); i <= 9; i++ {
		list = append(list, key(co.VK('0'+i), cmdJump+i))
	}
	return list
}

func (a *app) run() { a.wnd.RunAsMain() }

func (a *app) events() {
	a.wnd.On().WmCreate(func(_ ui.WmCreate) int {
		a.startPlayer()
		a.wnd.Hwnd().SetTimer(timerClock, 250)
		a.refresh()
		go a.scan()
		return 0
	})
	a.wnd.On().WmDestroy(func() {
		a.closing = true
		if a.player != nil {
			close(a.mpvCmds)
			a.player.Quit()
		}
	})
	a.wnd.On().WmTimer(timerClock, a.showClock)

	a.btnKeep.On().BnClicked(a.keep)
	a.btnDelete.On().BnClicked(a.delete)
	a.btnSkip.On().BnClicked(a.skip)
	a.btnUndo.On().BnClicked(a.undo)

	a.wnd.On().WmCommandAccelMenu(cmdKeep, a.keep)
	a.wnd.On().WmCommandAccelMenu(cmdDelete, a.delete)
	a.wnd.On().WmCommandAccelMenu(cmdSkip, a.skip)
	a.wnd.On().WmCommandAccelMenu(cmdUndo, a.undo)
	a.wnd.On().WmCommandAccelMenu(cmdPause, func() { a.mpv((*mpv.Player).TogglePause) })
	a.wnd.On().WmCommandAccelMenu(cmdBack, func() { a.mpv(func(p *mpv.Player) error { return p.Seek(-seekStep) }) })
	a.wnd.On().WmCommandAccelMenu(cmdForward, func() { a.mpv(func(p *mpv.Player) error { return p.Seek(seekStep) }) })
	a.wnd.On().WmCommandAccelMenu(cmdSlower, func() { a.mpv(func(p *mpv.Player) error { return p.MultiplySpeed(1 / 1.1) }) })
	a.wnd.On().WmCommandAccelMenu(cmdFaster, func() { a.mpv(func(p *mpv.Player) error { return p.MultiplySpeed(1.1) }) })
	for i := 1; i <= 9; i++ {
		pct := i * 10
		a.wnd.On().WmCommandAccelMenu(cmdJump+uint16(i), func() {
			a.mpv(func(p *mpv.Player) error { return p.SeekPercent(pct) })
		})
	}

	a.list.On().LvnGetDispInfo(a.rowText)
	a.list.On().LvnItemChanged(func(p *win.NMLISTVIEW) {
		selected := p.UNewState&co.LVIS_SELECTED != 0 && p.UOldState&co.LVIS_SELECTED == 0
		if a.syncing || a.q == nil || !selected {
			return
		}
		a.q.Select(int(p.IItem))
		a.showCurrent()
	})
	a.list.On().LvnColumnClick(func(p *win.NMLISTVIEW) {
		if a.q == nil {
			return
		}
		a.q.Resort(columnOrder[int(p.ISubItem)])
		a.fillList()
		a.showCurrent()
		a.message("Sorted by " + a.list.Col(int(p.ISubItem)).Title())
	})
}

// startPlayer runs mpv inside the video panel and starts the goroutines
// that send it commands and read its events.
func (a *app) startPlayer() {
	p, err := mpv.Start(a.mpvPath, uintptr(a.video.Hwnd()))
	if err != nil {
		a.fatal(err)
		return
	}
	a.player = p

	// Bindings for when mpv's own window has the keyboard focus: the
	// decisions come back to us as client-message events.
	for key, msg := range map[string]string{
		"k": "keep", "K": "keep", "DEL": "delete", "n": "skip", "N": "skip", "Ctrl+z": "undo",
	} {
		p.BindMessage(key, "vidsort "+msg)
	}
	for i := 1; i <= 9; i++ {
		p.Command("keybind", fmt.Sprint(i), fmt.Sprintf("seek %d absolute-percent", i*10))
	}
	p.Command("keybind", "LEFT", fmt.Sprintf("seek -%d", seekStep))
	p.Command("keybind", "RIGHT", fmt.Sprintf("seek %d", seekStep))
	p.Observe(1, "time-pos")
	p.Observe(2, "duration")

	go func() {
		for f := range a.mpvCmds {
			f(p)
		}
	}()
	go a.readEvents(p)
}

func (a *app) readEvents(p *mpv.Player) {
	for ev := range p.Events() {
		switch ev.Event {
		case "property-change":
			var v float64
			json.Unmarshal(ev.Data, &v) // null (nothing loaded) leaves 0
			a.clockMu.Lock()
			if ev.Name == "time-pos" {
				a.pos = v
			} else if ev.Name == "duration" {
				a.dur = v
			}
			a.clockMu.Unlock()
		case "client-message":
			if len(ev.Args) == 2 && ev.Args[0] == "vidsort" {
				a.ui(func() {
					switch ev.Args[1] {
					case "keep":
						a.keep()
					case "delete":
						a.delete()
					case "skip":
						a.skip()
					case "undo":
						a.undo()
					}
				})
			}
		case "end-file":
			if ev.Reason == "error" {
				a.ui(func() { a.message("mpv couldn't play this file") })
			}
		}
	}
	a.ui(func() { a.fatal(errors.New("mpv has stopped")) })
}

// mpv queues a player command. Errors show in the status bar.
func (a *app) mpv(f func(*mpv.Player) error) {
	if a.player == nil || a.closing {
		return
	}
	a.mpvCmds <- func(p *mpv.Player) {
		if err := f(p); err != nil {
			a.ui(func() { a.message(err.Error()) })
		}
	}
}

// ui runs f on the UI thread, unless the window has gone.
func (a *app) ui(f func()) {
	a.wnd.UiThread(func() {
		if !a.closing {
			f()
		}
	})
}

func (a *app) fatal(err error) {
	if a.closing {
		return
	}
	ui.MsgError(a.wnd, "vidsort", "Something went wrong", err.Error())
	a.wnd.Hwnd().DestroyWindow()
}

// scan finds the videos (off the UI thread), then fills the list.
func (a *app) scan() {
	unreadable := 0
	found := scan.Walk(a.cfg.Roots,
		func(string, error) { unreadable++ },
		func(n int) { a.ui(func() { a.message(fmt.Sprintf("Scanning… %d videos so far", n)) }) })

	a.ui(func() {
		var todo []scan.Video
		kept := 0
		for _, v := range found {
			switch {
			case v.Size < a.cfg.MinSizeMB*1024*1024:
			case a.st.IsKept(v):
				kept++
			default:
				todo = append(todo, v)
			}
		}
		a.q = review.New(todo, scan.BySize)
		a.fillList()
		a.showCurrent()
		msg := fmt.Sprintf("Found %d videos; %d kept before", len(found), kept)
		if unreadable > 0 {
			msg += fmt.Sprintf("; %d folders couldn't be read", unreadable)
		}
		a.message(msg)
	})
}

// fillList tells the list how many rows there are and redraws them.
func (a *app) fillList() {
	a.syncing = true
	a.list.Hwnd().SendMessage(co.LVM_SETITEMCOUNT, win.WPARAM(len(a.q.Items)), 0)
	a.list.Hwnd().InvalidateRect(nil, false)
	a.syncing = false
}

// rowText supplies the text of one cell when the list asks for it.
func (a *app) rowText(p *win.NMLVDISPINFO) {
	i := int(p.Item.IItem)
	if p.Item.Mask&co.LVIF_TEXT == 0 || a.q == nil || i < 0 || i >= len(a.q.Items) {
		return
	}
	v := a.q.Items[i]
	var text string
	switch p.Item.ISubItem {
	case colSize:
		text = review.Size(v.Size)
	case colDate:
		text = v.ModTime.Format("2006-01-02")
	case colName:
		text = v.Name()
	case colFolder:
		text = v.Dir()
	}
	buf := p.Item.PszText()
	if len(buf) == 0 {
		return
	}
	n := copy(buf[:len(buf)-1], utf16.Encode([]rune(text)))
	buf[n] = 0
}

// showCurrent selects the current video in the list, loads it into mpv if
// it isn't already playing, and updates the labels.
func (a *app) showCurrent() {
	v, ok := a.q.Current()
	if ok {
		a.syncing = true
		a.list.Item(a.q.Cur).Select(true).Focus().EnsureVisible()
		a.syncing = false
		a.list.Focus() // so the row shows in the bright selected colour
	}
	if ok && v.Path != a.playing {
		path := v.Path
		a.mpv(func(p *mpv.Player) error { return p.LoadFile(path) })
		a.playing = path
	} else if !ok && a.playing != "" {
		a.mpv((*mpv.Player).Stop)
		a.playing = ""
	}
	a.refresh()
}

// refresh updates the info line, buttons and status bar.
func (a *app) refresh() {
	v, ok := a.Current()
	switch {
	case a.q == nil:
		a.info.Hwnd().SetWindowText("Looking for videos…")
	case !ok:
		a.info.Hwnd().SetWindowText("Nothing left to review.")
	default:
		a.info.Hwnd().SetWindowText(fmt.Sprintf("%s  ·  %s  ·  %s",
			review.Size(v.Size), v.ModTime.Format("2 Jan 2006"), v.Path))
	}
	for _, b := range []*ui.Button{a.btnKeep, a.btnDelete, a.btnSkip} {
		b.Hwnd().EnableWindow(ok)
	}
	a.btnUndo.Hwnd().EnableWindow(a.q != nil && a.q.CanUndo())

	if a.q != nil {
		a.status.Part(0).SetText(fmt.Sprintf("%d left · %s", len(a.q.Items), review.Size(a.q.TotalSize())))
	}
	a.status.Part(1).SetText("Freed " + review.Size(a.st.Freed))
}

// Current is the video under review, if the scan has finished and any are left.
func (a *app) Current() (scan.Video, bool) {
	if a.q == nil {
		return scan.Video{}, false
	}
	return a.q.Current()
}

func (a *app) showClock() {
	a.clockMu.Lock()
	pos, dur := a.pos, a.dur
	a.clockMu.Unlock()
	text := ""
	if a.playing != "" {
		text = review.Clock(pos) + " / " + review.Clock(dur)
	}
	a.status.Part(2).SetText(text)
}

func (a *app) message(s string) { a.status.Part(3).SetText(s) }

// allowDecision rate-limits keep and delete so a held or bounced key can't
// run through several files.
func (a *app) allowDecision() bool {
	now := time.Now()
	ok := now.Sub(a.lastKey) >= decisionGap
	a.lastKey = now
	return ok
}

func (a *app) keep() {
	if _, ok := a.Current(); !ok || !a.allowDecision() {
		return
	}
	v, _ := a.q.Keep()
	if err := a.st.Keep(v, time.Now()); err != nil {
		a.message("Couldn't save: " + err.Error())
	} else {
		a.message("Kept " + v.Name())
	}
	a.fillList()
	a.showCurrent()
}

func (a *app) delete() {
	if _, ok := a.Current(); !ok || !a.allowDecision() {
		return
	}
	v, _ := a.q.Delete()
	a.fillList()
	a.showCurrent() // loads the next file, so mpv lets go of this one
	a.message("Deleting " + v.Name() + "…")
	go a.removeFile(v)
}

// removeFile deletes v permanently. mpv may still have it open for a moment
// after moving on, so it retries for a few seconds.
func (a *app) removeFile(v scan.Video) {
	var err error
	for try := 0; try < 40; try++ {
		err = os.Remove(v.Path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			err = nil
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	a.ui(func() {
		if err != nil {
			ui.MsgError(a.wnd, "vidsort", "Couldn't delete "+v.Name(), err.Error())
			a.message("Couldn't delete " + v.Name())
			return
		}
		if err := a.st.Deleted(v, time.Now()); err != nil {
			a.message("Deleted, but couldn't save: " + err.Error())
		} else {
			a.message(fmt.Sprintf("Deleted %s (%s)", v.Name(), review.Size(v.Size)))
		}
		a.refresh()
	})
}

func (a *app) skip() {
	if _, ok := a.Current(); !ok {
		return
	}
	a.q.Skip()
	a.showCurrent()
}

func (a *app) undo() {
	if a.q == nil {
		return
	}
	v, ok := a.q.Undo()
	if !ok {
		return
	}
	if err := a.st.Unkeep(v); err != nil {
		a.message("Couldn't save: " + err.Error())
	} else {
		a.message("Undid keep: " + v.Name())
	}
	a.fillList()
	a.showCurrent()
}
