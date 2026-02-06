package torrent

import (
	"encoding/base64"
	"errors"
	"io"

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
