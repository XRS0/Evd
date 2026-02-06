package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	mediadomain "evd/internal/domain/media"
	torrentdomain "evd/internal/domain/torrent"
	"github.com/gorilla/mux"
)

type mediaUseCases interface {
	ListVideos() ([]mediadomain.Video, error)
	StartHLS(ctx context.Context, rawPath string, follow bool) (mediadomain.JobStatus, error)
	HLSStatus(rawPath string) (mediadomain.JobStatus, error)
	StartMP4(ctx context.Context, rawPath string) (mediadomain.JobStatus, error)
	MP4Status(rawPath string) (mediadomain.JobStatus, error)
	StreamMP4(ctx context.Context, rawPath string, follow bool, out io.Writer) error
}

type torrentUseCases interface {
	Enabled() bool
	List() ([]torrentdomain.Info, error)
	AddTorrent(r io.Reader) error
	EnableStreaming(id int) error
}

type mediaPathStore interface {
	ResolveVideoPath(raw string) (string, string, error)
	MP4Paths(relPath string) (string, string, string)
	VideosRoot() string
}

type Handler struct {
	media    mediaUseCases
	torrents torrentUseCases
	store    mediaPathStore
}

// NewHandler wires HTTP handlers with application use cases.
func NewHandler(mediaService mediaUseCases, torrentService torrentUseCases, store mediaPathStore) *Handler {
	return &Handler{media: mediaService, torrents: torrentService, store: store}
}

// ListVideos handles GET /api/videos.
func (h *Handler) ListVideos(w http.ResponseWriter, r *http.Request) {
	videos, err := h.media.ListVideos()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := make([]map[string]interface{}, 0, len(videos))
	for _, v := range videos {
		resp = append(resp, map[string]interface{}{
			"name":       v.Name,
			"path":       v.Path,
			"size":       v.Size,
			"modifiedAt": v.ModifiedAt.Unix(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// StreamVideo handles direct file streaming endpoint.
func (h *Handler) StreamVideo(w http.ResponseWriter, r *http.Request) {
	_, full, err := h.store.ResolveVideoPath(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(full)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	streamFile(w, r, full, contentType)
}

// StreamPlay handles ffmpeg-based live mp4 stream endpoint.
func (h *Handler) StreamPlay(w http.ResponseWriter, r *http.Request) {
	follow := r.URL.Query().Get("follow") == "1"
	path := getPathParam(r)
	if path == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	_ = h.media.StreamMP4(r.Context(), path, follow, w)
}

// StreamMP4 handles seekable mp4 output endpoint.
func (h *Handler) StreamMP4(w http.ResponseWriter, r *http.Request) {
	rel, _, err := h.store.ResolveVideoPath(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.ToLower(filepath.Ext(rel)) == ".mp4" {
		http.Error(w, "Unsupported file type", http.StatusBadRequest)
		return
	}
	_, outputPath, _ := h.store.MP4Paths(rel)
	status, err := h.media.MP4Status(rel)
	if err != nil || !status.Ready {
		http.Error(w, "MP4 not ready", http.StatusNotFound)
		return
	}
	streamFile(w, r, outputPath, "video/mp4")
}

// StartHLS handles HLS conversion kickoff endpoint.
func (h *Handler) StartHLS(w http.ResponseWriter, r *http.Request) {
	follow := r.URL.Query().Get("follow") == "1"
	status, err := h.media.StartHLS(r.Context(), getPathParam(r), follow)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "Video not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": string(status.State),
		"url":    status.URL,
	})
}

// HLSStatus handles HLS conversion status endpoint.
func (h *Handler) HLSStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.media.HLSStatus(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ready":      status.Ready,
		"processing": status.Processing,
		"segments":   status.Segments,
		"url":        status.URL,
		"state":      status.State,
		"error":      status.Error,
	})
}

// StartMP4 handles mp4 conversion kickoff endpoint.
func (h *Handler) StartMP4(w http.ResponseWriter, r *http.Request) {
	status, err := h.media.StartMP4(r.Context(), getPathParam(r))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "Video not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": string(status.State),
		"url":    status.URL,
	})
}

// MP4Status handles mp4 conversion status endpoint.
func (h *Handler) MP4Status(w http.ResponseWriter, r *http.Request) {
	status, err := h.media.MP4Status(getPathParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ready":      status.Ready,
		"processing": status.Processing,
		"url":        status.URL,
		"state":      status.State,
		"error":      status.Error,
		"progress":   status.Progress,
	})
}

// UploadChunk handles chunked file uploads endpoint.
func (h *Handler) UploadChunk(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fileName, err := mediadomain.NormalizeVideoPath(r.FormValue("fileName"))
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

	finalPath := filepath.Join(h.store.VideosRoot(), fileName)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var dst *os.File
	if chunkIndex == 0 {
		dst, err = os.Create(finalPath)
	} else {
		dst, err = os.OpenFile(finalPath, os.O_APPEND|os.O_WRONLY, 0o644)
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
		if strings.ToLower(filepath.Ext(fileName)) != ".mp4" {
			status, err := h.media.StartHLS(r.Context(), fileName, false)
			if err == nil {
				response["hlsStatus"] = string(status.State)
				response["url"] = status.URL
			}
		}
		response["status"] = "complete"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// ListTorrents handles torrent listing endpoint.
func (h *Handler) ListTorrents(w http.ResponseWriter, r *http.Request) {
	if !h.torrents.Enabled() {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled": false,
			"items":   []interface{}{},
		})
		return
	}

	items, err := h.torrents.List()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled": true,
			"error":   err.Error(),
			"items":   []interface{}{},
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled": true,
		"items":   items,
	})
}

// UploadTorrent handles torrent file upload endpoint.
func (h *Handler) UploadTorrent(w http.ResponseWriter, r *http.Request) {
	if !h.torrents.Enabled() {
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

	if err := h.torrents.AddTorrent(file); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "queued"})
}

// EnableTorrentStream handles sequential download toggle endpoint.
func (h *Handler) EnableTorrentStream(w http.ResponseWriter, r *http.Request) {
	if !h.torrents.Enabled() {
		http.Error(w, "Transmission is not configured", http.StatusServiceUnavailable)
		return
	}

	idParam := mux.Vars(r)["id"]
	id, err := strconv.Atoi(idParam)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid torrent id", http.StatusBadRequest)
		return
	}

	if err := h.torrents.EnableStreaming(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func getPathParam(r *http.Request) string {
	value := mux.Vars(r)["path"]
	if value != "" {
		return value
	}
	return r.URL.Query().Get("path")
}
