//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"

	"github.com/seventy-two/vidsort/av1"
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
	cmdVolDown
	cmdVolUp
	cmdMute
	cmdConvert
	cmdJump // cmdJump+1 to cmdJump+9 jump to 10% to 90%
)

const (
	timerTick = 1  // redraws the seek bar while playing
	seekStep  = 10 // seconds moved by the arrow keys
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
	folders []string // looked through for videos
	mpvPath string
	st      *store.Store
	q       *review.Queue // nil until the scan finishes

	wnd   *ui.Main
	video *ui.Control
	list  *ui.ListView
	panel *panel
	msg   string // latest message, shown on the status line

	tools av1.Tools // ffmpeg, for AV1 conversions; FFmpeg is empty if it wasn't found
	jobs  []*job    // queued conversions, the running one first

	player  *mpv.Player
	mpvCmds chan func(*mpv.Player) // run in order, off the UI thread
	playing string                 // path loaded in mpv
	syncing bool                   // set while the code itself changes the list selection
	closing bool
	lastKey time.Time // last keep/delete press, for decisionGap

	stateMu sync.Mutex
	state   playerState // from mpv's property-change events
	drawn   playerState // what the panel last showed
}

// playerState is what the panel shows about playback.
type playerState struct {
	pos, dur, volume float64
	paused, muted    bool
}

func newApp(cfg config.Config, mpvPath string, st *store.Store, folders []string) *app {
	a := &app{cfg: cfg, folders: folders, mpvPath: mpvPath, st: st, mpvCmds: make(chan func(*mpv.Player), 32)}
	a.tools = findTools(cfg.FFmpeg)

	accel, _ := win.CreateAcceleratorTable(a.shortcuts()...)
	a.wnd = ui.NewMain(ui.OptsMain().
		Title(windowTitle(folders)).
		ClassBrush(solidBrush(colBg)).
		Size(ui.Dpi(1400, 860)).
		Style(co.WS_CAPTION | co.WS_SYSMENU | co.WS_CLIPCHILDREN | co.WS_BORDER | co.WS_VISIBLE |
			co.WS_MINIMIZEBOX | co.WS_MAXIMIZEBOX | co.WS_SIZEBOX).
		AccelTable(accel))

	black, _ := win.GetStockObject(co.STOCK_BLACK_BRUSH)
	a.video = ui.NewControl(a.wnd, ui.OptsControl().
		Position(0, 0).Size(ui.Dpi(966, 860-panelHeight)).
		ClassBrush(win.HBRUSH(black)).
		ExStyle(co.WS_EX_LEFT). // no border
		Layout(ui.LAY_RESIZE_RESIZE))

	a.list = ui.NewListView(a.wnd, ui.OptsListView().
		Position(ui.Dpi(970, 0)).Size(ui.Dpi(430, 860)).
		// Owner data: the list asks for each visible row's text, so tens of
		// thousands of files cost nothing to show or re-sort.
		CtrlStyle(co.LVS_REPORT|co.LVS_SHOWSELALWAYS|co.LVS_SINGLESEL|co.LVS_OWNERDATA).
		CtrlExStyle(co.LVS_EX_FULLROWSELECT|co.LVS_EX_DOUBLEBUFFER).
		WndExStyle(co.WS_EX_LEFT). // no border
		Column("Size", ui.DpiX(70)).
		Column("Modified", ui.DpiX(80)).
		Column("Name", ui.DpiX(170)).
		Column("Folder", ui.DpiX(200)).
		Layout(ui.LAY_MOVE_RESIZE))

	a.panel = newPanel(a)

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
		key(co.VK_OEM_MINUS, cmdVolDown),
		key(co.VK_OEM_PLUS, cmdVolUp), // the = key
		key('M', cmdMute),
		key('C', cmdConvert),
	}
	for i := uint16(1); i <= 9; i++ {
		list = append(list, key(co.VK('0'+i), cmdJump+i))
	}
	return list
}

func (a *app) run() { a.wnd.RunAsMain() }

// windowTitle names the folders being looked through.
func windowTitle(folders []string) string {
	if len(folders) == 1 {
		return "vidsort · " + folders[0]
	}
	return fmt.Sprintf("vidsort · %s and %d more", folders[0], len(folders)-1)
}

