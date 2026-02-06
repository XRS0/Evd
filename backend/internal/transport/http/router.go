package http

import (
	"net/http"

	"github.com/gorilla/mux"
)

// NewRouter configures HTTP routes and static HLS serving.
func NewRouter(handler *Handler, hlsDir string) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/api/videos", handler.ListVideos).Methods("GET")
	r.HandleFunc("/api/stream/{path:.*}", handler.StreamVideo).Methods("GET")
	r.HandleFunc("/api/play/{path:.*}", handler.StreamPlay).Methods("GET")
	r.HandleFunc("/api/stream-mp4/{path:.*}", handler.StreamMP4).Methods("GET")
	r.HandleFunc("/api/hls-start/{path:.*}", handler.StartHLS).Methods("POST")
	r.HandleFunc("/api/hls-status/{path:.*}", handler.HLSStatus).Methods("GET")
	r.HandleFunc("/api/mp4-start/{path:.*}", handler.StartMP4).Methods("POST")
	r.HandleFunc("/api/mp4-status/{path:.*}", handler.MP4Status).Methods("GET")
	r.HandleFunc("/api/upload", handler.UploadChunk).Methods("POST")
	r.HandleFunc("/api/torrents", handler.ListTorrents).Methods("GET")
	r.HandleFunc("/api/torrent/upload", handler.UploadTorrent).Methods("POST")
	r.HandleFunc("/api/torrent/stream/{id}", handler.EnableTorrentStream).Methods("POST")
	r.PathPrefix("/hls/").Handler(http.StripPrefix("/hls/", http.FileServer(http.Dir(hlsDir))))
	return r
}
