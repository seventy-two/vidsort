# vidsort

A Windows app for clearing out video files. It asks which folders to look
through (pick several at once with Ctrl or Shift, or whole drives), finds
every video under them, plays them one at a time (largest first by default)
in an embedded [mpv](https://mpv.io), and lets you keep or delete each one.
The picker opens where you were last time, so **Look through** straight
away repeats the last choice; cancelling it closes vidsort.

Deleting is **permanent**: files don't go to the Recycle Bin.

## Keys

| Key | Does |
| --- | --- |
| `K` | Keep: remembered, so it won't come up again |
| `Del` | Delete the file permanently |
| `N` | Skip for now (it comes up again next time) |
| `Ctrl+Z` | Undo the last keep (deletes can't be undone) |
| `Space` | Pause |
| `←` / `→` | Back / forward 10 seconds |
| `1`–`9` | Jump to 10%–90% of the way through |
| `[` / `]` | Slower / faster |
| `-` / `=` | Volume down / up |
| `M` | Mute |

The keys work whether the list or the video has focus. Keep and delete
ignore a second press within 0.4s, so a held key can't run through several
files. Click a row to play it; click a column header to sort by it.

Under the video, a seek bar (click or drag; it shows the knob on hover) and
a volume bar (click, drag or scroll the mouse wheel) sit either side of
the play/pause and mute buttons. The window is dark throughout.

## Settings

`%APPDATA%\vidsort\config.json`:

```json
{
  "mpv": "C:\\path\\to\\mpv.exe",
  "minSizeMB": 0
}
```

- `mpv`: path to mpv.exe (a bare name is looked up on `PATH`).
- `minSizeMB`: hide files smaller than this.

Command-line arguments override the file: `vidsort.exe [-mpv path] [-min MB] [folder ...]`.
Folders given there skip the picker.

`$RECYCLE.BIN`, `System Volume Information` and similar are always skipped.

The same folder holds `decisions.json` (kept files, matched on path and
size, so a file that changes comes up again, the total freed and the
folders picked last) and
`deleted.log` (one tab-separated line per deleted file: time, size, path).

## How it works

mpv runs as a child process drawing into the app's video panel (`--wid`),
and the app controls it over mpv's JSON IPC on a named pipe. The window is
plain Win32 through [windigo](https://github.com/rodrigocfd/windigo), so
it's pure Go with no cgo. Your own mpv config and scripts (uosc, say) still
load.

mpv is started with `--hwdec=auto-safe`. mpv leaves hardware decoding off
by default, and without it 8K HEVC at 60fps drops frames even on a fast
CPU; with it, the GPU decodes and nothing drops.

The panel under the video (`panel.go`) is drawn by hand with GDI, as Win32
buttons, labels and status bars can't be made properly dark. `theme.go`
holds the colours and darkens the list, its header and the title bar.

| Package | Does |
| --- | --- |
| `scan` | Finds video files and sorts them |
| `review` | The queue: current file, keep, delete, skip, undo |
| `store` | Saves kept files, the deletion log and the last folders |
| `mpv` | IPC client, and starting mpv embedded in a window |
| `config` | Reads `config.json` |
| `main` | The window: `app.go` (layout, keys, mpv), `panel.go` (controls under the video), `theme.go` (dark theme), `pick.go` (folder picker) |

## Build

From Linux or WSL (Go 1.26):

```sh
go test ./...
GOOS=windows go build -trimpath -ldflags "-s -w -H=windowsgui" -o vidsort.exe .
```

`rsrc_windows_amd64.syso` is windigo's resource file: an icon and a manifest
for visual styles and DPI awareness.
