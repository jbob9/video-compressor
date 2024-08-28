//go:build !js

package main

import (
	"flag"
)

// case "high":
// 		crf = "28"
// 		preset = "slow"
// 	case "very_high":
// 		crf = "30"
// 		preset = "slower"
// 	case "maximum":
// 		crf = "32"
// 		preset = "veryslow"
// 	default: // normal
// 		crf = "23"
// 		preset = "medium"
// go build -o videocompressor
//  ./videocompressor -i input.mp4 -o output.mp4 -l high -t 4
//    ./videocompressor -i input.mp4 -l very_high
//    ./videocompressor -h
//    ./videocompressor -l high -t 4 -c 2 -o /path/to/output/dir file1.mp4 file2.mp4 file3.mp4
var useGUI bool

func init() {
	flag.BoolVar(&useGUI, "gui", false, "Use GUI instead of CLI")
}

func main() {
	flag.Parse()

	if useGUI {
		runGUI()
	} else {
		runCLI()
	}
}
