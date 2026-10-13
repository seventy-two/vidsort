package av1

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const probeJSON = `{
  "streams": [
    {"codec_type": "video", "codec_name": "mjpeg", "width": 300, "height": 300, "avg_frame_rate": "0/0", "disposition": {"attached_pic": 1}},
    {"codec_type": "video", "codec_name": "hevc", "pix_fmt": "yuv420p", "width": 8192, "height": 4096, "avg_frame_rate": "60000/1001", "disposition": {"attached_pic": 0}},
    {"codec_type": "audio", "codec_name": "aac", "bit_rate": "256000"},
    {"codec_type": "audio", "codec_name": "aac", "bit_rate": "64000"}
  ],
  "format": {"duration": "3769.950000", "size": "30495114510"}
}`

func TestParseProbe(t *testing.T) {
	i, err := ParseProbe([]byte(probeJSON))
	require.NoError(t, err)
	assert.Equal(t, Info{
		Duration: 3769.95, Size: 30495114510, VideoCodec: "hevc", PixFmt: "yuv420p",
		Width: 8192, Height: 4096, FPS: 59.94, AudioBitrate: 320000,
	}, i, "the cover picture isn't the video")
	assert.Equal(t, "HEVC 8192×4096 · 59.94 fps", i.Describe())

	_, err = ParseProbe([]byte(`{"streams": [{"codec_type": "audio"}], "format": {}}`))
	assert.ErrorContains(t, err, "no video")
}

func TestSamplePoints(t *testing.T) {
	assert.Equal(t, []float64{0}, SamplePoints(10))
	assert.Equal(t, []float64{20, 50, 80}, SamplePoints(100))
}

func TestSampleArgs(t *testing.T) {
	tools := Tools{FFmpeg: "ffmpeg", Encoder: "av1_nvenc", GPUDecode: true}
	i := Info{VideoCodec: "hevc", PixFmt: "yuv420p10le"}
	args := tools.SampleArgs(i, "in.mp4", 12.5, 3)
	assert.Equal(t, []string{"-hide_banner", "-nostdin", "-hwaccel", "cuda", "-hwaccel_output_format", "cuda",
		"-ss", "12.500", "-t", "3.000", "-i", "in.mp4", "-filter_complex", "[0:v:0]split=3[l0][l1][l2]",
		"-map", "[l0]", "-c:v", "av1_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "28", "-b:v", "0", "-maxrate", "1000M", "-bufsize", "2000M", "-f", "null", os.DevNull,
		"-map", "[l1]", "-c:v", "av1_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "32", "-b:v", "0", "-maxrate", "1000M", "-bufsize", "2000M", "-f", "null", os.DevNull,
		"-map", "[l2]", "-c:v", "av1_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "36", "-b:v", "0", "-maxrate", "1000M", "-bufsize", "2000M", "-f", "null", os.DevNull,
	}, args)

	// The GPU decoder can't take 4:2:2, so that decodes on the CPU.
	args = tools.SampleArgs(Info{VideoCodec: "h264", PixFmt: "yuv422p"}, "in.mp4", 0, 3)
	assert.NotContains(t, args, "-hwaccel")
}

func TestParseSampleSizes(t *testing.T) {
	log := `frame=  180 fps= 14
[out#0/null @ 0000026282806f80] video:37657KiB audio:0KiB subtitle:0KiB other streams:0KiB
[out#1/null @ 0000026282804a40] video:33658KiB audio:0KiB
[out#2/null @ 0000026282802900] video:21960KiB audio:0KiB`
	sizes, err := ParseSampleSizes(log, 3)
	require.NoError(t, err)
	assert.Equal(t, []int64{37657 * 1024, 33658 * 1024, 21960 * 1024}, sizes)

	_, err = ParseSampleSizes("[out#0/null @ 1] video:5KiB", 3)
	assert.Error(t, err)
}

