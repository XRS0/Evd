package watchparty

import (
	"testing"
	"time"
)

func TestMaterializePosition(t *testing.T) {
	t.Parallel()

	state := RoomPlaybackState{
		RoomID:            "hub-1",
		VideoPath:         "movie.mp4",
		Status:            StatusPlaying,
		BasePositionSec:   10,
		PlaybackRate:      1.25,
		StateVersion:      4,
		ServerTimestampMs: 1_000,
	}

	got := materializePosition(state, 5_000)
	want := 15.0
	if got != want {
		t.Fatalf("materializePosition() = %.3f, want %.3f", got, want)
	}
}

func TestMaterializePositionPausedDoesNotAdvance(t *testing.T) {
	t.Parallel()

	state := RoomPlaybackState{
		RoomID:            "hub-1",
		Status:            StatusPaused,
		BasePositionSec:   42.5,
		PlaybackRate:      1,
		ServerTimestampMs: 1_000,
	}

	got := materializePosition(state, 9_000)
	if got != 42.5 {
		t.Fatalf("materializePosition() paused = %.3f, want %.3f", got, 42.5)
	}
}

func TestReducePlaybackCommandSeekWhilePlaying(t *testing.T) {
	t.Parallel()

	prev := RoomPlaybackState{
		RoomID:            "hub-1",
		VideoPath:         "movie.mp4",
		Status:            StatusPlaying,
		BasePositionSec:   15,
		PlaybackRate:      1,
		StateVersion:      2,
		ServerTimestampMs: 1_000,
		ControllerUserID:  "owner",
	}

	next, changed, err := reducePlaybackCommand(prev, ControlInput{
		Action:      ActionSeek,
		CurrentTime: 88,
	}, 4_000, "user-2")
	if err != nil {
		t.Fatalf("reducePlaybackCommand() error = %v", err)
	}
	if !changed {
		t.Fatal("reducePlaybackCommand() changed = false, want true")
	}
	if next.BasePositionSec != 88 {
		t.Fatalf("next.BasePositionSec = %.3f, want 88", next.BasePositionSec)
	}
	if next.Status != StatusPlaying {
		t.Fatalf("next.Status = %s, want %s", next.Status, StatusPlaying)
	}
	if next.StateVersion != 3 {
		t.Fatalf("next.StateVersion = %d, want 3", next.StateVersion)
	}
	if next.ServerTimestampMs != 4_000 {
		t.Fatalf("next.ServerTimestampMs = %d, want 4000", next.ServerTimestampMs)
	}
	if next.ControllerUserID != "user-2" {
		t.Fatalf("next.ControllerUserID = %s, want user-2", next.ControllerUserID)
	}
}

func TestReducePlaybackCommandPauseMaterializesPosition(t *testing.T) {
	t.Parallel()

	prev := RoomPlaybackState{
		RoomID:            "hub-1",
		VideoPath:         "movie.mp4",
		Status:            StatusPlaying,
		BasePositionSec:   5,
		PlaybackRate:      1.5,
		StateVersion:      7,
		ServerTimestampMs: 2_000,
	}

	next, changed, err := reducePlaybackCommand(prev, ControlInput{
		Action: ActionPause,
	}, 6_000, "user-9")
	if err != nil {
		t.Fatalf("reducePlaybackCommand() error = %v", err)
	}
	if !changed {
		t.Fatal("reducePlaybackCommand() changed = false, want true")
	}
	if next.Status != StatusPaused {
		t.Fatalf("next.Status = %s, want %s", next.Status, StatusPaused)
	}
	if next.BasePositionSec != 11 {
		t.Fatalf("next.BasePositionSec = %.3f, want 11", next.BasePositionSec)
	}
	if next.StateVersion != 8 {
		t.Fatalf("next.StateVersion = %d, want 8", next.StateVersion)
	}
}

func TestReducePlaybackCommandChangeRateMaterializesBase(t *testing.T) {
	t.Parallel()

	prev := RoomPlaybackState{
		RoomID:            "hub-1",
		VideoPath:         "movie.mp4",
		Status:            StatusPlaying,
		BasePositionSec:   10,
		PlaybackRate:      1,
		StateVersion:      3,
		ServerTimestampMs: 1_000,
	}

	next, changed, err := reducePlaybackCommand(prev, ControlInput{
		Action:       ActionChangeRate,
		PlaybackRate: 1.5,
	}, 3_000, "user-5")
	if err != nil {
		t.Fatalf("reducePlaybackCommand() error = %v", err)
	}
	if !changed {
		t.Fatal("reducePlaybackCommand() changed = false, want true")
	}
	if next.BasePositionSec != 12 {
		t.Fatalf("next.BasePositionSec = %.3f, want 12", next.BasePositionSec)
	}
	if next.PlaybackRate != 1.5 {
		t.Fatalf("next.PlaybackRate = %.3f, want 1.5", next.PlaybackRate)
	}
}

func TestControlRejectsStaleExpectedVersion(t *testing.T) {
	t.Parallel()

	service := NewService()
	hub, err := service.CreateHub("owner", "Owner", "movie.mp4", 0, false)
	if err != nil {
		t.Fatalf("CreateHub() error = %v", err)
	}

	expected := hub.StateVersion - 1
	_, err = service.Control(hub.ID, "user-1", "User 1", ControlInput{
		Action:          ActionPlay,
		ExpectedVersion: &expected,
	})
	if err == nil {
		t.Fatal("Control() error = nil, want version conflict")
	}

	conflict, ok := err.(*VersionConflictError)
	if !ok {
		t.Fatalf("Control() error = %T, want *VersionConflictError", err)
	}
	if conflict.ActualVersion != hub.StateVersion {
		t.Fatalf("conflict.ActualVersion = %d, want %d", conflict.ActualVersion, hub.StateVersion)
	}
}

func TestSubscribeDeliversInitialSnapshotAndControlUpdates(t *testing.T) {
	t.Parallel()

	service := NewService()
	hub, err := service.CreateHub("owner", "Owner", "movie.mp4", 12, false)
	if err != nil {
		t.Fatalf("CreateHub() error = %v", err)
	}

	events, done, err := service.Subscribe(hub.ID, "user-1", "User 1")
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer done()

	select {
	case event := <-events:
		if event.Type != "room_state" {
			t.Fatalf("initial event.Type = %s, want room_state", event.Type)
		}
		if event.Hub.StateVersion != 1 {
			t.Fatalf("initial event.Hub.StateVersion = %d, want 1", event.Hub.StateVersion)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial room snapshot")
	}

	expectedVersion := int64(1)
	if _, err := service.Control(hub.ID, "user-1", "User 1", ControlInput{
		Action:          ActionSeek,
		CurrentTime:     30,
		ExpectedVersion: &expectedVersion,
	}); err != nil {
		t.Fatalf("Control() error = %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Type != "room_state" || event.Hub.StateVersion != 2 {
				continue
			}
			if event.Hub.BasePositionSec != 30 {
				t.Fatalf("event.Hub.BasePositionSec = %.3f, want 30", event.Hub.BasePositionSec)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for updated room snapshot")
		}
	}
}
