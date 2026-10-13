//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
)

// Dark theme colours.
var (
	colBg        = win.RGB(0x1b, 0x1b, 0x1d) // window and panel background
	colList      = win.RGB(0x22, 0x22, 0x25) // list background
	colHeader    = win.RGB(0x2a, 0x2a, 0x2f) // list column headers
	colButton    = win.RGB(0x2e, 0x2e, 0x33)
	colButtonHot = win.RGB(0x3c, 0x3c, 0x43)
	colText      = win.RGB(0xe8, 0xe8, 0xea)
	colDim       = win.RGB(0x96, 0x96, 0x9e)
	colDisabled  = win.RGB(0x5a, 0x5a, 0x60)
	colTrack     = win.RGB(0x3a, 0x3a, 0x40) // empty part of the seek and volume bars
	colAccent    = win.RGB(0x4c, 0x9a, 0xff) // filled part
	colKeep      = win.RGB(0x2e, 0x7d, 0x46)
	colKeepHot   = win.RGB(0x38, 0x96, 0x55)
	colDelete    = win.RGB(0xa3, 0x2a, 0x2a)
	colDeleteHot = win.RGB(0xc2, 0x33, 0x33)
)

// Functions windigo doesn't wrap.
var (
	uxtheme               = windows.NewLazySystemDLL("uxtheme.dll")
	procSetWindowTheme    = uxtheme.NewProc("SetWindowTheme")
	dwmapi                = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttr  = dwmapi.NewProc("DwmSetWindowAttribute")
	gdi32                 = windows.NewLazySystemDLL("gdi32.dll")
	procCreateSolidBrush  = gdi32.NewProc("CreateSolidBrush")
	dwmUseImmersiveDark   = uintptr(20) // DWMWA_USE_IMMERSIVE_DARK_MODE
	dwmUseImmersiveDarkV1 = uintptr(19) // the same, before Windows 10 20H1
)

func solidBrush(c win.COLORREF) win.HBRUSH {
	h, _, _ := procCreateSolidBrush.Call(uintptr(c))
	return win.HBRUSH(h)
}

// setWindowTheme applies a visual style by name, e.g. "DarkMode_Explorer".
func setWindowTheme(h win.HWND, name string) {
	p, _ := windows.UTF16PtrFromString(name)
	procSetWindowTheme.Call(uintptr(h), uintptr(unsafe.Pointer(p)), 0)
}

// darkTitleBar asks DWM to draw the window's title bar dark.
func darkTitleBar(h win.HWND) {
	on := uint32(1)
	for _, attr := range []uintptr{dwmUseImmersiveDark, dwmUseImmersiveDarkV1} {
		if r, _, _ := procDwmSetWindowAttr.Call(uintptr(h), attr, uintptr(unsafe.Pointer(&on)), 4); r == 0 {
			return
		}
	}
}

// darkList colours the list and gives its scroll bars and header the dark
// Explorer style.
func darkList(lv *ui.ListView) {
	h := lv.Hwnd()
	setWindowTheme(h, "DarkMode_Explorer")
	h.SendMessage(co.LVM_SETBKCOLOR, 0, win.LPARAM(colList))
	h.SendMessage(co.LVM_SETTEXTBKCOLOR, 0, win.LPARAM(colList))
	h.SendMessage(co.LVM_SETTEXTCOLOR, 0, win.LPARAM(colText))
	setWindowTheme(lv.Header().Hwnd(), "DarkMode_ItemsView")
	darkHeaderList = lv
	h.SetWindowSubclass(darkHeaderProc, 1, nil)
}

// darkHeaderList is the list whose header darkHeaderProc draws.
var darkHeaderList *ui.ListView

// darkHeaderProc subclasses the list to catch its header's custom-draw
// notifications, which the header sends to the list.
var darkHeaderProc = syscall.NewCallback(func(h win.HWND, msg co.WM, wp win.WPARAM, lp win.LPARAM, _, _ uintptr) uintptr {
	if msg == co.WM_NOTIFY {
		// lp points at the notification; read it through its address so vet is happy.
		nm := *(**win.NMHDR)(unsafe.Pointer(&lp))
		if int32(nm.Code) == int32(co.NM_CUSTOMDRAW) && nm.HWndFrom == darkHeaderList.Header().Hwnd() {
			return drawHeader(darkHeaderList, *(**win.NMCUSTOMDRAW)(unsafe.Pointer(&lp)))
		}
	}
	return h.DefSubclassProc(msg, wp, lp)
})

// uiFonts are the fonts the controls panel draws with.
type uiFonts struct {
	text, bold, icons win.HFONT
}

// newFonts makes fonts from the system message font, plus Segoe MDL2
// Assets (on every Windows 10) for the play, pause and speaker symbols.
func newFonts() uiFonts {
	var ncm win.NONCLIENTMETRICS
	ncm.SetCbSize()
	win.SystemParametersInfo(co.SPI_GETNONCLIENTMETRICS, uint32(unsafe.Sizeof(ncm)), unsafe.Pointer(&ncm), 0)

	var f uiFonts
	lf := ncm.LfMessageFont
	f.text, _ = win.CreateFontIndirect(&lf)
	lf.Weight = co.FW_SEMIBOLD
	f.bold, _ = win.CreateFontIndirect(&lf)
	icons := win.LOGFONT{Height: lf.Height * 4 / 3}
	icons.SetFaceName("Segoe MDL2 Assets")
	f.icons, _ = win.CreateFontIndirect(&icons)
	return f
}

func (f uiFonts) release() {
	f.text.DeleteObject()
	f.bold.DeleteObject()
	f.icons.DeleteObject()
}

// drawHeader draws the list's column headers dark. Windows 10's dark
// header theme leaves the text black, so each item is drawn here instead.
func drawHeader(lv *ui.ListView, cd *win.NMCUSTOMDRAW) uintptr {
	switch cd.DwDrawStage {
	case co.CDDS_PREPAINT:
		return uintptr(co.CDRF_NOTIFYITEMDRAW)
	case co.CDDS_ITEMPREPAINT:
		rc := cd.Rc
		fill(cd.Hdc, rc, colHeader)
		edge := rc
		edge.Left = edge.Right - 1
		fill(cd.Hdc, edge, colTrack)
		rc.Left += int32(ui.DpiX(6))
		cd.Hdc.SetBkMode(co.BKMODE_TRANSPARENT)
		cd.Hdc.SetTextColor(colDim)
		title := lv.Header().Item(int(cd.DwItemSpec)).Text()
		cd.Hdc.DrawText(title, &rc, co.DT_LEFT|co.DT_SINGLELINE|co.DT_VCENTER|co.DT_NOPREFIX|co.DT_END_ELLIPSIS)
		return uintptr(co.CDRF_SKIPDEFAULT)
	}
	return uintptr(co.CDRF_DODEFAULT)
}
