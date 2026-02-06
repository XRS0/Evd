//go:build legacy
// +build legacy

// Legacy monolithic server kept only for reference.
// Use cmd/server as the active Clean Architecture entrypoint.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/cors"
)

var (
	videosDir = "./videos"
	hlsDir    = "./hls"
	mp4Dir    = "./mp4"

	allowedExts = map[string]bool{
		".mp4": true,
		".mkv": true,
		".avi": true,
		".mov": true,
	}

	conversionMu     sync.Mutex
	conversionActive = map[string]bool{}

	mp4ConversionMu     sync.Mutex
	mp4ConversionActive = map[string]bool{}

	transmissionURL         = strings.TrimSpace(os.Getenv("TRANSMISSION_URL"))
	transmissionUser        = os.Getenv("TRANSMISSION_USER")
	transmissionPass        = os.Getenv("TRANSMISSION_PASS")
	transmissionDownloadDir = getEnv("TRANSMISSION_DOWNLOAD_DIR", "/downloads")
	transmissionClient      = &http.Client{Timeout: 12 * time.Second}
	transmissionSessionID   string
	transmissionSessionMu   sync.Mutex
)

const (
	hlsTranscodeVersion = "v4"
	mp4TranscodeVersion = "v2"
	mp4ReadyMinBytes    = 64 * 1024
)

func init() {
	_ = mime.AddExtensionType(".m3u8", "application/vnd.apple.mpegurl")
	_ = mime.AddExtensionType(".ts", "video/mp2t")
}

type Video struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modifiedAt"`
}

type TorrentInfo struct {
	ID             int           `json:"id"`
	Name           string        `json:"name"`
	Status         string        `json:"status"`
	PercentDone    float64       `json:"percentDone"`
	Progress       int           `json:"progress"`
	RateDownload   int64         `json:"rateDownload"`
	ETA            int           `json:"eta"`
	SizeWhenDone   int64         `json:"sizeWhenDone"`
	DownloadedEver int64         `json:"downloadedEver"`
	AddedDate      int64         `json:"addedDate"`
	IsFinished     bool          `json:"isFinished"`
	Files          []TorrentFile `json:"files"`
}

