package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func printUsage() {
	fmt.Println("Usage: videocompressor [options] <input_file1> <input_file2> ...")
	fmt.Println("\nOptions:")
	flag.PrintDefaults()
}

func GetOutputPath(inputPath, compressionLevel, outputDir string) string {
	filename := filepath.Base(inputPath)
	ext := filepath.Ext(filename)
	name := filename[:len(filename)-len(ext)]
	newFilename := fmt.Sprintf("%s_%s_compressed%s", name, compressionLevel, ext)

	if outputDir != "" {
		return filepath.Join(outputDir, newFilename)
	}
	
	return filepath.Join(filepath.Dir(inputPath), newFilename)
}

func CompressVideo(inputPath, outputPath, compressionLevel string, threads int) error {
	var crf, preset string

	switch compressionLevel {
	case "high":
		crf = "28"
		preset = "fast"
	case "very_high":
		crf = "30"
		preset = "faster"
	case "maximum":
		crf = "32"
		preset = "veryfast"
	default: // normal
		crf = "23"
		preset = "ultrafast"
	}

	cmd := exec.Command("ffmpeg",
		"-i", inputPath,
		"-c:v", "libx264",
		"-crf", crf,
		"-preset", preset,
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart",
		"-threads", fmt.Sprintf("%d", threads),
		"-tune", "fastdecode",
		"-max_muxing_queue_size", "9999",
		outputPath)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}