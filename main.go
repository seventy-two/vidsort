//go:build windows

// vidsort plays the video files on a set of drives one at a time, largest
// first, with mpv embedded in its window, so each can be kept or deleted.
//
// Usage: vidsort [-mpv path] [-min MB] [folder ...]
//
// Folders and settings default to %APPDATA%\vidsort\config.json.
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
	if flag.NArg() > 0 {
		cfg.Roots = flag.Args()
	}
	if len(cfg.Roots) == 0 {
		return fmt.Errorf("No folders to look through.\n\nPass them on the command line, or list them as \"roots\" in\n%s",
			filepath.Join(dir, "config.json"))
	}

	mpvPath, err := exec.LookPath(cfg.MPV)
	if err != nil {
		return fmt.Errorf("Can't find mpv (%s).\n\nSet \"mpv\" in %s to the full path of mpv.exe.",
			cfg.MPV, filepath.Join(dir, "config.json"))
	}

	st, err := store.Open(dir)
	if err != nil {
		return err
	}

	newApp(cfg, mpvPath, st).run()
	return nil
}