func TestPacketBytes(t *testing.T) {
	csv := "9.900000,500\n10.000000,1000,\n11.500000,200\n12.999000,300\n13.000000,9999\nN/A,7\n"
	assert.Equal(t, int64(1500), PacketBytes(csv, 10, 3))
}

func TestProject(t *testing.T) {
	// 100s, 1 MB of which is audio (80 kb/s); samples at 0.5×, 1× and 1.5× the original.
	i := Info{Duration: 100, Size: 11_000_000, AudioBitrate: 80_000}
	est := Project(i, 1000, []int64{500, 1000, 1500}, 24*time.Second, 9*time.Second)
	require.Len(t, est, 3)
	assert.Equal(t, int64(6_000_000), est[0].Size)
	assert.Equal(t, int64(11_000_000), est[1].Size)
	assert.Equal(t, int64(16_000_000), est[2].Size)
	assert.InDelta(t, 0.4545, est[0].Saving(i.Size), 0.001)
	assert.InDelta(t, -0.4545, est[2].Saving(i.Size), 0.001)
	// 24s to sample 9s three ways is 24/9/2.4 seconds per second for one.
	assert.InDelta(t, (100 * 24.0 / 9 / 2.4), est[0].Time.Seconds(), 0.01)
	assert.Equal(t, "High quality", est[0].Name)
}

func TestOutputs(t *testing.T) {
	dir := t.TempDir()
	final, part, format := Outputs(filepath.Join(dir, "a.MP4"))
	assert.Equal(t, filepath.Join(dir, "a.mp4"), final)
	assert.Equal(t, final+".vidsort-part", part)
	assert.Equal(t, "mp4", format)

	final, _, format = Outputs(filepath.Join(dir, "b.avi"))
	assert.Equal(t, filepath.Join(dir, "b.mkv"), final)
	assert.Equal(t, "matroska", format)

	// Never overwrite a different file that happens to have the new name.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.mkv"), nil, 0o644))
	final, _, _ = Outputs(filepath.Join(dir, "c.wmv"))
	assert.Equal(t, filepath.Join(dir, "c (AV1).mkv"), final)
}

func TestConvertArgs(t *testing.T) {
	tools := Tools{Encoder: "av1_nvenc"}
	args := tools.ConvertArgs(Info{VideoCodec: "wmv3"}, "in.wmv", "out.part", "matroska", 32)
	assert.Equal(t, []string{"-hide_banner", "-nostdin", "-y", "-i", "in.wmv", "-map", "0:v:0", "-map", "0:a?",
		"-map", "0:s?", "-c:s", "copy",
		"-c:v", "av1_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "32", "-b:v", "0", "-maxrate", "1000M", "-bufsize", "2000M",
		"-c:a", "copy", "-map_metadata", "0", "-f", "matroska", "-progress", "pipe:1", "-nostats", "out.part"}, args)

	args = tools.ConvertArgs(Info{VideoCodec: "h264"}, "in.mp4", "out.part", "mp4", 28)
	assert.Contains(t, args, "unofficial")
	assert.NotContains(t, args, "0:s?")
}

func TestParseProgress(t *testing.T) {
	pos, ok := ParseProgress("out_time_us=12500000\n")
	assert.True(t, ok)
	assert.Equal(t, 12.5, pos)
	_, ok = ParseProgress("out_time_us=N/A")
	assert.False(t, ok)
	_, ok = ParseProgress("speed=1.2x")
	assert.False(t, ok)
}

func TestVerify(t *testing.T) {
	src := Info{Duration: 600}
	assert.NoError(t, Verify(src, Info{VideoCodec: "av1", Duration: 601.5}))
	assert.ErrorContains(t, Verify(src, Info{VideoCodec: "av1", Duration: 590}), "long")
	assert.ErrorContains(t, Verify(src, Info{VideoCodec: "hevc", Duration: 600}), "not AV1")
}

