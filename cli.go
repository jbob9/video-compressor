package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
)

func runCLI() {
	outputDir := flag.String("o", "", "Output directory (optional)")
	compressionLevel := flag.String("l", "normal", "Compression level: normal, high, very_high, maximum")
	threads := flag.Int("t", runtime.NumCPU(), "Number of threads to use per file")
	maxConcurrent := flag.Int("c", 1, "Maximum number of files to process concurrently")
	help := flag.Bool("h", false, "Show help")

	// Parse flags
	flag.Parse()

	// Show help if requested or if no input files are provided
	if *help || flag.NArg() == 0 {
		printUsage()
		return
	}

	// Get input files from remaining arguments
	inputFiles := flag.Args()

	// Create output directory if specified
	if *outputDir != "" {
		err := os.MkdirAll(*outputDir, os.ModePerm)
		if err != nil {
			fmt.Printf("Error creating output directory: %v\n", err)
			return
		}
	}

	// Use a wait group to wait for all goroutines to finish
	var wg sync.WaitGroup
	// Use a semaphore to limit the number of concurrent compressions
	semaphore := make(chan struct{}, *maxConcurrent)

	for _, inputFile := range inputFiles {
		wg.Add(1)
		go func(input string) {
			defer wg.Done()
			semaphore <- struct{}{} // Acquire semaphore
			defer func() { <-semaphore }() // Release semaphore

			outputPath := GetOutputPath(input, *compressionLevel, *outputDir)
			err := CompressVideo(input, outputPath, *compressionLevel, *threads)
			if err != nil {
				fmt.Printf("Error compressing %s: %v\n", input, err)
			} else {
				fmt.Printf("Successfully compressed %s to %s\n", input, outputPath)
			}
		}(inputFile)
	}

	wg.Wait()
	fmt.Println("All files processed.")
}
