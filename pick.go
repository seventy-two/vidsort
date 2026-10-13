//go:build windows

package main

import (
	"errors"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
	"github.com/rodrigocfd/windigo/x/cosh"
	"github.com/rodrigocfd/windigo/x/winsh"
)

// hresultCancelled is HRESULT_FROM_WIN32(ERROR_CANCELLED).
const hresultCancelled = 0x800704c7

// pickFolders shows the Windows folder picker, starting in start if given.
// Several folders can be chosen at once. It returns nil if cancelled.
func pickFolders(start string) ([]string, error) {
	rel := win.NewOleReleaser()
	defer rel.Release()

	var fod *winsh.IFileOpenDialog
	if err := win.CoCreateInstance(rel, &cosh.CLSID_FileOpenDialog, nil, co.CLSCTX_INPROC_SERVER, &fod); err != nil {
		return nil, err
	}
	opts, _ := fod.GetOptions()
	fod.SetOptions(opts | cosh.FOS_PICKFOLDERS | cosh.FOS_ALLOWMULTISELECT | cosh.FOS_FORCEFILESYSTEM)
	fod.SetTitle("vidsort: choose the folders to look through")
	fod.SetOkButtonLabel("Look through")
	if start != "" {
		var item *winsh.IShellItem
		if winsh.SHCreateItemFromParsingName(rel, start, &item) == nil {
			fod.SetFolder(item)
		}
	}

	if ok, err := fod.Show(0); !ok {
		// windigo only expects the plain ERROR_CANCELLED code, but the dialog
		// returns it as an HRESULT, which comes through as an error.
		var hr co.HRESULT
		if errors.As(err, &hr) && uint32(hr) == hresultCancelled {
			err = nil
		}
		return nil, err
	}
	items, err := fod.GetResults(rel)
	if err != nil {
		return nil, err
	}
	return items.EnumDisplayNames(cosh.SIGDN_FILESYSPATH)
}
