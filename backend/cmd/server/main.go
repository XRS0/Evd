package main

import (
	"context"
	"log"
	"mime"
	"net/http"
	"time"

	"evd/internal/application/auth"
	"evd/internal/application/media"
	"evd/internal/application/torrent"
	"evd/internal/application/watchparty"
	"evd/internal/config"
	"evd/internal/infrastructure/ffmpeg"
	"evd/internal/infrastructure/filesystem"
	"evd/internal/infrastructure/transmission"
	httptransport "evd/internal/transport/http"
	"github.com/rs/cors"
)

func main() {
	cfg := config.Load()

	_ = mime.AddExtensionType(".m3u8", "application/vnd.apple.mpegurl")
	_ = mime.AddExtensionType(".ts", "video/mp2t")

	store := filesystem.NewStore(cfg.VideosDir, cfg.HLSDir, cfg.MP4Dir)
	if err := store.EnsureDirs(); err != nil {
		log.Fatalf("storage init failed: %v", err)
	}

	converter := ffmpeg.NewConverter("v4", "v4", cfg.HlsSegmentSeconds)
	mediaService := media.NewService(store, converter, log.Default())
	mediaService.StartMP4Prewarm(context.Background(), 45*time.Second)

	transmissionClient := transmission.NewClient(cfg.TransmissionURL, cfg.TransmissionUser, cfg.TransmissionPass, cfg.TransmissionDownloadDir, store)
	torrentService := torrent.NewService(transmissionClient)

	authService, err := auth.NewService(cfg.UsersFile, time.Duration(cfg.SessionTTLHours)*time.Hour)
	if err != nil {
		log.Fatalf("auth init failed: %v", err)
	}
	watchPartyService := watchparty.NewService()

	handler := httptransport.NewHandler(mediaService, torrentService, store, authService, watchPartyService)
	router := httptransport.NewRouter(handler, cfg.HLSDir)

	c := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "OPTIONS"},
	})

	log.Printf("Server started on %s", cfg.ServerAddr)
	log.Fatal(http.ListenAndServe(cfg.ServerAddr, c.Handler(router)))
}
