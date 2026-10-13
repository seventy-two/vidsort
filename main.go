//go:build windows

// vidsort plays the video files in the folders you pick one at a time,
// largest first, with mpv embedded in its window, so each can be kept or
// deleted.
//
// Usage: vidsort [-mpv path] [-min MB] [folder ...]
//
// With no folders given it asks for them. Settings default to
// %APPDATA%\vidsort\config.json.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"

	"github.com/seventy-two/vidsort/config"
	"github.com/seventy-two/vidsort/store"
)

func main() {
	runtime.LockOSThread() // the Win32 UI must stay on one thread

	if err := run(); err != nil {
		win.HWND(0).MessageBox(err.Error(), "vidsort", co.MB_ICONERROR)
		os.Exit(1)
	}
}

func run() error {
	appData, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(appData, "vidsort")
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}

	flag.StringVar(&cfg.MPV, "mpv", cfg.MPV, "path to mpv.exe")
	flag.Int64Var(&cfg.MinSizeMB, "min", cfg.MinSizeMB, "skip files smaller than this many MB")
	flag.Parse()

	mpvPath, err := exec.LookPath(cfg.MPV)
	if err != nil {
		return fmt.Errorf("Can't find mpv (%s).\n\nSet \"mpv\" in %s to the full path of mpv.exe.",
			cfg.MPV, filepath.Join(dir, "config.json"))
	}

	st, err := store.Open(dir)
	if err != nil {
		return err
	}

	folders := flag.Args()
	if len(folders) == 0 {
		// The folder picker is a COM object, which needs COM set up on this thread.
		win.CoInitializeEx(co.COINIT_APARTMENTTHREADED | co.COINIT_DISABLE_OLE1DDE)
		defer win.CoUninitialize()
		start := ""
		if len(st.Folders) > 0 {
			start = st.Folders[0]
		}
		if folders, err = pickFolders(start); err != nil {
			return err
		}
		if len(folders) == 0 {
			return nil // cancelled
		}
	}
	if err := st.SetFolders(folders); err != nil {
		return err
	}

	newApp(cfg, mpvPath, st, folders).run()
	return nil
}