func (a *app) events() {
	a.wnd.On().WmCreate(func(_ ui.WmCreate) int {
		darkTitleBar(a.wnd.Hwnd())
		darkList(a.list)
		a.startPlayer()
		a.wnd.Hwnd().SetTimer(timerTick, 100)
		a.refresh()
		go a.scan()
		return 0
	})
	a.wnd.On().WmClose(func() {
		if len(a.jobs) > 0 {
			text, _ := a.jobStatus()
			if !ui.MsgOkCancel(a.wnd, "vidsort", "A conversion is still running",
				text+"\n\nQuitting stops it. The original stays as it was.", "Quit") {
				return
			}
			a.stopJobs()
		}
		a.wnd.Hwnd().DestroyWindow()
	})
	a.wnd.On().WmDestroy(func() {
		a.closing = true
		if a.player != nil {
			close(a.mpvCmds)
			a.player.Quit()
		}
	})
	a.wnd.On().WmTimer(timerTick, a.tick)

	a.wnd.On().WmCommandAccelMenu(cmdKeep, a.keep)
	a.wnd.On().WmCommandAccelMenu(cmdDelete, a.delete)
	a.wnd.On().WmCommandAccelMenu(cmdSkip, a.skip)
	a.wnd.On().WmCommandAccelMenu(cmdUndo, a.undo)
	a.wnd.On().WmCommandAccelMenu(cmdPause, a.togglePause)
	a.wnd.On().WmCommandAccelMenu(cmdVolDown, func() { a.addVolume(-5) })
	a.wnd.On().WmCommandAccelMenu(cmdVolUp, func() { a.addVolume(5) })
	a.wnd.On().WmCommandAccelMenu(cmdMute, a.toggleMute)
	a.wnd.On().WmCommandAccelMenu(cmdConvert, a.convert)
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
		"c": "convert", "C": "convert",
	} {
		p.BindMessage(key, "vidsort "+msg)
	}
	for i := 1; i <= 9; i++ {
		p.Command("keybind", fmt.Sprint(i), fmt.Sprintf("seek %d absolute-percent", i*10))
	}
	p.Command("keybind", "LEFT", fmt.Sprintf("seek -%d", seekStep))
	p.Command("keybind", "RIGHT", fmt.Sprintf("seek %d", seekStep))
	p.Command("keybind", "-", "add volume -5")
	p.Command("keybind", "=", "add volume 5")
	for i, name := range []string{"time-pos", "duration", "volume", "mute", "pause"} {
		p.Observe(i+1, name)
	}

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
			// Values are null while nothing is loaded, which leaves them zero.
			var f float64
			var b bool
			json.Unmarshal(ev.Data, &f)
			json.Unmarshal(ev.Data, &b)
			a.stateMu.Lock()
			switch ev.Name {
			case "time-pos":
				a.state.pos = f
			case "duration":
				a.state.dur = f
			case "volume":
				a.state.volume = f
			case "mute":
				a.state.muted = b
			case "pause":
				a.state.paused = b
			}
			a.stateMu.Unlock()
		case "client-message":
			if len(ev.Args) == 2 && ev.Args[0] == "vidsort" {
				// From a goroutine of its own: ui waits for the UI thread,
				// and the convert dialog keeps it busy for a while, during
				// which this loop must keep reading mpv's events.
				go a.ui(func() {
					switch ev.Args[1] {
					case "keep":
						a.keep()
					case "delete":
						a.delete()
					case "skip":
						a.skip()
					case "undo":
						a.undo()
					case "convert":
						a.convert()
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
	found := scan.Walk(a.folders,
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
		a.mpv(func(p *mpv.Player) error {
			if err := p.LoadFile(path); err != nil {
				return err
			}
			// Reaching the end of a file pauses it (keep-open), and the pause
			// would carry over to this one.
			_, err := p.Command("set_property", "pause", false)
			return err
		})
		a.playing = path
	} else if !ok && a.playing != "" {
		a.mpv((*mpv.Player).Stop)
		a.playing = ""
	}
	a.refresh()
}

// refresh redraws the panel: file details, buttons and status line.
func (a *app) refresh() { a.panel.redraw() }

// Current is the video under review, if the scan has finished and any are left.
func (a *app) Current() (scan.Video, bool) {
	if a.q == nil {
		return scan.Video{}, false
	}
	return a.q.Current()
}

func (a *app) playerState() playerState {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.state
}

// tick redraws the panel when playback or a conversion has moved on.
func (a *app) tick() {
	if st := a.playerState(); st != a.drawn || a.checkJobs() {
		a.drawn = st
		a.panel.redraw()
	}
}

// findTools locates ffmpeg and the ffprobe beside it, for NVIDIA AV1 encoding.
func findTools(ffmpeg string) av1.Tools {
	t := av1.Tools{Encoder: "av1_nvenc", GPUDecode: true}
	path, err := exec.LookPath(ffmpeg)
	if err != nil {
		return t
	}
	probe := filepath.Join(filepath.Dir(path), "ffprobe.exe")
	if _, err := os.Stat(probe); err != nil {
		if probe, err = exec.LookPath("ffprobe.exe"); err != nil {
			return t
		}
	}
	t.FFmpeg, t.FFprobe = path, probe
	return t
}

func (a *app) togglePause() { a.mpv((*mpv.Player).TogglePause) }

func (a *app) toggleMute() {
	a.mpv(func(p *mpv.Player) error { _, err := p.Command("cycle", "mute"); return err })
}

// seekFraction jumps to frac (0–1) of the way through. Exact seeks decode
// up to the precise frame; the others land on the nearest keyframe, which
// is much quicker while dragging.
func (a *app) seekFraction(frac float64, exact bool) {
	if a.playing == "" {
		return
	}
	flags := "absolute-percent+keyframes"
	if exact {
		flags = "absolute-percent+exact"
	}
	a.mpv(func(p *mpv.Player) error { _, err := p.Command("seek", frac*100, flags); return err })
}

func (a *app) setVolume(v float64) {
	v = min(max(v, 0), 100)
	a.stateMu.Lock()
	a.state.volume, a.state.muted = v, false // show it straight away
	a.stateMu.Unlock()
	a.mpv(func(p *mpv.Player) error {
		if _, err := p.Command("set_property", "mute", false); err != nil {
			return err
		}
		_, err := p.Command("set_property", "volume", v)
		return err
	})
	a.panel.redraw()
}

func (a *app) addVolume(d float64) { a.setVolume(a.playerState().volume + d) }

func (a *app) message(s string) {
	a.msg = s
	a.panel.redraw()
}

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
	v, _ := a.q.Take()
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
