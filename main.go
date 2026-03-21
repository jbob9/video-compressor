//go:build !js

package main

import (
	"flag"
)

// Package-level flags – registered once, parsed once.
var (
	useGUI       bool
	flagOutput   string
	flagLevel    string
	flagCodec    string
	flagRes      string
	flagAudio    string
	flagThreads  int
	flagConc     int
	flagHW       bool
	flagHelp     bool
)

func init() {
	flag.BoolVar(&useGUI, "gui", false, "Launch the web GUI")
	flag.StringVar(&flagOutput, "o", "", "Output directory (optional)")
	flag.StringVar(&flagLevel, "l", "normal", "Compression level: normal, high, very_high, maximum")
	flag.StringVar(&flagCodec, "codec", "h264", "Video codec: h264, h265")
	flag.StringVar(&flagRes, "r", "original", "Resolution: original, 1080p, 720p, 480p")
	flag.StringVar(&flagAudio, "ab", "128k", "Audio bitrate: 128k, 96k, 64k")
	flag.IntVar(&flagThreads, "t", 0, "Threads per file (0 = auto-detect)")
	flag.IntVar(&flagConc, "c", 1, "Max concurrent compressions")
	flag.BoolVar(&flagHW, "hw", false, "Enable hardware acceleration")
	flag.BoolVar(&flagHelp, "h", false, "Show help")
}

func main() {
	flag.Parse()

	if useGUI {
		runGUI()
	} else {
		runCLI()
	}
}