type TorrentFile struct {
	Index          int    `json:"index"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	BytesCompleted int64  `json:"bytesCompleted"`
	Progress       int    `json:"progress"`
	Streamable     bool   `json:"streamable"`
}

type transmissionResponse struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

type transmissionTorrent struct {
	ID             int                `json:"id"`
	Name           string             `json:"name"`
	Status         int                `json:"status"`
	PercentDone    float64            `json:"percentDone"`
	RateDownload   int64              `json:"rateDownload"`
	ETA            int                `json:"eta"`
	SizeWhenDone   int64              `json:"sizeWhenDone"`
	DownloadedEver int64              `json:"downloadedEver"`
	AddedDate      int64              `json:"addedDate"`
	IsFinished     bool               `json:"isFinished"`
	Files          []transmissionFile `json:"files"`
}

type transmissionFile struct {
	BytesCompleted int64  `json:"bytesCompleted"`
	Length         int64  `json:"length"`
	Name           string `json:"name"`
}

func main() {
	os.MkdirAll(videosDir, 0755)
	os.MkdirAll(hlsDir, 0755)
	os.MkdirAll(mp4Dir, 0755)

	r := mux.NewRouter()

	r.HandleFunc("/api/videos", listVideos).Methods("GET")
	r.HandleFunc("/api/stream/{path:.*}", streamVideo).Methods("GET")
	r.HandleFunc("/api/stream-mp4/{path:.*}", streamMKVAsMP4).Methods("GET")
	r.HandleFunc("/api/mp4-start/{path:.*}", startMP4).Methods("POST")
	r.HandleFunc("/api/mp4-status/{path:.*}", mp4Status).Methods("GET")
	r.HandleFunc("/api/upload", uploadChunk).Methods("POST")
	r.HandleFunc("/api/hls-start/{path:.*}", startHLS).Methods("POST")
	r.HandleFunc("/api/hls-status/{path:.*}", hlsStatus).Methods("GET")
	r.HandleFunc("/api/torrents", listTorrents).Methods("GET")
	r.HandleFunc("/api/torrent/upload", uploadTorrent).Methods("POST")
	r.PathPrefix("/hls/").Handler(http.StripPrefix("/hls/", http.FileServer(http.Dir(hlsDir))))

	c := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "OPTIONS"},
	})

	handler := c.Handler(r)

	fmt.Println("Server started on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}

func listVideos(w http.ResponseWriter, r *http.Request) {
	var videos []Video

	_ = filepath.WalkDir(videosDir, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !allowedExts[ext] {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(videosDir, filePath)
		if err != nil {
			return nil
		}

		rel = filepath.ToSlash(rel)

		videos = append(videos, Video{
			Name:       entry.Name(),
			Path:       rel,
			Size:       info.Size(),
			ModifiedAt: info.ModTime().Unix(),
		})

		return nil
	})

	sort.Slice(videos, func(i, j int) bool {
		return videos[i].ModifiedAt > videos[j].ModifiedAt
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(videos)
}

func streamVideo(w http.ResponseWriter, r *http.Request) {
	relPath, fullPath, err := resolveVideoPath(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	contentType := contentTypeFor(relPath)
	streamFile(w, r, fullPath, contentType)
}

func streamFile(w http.ResponseWriter, r *http.Request, fullPath, contentType string) {
	file, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fileSize := fileInfo.Size()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType)

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		io.Copy(w, file)
		return
	}

	var start, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &start); err != nil {
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	end = fileSize - 1
	if strings.Contains(rangeHeader, "-") {
		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		if len(parts) == 2 && parts[1] != "" {
			fmt.Sscanf(parts[1], "%d", &end)
		}
	}

	if start < 0 || start >= fileSize {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	if end >= fileSize {
		end = fileSize - 1
	}

	if start > end {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	contentLength := end - start + 1

	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.WriteHeader(http.StatusPartialContent)

	file.Seek(start, 0)
	io.CopyN(w, file, contentLength)
}

func streamGrowingFile(w http.ResponseWriter, r *http.Request, fullPath, contentType string, allowWait bool) {
	file, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fileSize := fileInfo.Size()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType)

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		io.Copy(w, file)
		return
	}

	var start, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &start); err != nil {
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	end = fileSize - 1
	if strings.Contains(rangeHeader, "-") {
		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		if len(parts) == 2 && parts[1] != "" {
			fmt.Sscanf(parts[1], "%d", &end)
		}
	}

	if allowWait && start >= fileSize {
		for i := 0; i < 25; i++ {
			time.Sleep(200 * time.Millisecond)
			info, err := os.Stat(fullPath)
			if err != nil {
				break
			}
			fileSize = info.Size()
			if start < fileSize {
				break
			}
		}
	}

	if start < 0 || start >= fileSize {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	if end >= fileSize {
		end = fileSize - 1
	}

	if start > end {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	contentLength := end - start + 1

	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.WriteHeader(http.StatusPartialContent)

	file.Seek(start, 0)
	io.CopyN(w, file, contentLength)
}

func streamMKVAsMP4(w http.ResponseWriter, r *http.Request) {
	relPath, _, err := resolveVideoPath(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.ToLower(filepath.Ext(relPath)) != ".mkv" {
		http.Error(w, "Unsupported file type", http.StatusBadRequest)
		return
	}

	outputDir, outputPath, _ := mp4Paths(relPath)
	if info, err := os.Stat(outputPath); err != nil || info.Size() < mp4ReadyMinBytes || !mp4TranscodeMarkerExists(outputDir) {
		http.Error(w, "MP4 not ready", http.StatusNotFound)
		return
	}

	streamGrowingFile(w, r, outputPath, "video/mp4", isMP4ConversionActive(relPath))
}

func startMP4(w http.ResponseWriter, r *http.Request) {
	relPath, _, err := resolveVideoPath(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.ToLower(filepath.Ext(relPath)) != ".mkv" {
		http.Error(w, "Unsupported file type", http.StatusBadRequest)
		return
	}

	status, url, err := ensureMP4Conversion(relPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "Video not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": status,
		"url":    url,
	})
}

func mp4Status(w http.ResponseWriter, r *http.Request) {
	relPath, _, err := resolveVideoPath(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.ToLower(filepath.Ext(relPath)) != ".mkv" {
		http.Error(w, "Unsupported file type", http.StatusBadRequest)
		return
	}

	outputDir, outputPath, url := mp4Paths(relPath)
	if info, err := os.Stat(outputPath); err == nil && info.Size() >= mp4ReadyMinBytes && mp4TranscodeMarkerExists(outputDir) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ready": true,
			"url":   url,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ready":      false,
		"processing": isMP4ConversionActive(relPath),
		"url":        url,
	})
}

func startHLS(w http.ResponseWriter, r *http.Request) {
	relPath, _, err := resolveVideoPath(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	status, url, err := ensureHLSConversion(relPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "Video not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": status,
		"url":    url,
	})
}

func uploadChunk(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fileName, err := sanitizeUploadName(r.FormValue("fileName"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	chunkIndex, err := strconv.Atoi(r.FormValue("chunkIndex"))
	if err != nil || chunkIndex < 0 {
		http.Error(w, "Invalid chunk index", http.StatusBadRequest)
		return
	}

	totalChunks, err := strconv.Atoi(r.FormValue("totalChunks"))
	if err != nil || totalChunks <= 0 {
		http.Error(w, "Invalid total chunks", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("chunk")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	finalPath := filepath.Join(videosDir, fileName)

	var dst *os.File
	if chunkIndex == 0 {
		dst, err = os.Create(finalPath)
	} else {
		dst, err = os.OpenFile(finalPath, os.O_APPEND|os.O_WRONLY, 0644)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := dst.ReadFrom(file); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]string{"status": "uploaded"}

	if chunkIndex+1 == totalChunks {
		status, url, err := ensureHLSConversion(fileName)
		if err != nil {
			log.Printf("HLS conversion start failed: %v", err)
		} else {
			response["hlsStatus"] = status
			response["url"] = url
		}
		response["status"] = "complete"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func hlsStatus(w http.ResponseWriter, r *http.Request) {
	relPath, _, err := resolveVideoPath(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	outputDir, outputPath, url := hlsPaths(relPath)

	ready, segmentCount := hlsPlaylistReady(outputPath, outputDir)
	if ready {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ready":    true,
			"segments": segmentCount,
			"url":      url,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ready":      false,
		"processing": isConversionActive(relPath),
		"segments":   segmentCount,
		"url":        url,
	})
}

func listTorrents(w http.ResponseWriter, r *http.Request) {
	if !transmissionConfigured() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled": false,
			"items":   []TorrentInfo{},
		})
		return
	}

	items, err := transmissionListTorrents()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled": true,
			"error":   err.Error(),
			"items":   []TorrentInfo{},
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled": true,
		"items":   items,
	})
}

func uploadTorrent(w http.ResponseWriter, r *http.Request) {
	if !transmissionConfigured() {
		http.Error(w, "Transmission is not configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseMultipartForm(5 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("torrent")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	if strings.ToLower(filepath.Ext(header.Filename)) != ".torrent" {
		http.Error(w, "Invalid torrent file", http.StatusBadRequest)
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, 5<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(data) == 0 {
		http.Error(w, "Empty torrent file", http.StatusBadRequest)
		return
	}

	metainfo := base64.StdEncoding.EncodeToString(data)

	_, err = transmissionRequest("torrent-add", map[string]interface{}{
		"metainfo":     metainfo,
		"download-dir": transmissionDownloadDir,
		"paused":       false,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "queued",
	})
}

func ensureHLSConversion(relPath string) (string, string, error) {
	inputPath := filepath.Join(videosDir, filepath.FromSlash(relPath))
	if _, err := os.Stat(inputPath); err != nil {
		return "", "", err
	}

	outputDir, outputPath, url := hlsPaths(relPath)
	if ready, _ := hlsPlaylistReady(outputPath, outputDir); ready {
		if shouldTranscodeForHLS(inputPath) && !hlsTranscodeMarkerExists(outputDir) {
			if isConversionActive(relPath) {
				return "processing", url, nil
			}
			_ = os.RemoveAll(outputDir)
		} else {
			return "ready", url, nil
		}
	}
	if isConversionActive(relPath) {
		return "processing", url, nil
	}
	if _, err := os.Stat(outputPath); err == nil {
		_ = os.RemoveAll(outputDir)
	}

	conversionMu.Lock()
	if conversionActive[relPath] {
		conversionMu.Unlock()
		return "processing", url, nil
	}
	conversionActive[relPath] = true
	conversionMu.Unlock()

	os.MkdirAll(outputDir, 0755)

	go func() {
		defer func() {
			conversionMu.Lock()
			delete(conversionActive, relPath)
			conversionMu.Unlock()
		}()

		if err := runHLSConversion(inputPath, outputDir, outputPath); err != nil {
			log.Printf("HLS conversion error for %s: %v", relPath, err)
		}
	}()

	return "started", url, nil
}

func ensureMP4Conversion(relPath string) (string, string, error) {
	inputPath := filepath.Join(videosDir, filepath.FromSlash(relPath))
	if _, err := os.Stat(inputPath); err != nil {
		return "", "", err
	}

	outputDir, outputPath, url := mp4Paths(relPath)
	if info, err := os.Stat(outputPath); err == nil && info.Size() >= mp4ReadyMinBytes && mp4TranscodeMarkerExists(outputDir) {
		return "ready", url, nil
	}
	if isMP4ConversionActive(relPath) {
		return "processing", url, nil
	}
	if _, err := os.Stat(outputPath); err == nil {
		_ = os.Remove(outputPath)
	}
	if !mp4TranscodeMarkerExists(outputDir) {
		_ = os.Remove(filepath.Join(outputDir, ".mp4transcoded"))
	}

	mp4ConversionMu.Lock()
	if mp4ConversionActive[relPath] {
		mp4ConversionMu.Unlock()
		return "processing", url, nil
	}
	mp4ConversionActive[relPath] = true
	mp4ConversionMu.Unlock()

	os.MkdirAll(outputDir, 0755)
	writeMp4TranscodeMarker(outputDir)

	go func() {
		defer func() {
			mp4ConversionMu.Lock()
			delete(mp4ConversionActive, relPath)
			mp4ConversionMu.Unlock()
		}()

		if err := runMP4Conversion(inputPath, outputPath); err != nil {
			log.Printf("MP4 conversion error for %s: %v", relPath, err)
			_ = os.Remove(outputPath)
			_ = os.Remove(filepath.Join(outputDir, ".mp4transcoded"))
			return
		}
	}()

	return "started", url, nil
}

func runHLSConversion(inputPath, outputDir, outputPath string) error {
	if shouldTranscodeForHLS(inputPath) {
		os.RemoveAll(outputDir)
		os.MkdirAll(outputDir, 0755)
		if err := runFFmpegHLS(inputPath, outputDir, outputPath, true); err != nil {
			os.RemoveAll(outputDir)
			return err
		}
		writeHlsTranscodeMarker(outputDir)
		return nil
	}

	if err := runFFmpegHLS(inputPath, outputDir, outputPath, false); err == nil {
		return nil
	}

	os.RemoveAll(outputDir)
	os.MkdirAll(outputDir, 0755)
	if err := runFFmpegHLS(inputPath, outputDir, outputPath, true); err != nil {
		os.RemoveAll(outputDir)
		return err
	}
	writeHlsTranscodeMarker(outputDir)
	return nil
}

func shouldTranscodeForHLS(inputPath string) bool {
	ext := strings.ToLower(filepath.Ext(inputPath))
	return ext == ".mkv"
}

func runMP4Conversion(inputPath, outputPath string) error {
	if err := runFFmpegMP4(inputPath, outputPath, false); err == nil {
		return nil
	}

	_ = os.Remove(outputPath)
	return runFFmpegMP4(inputPath, outputPath, true)
}

func runFFmpegMP4(inputPath, outputPath string, transcodeVideo bool) error {
	args := []string{
		"-y",
		"-i", inputPath,
		"-sn",
		"-map", "0:v:0?",
		"-map", "0:a:0?",
	}

	if transcodeVideo {
		args = append(args,
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-crf", "20",
			"-sc_threshold", "0",
			"-force_key_frames", "expr:gte(t,n_forced*4)",
		)
	} else {
		args = append(args, "-c:v", "copy")
	}

	args = append(args,
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "192k",
		"-ar", "48000",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4",
		outputPath,
	)

	cmd := exec.Command("ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func runFFmpegHLS(inputPath, outputDir, outputPath string, transcode bool) error {
	args := []string{"-y", "-i", inputPath, "-sn"}
	if transcode {
		args = append(args,
			"-c:v", "libx264",
			"-preset", "fast",
			"-crf", "18",
			"-sc_threshold", "0",
			"-force_key_frames", "expr:gte(t,n_forced*4)",
			"-c:a", "aac",
			"-ac", "2",
			"-b:a", "192k",
			"-ar", "48000",
		)
	} else {
		args = append(args,
			"-c:v", "copy",
			"-c:a", "aac",
			"-ac", "2",
			"-b:a", "192k",
			"-ar", "48000",
		)
	}

	args = append(args,
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "0",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", filepath.Join(outputDir, "segment%03d.ts"),
		"-start_number", "0",
		outputPath,
	)

	cmd := exec.Command("ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func transmissionListTorrents() ([]TorrentInfo, error) {
	resp, err := transmissionRequest("torrent-get", map[string]interface{}{
		"fields": []string{
			"id",
			"name",
			"status",
			"percentDone",
			"rateDownload",
			"eta",
			"sizeWhenDone",
			"downloadedEver",
			"addedDate",
			"isFinished",
			"files",
		},
	})
	if err != nil {
		return nil, err
	}

	var args struct {
		Torrents []transmissionTorrent `json:"torrents"`
	}
	if err := json.Unmarshal(resp.Arguments, &args); err != nil {
		return nil, err
	}

	items := make([]TorrentInfo, 0, len(args.Torrents))
	for _, torrent := range args.Torrents {
		progress := int(torrent.PercentDone*100 + 0.5)
		items = append(items, TorrentInfo{
			ID:             torrent.ID,
			Name:           torrent.Name,
			Status:         mapTransmissionStatus(torrent.Status),
			PercentDone:    torrent.PercentDone,
			Progress:       progress,
			RateDownload:   torrent.RateDownload,
			ETA:            torrent.ETA,
			SizeWhenDone:   torrent.SizeWhenDone,
			DownloadedEver: torrent.DownloadedEver,
			AddedDate:      torrent.AddedDate,
			IsFinished:     torrent.IsFinished,
			Files:          mapTorrentFiles(torrent.Files),
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].AddedDate > items[j].AddedDate
	})

	return items, nil
}

func transmissionRequest(method string, arguments map[string]interface{}) (transmissionResponse, error) {
	if !transmissionConfigured() {
		return transmissionResponse{}, errors.New("Transmission is not configured")
	}

	payload := map[string]interface{}{
		"method":    method,
		"arguments": arguments,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return transmissionResponse{}, err
	}

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest("POST", transmissionURL, bytes.NewReader(data))
		if err != nil {
			return transmissionResponse{}, err
		}

		req.Header.Set("Content-Type", "application/json")
		if sessionID := getTransmissionSessionID(); sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", sessionID)
		}
		if transmissionUser != "" || transmissionPass != "" {
			req.SetBasicAuth(transmissionUser, transmissionPass)
		}

		resp, err := transmissionClient.Do(req)
		if err != nil {
			return transmissionResponse{}, err
		}

		if resp.StatusCode == http.StatusConflict {
			newID := resp.Header.Get("X-Transmission-Session-Id")
			resp.Body.Close()
			if newID == "" {
				return transmissionResponse{}, errors.New("Transmission session id missing")
			}
			setTransmissionSessionID(newID)
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			return transmissionResponse{}, fmt.Errorf("Transmission error: %s", strings.TrimSpace(string(body)))
		}

		var response transmissionResponse
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			return transmissionResponse{}, err
		}

		if response.Result != "success" {
			return transmissionResponse{}, fmt.Errorf("Transmission error: %s", response.Result)
		}

		return response, nil
	}

	return transmissionResponse{}, errors.New("Transmission session negotiation failed")
}

func mapTransmissionStatus(code int) string {
	switch code {
	case 0:
		return "stopped"
	case 1:
		return "check_wait"
	case 2:
		return "checking"
	case 3:
		return "download_wait"
	case 4:
		return "downloading"
	case 5:
		return "seed_wait"
	case 6:
		return "seeding"
	default:
		return "unknown"
	}
}

func mapTorrentFiles(files []transmissionFile) []TorrentFile {
	items := make([]TorrentFile, 0, len(files))
	for idx, file := range files {
		rel := filepath.ToSlash(file.Name)
		rel, err := sanitizeVideoPath(rel)
		if err != nil {
			continue
		}

		progress := 0
		if file.Length > 0 {
			progress = int(float64(file.BytesCompleted)/float64(file.Length)*100 + 0.5)
		}

		items = append(items, TorrentFile{
			Index:          idx,
			Name:           file.Name,
			Path:           rel,
			Size:           file.Length,
			BytesCompleted: file.BytesCompleted,
			Progress:       progress,
			Streamable:     file.BytesCompleted > 0 && fileExists(rel),
		})
	}

	return items
}

func getPathParam(r *http.Request) string {
	vars := mux.Vars(r)
	if value := vars["path"]; value != "" {
		return value
	}
	return r.URL.Query().Get("path")
}

func resolveVideoPath(raw string) (string, string, error) {
	relPath, err := sanitizeVideoPath(raw)
	if err != nil {
		return "", "", err
	}

	fullPath := filepath.Join(videosDir, filepath.FromSlash(relPath))
	if !isWithinDir(videosDir, fullPath) {
		return "", "", errors.New("Invalid file name")
	}

	return relPath, fullPath, nil
}

func sanitizeVideoPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("Invalid file name")
	}

	value = strings.ReplaceAll(value, "\\", "/")
	cleaned := path.Clean("/" + value)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return "", errors.New("Invalid file name")
	}

	ext := strings.ToLower(path.Ext(cleaned))
	if !allowedExts[ext] {
		return "", errors.New("Unsupported file type")
	}

	return cleaned, nil
}

func sanitizeUploadName(raw string) (string, error) {
	relPath, err := sanitizeVideoPath(raw)
	if err != nil {
		return "", err
	}
	if strings.Contains(relPath, "/") {
		return "", errors.New("Invalid file name")
	}
	return relPath, nil
}

func isWithinDir(basePath, targetPath string) bool {
	baseAbs, err := filepath.Abs(basePath)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return false
	}

	separator := string(os.PathSeparator)
	if rel == ".." || strings.HasPrefix(rel, ".."+separator) {
		return false
	}

	return true
}

func fileExists(relPath string) bool {
	fullPath := filepath.Join(videosDir, filepath.FromSlash(relPath))
	if !isWithinDir(videosDir, fullPath) {
		return false
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return false
	}

	return !info.IsDir()
}

func isConversionActive(relPath string) bool {
	conversionMu.Lock()
	defer conversionMu.Unlock()
	return conversionActive[relPath]
}

func isMP4ConversionActive(relPath string) bool {
	mp4ConversionMu.Lock()
	defer mp4ConversionMu.Unlock()
	return mp4ConversionActive[relPath]
}

func hlsTranscodeMarkerExists(outputDir string) bool {
	data, err := os.ReadFile(filepath.Join(outputDir, ".transcoded"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == hlsTranscodeVersion
}

func writeHlsTranscodeMarker(outputDir string) {
	_ = os.WriteFile(filepath.Join(outputDir, ".transcoded"), []byte(hlsTranscodeVersion), 0644)
}

func mp4TranscodeMarkerExists(outputDir string) bool {
	data, err := os.ReadFile(filepath.Join(outputDir, ".mp4transcoded"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == mp4TranscodeVersion
}

func writeMp4TranscodeMarker(outputDir string) {
	_ = os.WriteFile(filepath.Join(outputDir, ".mp4transcoded"), []byte(mp4TranscodeVersion), 0644)
}

func hlsPlaylistReady(outputPath, outputDir string) (bool, int) {
	segmentCount := hlsSegmentCount(outputDir)

	info, err := os.Stat(outputPath)
	if err != nil || info.Size() == 0 {
		return false, segmentCount
	}

	file, err := os.Open(outputPath)
	if err != nil {
		return false, segmentCount
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return false, segmentCount
	}

	content := string(data)
	if !strings.Contains(content, "#EXTM3U") {
		return false, segmentCount
	}
	if !strings.Contains(content, "#EXTINF") {
		return false, segmentCount
	}

	return segmentCount > 0, segmentCount
}

func hlsSegmentCount(outputDir string) int {
	files, err := os.ReadDir(outputDir)
	if err != nil {
		return 0
	}

	segmentCount := 0
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".ts") {
			segmentCount++
		}
	}

	return segmentCount
}

func hlsPaths(relPath string) (string, string, string) {
	base := strings.TrimSuffix(relPath, path.Ext(relPath))
	outputDir := filepath.Join(hlsDir, filepath.FromSlash(base))
	outputPath := filepath.Join(outputDir, "index.m3u8")
	rawURLPath := "/hls/" + base + "/index.m3u8"
	urlValue := (&url.URL{Path: rawURLPath}).String()
	return outputDir, outputPath, urlValue
}

func mp4Paths(relPath string) (string, string, string) {
	base := strings.TrimSuffix(relPath, path.Ext(relPath))
	outputPath := filepath.Join(mp4Dir, filepath.FromSlash(base)+".mp4")
	outputDir := filepath.Dir(outputPath)
	rawURLPath := "/api/stream-mp4/" + relPath
	urlValue := (&url.URL{Path: rawURLPath}).String()
	return outputDir, outputPath, urlValue
}

func contentTypeFor(name string) string {
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if contentType == "" {
		return "application/octet-stream"
	}
	return contentType
}

func transmissionConfigured() bool {
	return transmissionURL != ""
}

func getTransmissionSessionID() string {
	transmissionSessionMu.Lock()
	defer transmissionSessionMu.Unlock()
	return transmissionSessionID
}

func setTransmissionSessionID(value string) {
	transmissionSessionMu.Lock()
	defer transmissionSessionMu.Unlock()
	transmissionSessionID = value
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
