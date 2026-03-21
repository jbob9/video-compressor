package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Job management
// ---------------------------------------------------------------------------

// Job tracks one compression task.
type Job struct {
	ID        string
	FileName  string
	TempPath  string
	Options   CompressOptions
	VideoInfo *VideoInfo
	Progress  chan CompressProgress
	cancel    context.CancelFunc
	mu        sync.Mutex
	err       error
	done      bool
}

var (
	jobs   = make(map[string]*Job)
	jobsMu sync.RWMutex
)

func genID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

func handleHWInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(DetectHWAccel())
}

func handleCompress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(2 << 30); err != nil { // 2 GB
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		http.Error(w, "No file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Read settings
	level := r.FormValue("compressionLevel")
	if level == "" {
		level = "normal"
	}
	codec := r.FormValue("codec")
	if codec == "" {
		codec = "h264"
	}
	resolution := r.FormValue("resolution")
	audioBitrate := r.FormValue("audioBitrate")
	if audioBitrate == "" {
		audioBitrate = "128k"
	}
	outputDir := r.FormValue("outputDir")
	if outputDir == "" {
		outputDir = GetDefaultOutputDir()
	}
	hwaccel := r.FormValue("hwaccel") == "true"

	// Save uploaded file to a temp location
	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("vc_%s_%s", genID()[:8], header.Filename))
	tmp, err := os.Create(tempPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("temp file: %v", err), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		os.Remove(tempPath)
		http.Error(w, fmt.Sprintf("save file: %v", err), http.StatusInternalServerError)
		return
	}
	tmp.Close()

	// Probe
	videoInfo, _ := ProbeVideo(tempPath)

	// Build output path from original filename
	os.MkdirAll(outputDir, os.ModePerm)
	ext := filepath.Ext(header.Filename)
	name := header.Filename[:len(header.Filename)-len(ext)]
	outputPath := filepath.Join(outputDir, fmt.Sprintf("%s_%s_compressed%s", name, level, ext))

	// Create job
	jobID := genID()
	ctx, cancel := context.WithCancel(context.Background())
	job := &Job{
		ID:       jobID,
		FileName: header.Filename,
		TempPath: tempPath,
		Options: CompressOptions{
			InputPath:        tempPath,
			OutputPath:       outputPath,
			CompressionLevel: level,
			Codec:            codec,
			Resolution:       resolution,
			Threads:          runtime.NumCPU(),
			HWAccel:          hwaccel,
			AudioBitrate:     audioBitrate,
		},
		VideoInfo: videoInfo,
		Progress:  make(chan CompressProgress, 100),
		cancel:    cancel,
	}

	jobsMu.Lock()
	jobs[jobID] = job
	jobsMu.Unlock()

	// Run compression in background
	go func() {
		defer func() {
			close(job.Progress)
			os.Remove(tempPath)
			job.mu.Lock()
			job.done = true
			job.mu.Unlock()
		}()
		err := CompressVideoWithProgress(ctx, job.Options, func(p CompressProgress) {
			select {
			case job.Progress <- p:
			default: // drop if channel full
			}
		})
		if err != nil {
			job.mu.Lock()
			job.err = err
			job.mu.Unlock()
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":      jobID,
		"videoInfo":  videoInfo,
		"outputPath": outputPath,
	})
}

