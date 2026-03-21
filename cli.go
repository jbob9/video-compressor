package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
)

func printUsage() {
	fmt.Println("Video Compressor — Fast video compression powered by FFmpeg")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  videocompressor [options] <file1> <file2> ...")
	fmt.Println("  videocompressor -gui")
	fmt.Println()
	fmt.Println("Options:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  videocompressor -l high video.mp4")
	fmt.Println("  videocompressor -codec h265 -r 720p -o ./output/ video1.mp4 video2.mp4")
	fmt.Println("  videocompressor -hw -l maximum -c 2 *.mp4")
	fmt.Println("  videocompressor -gui")
}

func runCLI() {
	if flagHelp || flag.NArg() == 0 {
		printUsage()
		return
	}

	inputFiles := flag.Args()

	if flagOutput != "" {
		if err := os.MkdirAll(flagOutput, os.ModePerm); err != nil {
			fmt.Printf("Error creating output directory: %v\n", err)
			return
		}
	}

	// Hardware acceleration info
	if flagHW {
		info := DetectHWAccel()
		switch {
		case info.NVENC:
			fmt.Println("✓ Hardware acceleration: NVENC (NVIDIA)")
		case info.VAAPI:
			fmt.Println("✓ Hardware acceleration: VAAPI")
		case info.QSV:
			fmt.Println("✓ Hardware acceleration: QSV (Intel)")
		default:
			fmt.Println("⚠ No hardware acceleration found — falling back to software encoding")
			flagHW = false
		}
	}

	fmt.Printf("Codec: %s | Level: %s | Resolution: %s\n\n", flagCodec, flagLevel, flagRes)

	var wg sync.WaitGroup
	sem := make(chan struct{}, flagConc)

	for _, input := range inputFiles {
		wg.Add(1)
		go func(in string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			outPath := GetOutputPath(in, flagLevel, flagOutput)
			opts := CompressOptions{
				InputPath:        in,
				OutputPath:       outPath,
				CompressionLevel: flagLevel,
				Codec:            flagCodec,
				Resolution:       flagRes,
				Threads:          flagThreads,
				HWAccel:          flagHW,
				AudioBitrate:     flagAudio,
			}

			fmt.Printf("Compressing: %s\n", in)
			if err := CompressVideoWithProgress(context.Background(), opts, nil); err != nil {
				fmt.Printf("✕ Error compressing %s: %v\n", in, err)
			} else {
				fmt.Printf("✓ Compressed %s → %s\n", in, outPath)
			}
		}(input)
	}

	wg.Wait()
	fmt.Println("\nAll files processed.")
}
