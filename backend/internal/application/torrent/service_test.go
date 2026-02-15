package torrent

import (
	"errors"
	"io"
	"testing"

	domain "evd/internal/domain/torrent"
)

type stubGateway struct {
	enabled bool

	lastID        int
	lastFileIndex int
	lastRatio     float64

	focusErr error
}

func (s *stubGateway) Enabled() bool { return s.enabled }

func (s *stubGateway) List() ([]domain.Info, error) { return nil, nil }

func (s *stubGateway) AddTorrent(_ string) error { return nil }

func (s *stubGateway) SetSequentialDownload(_ int, _ bool) error { return nil }

func (s *stubGateway) SetStreamingFocus(id, fileIndex int, positionRatio float64) error {
	s.lastID = id
	s.lastFileIndex = fileIndex
	s.lastRatio = positionRatio
	return s.focusErr
}

func TestSetStreamingFocus_UsesPlaybackRatio(t *testing.T) {
	gw := &stubGateway{enabled: true}
	svc := NewService(gw)

	if err := svc.SetStreamingFocus(4, 2, 45, 90); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if gw.lastID != 4 || gw.lastFileIndex != 2 {
		t.Fatalf("unexpected target: id=%d fileIndex=%d", gw.lastID, gw.lastFileIndex)
	}
	if gw.lastRatio != 0.5 {
		t.Fatalf("expected ratio 0.5, got %.4f", gw.lastRatio)
	}
}

func TestSetStreamingFocus_ClampsInvalidRatio(t *testing.T) {
	gw := &stubGateway{enabled: true}
	svc := NewService(gw)

	if err := svc.SetStreamingFocus(7, 1, 10, 0); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if gw.lastRatio != 0 {
		t.Fatalf("expected ratio 0 for unknown duration, got %.4f", gw.lastRatio)
	}

	if err := svc.SetStreamingFocus(7, 1, 300, 10); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if gw.lastRatio != 1 {
		t.Fatalf("expected ratio 1 for overflow, got %.4f", gw.lastRatio)
	}
}

func TestSetStreamingFocus_RejectsInvalidTarget(t *testing.T) {
	gw := &stubGateway{enabled: true}
	svc := NewService(gw)

	if err := svc.SetStreamingFocus(0, 1, 5, 10); err == nil {
		t.Fatalf("expected error for invalid torrent id")
	}
	if err := svc.SetStreamingFocus(2, -1, 5, 10); err == nil {
		t.Fatalf("expected error for invalid file index")
	}
}

func TestSetStreamingFocus_RequiresEnabledGateway(t *testing.T) {
	gw := &stubGateway{enabled: false}
	svc := NewService(gw)

	if err := svc.SetStreamingFocus(2, 1, 5, 10); err == nil {
		t.Fatalf("expected configuration error when gateway is disabled")
	}
}

func TestSetStreamingFocus_PropagatesGatewayError(t *testing.T) {
	expected := errors.New("upstream failed")
	gw := &stubGateway{enabled: true, focusErr: expected}
	svc := NewService(gw)

	err := svc.SetStreamingFocus(3, 0, 5, 10)
	if !errors.Is(err, expected) {
		t.Fatalf("expected propagated error %v, got %v", expected, err)
	}
}

func TestAddTorrent_RejectsEmptyPayload(t *testing.T) {
	gw := &stubGateway{enabled: true}
	svc := NewService(gw)
	err := svc.AddTorrent(io.LimitReader(&emptyReader{}, 0))
	if err == nil {
		t.Fatalf("expected error for empty payload")
	}
}

type emptyReader struct{}

func (r *emptyReader) Read(_ []byte) (int, error) { return 0, io.EOF }
