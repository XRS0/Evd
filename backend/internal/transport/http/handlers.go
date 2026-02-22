package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	authapp "evd/internal/application/auth"
	watchpartyapp "evd/internal/application/watchparty"
	mediadomain "evd/internal/domain/media"
	torrentdomain "evd/internal/domain/torrent"
	"github.com/gorilla/mux"
)

type mediaUseCases interface {
	ListVideos() ([]mediadomain.Video, error)
	DeleteVideo(rawPath string) error
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
	SetStreamingFocus(id, fileIndex int, currentTime, duration float64) error
}

type mediaPathStore interface {
	ResolveVideoPath(raw string) (string, string, error)
	MP4Paths(relPath string) (string, string, string)
	VideosRoot() string
}

type authUseCases interface {
	Register(username, password string) (authapp.User, string, error)
	Login(username, password string) (authapp.User, string, error)
	LoginGuest() (authapp.User, string, error)
	Authenticate(token string) (authapp.User, error)
	Logout(token string)
	SessionTTL() time.Duration
}

type watchPartyUseCases interface {
	CreateHub(ownerID, ownerName, videoPath string, currentTime float64, playing bool) (watchpartyapp.Snapshot, error)
	GetHub(hubID string) (watchpartyapp.Snapshot, error)
	Subscribe(hubID, userID, username string) (<-chan watchpartyapp.Event, func(), error)
	Control(hubID, userID, username string, input watchpartyapp.ControlInput) (watchpartyapp.Event, error)
	Chat(hubID, userID, username, text string) (watchpartyapp.Event, error)
}

type Handler struct {
	media    mediaUseCases
	torrents torrentUseCases
	store    mediaPathStore
	auth     authUseCases
	watch    watchPartyUseCases
}

const sessionCookieName = "evd_session"

type contextKey string

const userContextKey contextKey = "user"

// NewHandler wires HTTP handlers with application use cases.
func NewHandler(
	mediaService mediaUseCases,
	torrentService torrentUseCases,
	store mediaPathStore,
	authService authUseCases,
	watchService watchPartyUseCases,
) *Handler {
	return &Handler{
		media:    mediaService,
		torrents: torrentService,
		store:    store,
		auth:     authService,
		watch:    watchService,
	}
}

// RequireAuth verifies the request session and injects user context.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		token := sessionTokenFromRequest(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		user, err := h.auth.Authenticate(token)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Register handles account registration and starts a session.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var payload credentialsRequest
	if err := decodeJSON(r, &payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	user, sessionToken, err := h.auth.Register(payload.Username, payload.Password)
	if err != nil {
		switch {
		case errors.Is(err, authapp.ErrUserExists):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, authapp.ErrInvalidInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, "Unable to register user", http.StatusInternalServerError)
		}
		return
	}

	setSessionCookie(w, sessionToken, h.auth.SessionTTL())
	writeJSON(w, map[string]interface{}{
		"user": user,
	})
}

// Login handles account sign-in and starts a session.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var payload credentialsRequest
	if err := decodeJSON(r, &payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	user, sessionToken, err := h.auth.Login(payload.Username, payload.Password)
	if err != nil {
		switch {
		case errors.Is(err, authapp.ErrInvalidCredentials):
			http.Error(w, err.Error(), http.StatusUnauthorized)
		default:
			http.Error(w, "Unable to login", http.StatusInternalServerError)
		}
		return
	}

	setSessionCookie(w, sessionToken, h.auth.SessionTTL())
	writeJSON(w, map[string]interface{}{
		"user": user,
	})
}

// LoginGuest starts an anonymous guest session.
func (h *Handler) LoginGuest(w http.ResponseWriter, _ *http.Request) {
	user, sessionToken, err := h.auth.LoginGuest()
	if err != nil {
		http.Error(w, "Unable to login as guest", http.StatusInternalServerError)
		return
	}

	setSessionCookie(w, sessionToken, h.auth.SessionTTL())
	writeJSON(w, map[string]interface{}{
		"user": user,
	})
}

// Logout clears the current session.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	sessionToken := sessionTokenFromRequest(r)
	if sessionToken != "" {
		h.auth.Logout(sessionToken)
	}

	clearSessionCookie(w)
	writeJSON(w, map[string]string{"status": "ok"})
}

// Me returns the active authenticated user.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	sessionToken := sessionTokenFromRequest(r)
	if sessionToken == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user, err := h.auth.Authenticate(sessionToken)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	writeJSON(w, map[string]interface{}{
		"user": user,
	})
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

