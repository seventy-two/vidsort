// Package av1 estimates how much space converting a video to AV1 would
// save, and does the conversion, by running ffmpeg and ffprobe.
package av1

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Info is what ffprobe reports about a file.
type Info struct {
	Duration     float64 // seconds
	Size         int64   // bytes
	VideoCodec   string  // e.g. "hevc"
	PixFmt       string  // e.g. "yuv420p"
	Width        int
	Height       int
	FPS          float64
	AudioBitrate int64 // bits per second, all audio streams together; 0 if unknown
}

// Describe summarises the video stream, e.g. "HEVC 8192×4096 · 59.94 fps".
func (i Info) Describe() string {
	codec := strings.ToUpper(i.VideoCodec)
	if i.VideoCodec == "h264" {
		codec = "H.264"
	}
	return fmt.Sprintf("%s %d×%d · %s fps", codec, i.Width, i.Height, strconv.FormatFloat(i.FPS, 'f', -1, 64))
}

// Tools are the paths to ffmpeg and ffprobe, and the AV1 encoder to use.
type Tools struct {
	FFmpeg, FFprobe string
	// Encoder is "av1_nvenc" (NVIDIA GPU) or "libsvtav1" (CPU, used by the tests).
	Encoder string
	// GPUDecode lets NVIDIA's decoder feed the encoder directly, for codecs it supports.
	GPUDecode bool
}

// command builds an exec.Cmd, set up per platform (no console window on Windows).
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	return cmd
}

// Probe runs ffprobe on path.
func (t Tools) Probe(ctx context.Context, path string) (Info, error) {
	out, err := command(ctx, t.FFprobe, "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path).Output()
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe: %w", err)
	}
	return ParseProbe(out)
}

// ParseProbe reads ffprobe's JSON (-show_format -show_streams).
func ParseProbe(b []byte) (Info, error) {
	var p struct {
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
		} `json:"format"`
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			PixFmt       string `json:"pix_fmt"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
			BitRate      string `json:"bit_rate"`
			Disposition  struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return Info{}, fmt.Errorf("reading ffprobe output: %w", err)
	}
	var i Info
	i.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	i.Size, _ = strconv.ParseInt(p.Format.Size, 10, 64)
	for _, s := range p.Streams {
		switch {
		case s.CodecType == "video" && i.VideoCodec == "" && s.Disposition.AttachedPic == 0:
			i.VideoCodec, i.PixFmt, i.Width, i.Height = s.CodecName, s.PixFmt, s.Width, s.Height
			i.FPS = parseRate(s.AvgFrameRate)
		case s.CodecType == "audio":
			br, _ := strconv.ParseInt(s.BitRate, 10, 64)
			i.AudioBitrate += br
		}
	}
	if i.VideoCodec == "" {
		return i, fmt.Errorf("no video stream")
	}
	return i, nil
}

// parseRate turns "60000/1001" into 59.94 (to two places).
func parseRate(r string) float64 {
	num, den, ok := strings.Cut(r, "/")
	n, _ := strconv.ParseFloat(num, 64)
	d := 1.0
	if ok {
		d, _ = strconv.ParseFloat(den, 64)
	}
	if d == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(strconv.FormatFloat(n/d, 'f', 2, 64), 64)
	return f
}

// gpuCodecs and gpuPixFmts are what NVIDIA's decoder handles, so frames can
// stay on the GPU from decoder to encoder. Anything else decodes on the CPU.
var (
	gpuCodecs  = map[string]bool{"h264": true, "hevc": true, "av1": true, "vp9": true, "mpeg2video": true, "vc1": true}
	gpuPixFmts = map[string]bool{"yuv420p": true, "yuvj420p": true, "nv12": true, "yuv420p10le": true, "p010le": true}
)

// decodeArgs are the input options for decoding i.
func (t Tools) decodeArgs(i Info) []string {
	if t.GPUDecode && gpuCodecs[i.VideoCodec] && gpuPixFmts[i.PixFmt] {
		return []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
	}
	return nil
}

// encodeArgs are the video encoder options for a quality level.
func (t Tools) encodeArgs(cq int) []string {
	if t.Encoder == "libsvtav1" {
		return []string{"-c:v", "libsvtav1", "-preset", "12", "-crf", strconv.Itoa(cq)}
	}
	// Preset p5 with the hq tune: slower presets made 8K files no smaller
	// and took twice as long. Without a high -maxrate, NVENC's VBR caps the
	// bitrate at the AV1 level's limit (about 40 Mbps for 4K60), so every
	// quality level came out the same size.
	return []string{"-c:v", "av1_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", strconv.Itoa(cq),
		"-b:v", "0", "-maxrate", "1000M", "-bufsize", "2000M"}
}
