package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Structs
// ---------------------------------------------------------------------------

// VideoInfo holds metadata about a video file (populated by ffprobe).
type VideoInfo struct {
	Duration   float64 `json:"duration"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	VideoCodec string  `json:"videoCodec"`
	AudioCodec string  `json:"audioCodec"`
	Bitrate    int64   `json:"bitrate"`
	FileSize   int64   `json:"fileSize"`
	FrameRate  float64 `json:"frameRate"`
}

// CompressOptions holds every knob for a compression job.
type CompressOptions struct {
	InputPath        string
	OutputPath       string
	CompressionLevel string // normal, high, very_high, maximum
	Codec            string // h264, h265
	Resolution       string // original, 1080p, 720p, 480p
	Threads          int
	HWAccel          bool
	AudioBitrate     string // 128k, 96k, 64k
}

// CompressProgress is emitted while ffmpeg is running.
type CompressProgress struct {
	Frame   int     `json:"frame"`
	FPS     float64 `json:"fps"`
	Size    string  `json:"size"`
	Time    string  `json:"time"`
	Bitrate string  `json:"bitrate"`
	Speed   string  `json:"speed"`
	Percent float64 `json:"percent"`
}

// HWAccelInfo reports which hardware encoders ffmpeg can see.
type HWAccelInfo struct {
	NVENC    bool `json:"nvenc"`
	VAAPI    bool `json:"vaapi"`
	QSV      bool `json:"qsv"`
	Detected bool `json:"detected"`
}

// ---------------------------------------------------------------------------
// Hardware-acceleration detection (runs once)
// ---------------------------------------------------------------------------

var (
	hwAccelCache HWAccelInfo
	hwAccelOnce  sync.Once
)

// DetectHWAccel probes ffmpeg for hardware encoder support.
func DetectHWAccel() HWAccelInfo {
	hwAccelOnce.Do(func() {
		cmd := exec.Command("ffmpeg", "-hide_banner", "-encoders")
		out, err := cmd.Output()
		if err != nil {
			hwAccelCache.Detected = true
			return
		}
		s := string(out)
		hwAccelCache.NVENC = strings.Contains(s, "h264_nvenc")
		hwAccelCache.VAAPI = strings.Contains(s, "h264_vaapi")
		hwAccelCache.QSV = strings.Contains(s, "h264_qsv")
		hwAccelCache.Detected = true
	})
	return hwAccelCache
}

// ---------------------------------------------------------------------------
// Video probe (ffprobe)
// ---------------------------------------------------------------------------

// ProbeVideo reads video metadata via ffprobe.
func ProbeVideo(path string) (*VideoInfo, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}

	var raw struct {
		Streams []struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			RFrameRate string `json:"r_frame_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe parse: %w", err)
	}

	info := &VideoInfo{}
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			info.VideoCodec = s.CodecName
			info.Width = s.Width
			info.Height = s.Height
			if parts := strings.Split(s.RFrameRate, "/"); len(parts) == 2 {
				num, _ := strconv.ParseFloat(parts[0], 64)
				den, _ := strconv.ParseFloat(parts[1], 64)
				if den > 0 {
					info.FrameRate = num / den
				}
			}
		case "audio":
			info.AudioCodec = s.CodecName
		}
	}
	info.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	info.FileSize, _ = strconv.ParseInt(raw.Format.Size, 10, 64)
	info.Bitrate, _ = strconv.ParseInt(raw.Format.BitRate, 10, 64)
	return info, nil
}

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

// GetOutputPath builds the output file path from the input path.
func GetOutputPath(inputPath, level, outputDir string) string {
	base := filepath.Base(inputPath)
	ext := filepath.Ext(base)
	name := base[:len(base)-len(ext)]
	out := fmt.Sprintf("%s_%s_compressed%s", name, level, ext)
	if outputDir != "" {
		return filepath.Join(outputDir, out)
	}
	return filepath.Join(filepath.Dir(inputPath), out)
}

