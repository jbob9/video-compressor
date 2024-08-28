package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const indexHTML = `
<!DOCTYPE html>
<html>
<head>
    <title>Video Compressor</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            min-height: 100vh;
            margin: 0;
            background-color: #f0f0f0;
            padding: 20px;
            box-sizing: border-box;
        }
        .container {
            background-color: white;
            padding: 2rem;
            border-radius: 8px;
            box-shadow: 0 0 10px rgba(0,0,0,0.1);
            width: 100%;
            max-width: 600px;
        }
        h1 {
            text-align: center;
            color: #333;
            margin-bottom: 1.5rem;
        }
        form {
            display: flex;
            flex-direction: column;
            gap: 1rem;
        }
        input[type="file"] {
            display: none;
        }
        .file-input-label {
            background-color: #4CAF50;
            color: white;
            padding: 10px 15px;
            border-radius: 4px;
            cursor: pointer;
            text-align: center;
            transition: background-color 0.3s;
        }
        .file-input-label:hover {
            background-color: #45a049;
        }
        select, input[type="text"], input[type="submit"] {
            padding: 10px;
            border: 1px solid #ddd;
            border-radius: 4px;
            font-size: 14px;
        }
        input[type="submit"] {
            background-color: #008CBA;
            color: white;
            cursor: pointer;
            transition: background-color 0.3s;
        }
        input[type="submit"]:hover {
            background-color: #007B9A;
        }
        #feedback {
            margin-top: 1rem;
            padding: 10px;
            border-radius: 4px;
            background-color: #f8f8f8;
            border: 1px solid #ddd;
            max-height: 150px;
            overflow-y: auto;
        }
        #fileList {
            margin-top: 10px;
            padding: 10px;
            border: 1px solid #ddd;
            border-radius: 4px;
            max-height: 200px;
            overflow-y: auto;
        }
        .file-item {
            display: flex;
            align-items: center;
            margin-bottom: 10px;
            padding: 8px;
            border: 1px solid #eee;
            border-radius: 4px;
            transition: background-color 0.3s;
        }
        .file-item .file-name {
            flex-grow: 1;
            margin-right: 10px;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }
        .file-item progress {
            width: 100px;
        }
        .file-item.processed {
            background-color: #e6ffe6;
            border-color: #4CAF50;
        }
        .file-item.error {
            background-color: #ffe6e6;
            border-color: #f44336;
        }
        .success-message {
            color: #4CAF50;
        }
        .error-message {
            color: #f44336;
        }
        .file-status {
            margin-left: 10px;
            font-size: 0.9em;
            color: #666;
        }
        .file-item.processed .file-status {
            color: #4CAF50;
        }
        .file-item.error .file-status {
            color: #f44336;
        }
        #feedback p {
            margin: 5px 0;
        }
        .processed-files-list {
            list-style-type: none;
            padding-left: 0;
            margin-top: 10px;
        }
        .processed-files-list li {
            margin-bottom: 5px;
            padding: 5px;
            border-radius: 4px;
            background-color: #f0f0f0;
        }
        .processed-files-list .success-message {
            color: #4CAF50;
        }
        .processed-files-list .error-message {
            color: #f44336;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Video Compressor</h1>
        <form id="compressForm" action="/compress" method="post" enctype="multipart/form-data">
            <label for="fileInput" class="file-input-label">Choose Files</label>
            <input id="fileInput" type="file" name="videos" multiple accept=".mp4,.avi,.mov">
            <div id="fileList"></div>
            <select name="compressionLevel">
                <option value="normal">Normal</option>
                <option value="high">High</option>
                <option value="very_high">Very High</option>
                <option value="maximum">Maximum</option>
            </select>
            <input type="text" name="outputDir" placeholder="Output directory (optional)">
            <input type="submit" value="Compress">
        </form>
        <div id="feedback"></div>
    </div>

    <script>
        document.getElementById('fileInput').addEventListener('change', function(e) {
            var fileList = document.getElementById('fileList');
            fileList.innerHTML = '';
            if (this.files.length > 0) {
                for (var i = 0; i < this.files.length; i++) {
                    var fileItem = document.createElement('div');
                    fileItem.className = 'file-item';
                    
                    var fileName = document.createElement('span');
                    fileName.className = 'file-name';
                    fileName.textContent = this.files[i].name;
                    
                    var progressBar = document.createElement('progress');
                    progressBar.max = 100;
                    progressBar.value = 0;
                    
                    var status = document.createElement('span');
                    status.className = 'file-status';
                    status.textContent = 'Pending';
                    
                    fileItem.appendChild(fileName);
                    fileItem.appendChild(progressBar);
                    fileItem.appendChild(status);
                    fileList.appendChild(fileItem);
                }
                document.querySelector('.file-input-label').innerHTML = this.files.length + ' file(s) selected';
            } else {
                document.querySelector('.file-input-label').innerHTML = 'Choose Files';
            }
        });

        document.getElementById('compressForm').addEventListener('submit', function(e) {
            e.preventDefault();
            var files = document.getElementById('fileInput').files;
            var feedback = document.getElementById('feedback');
            feedback.innerHTML = '';
            
            var totalFiles = files.length;
            var processedFiles = 0;
            var processedFilesList = document.createElement('ul');
            processedFilesList.className = 'processed-files-list';
            feedback.appendChild(processedFilesList);
            
            function updateOverallStatus() {
                var statusMessage = feedback.querySelector('p') || document.createElement('p');
                if (processedFiles === totalFiles) {
                    statusMessage.innerHTML = '<strong class="success-message">All ' + totalFiles + ' file(s) processed.</strong>';
                } else {
                    statusMessage.textContent = 'Processing ' + (processedFiles + 1) + ' of ' + totalFiles + ' file(s)...';
                }
                if (!statusMessage.parentNode) {
                    feedback.insertBefore(statusMessage, processedFilesList);
                }
            }
            
            updateOverallStatus();

            for (var i = 0; i < files.length; i++) {
                uploadFile(files[i], i);
            }

            function uploadFile(file, index) {
                var xhr = new XMLHttpRequest();
                var formData = new FormData();
                formData.append('videos', file);
                formData.append('compressionLevel', document.querySelector('select[name="compressionLevel"]').value);
                var outputDir = document.querySelector('input[name="outputDir"]').value;
                formData.append('outputDir', outputDir || ''); // Send empty string if no directory is selected

                xhr.open('POST', '/compress', true);

                var fileItem = document.getElementById('fileList').children[index];
                var progressBar = fileItem.querySelector('progress');
                var status = fileItem.querySelector('.file-status');

                xhr.upload.onprogress = function(e) {
                    if (e.lengthComputable) {
                        var percentComplete = (e.loaded / e.total) * 100;
                        progressBar.value = percentComplete;
                        status.textContent = 'Uploading: ' + percentComplete.toFixed(0) + '%';
                    }
                };

                xhr.onload = function() {
                    processedFiles++;
                    
                    var listItem = document.createElement('li');
                    if (xhr.status === 200) {
                        fileItem.classList.add('processed');
                        progressBar.value = 100;
                        status.textContent = 'Compressed';
                        listItem.innerHTML = '<span class="success-message">' + file.name + ' - Compressed successfully</span>';
                    } else {
                        fileItem.classList.add('error');
                        status.textContent = 'Error';
                        listItem.innerHTML = '<span class="error-message">' + file.name + ' - Error: ' + xhr.statusText + '</span>';
                    }
                    processedFilesList.appendChild(listItem);
                    
                    updateOverallStatus();
                };

                xhr.onerror = function() {
                    processedFiles++;
                    
                    fileItem.classList.add('error');
                    status.textContent = 'Upload Error';
                    var listItem = document.createElement('li');
                    listItem.innerHTML = '<span class="error-message">' + file.name + ' - Upload Error</span>';
                    processedFilesList.appendChild(listItem);
                    
                    updateOverallStatus();
                };

                xhr.send(formData);
            }
        });
    </script>
</body>
</html>
`

