package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config represents the external json configuration layout
type Config struct {
	SMBPath            string `json:"smb_path"`
	LocalTempDir       string `json:"local_temp_dir"`
	LogFilePath        string `json:"log_file_path"`
	VideoEncoder       string `json:"video_encoder"`
	RunDurationMinutes int    `json:"run_duration_minutes"`
}

var (
	logger            *log.Logger
	allowedExtensions = map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".mov": true}
	globalConfig      Config
	deadline          time.Time
)

func main() {
	// 1. Load the Configuration File
	configFile, err := os.Open("config.json")
	if err != nil {
		fmt.Printf("Fatal: Could not open config.json file: %v\n", err)
		return
	}
	defer configFile.Close()

	jsonParser := json.NewDecoder(configFile)
	if err := jsonParser.Decode(&globalConfig); err != nil {
		fmt.Printf("Fatal: Failed to parse config.json format: %v\n", err)
		return
	}

	// 2. Setup root local environment & logging
	if err := os.MkdirAll(globalConfig.LocalTempDir, os.ModePerm); err != nil {
		fmt.Printf("Failed to create local temp dir: %v\n", err)
		return
	}

	logFile, err := os.OpenFile(globalConfig.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Printf("Failed to open log file: %v\n", err)
		return
	}
	defer logFile.Close()

	logger = log.New(io.MultiWriter(os.Stdout, logFile), "", log.LstdFlags)
	logger.Println("[SYSTEM] Video conversion engine initialized.")

	// Set execution deadline
	deadline = time.Now().Add(time.Duration(globalConfig.RunDurationMinutes) * time.Minute)
	logger.Printf("[SYSTEM] Application hard deadline set for: %s", deadline.Format("15:04:05"))

	// 3. Setup Worker Pools (2 concurrent streams)
	numWorkers := 2
	jobs := make(chan string, 5000) // Highly padded buffer to store mapped network paths
	var wg sync.WaitGroup

	logger.Printf("[SYSTEM] Spawning %d concurrent hardware processing streams...", numWorkers)
	for w := 1; w <= numWorkers; w++ {
		workerTempDir := filepath.Join(globalConfig.LocalTempDir, fmt.Sprintf("worker_%d", w))
		
		// Setup isolated directory workspace for this specific worker thread
		_ = os.MkdirAll(workerTempDir, os.ModePerm)
		cleanupWorkerTemp(workerTempDir) // Flush any dirty cache files from previous crashes

		wg.Add(1)
		go worker(w, workerTempDir, jobs, &wg)
	}

	// 4. Main thread sweeps the directory and queues work items
	logger.Printf("[SYSTEM] Sweeping SMB Share paths: %s...", globalConfig.SMBPath)
	err = filepath.WalkDir(globalConfig.SMBPath, func(path string, d os.DirEntry, err error) error {
		// Stop scanning paths entirely if the main thread catches the timeout deadline early
		if time.Now().After(deadline) {
			return filepath.SkipAll
		}

		if err != nil {
			logger.Printf("[SYSTEM ERROR] Access fault at path %s: %v", path, err)
			return nil
		}

		if !d.IsDir() {
			ext := strings.ToLower(filepath.Ext(path))
			if allowedExtensions[ext] {
				jobs <- path // Safely hand the path off to the concurrent workers queue
			}
		}
		return nil
	})

	if err != nil {
		logger.Printf("[SYSTEM FATAL] Sweeper failed completely: %v", err)
	}

	// Close the job pipeline channel; this flags the workers that no *new* files are coming
	close(jobs)

	// Wait for both worker streams to completely drain or handle their current payloads
	wg.Wait()
	logger.Println("[SYSTEM] All active stream routines terminated. Application shut down cleanly.")
}

// worker routine runs in its own concurrent thread space
func worker(id int, workerDir string, jobs <-chan string, wg *sync.WaitGroup) {
	defer wg.Done()

	for remotePath := range jobs {
		fileName := filepath.Base(remotePath)

		// DEADLINE CHECK: If time window closed while in queue, drop work item safely
		if time.Now().After(deadline) {
			logger.Printf("[W-%d][TIMEOUT] Processing frame closed. Dropping from queue: %s", id, fileName)
			continue
		}

		// Process the file inside the worker's isolated storage sandbox
		processVideo(id, workerDir, remotePath)
	}
}

