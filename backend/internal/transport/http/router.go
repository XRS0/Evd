package http

import (
	"net/http"

	"github.com/gorilla/mux"
)

// NewRouter configures HTTP routes and static HLS serving.
func NewRouter(handler *Handler, hlsDir string) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/api/auth/register", handler.Register).Methods("POST")
	r.HandleFunc("/api/auth/login", handler.Login).Methods("POST")
	r.HandleFunc("/api/auth/guest", handler.LoginGuest).Methods("POST")
	r.HandleFunc("/api/auth/logout", handler.Logout).Methods("POST")
	r.HandleFunc("/api/auth/me", handler.Me).Methods("GET")

	api := r.PathPrefix("/api").Subrouter()
	api.Use(handler.RequireAuth)
	api.HandleFunc("/videos", handler.ListVideos).Methods("GET")
	api.HandleFunc("/stream/{path:.*}", handler.StreamVideo).Methods("GET")
	api.HandleFunc("/play/{path:.*}", handler.StreamPlay).Methods("GET")
	api.HandleFunc("/stream-mp4/{path:.*}", handler.StreamMP4).Methods("GET")
	api.HandleFunc("/hls-start/{path:.*}", handler.StartHLS).Methods("POST")
	api.HandleFunc("/hls-status/{path:.*}", handler.HLSStatus).Methods("GET")
	api.HandleFunc("/mp4-start/{path:.*}", handler.StartMP4).Methods("POST")
	api.HandleFunc("/mp4-status/{path:.*}", handler.MP4Status).Methods("GET")
	api.HandleFunc("/upload", handler.UploadChunk).Methods("POST")
	api.HandleFunc("/torrents", handler.ListTorrents).Methods("GET")
	api.HandleFunc("/torrent/upload", handler.UploadTorrent).Methods("POST")
	api.HandleFunc("/torrent/stream/{id}", handler.EnableTorrentStream).Methods("POST")
	api.HandleFunc("/watch-hubs", handler.CreateWatchHub).Methods("POST")
	api.HandleFunc("/watch-hubs/{id}", handler.GetWatchHub).Methods("GET")
	api.HandleFunc("/watch-hubs/{id}/control", handler.ControlWatchHub).Methods("POST")
	api.HandleFunc("/watch-hubs/{id}/chat", handler.SendWatchHubChat).Methods("POST")
	api.HandleFunc("/watch-hubs/{id}/events", handler.WatchHubEvents).Methods("GET")

	hls := r.PathPrefix("/hls/").Subrouter()
	hls.Use(handler.RequireAuth)
	hls.PathPrefix("/").Handler(http.StripPrefix("/hls/", http.FileServer(http.Dir(hlsDir))))
	return r
}