// handleProgress streams SSE events for a running job.
func handleProgress(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	jobsMu.RLock()
	job, ok := jobs[id]
	jobsMu.RUnlock()
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send video info first
	if job.VideoInfo != nil {
		d, _ := json.Marshal(job.VideoInfo)
		fmt.Fprintf(w, "event: info\ndata: %s\n\n", d)
		flusher.Flush()
	}

	// Stream progress until channel closes
	for p := range job.Progress {
		d, _ := json.Marshal(p)
		fmt.Fprintf(w, "event: progress\ndata: %s\n\n", d)
		flusher.Flush()
	}

	// Final event
	job.mu.Lock()
	jobErr := job.err
	job.mu.Unlock()

	if jobErr != nil {
		d, _ := json.Marshal(map[string]string{"message": jobErr.Error()})
		fmt.Fprintf(w, "event: fail\ndata: %s\n\n", d)
	} else {
		var outSize int64
		if fi, err := os.Stat(job.Options.OutputPath); err == nil {
			outSize = fi.Size()
		}
		inSize := int64(0)
		if job.VideoInfo != nil {
			inSize = job.VideoInfo.FileSize
		}
		ratio := 0.0
		if inSize > 0 {
			ratio = (1 - float64(outSize)/float64(inSize)) * 100
		}
		d, _ := json.Marshal(map[string]interface{}{
			"outputPath":          job.Options.OutputPath,
			"outputSize":          outSize,
			"inputSize":           inSize,
			"ratio":               ratio,
			"outputSizeFormatted": formatFileSize(outSize),
			"inputSizeFormatted":  formatFileSize(inSize),
		})
		fmt.Fprintf(w, "event: complete\ndata: %s\n\n", d)
	}
	flusher.Flush()

	// Cleanup job after a delay
	go func() {
		time.Sleep(5 * time.Minute)
		jobsMu.Lock()
		delete(jobs, id)
		jobsMu.Unlock()
	}()
}

func handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	jobsMu.RLock()
	job, ok := jobs[id]
	jobsMu.RUnlock()
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	job.cancel()
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "cancelled")
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

func runGUI() {
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/compress", handleCompress)
	http.HandleFunc("/progress", handleProgress)
	http.HandleFunc("/cancel", handleCancel)
	http.HandleFunc("/hwinfo", handleHWInfo)

	srv := &http.Server{
		Addr:           ":8080",
		MaxHeaderBytes: 2 << 30,
		ReadTimeout:    60 * time.Minute,
		WriteTimeout:   60 * time.Minute,
	}
	fmt.Println("Video Compressor running at http://localhost:8080")
	log.Fatal(srv.ListenAndServe())
}

// ---------------------------------------------------------------------------
// HTML / CSS / JS  (single-page app, no external deps)
// ---------------------------------------------------------------------------

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Video Compressor</title>
<style>
:root{
  --bg:#0a0a1a;--bg2:#111127;--card:rgba(30,41,59,.45);
  --border:rgba(148,163,184,.08);--border-h:rgba(148,163,184,.18);
  --accent:#6366f1;--accent2:#818cf8;--accent-g:rgba(99,102,241,.15);
  --ok:#10b981;--err:#ef4444;--warn:#f59e0b;
  --t1:#f1f5f9;--t2:#94a3b8;--t3:#64748b;--t4:#475569;
  --r:12px;--r2:16px;
}
*{margin:0;padding:0;box-sizing:border-box}
body{background:var(--bg);color:var(--t1);min-height:100vh;
  font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;
  line-height:1.6;
  background-image:radial-gradient(ellipse 80% 50% at 50% -10%,rgba(99,102,241,.1),transparent)}
.app{max-width:840px;margin:0 auto;padding:2rem 1.5rem 4rem}

