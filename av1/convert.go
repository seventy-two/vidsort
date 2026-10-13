package av1

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrNotSmaller means the AV1 file came out no smaller, so the original was kept.
var ErrNotSmaller = errors.New("the AV1 version wasn't smaller, so the original was kept")

// Outputs works out where a conversion of src goes. MP4s stay MP4 (AV1 in
// MP4 plays everywhere, and VR players read its spherical metadata);
// everything else becomes MKV, which takes any audio codec as it is. The
// name stays the same apart from the extension, unless that would
// overwrite a different file. part is where ffmpeg writes until it's done.
func Outputs(src string) (final, part, format string) {
	ext := strings.ToLower(filepath.Ext(src))
	base := strings.TrimSuffix(src, filepath.Ext(src))
	newExt, format := ".mkv", "matroska"
	if ext == ".mp4" || ext == ".m4v" {
		newExt, format = ".mp4", "mp4"
	}
	final = base + newExt
	if !strings.EqualFold(final, src) {
		if _, err := os.Stat(final); err == nil {
			final = base + " (AV1)" + newExt
		}
	}
	return final, final + ".vidsort-part", format
}

// ConvertArgs re-encodes the video of src to AV1 at quality cq, copying
// the audio (and, in MKV, subtitles) and metadata. Progress goes to stdout.
func (t Tools) ConvertArgs(i Info, src, part, format string, cq int) []string {
	args := []string{"-hide_banner", "-nostdin", "-y"}
	args = append(args, t.decodeArgs(i)...)
	args = append(args, "-i", src, "-map", "0:v:0", "-map", "0:a?")
	if format == "matroska" {
		args = append(args, "-map", "0:s?", "-c:s", "copy")
	}
	args = append(args, t.encodeArgs(cq)...)
	args = append(args, "-c:a", "copy", "-map_metadata", "0")
	if format == "mp4" {
		args = append(args, "-strict", "unofficial") // keep VR (spherical) metadata
	}
	return append(args, "-f", format, "-progress", "pipe:1", "-nostats", part)
}

// ParseProgress reads one line of ffmpeg's -progress output, returning the
// position reached in seconds, if the line gives it.
func ParseProgress(line string) (float64, bool) {
	key, val, _ := strings.Cut(strings.TrimSpace(line), "=")
	if key != "out_time_us" {
		return 0, false
	}
	us, err := strconv.ParseInt(val, 10, 64)
	if err != nil || us < 0 {
		return 0, false
	}
	return float64(us) / 1e6, true
}

// Result is a finished conversion.
type Result struct {
	Path    string // the new file
	OldSize int64
	NewSize int64
}

// Convert re-encodes src at quality cq and, once the new file checks out,
// replaces the original with it. On any failure, or if the result isn't
// smaller, the original is left alone and the partial file removed.
func (t Tools) Convert(ctx context.Context, i Info, src string, cq int, progress Progress) (Result, error) {
	final, part, format := Outputs(src)
	defer os.Remove(part) // gone already if it worked

	cmd := command(ctx, t.FFmpeg, t.ConvertArgs(i, src, part, format, cq)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("starting ffmpeg: %w", err)
	}
	began := time.Now()
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if pos, ok := ParseProgress(sc.Text()); ok && i.Duration > 0 {
			done := min(pos/i.Duration, 1)
			progress(done, remaining(time.Since(began), done))
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, fmt.Errorf("ffmpeg: %w\n%s", err, lastLines(stderr.String(), 4))
	}
	return t.finish(ctx, i, src, part, final)
}

// remaining estimates the time left from the time taken so far.
func remaining(elapsed time.Duration, done float64) string {
	if done < 0.01 {
		return ""
	}
	left := time.Duration(float64(elapsed) * (1 - done) / done)
	return FormatDuration(left) + " left"
}

// finish checks the new file and swaps it in for the original.
func (t Tools) finish(ctx context.Context, src Info, srcPath, part, final string) (Result, error) {
	out, err := t.Probe(ctx, part)
	if err != nil {
		return Result{}, fmt.Errorf("checking the new file: %w", err)
	}
	if err := Verify(src, out); err != nil {
		return Result{}, err
	}
	st, err := os.Stat(srcPath)
	if err != nil {
		return Result{}, err
	}
	pst, err := os.Stat(part)
	if err != nil {
		return Result{}, err
	}
	if pst.Size() >= st.Size() {
		return Result{}, ErrNotSmaller
	}

	// Keep the original's date, so sorting by date still makes sense.
	os.Chtimes(part, st.ModTime(), st.ModTime())
	if err := os.Rename(part, final); err != nil { // replaces the original when the names match
		return Result{}, err
	}
	if !strings.EqualFold(final, srcPath) {
		if err := os.Remove(srcPath); err != nil {
			return Result{}, fmt.Errorf("converted to %s, but couldn't delete the original: %w", final, err)
		}
	}
	return Result{Path: final, OldSize: st.Size(), NewSize: pst.Size()}, nil
}

// Verify checks that out is a complete AV1 copy of src: AV1 video, and the
// same length to within a second (or 0.5% for long videos).
func Verify(src, out Info) error {
	if out.VideoCodec != "av1" {
		return fmt.Errorf("the new file's video is %q, not AV1", out.VideoCodec)
	}
	if math.Abs(out.Duration-src.Duration) > max(1, src.Duration*0.005) {
		return fmt.Errorf("the new file is %.1fs long but the original is %.1fs", out.Duration, src.Duration)
	}
	return nil
}

// FormatDuration gives a rough, readable length: "45s", "12 min", "1 h 50 min".
func FormatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
}
