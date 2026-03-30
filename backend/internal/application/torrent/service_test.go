package torrent

import (
	"errors"
	"io"
	"strings"
	"testing"

	domain "evd/internal/domain/torrent"
)

type stubGateway struct {
	enabled bool

	lastID        int
	lastFileIndex int
	lastRatio     float64
	lastTitle     string

	startErr error
	stopErr  error
	focusErr error
}

func (s *stubGateway) Enabled() bool { return s.enabled }

func (s *stubGateway) List() ([]domain.Info, error) { return nil, nil }

func (s *stubGateway) AddTorrent(_ string, displayName string) error {
	s.lastTitle = displayName
	return nil
}

func (s *stubGateway) Start(id int) error {
	s.lastID = id
	return s.startErr
}

func (s *stubGateway) Stop(id int) error {
	s.lastID = id
	return s.stopErr
}

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

func TestStart_RejectsInvalidTorrentID(t *testing.T) {
	gw := &stubGateway{enabled: true}
	svc := NewService(gw)

	if err := svc.Start(0); err == nil {
		t.Fatalf("expected error for invalid torrent id")
	}
}

func TestStart_RequiresEnabledGateway(t *testing.T) {
	gw := &stubGateway{enabled: false}
	svc := NewService(gw)

	if err := svc.Start(2); err == nil {
		t.Fatalf("expected configuration error when gateway is disabled")
	}
}

func TestStart_PropagatesGatewayError(t *testing.T) {
	expected := errors.New("upstream start failed")
	gw := &stubGateway{enabled: true, startErr: expected}
	svc := NewService(gw)

	err := svc.Start(3)
	if !errors.Is(err, expected) {
		t.Fatalf("expected propagated error %v, got %v", expected, err)
	}
	if gw.lastID != 3 {
		t.Fatalf("expected start to target id=3, got %d", gw.lastID)
	}
}

func TestStop_RejectsInvalidTorrentID(t *testing.T) {
	gw := &stubGateway{enabled: true}
	svc := NewService(gw)

	if err := svc.Stop(0); err == nil {
		t.Fatalf("expected error for invalid torrent id")
	}
}

func TestStop_RequiresEnabledGateway(t *testing.T) {
	gw := &stubGateway{enabled: false}
	svc := NewService(gw)

	if err := svc.Stop(2); err == nil {
		t.Fatalf("expected configuration error when gateway is disabled")
	}
}

func TestStop_PropagatesGatewayError(t *testing.T) {
	expected := errors.New("upstream stop failed")
	gw := &stubGateway{enabled: true, stopErr: expected}
	svc := NewService(gw)

	err := svc.Stop(4)
	if !errors.Is(err, expected) {
		t.Fatalf("expected propagated error %v, got %v", expected, err)
	}
	if gw.lastID != 4 {
		t.Fatalf("expected stop to target id=4, got %d", gw.lastID)
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
	err := svc.AddTorrent(io.LimitReader(&emptyReader{}, 0), "")
	if err == nil {
		t.Fatalf("expected error for empty payload")
	}
}

func TestAddTorrent_TrimsOptionalDisplayName(t *testing.T) {
	gw := &stubGateway{enabled: true}
	svc := NewService(gw)

	err := svc.AddTorrent(io.LimitReader(strings.NewReader("torrent-data"), 64), "  My Title  ")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if gw.lastTitle != "My Title" {
		t.Fatalf("expected trimmed title, got %q", gw.lastTitle)
	}
}

func TestAddTorrent_RejectsTooLongDisplayName(t *testing.T) {
	gw := &stubGateway{enabled: true}
	svc := NewService(gw)

	err := svc.AddTorrent(io.LimitReader(strings.NewReader("torrent-data"), 64), strings.Repeat("a", maxDisplayNameLength+1))
	if err == nil {
		t.Fatalf("expected error for long display name")
	}
}

type emptyReader struct{}

func (r *emptyReader) Read(_ []byte) (int, error) { return 0, io.EOF }