/* header */
.hdr{text-align:center;margin-bottom:2.5rem}
.hdr h1{font-size:2rem;font-weight:700;
  background:linear-gradient(135deg,#f1f5f9,#94a3b8);
  -webkit-background-clip:text;-webkit-text-fill-color:transparent;
  background-clip:text;margin-bottom:.3rem}
.hdr p{color:var(--t3);font-size:.92rem}
.hw-badges{display:flex;justify-content:center;gap:.5rem;margin-top:.85rem;flex-wrap:wrap}
.hw-badge{display:inline-flex;align-items:center;gap:.3rem;
  padding:.2rem .7rem;border-radius:100px;font-size:.72rem;font-weight:600;
  background:var(--accent-g);border:1px solid rgba(99,102,241,.2);color:var(--accent2)}
.hw-badge.off{background:rgba(100,116,139,.08);border-color:rgba(100,116,139,.15);color:var(--t3)}

/* card */
.card{background:var(--card);backdrop-filter:blur(24px);-webkit-backdrop-filter:blur(24px);
  border:1px solid var(--border);border-radius:var(--r2);padding:1.5rem;margin-bottom:1.25rem}

/* dropzone */
.dz{border:2px dashed rgba(148,163,184,.12);border-radius:var(--r);
  padding:2.8rem 1rem;text-align:center;cursor:pointer;transition:all .3s;position:relative}
.dz:hover{border-color:rgba(99,102,241,.35);background:rgba(99,102,241,.03)}
.dz.over{border-color:var(--accent);background:rgba(99,102,241,.07);
  box-shadow:0 0 40px rgba(99,102,241,.08)}
.dz svg{width:44px;height:44px;color:var(--accent);opacity:.65;margin-bottom:.6rem}
.dz-t{color:var(--t2);font-size:.92rem}.dz-t b{color:var(--accent)}
.dz.disabled{opacity:.5;pointer-events:none}

/* file list */
.fl{margin-top:1rem}
.fi{display:flex;align-items:center;padding:.7rem .85rem;border-radius:10px;
  background:rgba(15,23,42,.45);margin-bottom:.45rem;border:1px solid rgba(148,163,184,.04);
  transition:background .2s}
.fi:hover{background:rgba(15,23,42,.65)}
.fi-icon{width:34px;height:34px;border-radius:8px;background:rgba(99,102,241,.08);
  display:flex;align-items:center;justify-content:center;margin-right:.7rem;flex-shrink:0}
.fi-icon svg{width:16px;height:16px;color:var(--accent2)}
.fi-det{flex:1;min-width:0}
.fi-name{font-size:.88rem;font-weight:500;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.fi-meta{font-size:.72rem;color:var(--t3);margin-top:.1rem}
.fi-prog{flex-shrink:0;width:180px;margin-left:.75rem}
.pb{height:5px;background:rgba(148,163,184,.08);border-radius:3px;overflow:hidden}
.pf{height:100%;background:linear-gradient(90deg,var(--accent),#8b5cf6);border-radius:3px;
  transition:width .35s ease;position:relative}
.pf.act::after{content:'';position:absolute;inset:0;
  background:linear-gradient(90deg,transparent,rgba(255,255,255,.25),transparent);
  animation:shim 1.5s infinite}
@keyframes shim{from{transform:translateX(-100%)}to{transform:translateX(100%)}}
.pt{font-size:.7rem;color:var(--t2);margin-top:.2rem;text-align:right}
.fi-st{flex-shrink:0;margin-left:.6rem;font-size:.78rem;font-weight:500}
.fi-st.pending{color:var(--t3)}.fi-st.uploading{color:var(--accent2)}
.fi-st.compressing{color:var(--accent)}.fi-st.done{color:var(--ok)}
.fi-st.error{color:var(--err)}.fi-st.cancelled{color:var(--warn)}
.fi-rm{background:none;border:none;color:var(--t3);cursor:pointer;padding:.2rem;
  margin-left:.4rem;border-radius:4px;transition:all .15s;font-size:.85rem;line-height:1}
.fi-rm:hover{color:var(--err);background:rgba(239,68,68,.08)}

/* settings */
.stitle{font-size:.95rem;font-weight:600;margin-bottom:.85rem;color:var(--t1)}
.lvls{display:grid;grid-template-columns:repeat(4,1fr);gap:.65rem;margin-bottom:1rem}
.lv{background:rgba(15,23,42,.45);border:1px solid rgba(148,163,184,.06);border-radius:10px;
  padding:.65rem .5rem;text-align:center;cursor:pointer;transition:all .2s;
  color:inherit;font-family:inherit;appearance:none;-webkit-appearance:none}
.lv:hover{border-color:rgba(99,102,241,.25);background:rgba(99,102,241,.04)}
.lv.on{border-color:var(--accent);background:rgba(99,102,241,.1);
  box-shadow:0 0 18px rgba(99,102,241,.08)}
.lv-i{font-size:1.4rem;margin-bottom:.15rem}
.lv-n{font-size:.82rem;font-weight:600;margin-bottom:.1rem}
.lv-d{font-size:.67rem;color:var(--t3)}

/* advanced */
.adv-btn{display:flex;align-items:center;gap:.4rem;padding:.45rem 0;cursor:pointer;
  color:var(--t2);font-size:.82rem;border:none;background:none;font-family:inherit;width:100%;
  transition:color .2s;user-select:none}
.adv-btn:hover{color:var(--t1)}
.adv-btn svg{width:14px;height:14px;transition:transform .3s}
.adv-btn.open svg{transform:rotate(90deg)}
.adv{display:none;grid-template-columns:1fr 1fr;gap:.85rem;padding:.85rem 0;
  animation:fi .3s ease}
.adv.open{display:grid}
@keyframes fi{from{opacity:0;transform:translateY(-6px)}to{opacity:1;transform:translateY(0)}}
.sg label{display:block;font-size:.78rem;color:var(--t2);margin-bottom:.3rem;font-weight:500}
.sg select,.sg input[type=text],.sg input[type=number]{width:100%;padding:.55rem .7rem;
  background:rgba(15,23,42,.55);border:1px solid rgba(148,163,184,.08);border-radius:8px;
  color:var(--t1);font-size:.83rem;font-family:inherit;transition:border-color .2s;
  appearance:none;-webkit-appearance:none}
.sg select:focus,.sg input:focus{outline:none;border-color:rgba(99,102,241,.45)}
.sw{position:relative}
.sw::after{content:'\25BE';position:absolute;right:.7rem;top:50%;
  transform:translateY(-50%);color:var(--t3);pointer-events:none;font-size:.75rem}
.cb{display:flex;align-items:center;gap:.5rem;padding-top:1.4rem}
.cb input[type=checkbox]{accent-color:var(--accent);width:15px;height:15px;cursor:pointer}
.cb label{margin:0;cursor:pointer;font-size:.82rem;color:var(--t2)}
.og{margin-top:.85rem;padding-top:.85rem;border-top:1px solid rgba(148,163,184,.05)}
.og label{display:block;font-size:.78rem;color:var(--t2);margin-bottom:.3rem;font-weight:500}
.og input{width:100%;padding:.55rem .7rem;background:rgba(15,23,42,.55);
  border:1px solid rgba(148,163,184,.08);border-radius:8px;color:var(--t1);
  font-size:.83rem;font-family:inherit}
.og input:focus{outline:none;border-color:rgba(99,102,241,.45)}

/* buttons */
.acts{display:flex;gap:.65rem;margin-bottom:1.25rem}
.btn{display:inline-flex;align-items:center;justify-content:center;gap:.5rem;
  padding:.8rem 1.8rem;border-radius:10px;font-size:.92rem;font-weight:600;
  border:none;cursor:pointer;transition:all .2s;font-family:inherit}
.btn-go{flex:1;background:linear-gradient(135deg,#6366f1,#8b5cf6);color:#fff;
  box-shadow:0 4px 18px rgba(99,102,241,.25)}
.btn-go:hover:not(:disabled){box-shadow:0 6px 28px rgba(99,102,241,.35);transform:translateY(-1px)}
.btn-go:disabled{opacity:.45;cursor:not-allowed;transform:none}
.btn-no{padding:.8rem 1.4rem;background:rgba(239,68,68,.08);color:var(--err);
  border:1px solid rgba(239,68,68,.18)}
.btn-no:hover{background:rgba(239,68,68,.15)}
.hide{display:none!important}

/* spinner */
.spin{width:15px;height:15px;border:2px solid rgba(255,255,255,.25);
  border-top-color:#fff;border-radius:50%;animation:sp .7s linear infinite;display:inline-block}
@keyframes sp{to{transform:rotate(360deg)}}

/* results */
.res{animation:fi .5s ease}
.ri{display:flex;align-items:center;padding:.7rem .85rem;border-radius:10px;
  margin-bottom:.45rem;background:rgba(15,23,42,.45);border:1px solid rgba(148,163,184,.04)}
.ri.ok{border-color:rgba(16,185,129,.12)}
.ri.bad{border-color:rgba(239,68,68,.12)}
.ri-ic{width:30px;height:30px;border-radius:50%;display:flex;align-items:center;
  justify-content:center;margin-right:.7rem;flex-shrink:0}
.ri-ic.ok{background:rgba(16,185,129,.08);color:var(--ok)}
.ri-ic.bad{background:rgba(239,68,68,.08);color:var(--err)}
.ri-ic svg{width:16px;height:16px}
.ri-det{flex:1;min-width:0}
.ri-name{font-size:.88rem;font-weight:500}
.ri-stat{font-size:.72rem;color:var(--t2);margin-top:.1rem}
.ri-ratio{font-size:.85rem;font-weight:700;margin-left:.75rem;color:var(--ok)}

/* footer */
.ftr{text-align:center;color:var(--t4);font-size:.72rem;margin-top:2.5rem}

/* responsive */
@media(max-width:600px){
  .lvls{grid-template-columns:repeat(2,1fr)}
  .adv.open{grid-template-columns:1fr}
  .fi-prog{width:120px}
}
</style>
</head>
<body>
<div class="app">

  <!-- header -->
  <header class="hdr">
    <h1>Video Compressor</h1>
    <p>Fast, powerful video compression powered by FFmpeg</p>
    <div id="hwBadges" class="hw-badges"></div>
  </header>

  <!-- upload card -->
  <section class="card">
    <div id="dropzone" class="dz">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
      <div class="dz-t"><b>Drop video files here</b> or click to browse</div>
    </div>
    <input type="file" id="fileInput" multiple accept="video/*" style="display:none">
    <div id="fileList" class="fl"></div>
  </section>

  <!-- settings card -->
  <section class="card">
    <div class="stitle">Compression Level</div>
    <div class="lvls">
      <button class="lv on" data-level="normal"><div class="lv-i">&#9889;</div><div class="lv-n">Normal</div><div class="lv-d">Balanced quality</div></button>
      <button class="lv" data-level="high"><div class="lv-i">&#128293;</div><div class="lv-n">High</div><div class="lv-d">Smaller files</div></button>
      <button class="lv" data-level="very_high"><div class="lv-i">&#128142;</div><div class="lv-n">Very High</div><div class="lv-d">Much smaller</div></button>
      <button class="lv" data-level="maximum"><div class="lv-i">&#128640;</div><div class="lv-n">Maximum</div><div class="lv-d">Smallest possible</div></button>
    </div>

    <button id="advToggle" class="adv-btn">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6"/></svg>
      Advanced Settings
    </button>

    <div id="advPanel" class="adv">
      <div class="sg"><label>Video Codec</label><div class="sw"><select id="selCodec"><option value="h264">H.264 (AVC)</option><option value="h265">H.265 (HEVC)</option></select></div></div>
      <div class="sg"><label>Resolution</label><div class="sw"><select id="selRes"><option value="original">Original</option><option value="1080p">1080p</option><option value="720p">720p</option><option value="480p">480p</option></select></div></div>
      <div class="sg"><label>Audio Bitrate</label><div class="sw"><select id="selAudio"><option value="128k">128 kbps</option><option value="96k">96 kbps</option><option value="64k">64 kbps</option></select></div></div>
      <div class="sg"><label>Concurrent Files</label><div class="sw"><select id="selConc"><option value="1">1 file</option><option value="2" selected>2 files</option><option value="3">3 files</option><option value="4">4 files</option></select></div></div>
      <div class="sg cb"><input type="checkbox" id="chkHW"><label for="chkHW">Hardware Acceleration</label></div>
    </div>

    <div class="og">
      <label>Output Directory (leave empty for ~/Downloads)</label>
      <input type="text" id="outDir" placeholder="~/Downloads">
    </div>
  </section>

  <!-- action buttons -->
  <div class="acts">
    <button id="btnGo" class="btn btn-go" disabled>Compress</button>
    <button id="btnStop" class="btn btn-no hide">Cancel</button>
  </div>

  <!-- results -->
  <section id="resCard" class="card res hide">
    <div class="stitle">Results</div>
    <div id="resList"></div>
  </section>

  <div class="ftr">Powered by FFmpeg</div>
</div>

<script>
/* ---- state ------------------------------------------------------------ */
var S={
  files:[],level:'normal',codec:'h264',res:'original',
  audio:'128k',hw:false,outDir:'',busy:false,hwInfo:null,conc:2
};

/* ---- refs ------------------------------------------------------------- */
var $=function(id){return document.getElementById(id)};
var dz=$('dropzone'),inp=$('fileInput'),fl=$('fileList'),
    advT=$('advToggle'),advP=$('advPanel'),
    btnGo=$('btnGo'),btnStop=$('btnStop'),
    resCard=$('resCard'),resL=$('resList');

/* ---- hw info ---------------------------------------------------------- */
fetch('/hwinfo').then(function(r){return r.json()}).then(function(d){
  S.hwInfo=d;var c=$('hwBadges'),h='';
  if(d.nvenc)h+='<span class="hw-badge">NVENC</span>';
  if(d.vaapi)h+='<span class="hw-badge">VAAPI</span>';
  if(d.qsv)h+='<span class="hw-badge">QSV</span>';
  if(!d.nvenc&&!d.vaapi&&!d.qsv)h='<span class="hw-badge off">Software encoding</span>';
  c.innerHTML=h;
}).catch(function(){});

/* ---- dropzone --------------------------------------------------------- */
dz.addEventListener('click',function(){if(!S.busy)inp.click()});
dz.addEventListener('dragover',function(e){e.preventDefault();dz.classList.add('over')});
dz.addEventListener('dragleave',function(){dz.classList.remove('over')});
dz.addEventListener('drop',function(e){e.preventDefault();dz.classList.remove('over');addFiles(e.dataTransfer.files)});
inp.addEventListener('change',function(){addFiles(this.files);this.value=''});

function addFiles(list){
  if(S.busy)return;
  for(var i=0;i<list.length;i++){
    if(list[i].type&&list[i].type.indexOf('video/')===0){
      S.files.push({file:list[i],st:'pending',jobId:null,pct:0,spd:'',res:null,xhr:null,sse:null});
    }
  }
  render();updateBtn();
}

/* ---- settings --------------------------------------------------------- */
var lvBtns=document.querySelectorAll('.lv');
for(var i=0;i<lvBtns.length;i++){
  lvBtns[i].addEventListener('click',function(){
    for(var j=0;j<lvBtns.length;j++)lvBtns[j].classList.remove('on');
    this.classList.add('on');S.level=this.getAttribute('data-level');
  });
}
advT.addEventListener('click',function(){this.classList.toggle('open');advP.classList.toggle('open')});
$('selCodec').addEventListener('change',function(){S.codec=this.value});
$('selRes').addEventListener('change',function(){S.res=this.value});
$('selAudio').addEventListener('change',function(){S.audio=this.value});
$('selConc').addEventListener('change',function(){S.conc=parseInt(this.value,10)||1});
$('chkHW').addEventListener('change',function(){S.hw=this.checked});
$('outDir').addEventListener('input',function(){S.outDir=this.value});

/* ---- render file list ------------------------------------------------- */
function render(){
  fl.innerHTML='';
  for(var i=0;i<S.files.length;i++){
    var f=S.files[i],d=document.createElement('div');d.className='fi';d.id='fi-'+i;
    var h='<div class="fi-icon"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="2" width="20" height="20" rx="2.18" ry="2.18"/><line x1="7" y1="2" x2="7" y2="22"/><line x1="17" y1="2" x2="17" y2="22"/><line x1="2" y1="12" x2="22" y2="12"/><line x1="2" y1="7" x2="7" y2="7"/><line x1="2" y1="17" x2="7" y2="17"/><line x1="17" y1="7" x2="22" y2="7"/><line x1="17" y1="17" x2="22" y2="17"/></svg></div>';
    h+='<div class="fi-det"><div class="fi-name">'+esc(f.file.name)+'</div>';
    h+='<div class="fi-meta">'+fmtSz(f.file.size)+'</div></div>';
    if(f.st==='uploading'||f.st==='compressing'){
      h+='<div class="fi-prog"><div class="pb"><div class="pf act" style="width:'+f.pct+'%"></div></div>';
      h+='<div class="pt">'+f.pct.toFixed(1)+'%'+(f.spd?' \u00b7 '+f.spd:'')+'</div></div>';
    }
    if(f.st==='done'){
      h+='<div class="fi-prog"><div class="pb"><div class="pf" style="width:100%"></div></div>';
      h+='<div class="pt">Done</div></div>';
    }
    h+='<span class="fi-st '+f.st+'">'+stTxt(f.st)+'</span>';
    if(f.st==='pending'&&!S.busy)h+='<button class="fi-rm" data-i="'+i+'">\u2715</button>';
    d.innerHTML=h;fl.appendChild(d);
  }
  /* bind remove buttons */
  var rms=fl.querySelectorAll('.fi-rm');
  for(var j=0;j<rms.length;j++){
    rms[j].addEventListener('click',function(){
      var idx=parseInt(this.getAttribute('data-i'),10);
      S.files.splice(idx,1);render();updateBtn();
    });
  }
}

function updateProg(idx){
  var el=document.getElementById('fi-'+idx);if(!el)return;
  var f=S.files[idx];
  var bar=el.querySelector('.pf');if(bar)bar.style.width=f.pct+'%';
  var txt=el.querySelector('.pt');if(txt)txt.textContent=f.pct.toFixed(1)+'%'+(f.spd?' \u00b7 '+f.spd:'');
}

function stTxt(s){
  switch(s){
    case 'pending':return 'Pending';case 'uploading':return 'Uploading\u2026';
    case 'compressing':return 'Compressing\u2026';case 'done':return '\u2713 Done';
    case 'error':return '\u2715 Error';case 'cancelled':return 'Cancelled';default:return '';
  }
}

function updateBtn(){
  var has=false;
  for(var i=0;i<S.files.length;i++)if(S.files[i].st==='pending'){has=true;break}
  btnGo.disabled=S.files.length===0||!has||S.busy;
}

/* ---- compress --------------------------------------------------------- */
btnGo.addEventListener('click',compressAll);
btnStop.addEventListener('click',cancelAll);

function compressAll(){
  if(S.busy)return;S.busy=true;
  btnGo.innerHTML='<span class="spin"></span> Compressing\u2026';btnGo.disabled=true;
  btnStop.classList.remove('hide');
  resCard.classList.add('hide');resL.innerHTML='';
  dz.classList.add('disabled');

  var pending=[];
  for(var i=0;i<S.files.length;i++)if(S.files[i].st==='pending')pending.push(i);

  var maxC=Math.max(1,Math.min(S.conc,pending.length));
  var running=0,ci=0;

  function tryNext(){
    /* launch as many as the concurrency limit allows */
    while(S.busy&&ci<pending.length&&running<maxC){
      running++;
      var idx=pending[ci++];
      compressOne(idx,function(){
        running--;
        if(!S.busy){if(running===0)finish();return}
        tryNext();
      });
    }
    /* when every worker has returned, we are done */
    if(running===0)finish();
  }
  tryNext();
}

function finish(){
  S.busy=false;
  btnGo.innerHTML='Compress';btnStop.classList.add('hide');
  dz.classList.remove('disabled');
  updateBtn();
  if(resL.children.length>0)resCard.classList.remove('hide');
}

function compressOne(idx,cb){
  var f=S.files[idx];f.st='uploading';f.pct=0;render();
  var done=false;function once(fn){return function(){if(!done){done=true;fn.apply(null,arguments)}}}
  var safeCb=once(cb);

  var fd=new FormData();
  fd.append('video',f.file);
  fd.append('compressionLevel',S.level);
  fd.append('codec',S.codec);
  fd.append('resolution',S.res);
  fd.append('audioBitrate',S.audio);
  fd.append('hwaccel',S.hw?'true':'false');
  fd.append('outputDir',S.outDir);

  var xhr=new XMLHttpRequest();f.xhr=xhr;
  xhr.open('POST','/compress',true);

  xhr.upload.onprogress=function(e){
    if(e.lengthComputable){f.pct=(e.loaded/e.total)*100;updateProg(idx)}
  };

  xhr.onload=function(){
    f.xhr=null;
    if(xhr.status!==200){
      f.st='error';f.res={error:xhr.responseText};addRes(f,false);render();safeCb();return;
    }
    var data;try{data=JSON.parse(xhr.responseText)}catch(ex){
      f.st='error';f.res={error:'Bad response'};addRes(f,false);render();safeCb();return;
    }
    f.jobId=data.jobId;f.st='compressing';f.pct=0;render();

    var es=new EventSource('/progress?id='+data.jobId);f.sse=es;

    es.addEventListener('progress',function(e){
      var p=JSON.parse(e.data);f.pct=p.percent||0;f.spd=p.speed||'';updateProg(idx);
    });
    es.addEventListener('complete',function(e){
      es.close();f.sse=null;f.st='done';f.pct=100;f.res=JSON.parse(e.data);
      addRes(f,true);render();safeCb();
    });
    es.addEventListener('fail',function(e){
      es.close();f.sse=null;var d=JSON.parse(e.data);
      f.st='error';f.res={error:d.message};addRes(f,false);render();safeCb();
    });
    es.onerror=function(){
      es.close();f.sse=null;
      if(f.st==='compressing'){f.st='error';f.res={error:'Connection lost'};addRes(f,false);render();safeCb()}
    };
  };

  xhr.onerror=function(){
    f.xhr=null;f.st='error';f.res={error:'Network error'};addRes(f,false);render();safeCb();
  };

  xhr.send(fd);
}

function cancelAll(){
  for(var i=0;i<S.files.length;i++){
    var f=S.files[i];
    if(f.st==='uploading'&&f.xhr){f.xhr.abort();f.xhr=null;f.st='cancelled'}
    if(f.st==='compressing'&&f.jobId){
      fetch('/cancel?id='+f.jobId,{method:'POST'});
      if(f.sse){f.sse.close();f.sse=null}
      f.st='cancelled';
    }
  }
  S.busy=false;finish();render();
}

/* ---- results ---------------------------------------------------------- */
function addRes(f,ok){
  resCard.classList.remove('hide');
  var d=document.createElement('div');
  d.className='ri '+(ok?'ok':'bad');
  var h='';
  if(ok){
    var r=f.res;
    h+='<div class="ri-ic ok"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22 4 12 14.01 9 11.01"/></svg></div>';
    h+='<div class="ri-det"><div class="ri-name">'+esc(f.file.name)+'</div>';
    h+='<div class="ri-stat">'+r.inputSizeFormatted+' \u2192 '+r.outputSizeFormatted+'</div></div>';
    h+='<div class="ri-ratio">'+r.ratio.toFixed(1)+'% smaller</div>';
  }else{
    h+='<div class="ri-ic bad"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg></div>';
    h+='<div class="ri-det"><div class="ri-name">'+esc(f.file.name)+'</div>';
    h+='<div class="ri-stat">'+(f.res&&f.res.error?esc(f.res.error):'Unknown error')+'</div></div>';
  }
  d.innerHTML=h;resL.appendChild(d);
}

/* ---- util ------------------------------------------------------------- */
function fmtSz(b){
  if(b>=1073741824)return(b/1073741824).toFixed(2)+' GB';
  if(b>=1048576)return(b/1048576).toFixed(2)+' MB';
  if(b>=1024)return(b/1024).toFixed(2)+' KB';
  return b+' B';
}
function esc(s){var d=document.createElement('div');d.textContent=s;return d.innerHTML}
</script>
</body>
</html>`