func processVideo(workerID int, workerDir string, remotePath string) {
	fileName := filepath.Base(remotePath)
	prefix := fmt.Sprintf("[W-%d]", workerID)

	logger.Printf("%s Analyzing file: %s", prefix, fileName)

	// Step A: Deep Probe Codec, Resolution, and Bitrate using ffprobe
	var stderr bytes.Buffer
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width", "-show_entries", "format=bit_rate", "-of", "default=noprint_wrappers=1", remotePath)
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		logger.Printf("%s [ERROR] Failed to probe %s: %v. Details: %s", prefix, fileName, err, strings.TrimSpace(stderr.String()))
		return
	}

	var codec string
	var width int
	var bitrate int64

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "codec_name=") {
			codec = strings.Split(line, "=")[1]
		} else if strings.HasPrefix(line, "width=") {
			_, _ = fmt.Sscanf(line, "width=%d", &width)
		} else if strings.HasPrefix(line, "bit_rate=") {
			var br int64
			_, err := fmt.Sscanf(line, "bit_rate=%d", &br)
			if err == nil && br > 0 {
				bitrate = br
			}
		}
	}

	// 1. Immediate exit if it's already H.265
	if strings.Contains(codec, "hevc") || strings.Contains(codec, "h265") {
		logger.Printf("%s [SKIP] %s is already H.265.", prefix, fileName)
		return
	}

	// 2. Pre-Check Optimization Matrix
	bitrateKbps := bitrate / 1000
	isWellCompressed := false

	if bitrateKbps > 0 {
		if width >= 3840 && bitrateKbps <= 7500 {
			isWellCompressed = true
		} else if width >= 1920 && bitrateKbps <= 2200 {
			isWellCompressed = true
		} else if width >= 1280 && bitrateKbps <= 1200 {
			isWellCompressed = true
		} else if width < 1280 && bitrateKbps <= 700 {
			isWellCompressed = true
		}
	}

	if isWellCompressed {
		logger.Printf("%s [SAFE SKIP] Already optimized (%dp at %d kbps): %s", prefix, width, bitrateKbps, fileName)
		return
	}

	logger.Printf("%s [PROCESS] File unoptimized (%dp at %d kbps). Allocating bandwidth: %s", prefix, width, bitrateKbps, fileName)

	// Step B: Define worker isolated paths
	localInput := filepath.Join(workerDir, fileName)
	outputName := "encoded_" + strings.TrimSuffix(fileName, filepath.Ext(fileName)) + ".mkv"
	localOutput := filepath.Join(workerDir, outputName)

	// Step C: Copy from network to local SSD
	logger.Printf("%s [IO] Downloading to local staging SSD...", prefix)
	startTime := time.Now()
	if err := copyFile(remotePath, localInput); err != nil {
		logger.Printf("%s [ERROR] Staging download failed: %v", prefix, err)
		return
	}
	logger.Printf("%s [IO] Staging sync complete in %.2fs", prefix, time.Since(startTime).Seconds())

	// Step D: Transcode via FFmpeg utilizing NVIDIA NVENC
	logger.Printf("%s [GPU] Commencing hardware NVENC transcode...", prefix)
	var ffmpegArgs []string
	if globalConfig.VideoEncoder == "hevc_nvenc" {
		ffmpegArgs = []string{"-y", "-i", localInput, "-c:v", "hevc_nvenc", "-pix_fmt", "yuv420p", "-cq", "24", "-c:a", "copy", localOutput}
	} else {
		ffmpegArgs = []string{"-y", "-i", localInput, "-c:v", "libx265", "-crf", "23", "-preset", "medium", "-c:a", "copy", localOutput}
	}

	var ffmpegStderr bytes.Buffer
	encodeCmd := exec.Command("ffmpeg", ffmpegArgs...)
	encodeCmd.Stderr = &ffmpegStderr

	conversionStart := time.Now()
	err = encodeCmd.Run() // Assigned correctly to pre-scoped global error variable
	conversionDuration := time.Since(conversionStart)

	if err != nil {
		logger.Printf("%s [ERROR] Core NVENC transcode aborted: %v. Details: %s", prefix, err, strings.TrimSpace(ffmpegStderr.String()))
		cleanupWorkerTemp(workerDir)
		return
	}

	logger.Printf("%s [GPU] Processing successful. Render Time: %.2f seconds (%.2f minutes)", prefix, conversionDuration.Seconds(), conversionDuration.Minutes())

	// Step E: Post-Conversion Size Evaluation (Loop-Proof Logic)
	originalInfo, _ := os.Stat(localInput)
	newFileInfo, _ := os.Stat(localOutput)
	originalSize := originalInfo.Size()
	newSize := newFileInfo.Size()

	if newSize >= originalSize {
		logger.Printf("%s [NOTICE] Bitrate expansion observed (Old: %.2f MB, New: %.2f MB). Enforcing upload to prevent job loops.", prefix, float64(originalSize)/(1024*1024), float64(newSize)/(1024*1024))
	} else {
		logger.Printf("%s [SHRUNK] Compacting metric: Old: %.2f MB -> New: %.2f MB | Delta Saved: %.2f MB", prefix, float64(originalSize)/(1024*1024), float64(newSize)/(1024*1024), float64(originalSize-newSize)/(1024*1024))
	}

	// Step F: Push back to SMB and drop original structure
	remoteDir := filepath.Dir(remotePath)
	finalRemotePath := filepath.Join(remoteDir, outputName)

	logger.Printf("%s [IO] Uploading finished asset back to network...", prefix)
	if err := copyFile(localOutput, finalRemotePath); err != nil {
		logger.Printf("%s [ERROR] Network push failed: %v", prefix, err)
		return
	}

	if remotePath != finalRemotePath {
		if err := os.Remove(remotePath); err != nil {
			logger.Printf("%s [ERROR] Could not purge legacy asset file: %v", prefix, err)
		} else {
			logger.Printf("%s [SUCCESS] Purged old resource file matching: %s", prefix, fileName)
		}
	}

	logger.Printf("%s [SUCCESS] Completed lifecycle loop for target video file.", prefix)
	cleanupWorkerTemp(workerDir)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func cleanupWorkerTemp(workerDir string) {
	files, err := os.ReadDir(workerDir)
	if err != nil {
		return
	}
	for _, f := range files {
		_ = os.Remove(filepath.Join(workerDir, f.Name()))
	}
}