func TestFormatDuration(t *testing.T) {
	assert.Equal(t, "45s", FormatDuration(45*time.Second))
	assert.Equal(t, "12 min", FormatDuration(12*time.Minute+30*time.Second))
	assert.Equal(t, "1 h 50 min", FormatDuration(110*time.Minute))
}

// localTools uses this machine's ffmpeg with the CPU AV1 encoder, or skips.
func localTools(t *testing.T) Tools {
	ff, err1 := exec.LookPath("ffmpeg")
	fp, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("needs ffmpeg and ffprobe")
	}
	if out, _ := exec.Command(ff, "-hide_banner", "-encoders").Output(); !strings.Contains(string(out), "libsvtav1") {
		t.Skip("needs ffmpeg with libsvtav1")
	}
	return Tools{FFmpeg: ff, FFprobe: fp, Encoder: "libsvtav1"}
}

// makeVideo writes a noisy (hard to compress) H.264 test video with audio.
func makeVideo(t *testing.T, path string, secs int, bitrate string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30,noise=alls=20:allf=t",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-t", strconv.Itoa(secs), "-c:v", "libx264", "-preset", "ultrafast", "-b:v", bitrate,
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path).CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestEstimateAndConvert(t *testing.T) {
	tools := localTools(t)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "big.mp4")
	makeVideo(t, src, 20, "20M") // far more bitrate than 360p needs
	old := time.Date(2020, 5, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(src, old, old))

	i, err := tools.Probe(ctx, src)
	require.NoError(t, err)
	var notes []string
	est, err := tools.Estimate(ctx, i, src, func(_ float64, note string) { notes = append(notes, note) })
	require.NoError(t, err)
	assert.Equal(t, []string{"Encoding sample 1 of 3…", "Encoding sample 2 of 3…", "Encoding sample 3 of 3…"}, notes)
	require.Len(t, est, 3)
	assert.Greater(t, est[0].Saving(i.Size), 0.0, "an overfed 360p video shrinks even at the highest quality")
	assert.Greater(t, est[0].Size, est[2].Size, "higher quality is bigger")

	var last float64
	res, err := tools.Convert(ctx, i, src, 32, func(done float64, _ string) { last = done })
	require.NoError(t, err)
	assert.Equal(t, src, res.Path, "same name, MP4 kept")
	assert.Equal(t, i.Size, res.OldSize)
	assert.Less(t, res.NewSize, res.OldSize)
	assert.InDelta(t, 1, last, 0.05)

	out, err := tools.Probe(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, "av1", out.VideoCodec)
	assert.NotZero(t, out.AudioBitrate, "audio copied")
	st, err := os.Stat(src)
	require.NoError(t, err)
	assert.True(t, st.ModTime().Equal(old), "date kept")
	assert.InDelta(t, float64(res.NewSize)/float64(res.OldSize), float64(est[1].Size)/float64(i.Size), 0.15,
		"the estimate is close to what happened")
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "no partial file left")
}

func TestConvertKeepsOriginalWhenNotSmaller(t *testing.T) {
	tools := localTools(t)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "tiny.avi")
	makeVideo(t, src, 3, "50k") // starved: AV1 at high quality can only grow it
	before, err := os.ReadFile(src)
	require.NoError(t, err)

	i, err := tools.Probe(ctx, src)
	require.NoError(t, err)
	_, err = tools.Convert(ctx, i, src, 10, func(float64, string) {})
	assert.ErrorIs(t, err, ErrNotSmaller)
	after, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Equal(t, before, after, "original untouched")
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "no partial or .mkv left")
}

func TestConvertCancelled(t *testing.T) {
	tools := localTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "v.mp4")
	makeVideo(t, src, 20, "5M")
	i, err := tools.Probe(context.Background(), src)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	_, err = tools.Convert(ctx, i, src, 32, func(done float64, _ string) {
		if done > 0.1 {
			cancel()
		}
	})
	assert.ErrorIs(t, err, context.Canceled)
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "partial file removed, original kept")
}