// GetDefaultOutputDir returns ~/Downloads if it exists, else ~/, else ".".
func GetDefaultOutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	dl := filepath.Join(home, "Downloads")
	if fi, err := os.Stat(dl); err == nil && fi.IsDir() {
		return dl
	}
	return home
}

// ---------------------------------------------------------------------------
// FFmpeg argument builder
// ---------------------------------------------------------------------------

func buildFFmpegArgs(opts CompressOptions) []string {
	hw := DetectHWAccel()
	useHW := opts.HWAccel && hw.Detected

	args := []string{"-i", opts.InputPath, "-y"}

	codec := opts.Codec
	if codec == "" {
		codec = "h264"
	}

	var vf []string
	isVAAPI := false

	// --- encoder + quality ---------------------------------------------------
	switch codec {
	case "h265":
		switch {
		case useHW && hw.NVENC:
			args = append(args, "-c:v", "hevc_nvenc")
			args = append(args, nvencQuality(opts.CompressionLevel)...)
		case useHW && hw.VAAPI:
			isVAAPI = true
			args = append(args, "-vaapi_device", "/dev/dri/renderD128",
				"-c:v", "hevc_vaapi")
			args = append(args, vaapiQuality(opts.CompressionLevel)...)
		default:
			args = append(args, "-c:v", "libx265")
			args = append(args, softH265Quality(opts.CompressionLevel)...)
			args = append(args, "-tag:v", "hvc1") // Apple compat
		}
	default: // h264
		switch {
		case useHW && hw.NVENC:
			args = append(args, "-c:v", "h264_nvenc")
			args = append(args, nvencQuality(opts.CompressionLevel)...)
		case useHW && hw.VAAPI:
			isVAAPI = true
			args = append(args, "-vaapi_device", "/dev/dri/renderD128",
				"-c:v", "h264_vaapi")
			args = append(args, vaapiQuality(opts.CompressionLevel)...)
		default:
			args = append(args, "-c:v", "libx264")
			args = append(args, softH264Quality(opts.CompressionLevel)...)
		}
	}

	// --- resolution scaling --------------------------------------------------
	if opts.Resolution != "" && opts.Resolution != "original" {
		h := targetHeight(opts.Resolution)
		if h > 0 {
			if isVAAPI {
				vf = append(vf, fmt.Sprintf("format=nv12,hwupload,scale_vaapi=w=-2:h=%d", h))
			} else {
				vf = append(vf, fmt.Sprintf("scale=-2:%d", h))
			}
		}
	} else if isVAAPI {
		vf = append(vf, "format=nv12,hwupload")
	}

	if len(vf) > 0 {
		args = append(args, "-vf", strings.Join(vf, ","))
	}

	// --- audio ---------------------------------------------------------------
	ab := opts.AudioBitrate
	if ab == "" {
		ab = "128k"
	}
	args = append(args, "-c:a", "aac", "-b:a", ab)

	// --- general -------------------------------------------------------------
	args = append(args, "-movflags", "+faststart")

	threads := opts.Threads
	if threads <= 0 {
		threads = runtime.NumCPU()
	}
	args = append(args, "-threads", fmt.Sprintf("%d", threads))

	args = append(args, opts.OutputPath)
	return args
}

// --- quality helpers (FIXED: slower preset = better compression) ----------

func softH264Quality(level string) []string {
	switch level {
	case "high":
		return []string{"-crf", "28", "-preset", "slow"}
	case "very_high":
		return []string{"-crf", "30", "-preset", "slower"}
	case "maximum":
		return []string{"-crf", "32", "-preset", "veryslow"}
	default: // normal
		return []string{"-crf", "23", "-preset", "medium"}
	}
}

func softH265Quality(level string) []string {
	switch level {
	case "high":
		return []string{"-crf", "30", "-preset", "medium"}
	case "very_high":
		return []string{"-crf", "33", "-preset", "slow"}
	case "maximum":
		return []string{"-crf", "36", "-preset", "slower"}
	default: // normal
		return []string{"-crf", "26", "-preset", "medium"}
	}
}

