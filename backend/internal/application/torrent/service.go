package torrent

import (
	"encoding/base64"
	"errors"
	"io"
	"math"

	"evd/internal/domain/torrent"
)

// Service handles torrent use cases.
type Service struct {
	gateway Gateway
}

// NewService creates torrent use-case service with injected gateway.
func NewService(gateway Gateway) *Service {
	return &Service{gateway: gateway}
}

// Enabled reports whether torrent backend is available.
func (s *Service) Enabled() bool {
	return s.gateway.Enabled()
}

// List returns torrents visible in backend.
func (s *Service) List() ([]torrent.Info, error) {
	return s.gateway.List()
}

// AddTorrent validates and submits torrent metadata.
func (s *Service) AddTorrent(r io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(r, 5<<20))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return io.ErrUnexpectedEOF
	}
	metainfo := base64.StdEncoding.EncodeToString(data)
	return s.gateway.AddTorrent(metainfo)
}

// EnableStreaming enables sequential download for faster preview playback.
func (s *Service) EnableStreaming(id int) error {
	if !s.Enabled() {
		return errors.New("Transmission is not configured")
	}
	return s.gateway.SetSequentialDownload(id, true)
}

// SetStreamingFocus prioritizes torrent download around current playback position.
func (s *Service) SetStreamingFocus(id, fileIndex int, currentTime, duration float64) error {
	if !s.Enabled() {
		return errors.New("Transmission is not configured")
	}
	if id <= 0 || fileIndex < 0 {
		return errors.New("invalid torrent or file index")
	}

	positionRatio := 0.0
	if !math.IsNaN(currentTime) && !math.IsInf(currentTime, 0) && currentTime > 0 &&
		!math.IsNaN(duration) && !math.IsInf(duration, 0) && duration > 0 {
		positionRatio = currentTime / duration
	}

	if positionRatio < 0 {
		positionRatio = 0
	}
	if positionRatio > 1 {
		positionRatio = 1
	}

	return s.gateway.SetStreamingFocus(id, fileIndex, positionRatio)
}
