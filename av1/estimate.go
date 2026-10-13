package av1

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Level is a quality setting: lower CQ means higher quality and larger files.
type Level struct {
	Name string
	CQ   int
}

// Levels are the choices offered, best quality first.
var Levels = []Level{{"High quality", 28}, {"Balanced", 32}, {"Smaller", 36}}

// SampleSecs is the length of each sample encoded for an estimate.
const SampleSecs = 3.0

// splitSpeedup is how much faster one encode runs than each of the three
// run side by side when sampling, measured on 8K HEVC with a recent NVIDIA card
// (3 seconds of all three took 12.6s; one alone ran at 33 fps). It's used
// to turn the sampling time into a conversion time.
const splitSpeedup = 2.4

// Estimate is the projected result of converting at one level.
type Estimate struct {
	Level
	Size int64         // projected file size
	Time time.Duration // rough conversion time
}

// Saving is the fraction of the original size saved (negative if it grows).
func (e Estimate) Saving(original int64) float64 {
	return 1 - float64(e.Size)/float64(original)
}

// SamplePoints are where the samples start: 20%, 50% and 80% through, or
// just the start for a video too short for three.
func SamplePoints(duration float64) []float64 {
	if duration < 6*SampleSecs {
		return []float64{0}
	}
	return []float64{duration * 0.2, duration * 0.5, duration * 0.8}
}

// SampleArgs encodes secs of src from start at every level in one pass (the
// decoded frames split three ways), throwing the output away: ffmpeg's
// summary gives each output's size.
func (t Tools) SampleArgs(i Info, src string, start, secs float64) []string {
	args := []string{"-hide_banner", "-nostdin"}
	args = append(args, t.decodeArgs(i)...)
	args = append(args, "-ss", ftoa(start), "-t", ftoa(secs), "-i", src)
	var labels strings.Builder
	for n := range Levels {
		fmt.Fprintf(&labels, "[l%d]", n)
	}
	args = append(args, "-filter_complex", fmt.Sprintf("[0:v:0]split=%d%s", len(Levels), labels.String()))
	for n, l := range Levels {
		args = append(args, "-map", fmt.Sprintf("[l%d]", n))
		args = append(args, t.encodeArgs(l.CQ)...)
		args = append(args, "-f", "null", os.DevNull)
	}
	return args
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

var outputSize = regexp.MustCompile(`\[out#(\d+)/null[^\]]*\] video:(\d+)KiB`)

// ParseSampleSizes reads each output's video size from ffmpeg's summary.
func ParseSampleSizes(log string, outputs int) ([]int64, error) {
	sizes := make([]int64, outputs)
	found := 0
	for _, m := range outputSize.FindAllStringSubmatch(log, -1) {
		n, _ := strconv.Atoi(m[1])
		kib, _ := strconv.ParseInt(m[2], 10, 64)
		if n < outputs {
			sizes[n] = kib * 1024
			found++
		}
	}
	if found != outputs {
		return nil, fmt.Errorf("ffmpeg reported %d of %d sample sizes", found, outputs)
	}
	return sizes, nil
}

// PacketBytes adds up the sizes of the video packets ffprobe listed
// ("pts_time,size" lines) that fall in [start, start+secs): the original's
// share of a sample.
func PacketBytes(csv string, start, secs float64) int64 {
	var n int64
	for line := range strings.Lines(csv) {
		pts, size, ok := strings.Cut(strings.TrimSpace(line), ",")
		if !ok {
			continue
		}
		t, err1 := strconv.ParseFloat(pts, 64)
		b, err2 := strconv.ParseInt(strings.TrimSuffix(size, ","), 10, 64)
		if err1 == nil && err2 == nil && t >= start && t < start+secs {
			n += b
		}
	}
	return n
}

// sourceBytes asks ffprobe for the original's video bytes in a sample window.
func (t Tools) sourceBytes(ctx context.Context, src string, start, secs float64) (int64, error) {
	// The interval's end is absolute: a relative "+secs" counts from the
	// keyframe ffprobe seeks back to, which can be long before start. Read a
	// little past the window too, as packets aren't in display order.
	out, err := command(ctx, t.FFprobe, "-v", "error", "-select_streams", "v:0",
		"-read_intervals", fmt.Sprintf("%s%%%s", ftoa(start), ftoa(start+secs+2)),
		"-show_entries", "packet=pts_time,size", "-of", "csv=p=0", src).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe packets: %w", err)
	}
	return PacketBytes(string(out), start, secs), nil
}

// Project turns sample sizes into whole-file estimates. Audio is copied
// unchanged, so only the video part shrinks or grows by each level's ratio.
func Project(i Info, sourceBytes int64, sampled []int64, sampling, sampledSecs time.Duration) []Estimate {
	audio := min(int64(float64(i.AudioBitrate)/8*i.Duration), i.Size)
	video := i.Size - audio
	perLevel := sampling.Seconds() / sampledSecs.Seconds() / splitSpeedup
	var out []Estimate
	for n, l := range Levels {
		ratio := float64(sampled[n]) / float64(sourceBytes)
		out = append(out, Estimate{
			Level: l,
			Size:  audio + int64(math.Round(ratio*float64(video))),
			Time:  time.Duration(perLevel * i.Duration * float64(time.Second)),
		})
	}
	return out
}

// Progress reports how far an estimate or conversion has got.
type Progress func(done float64, note string)

// Estimate samples src at every level and projects the results.
func (t Tools) Estimate(ctx context.Context, i Info, src string, progress Progress) ([]Estimate, error) {
	points := SamplePoints(i.Duration)
	secs := min(SampleSecs, i.Duration)
	var source int64
	sampled := make([]int64, len(Levels))
	var took time.Duration
	for n, start := range points {
		progress(float64(n)/float64(len(points)), fmt.Sprintf("Encoding sample %d of %d…", n+1, len(points)))
		began := time.Now()
		log, err := command(ctx, t.FFmpeg, t.SampleArgs(i, src, start, secs)...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("ffmpeg sample: %w\n%s", err, lastLines(string(log), 4))
		}
		took += time.Since(began)
		sizes, err := ParseSampleSizes(string(log), len(Levels))
		if err != nil {
			return nil, err
		}
		for l := range sizes {
			sampled[l] += sizes[l]
		}
		b, err := t.sourceBytes(ctx, src, start, secs)
		if err != nil {
			return nil, err
		}
		source += b
	}
	if source == 0 {
		return nil, fmt.Errorf("couldn't measure the original's samples")
	}
	sampledSecs := time.Duration(secs * float64(len(points)) * float64(time.Second))
	return Project(i, source, sampled, took, sampledSecs), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