func nvencQuality(level string) []string {
	switch level {
	case "high":
		return []string{"-cq", "28", "-preset", "p5"}
	case "very_high":
		return []string{"-cq", "30", "-preset", "p6"}
	case "maximum":
		return []string{"-cq", "32", "-preset", "p7"}
	default: // normal
		return []string{"-cq", "23", "-preset", "p4"}
	}
}

func vaapiQuality(level string) []string {
	switch level {
	case "high":
		return []string{"-qp", "28"}
	case "very_high":
		return []string{"-qp", "30"}
	case "maximum":
		return []string{"-qp", "32"}
	default:
		return []string{"-qp", "23"}
	}
}

func targetHeight(res string) int {
	switch res {
	case "1080p":
		return 1080
	case "720p":
		return 720
	case "480p":
		return 480
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Compression with real-time progress
// ---------------------------------------------------------------------------

// CompressVideoWithProgress runs ffmpeg with optional progress reporting and
// context-based cancellation.
func CompressVideoWithProgress(ctx context.Context, opts CompressOptions, progressCb func(CompressProgress)) error {
	videoInfo, _ := ProbeVideo(opts.InputPath)

	args := buildFFmpegArgs(opts)

	// Insert -progress pipe:1 before the output path when a callback is given
	if progressCb != nil {
		out := args[len(args)-1]
		args = args[:len(args)-1]
		args = append(args, "-progress", "pipe:1", out)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	// Graceful cancel: SIGINT first, then kill after 5 s
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return cmd.Process.Signal(os.Interrupt)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second

	// ---------- with progress callback ------------------------------------
	if progressCb != nil {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("stdout pipe: %w", err)
		}
		var stderrBuf bytes.Buffer
		cmd.Stderr = &stderrBuf

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start ffmpeg: %w", err)
		}

		// Parse -progress output in background
		done := make(chan struct{})
		go func() {
			defer close(done)
			scanner := bufio.NewScanner(stdout)
			p := CompressProgress{}
			for scanner.Scan() {
				kv := strings.SplitN(scanner.Text(), "=", 2)
				if len(kv) != 2 {
					continue
				}
				key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
				switch key {
				case "frame":
					p.Frame, _ = strconv.Atoi(val)
				case "fps":
					p.FPS, _ = strconv.ParseFloat(val, 64)
				case "total_size":
					sz, _ := strconv.ParseInt(val, 10, 64)
					p.Size = formatFileSize(sz)
				case "out_time":
					p.Time = val
					if videoInfo != nil && videoInfo.Duration > 0 {
						sec := parseFFmpegTime(val)
						p.Percent = (sec / videoInfo.Duration) * 100
						if p.Percent > 100 {
							p.Percent = 100
						}
					}
				case "bitrate":
					p.Bitrate = val
				case "speed":
					p.Speed = val
				case "progress":
					if val == "end" {
						p.Percent = 100
					}
					progressCb(p)
				}
			}
		}()

		<-done // wait for all stdout to be consumed
		err = cmd.Wait()

		if ctx.Err() != nil {
			os.Remove(opts.OutputPath)
			return fmt.Errorf("compression cancelled")
		}
		if err != nil {
			msg := strings.TrimSpace(stderrBuf.String())
			if msg != "" {
				return fmt.Errorf("ffmpeg: %s", lastLines(msg, 5))
			}
			return err
		}
		return nil
	}

	// ---------- without progress (CLI pass-through) -----------------------
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CompressVideo is the simple backward-compatible wrapper.
func CompressVideo(inputPath, outputPath, compressionLevel string, threads int) error {
	return CompressVideoWithProgress(context.Background(), CompressOptions{
		InputPath:        inputPath,
		OutputPath:       outputPath,
		CompressionLevel: compressionLevel,
		Codec:            "h264",
		Threads:          threads,
	}, nil)
}

// ---------------------------------------------------------------------------
// Tiny helpers
// ---------------------------------------------------------------------------

func parseFFmpegTime(s string) float64 {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, _ := strconv.ParseFloat(parts[0], 64)
	m, _ := strconv.ParseFloat(parts[1], 64)
	sec, _ := strconv.ParseFloat(parts[2], 64)
	return h*3600 + m*60 + sec
}

func formatFileSize(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
