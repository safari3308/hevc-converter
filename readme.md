# 🚀 Automated H.265/HEVC Concurrent Transcoder Engine

An ultra-lightweight, high-performance command-line utility written in Go (Golang) designed to autonomously crawl a network share (SMB/UNC paths), detect unoptimized H.264 videos, and compress them into H.265 (HEVC) using hardware acceleration (`hevc_nvenc`).

Built specifically to manage **terabytes of data** safely over strict, time-boxed execution windows without breaking network streams or risking file corruption.

---

## ✨ Key Features

* **🏎️ Dual-Stream Concurrency:** Employs a robust Worker Pool pattern to transcode two videos simultaneously, maximizing your GPU usage and keeping network pipelines saturated.
* **🛡️ Isolated Sandboxing:** Each concurrent worker processes files inside its own local SSD scratchpad folder (`worker_1/`, `worker_2/`) to eliminate file collision and lock-ups.
* **⏱️ Strict Time-Boxing:** Monitored execution deadline (configured in minutes). When time runs out, the engine stops pulling new items from the queue and shuts down cleanly.
* **📉 Smart Pre-Check Filter:** Probes resolution and bitrate via `ffprobe` over the network *before* downloading. If an H.264 file is already highly compressed, it is skipped permanently to save bandwidth and cycles.
* **🔁 Infinite-Loop Protection:** Post-conversion size checks ensure that even if an H.265 file turns out slightly larger due to NVENC limits, it is written back to the network anyway to permanently flag it as complete, avoiding infinite re-processing loops.
* **📝 Industrial Logging:** Native `MultiWriter` output flushes real-time metrics (conversion speed, durations, and space savings) to both the console and a persistent `.log` file.

---

## 🛠️ Prerequisites

Before compiling or running the tool, ensure the host machine has the following tools installed and added to the **System PATH**:

1. **Go (Golang):** [Download and Install Go](https://go.dev/dl/) (v1.16 or newer recommended).
2. **FFmpeg & FFprobe:** Must be globally accessible from the command line.
* *Windows Quick Install:* `winget install Gyan.FFmpeg`


3. **NVIDIA Graphics Card Drivers:** Ensure your drivers are updated to support NVENC pipelines (e.g., RTX 3060 Ti or similar).

---

## ⚙️ Configuration (`config.json`)

The application runs entirely stateless and derives its parameters from a `config.json` file placed in the same directory as the executable.

Create a file named `config.json` and adjust the paths to match your environment:

```json
{
  "smb_path": "\\\\192.168.1.50\\SharedVideos",
  "local_temp_dir": "C:\\TempEncoder",
  "log_file_path": "C:\\TempEncoder\\process.log",
  "video_encoder": "hevc_nvenc",
  "run_duration_minutes": 60
}

```

> ⚠️ **Windows Path Note:** Backslashes in JSON strings must be escaped. Always use double backslashes (`\\`) for local directory maps and quadruple backslashes (`\\\\`) for the initial machine address of a network UNC share.

---

## 🚀 How to Build & Run

### 1. Initialize the Module

Open your terminal/command prompt in the directory containing `main.go` and initialize the project space:

```cmd
go mod init videoencoder

```

### 2. Compile a High-Performance Binary

Compile the Go source code into a standalone, compressed Windows executable:

```cmd
go build -ldflags="-s -w" -o VideoEngine.exe main.go

```

*The `-ldflags="-s -w"` strip flag removes debugging data, reducing your final `.exe` footprint down to just a few megabytes.*

### 3. Execution

Ensure your `config.json` is in the same folder, then execute the application:

```cmd
.\VideoEngine.exe

```

---

## 🏗️ Processing Architecture (Per File Lifecycle)

```
[SMB Share Scan] ──> [FFprobe Codec/Bitrate Check]
                           │
                           ├──> (Already H.265 or Well-Compressed H.264) ──> [Instant Skip]
                           │
                           └──> (Needs Optimization) 
                                       │
                                 [Worker Allocation]
                                       │
                                 [Local Staging SSD Copy]
                                       │
                                 [RTX NVENC Transcode]
                                       │
                                 [Upload & Replace Original Asset]

```

---

## 📈 Log Diagnostics Example

The engine produces highly structured log streams detailing performance metrics:

```text
2026/07/11 11:05:01 [SYSTEM] Spawning 2 concurrent hardware processing streams...
2026/07/11 11:05:01 [W-1] Analyzing file: Movie_A.mp4
2026/07/11 11:05:01 [W-2] Analyzing file: Movie_B.mkv
2026/07/11 11:05:02 [W-1] [PROCESS] File unoptimized (1080p at 5400 kbps). Allocating bandwidth: Movie_A.mp4
2026/07/11 11:06:45 [W-1] [GPU] Processing successful. Render Time: 103.20 seconds (1.72 minutes)
2026/07/11 11:06:45 [W-1] [SHRUNK] Compacting metric: Old: 2450.10 MB -> New: 1310.45 MB | Delta Saved: 1139.65 MB
2026/07/11 11:06:50 [W-1] [SUCCESS] Completed lifecycle loop for target video file.
2026/07/11 11:15:00 [W-2] [TIMEOUT] Processing frame closed. Dropping from queue: Large_Movie.mkv

```

---

## 🛑 Automated Cron/Task Scheduling

Because the script is compiled into a dependency-free, zero-overhead executable, it is ideally suited for automated execution windows. You can use **Windows Task Scheduler** to trigger `VideoEngine.exe` to run every night at midnight, configuring `"run_duration_minutes": 360` to run for exactly 6 hours before yielding system resources automatically for daytime usage.