// DeleteVideo handles DELETE /api/videos/{path}.
func (h *Handler) DeleteVideo(w http.ResponseWriter, r *http.Request) {
	if err := h.media.DeleteVideo(getPathParam(r)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "Video not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]bool{"ok": true})
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

// FocusTorrentStream updates torrent download priority near current playback position.
func (h *Handler) FocusTorrentStream(w http.ResponseWriter, r *http.Request) {
	if !h.torrents.Enabled() {
		http.Error(w, "Transmission is not configured", http.StatusServiceUnavailable)
		return
	}

	var payload torrentFocusRequest
	if err := decodeJSON(r, &payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if payload.TorrentID <= 0 || payload.FileIndex < 0 {
		http.Error(w, "Invalid torrent target", http.StatusBadRequest)
		return
	}

	if err := h.torrents.SetStreamingFocus(payload.TorrentID, payload.FileIndex, payload.CurrentTime, payload.Duration); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}

// CreateWatchHub creates a collaborative watch hub.
func (h *Handler) CreateWatchHub(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var payload watchHubCreateRequest
	if err := decodeJSON(r, &payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	videoPath := strings.TrimSpace(payload.VideoPath)
	if videoPath == "" {
		http.Error(w, "videoPath is required", http.StatusBadRequest)
		return
	}

	relPath, _, err := h.store.ResolveVideoPath(videoPath)
	if err != nil {
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}

	currentTime := payload.CurrentTime
	if math.IsNaN(currentTime) || math.IsInf(currentTime, 0) || currentTime < 0 {
		currentTime = 0
	}
	playing := false
	if payload.Playing != nil {
		playing = *payload.Playing
	}

	hub, err := h.watch.CreateHub(user.ID, user.Username, relPath, currentTime, playing)
	if err != nil {
		http.Error(w, "Unable to create watch hub", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"hub":        hub,
		"invitePath": fmt.Sprintf("/watch-together?hub=%s", url.QueryEscape(hub.ID)),
	})
}

// GetWatchHub returns the current hub state.
func (h *Handler) GetWatchHub(w http.ResponseWriter, r *http.Request) {
	hubID := strings.TrimSpace(mux.Vars(r)["id"])
	hub, err := h.watch.GetHub(hubID)
	if err != nil {
		switch {
		case errors.Is(err, watchpartyapp.ErrHubNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}

	writeJSON(w, map[string]interface{}{
		"hub":        hub,
		"invitePath": fmt.Sprintf("/watch-together?hub=%s", url.QueryEscape(hub.ID)),
	})
}

// ControlWatchHub applies playback controls in a hub.
func (h *Handler) ControlWatchHub(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	hubID := strings.TrimSpace(mux.Vars(r)["id"])
	var payload watchHubControlRequest
	if err := decodeJSON(r, &payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	videoPath := strings.TrimSpace(payload.VideoPath)
	if videoPath != "" {
		relPath, _, err := h.store.ResolveVideoPath(videoPath)
		if err != nil {
			http.Error(w, "Video not found", http.StatusNotFound)
			return
		}
		videoPath = relPath
	}

	event, err := h.watch.Control(hubID, user.ID, user.Username, watchpartyapp.ControlInput{
		Action:      payload.Action,
		VideoPath:   videoPath,
		CurrentTime: payload.CurrentTime,
		Playing:     payload.Playing,
	})
	if err != nil {
		switch {
		case errors.Is(err, watchpartyapp.ErrHubNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, watchpartyapp.ErrInvalidInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, "Unable to update hub state", http.StatusInternalServerError)
		}
		return
	}

	writeJSON(w, map[string]interface{}{
		"event": event,
	})
}

// SendWatchHubChat appends a chat message into the hub.
func (h *Handler) SendWatchHubChat(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	hubID := strings.TrimSpace(mux.Vars(r)["id"])
	var payload watchHubChatRequest
	if err := decodeJSON(r, &payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	event, err := h.watch.Chat(hubID, user.ID, user.Username, payload.Text)
	if err != nil {
		switch {
		case errors.Is(err, watchpartyapp.ErrHubNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, watchpartyapp.ErrInvalidInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, "Unable to send chat message", http.StatusInternalServerError)
		}
		return
	}

	writeJSON(w, map[string]interface{}{
		"event": event,
	})
}

// WatchHubEvents streams SSE updates for a hub.
func (h *Handler) WatchHubEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	hubID := strings.TrimSpace(mux.Vars(r)["id"])
	events, done, err := h.watch.Subscribe(hubID, user.ID, user.Username)
	if err != nil {
		switch {
		case errors.Is(err, watchpartyapp.ErrHubNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	defer done()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, open := <-events:
			if !open {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := io.WriteString(w, "data: "); err != nil {
				return
			}
			if _, err := w.Write(payload); err != nil {
				return
			}
			if _, err := io.WriteString(w, "\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func getPathParam(r *http.Request) string {
	value := mux.Vars(r)["path"]
	if value != "" {
		return value
	}
	return r.URL.Query().Get("path")
}

func requestUser(r *http.Request) (authapp.User, bool) {
	value := r.Context().Value(userContextKey)
	user, ok := value.(authapp.User)
	if !ok || user.ID == "" {
		return authapp.User{}, false
	}
	return user, true
}

func decodeJSON(r *http.Request, out interface{}) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func sessionTokenFromRequest(r *http.Request) string {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if token := strings.TrimSpace(cookie.Value); token != "" {
			return token
		}
	}

	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}

	return ""
}

func setSessionCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

type credentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type watchHubCreateRequest struct {
	VideoPath   string  `json:"videoPath"`
	CurrentTime float64 `json:"currentTime"`
	Playing     *bool   `json:"playing"`
}

type watchHubControlRequest struct {
	Action      string  `json:"action"`
	VideoPath   string  `json:"videoPath"`
	CurrentTime float64 `json:"currentTime"`
	Playing     *bool   `json:"playing"`
}

type watchHubChatRequest struct {
	Text string `json:"text"`
}

type torrentFocusRequest struct {
	TorrentID   int     `json:"torrentId"`
	FileIndex   int     `json:"fileIndex"`
	CurrentTime float64 `json:"currentTime"`
	Duration    float64 `json:"duration"`
}
