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
| `C` | Convert to AV1 (see below) |
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

## Converting to AV1

For files worth keeping but too big, `C` (or **To AV1…**) opens a dialog
that estimates the result at three quality levels: High quality (CQ 28),
Balanced (CQ 32) and Smaller (CQ 36). It encodes three 3-second samples
(at 20%, 50% and 80% through) at all three levels in one GPU pass,
compares them with the same stretches of the original, and projects each
level's final size, saving and rough time. An option saving under 10% is
marked not worth it, and one that would grow the file can't be picked.

Picking one (click, or `1`–`3`) takes the file out of review and converts
it in the background, one file at a time, while you carry on; a progress
row under the buttons shows how far it's got. The video is re-encoded with
NVIDIA's AV1 encoder (`av1_nvenc`, decoding on the GPU too where it can);
audio, subtitles and metadata are copied. MP4s stay MP4 (keeping VR
spherical metadata), everything else becomes MKV, and the file keeps its
name and date. The original is replaced only once the new file is AV1, the
same length and smaller; otherwise it's left alone. Each conversion goes on
the freed total and in `converted.log`, and the new file counts as kept.
Quitting mid-conversion asks first, then stops ffmpeg and removes the
partial file.

What to expect: re-encoding already efficient video gains little. On 8K
HEVC files at ~65 Mbps only Smaller saved anything (~10%), and slowly
(~2 h per hour of 8K60 on a recent NVIDIA card). High-bitrate H.264 shrinks a lot:
a 4K file at ~200 Mbps came out at 11–23% of its size.

Two things learnt the hard way, in `av1/`: without a high `-maxrate`,
NVENC's VBR mode silently caps the bitrate at the AV1 level's limit (about
40 Mbps at 4K60), so every quality level came out the same size; and
ffprobe's `-read_intervals start%+secs` counts from the keyframe it seeks
back to, so the window's end must be absolute.

## Settings

`%APPDATA%\vidsort\config.json`:

```json
{
  "mpv": "C:\\path\\to\\mpv.exe",
  "ffmpeg": "C:\\path\\to\\ffmpeg.exe",
  "minSizeMB": 0
}
```

- `mpv`: path to mpv.exe (a bare name is looked up on `PATH`).
- `ffmpeg`: path to ffmpeg.exe, for converting; ffprobe.exe must be beside
  it. It needs an NVIDIA GPU with AV1 encoding (RTX 40 series or newer).
- `minSizeMB`: hide files smaller than this.

Command-line arguments override the file: `vidsort.exe [-mpv path] [-min MB] [folder ...]`.
Folders given there skip the picker.

`$RECYCLE.BIN`, `System Volume Information` and similar are always skipped.

The same folder holds `decisions.json` (kept files, matched on path and
size, so a file that changes comes up again, the total freed and the
folders picked last) and
`deleted.log` (one tab-separated line per deleted file: time, size, path)
and `converted.log` (time, old size, new size, old path, new path).

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
| `av1` | ffprobe and ffmpeg: estimates, conversion, checking and swapping in the result |
| `main` | The window: `app.go` (layout, keys, mpv), `panel.go` (controls under the video), `theme.go` (dark theme), `pick.go` (folder picker), `convert_dialog.go` and `jobs.go` (AV1) |

## Build

From Linux or WSL (Go 1.26):

```sh
go test ./...   # the av1 tests use a local ffmpeg with libsvtav1, or skip
GOOS=windows go build -trimpath -ldflags "-s -w -H=windowsgui" -o vidsort.exe .
```

`rsrc_windows_amd64.syso` holds the icon and manifest (visual styles and DPI
awareness), from `res/`:

```sh
go run github.com/akavel/rsrc@latest -manifest res/vidsort.manifest -ico res/vidsort.ico -arch amd64 -o rsrc_windows_amd64.syso
```