func handleIndex(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl.Execute(w, nil)
}

func handleCompress(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    err := r.ParseMultipartForm(1 << 30) // 1 GB
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    file, header, err := r.FormFile("videos")
    if err != nil {
        http.Error(w, "No file uploaded", http.StatusBadRequest)
        return
    }
    defer file.Close()

    compressionLevel := r.FormValue("compressionLevel")
    outputDir := r.FormValue("outputDir")

    if outputDir == "" {
        outputDir = filepath.Join(os.Getenv("USERPROFILE"), "Downloads")
    }

    err = os.MkdirAll(outputDir, os.ModePerm)
    if err != nil {
        http.Error(w, fmt.Sprintf("Error creating output directory: %v", err), http.StatusInternalServerError)
        return
    }

    tempFilePath := filepath.Join(os.TempDir(), header.Filename)
    tempFile, err := os.Create(tempFilePath)
    if err != nil {
        http.Error(w, fmt.Sprintf("Error creating temp file: %v", err), http.StatusInternalServerError)
        return
    }
    defer tempFile.Close()
    defer os.Remove(tempFilePath)

    _, err = io.Copy(tempFile, file)
    if err != nil {
        http.Error(w, fmt.Sprintf("Error copying file: %v", err), http.StatusInternalServerError)
        return
    }

    outputPath := GetOutputPath(tempFilePath, compressionLevel, outputDir)
    err = CompressVideo(tempFilePath, outputPath, compressionLevel, 4)
    if err != nil {
        http.Error(w, fmt.Sprintf("Error compressing %s: %v", header.Filename, err), http.StatusInternalServerError)
        return
    }

    fmt.Fprintf(w, "Successfully compressed %s to %s", header.Filename, outputPath)
}

func runGUI() {
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/compress", handleCompress)

	server := &http.Server{
		Addr:           ":8080",
		MaxHeaderBytes: 1 << 30, // 1 GB
		ReadTimeout:    10 * 60 * time.Second, // 10 minutes
		WriteTimeout:   10 * 60 * time.Second, // 10 minutes
	}

	fmt.Println("Starting server on http://localhost:8080")
	log.Fatal(server.ListenAndServe())
}