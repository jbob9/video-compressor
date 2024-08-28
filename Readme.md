# Video Compressor

Video Compressor is a Go-based application that provides both a command-line interface (CLI) and a web-based graphical user interface (GUI) for compressing video files using FFmpeg.

## Prerequisites

Before you can build and run this application, you need to have the following installed on your system:

1. Go (version 1.16 or later): https://golang.org/dl/
2. FFmpeg: https://ffmpeg.org/download.html

Ensure that both Go and FFmpeg are properly installed and available in your system's PATH.

## Building the Application

1. Clone this repository or download the source code.
2. Open a terminal and navigate to the project directory.
3. Run the following command to build the application:

   ```
   go build -o videocompressor
   ```

   This will create an executable named `videocompressor` (or `videocompressor.exe` on Windows) in your project directory.

## Running the Application

### CLI Mode

To use the application in CLI mode, run:

```
./videocompressor [options] <input_file1> <input_file2> ...
```

Options:
- `-o`: Output directory (optional)
- `-l`: Compression level: normal, high, very_high, maximum (default: normal)
- `-t`: Number of threads to use per file (default: number of CPU cores)
- `-c`: Maximum number of files to process concurrently (default: 1)
- `-h`: Show help

Example:
```
./videocompressor -o /path/to/output -l high video1.mp4 video2.mp4
```

### GUI Mode

To use the application in GUI mode, run:

```
./videocompressor -gui
```

Then open your web browser and navigate to `http://localhost:8080`.

## Building for Different Operating Systems

To create executables for different operating systems:

1. For Windows:
   ```
   GOOS=windows GOARCH=amd64 go build -o videocompressor.exe
   ```

2. For macOS:
   ```
   GOOS=darwin GOARCH=amd64 go build -o videocompressor
   ```

3. For Linux:
   ```
   GOOS=linux GOARCH=amd64 go build -o videocompressor
   ```

## Creating a Distribution

To create a distributable version of your application:

1. Create a directory for your distribution (e.g., `videocompressor-dist`).
2. Copy the built executable into this directory.
3. Include this README file.
4. If possible, include a pre-built FFmpeg binary for the target operating system.
5. Zip the directory for distribution.

## Usage Instructions

1. Select one or more video files to compress.
2. Choose a compression level:
   - Normal: Balanced compression and quality
   - High: Higher compression, slightly lower quality
   - Very High: Even higher compression, lower quality
   - Maximum: Maximum compression, lowest quality
3. (Optional) Specify an output directory. If not specified, compressed videos will be saved in the same directory as the input files.
4. Click "Compress" to start the compression process.
5. Monitor the progress in the file list and feedback area.

## Notes

- The GUI mode runs a local web server. Ensure that port 8080 is available on your system.
- Large files may take a significant amount of time to upload and process, depending on your system's capabilities.
- The application currently supports .mp4, .avi, and .mov file formats.

## Troubleshooting

- If you encounter any "command not found" errors, ensure that FFmpeg is properly installed and available in your system's PATH.
- For any other issues, please check the error messages in the feedback area (GUI mode) or console output (CLI mode).

## License

[Specify your license here]

## Contributing

[If you want to accept contributions, specify how others can contribute to your project]
