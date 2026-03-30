package watchparty

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ActionPlay       = "play"
	ActionPause      = "pause"
	ActionSeek       = "seek"
	ActionVideo      = "video"
	ActionChangeRate = "change_rate"
	ActionChat       = "chat"
)

const (
	StatusPlaying   = "playing"
	StatusPaused    = "paused"
	StatusBuffering = "buffering"
	StatusEnded     = "ended"
)

const maxChatMessages = 200

var (
	ErrHubNotFound  = errors.New("watch hub not found")
	ErrInvalidHubID = errors.New("invalid hub id")
	ErrInvalidInput = errors.New("invalid control payload")
)

var watchSyncDebugEnabled = strings.EqualFold(strings.TrimSpace(os.Getenv("EVD_WATCH_SYNC_DEBUG")), "1") ||
	strings.EqualFold(strings.TrimSpace(os.Getenv("EVD_WATCH_SYNC_DEBUG")), "true")

// VersionConflictError is returned when a client command references a stale snapshot.
type VersionConflictError struct {
	ActualVersion int64
}

func (e *VersionConflictError) Error() string {
	return "expected playback state version does not match current room version"
}

// ControlInput describes a client playback command.
type ControlInput struct {
	Action          string
	VideoPath       string
	CurrentTime     float64
	Playing         *bool
	PlaybackRate    float64
	ExpectedVersion *int64
}

// Member represents a current hub participant.
type Member struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// RoomPlaybackState is the canonical authoritative playback snapshot for a hub.
type RoomPlaybackState struct {
	RoomID            string  `json:"roomId"`
	VideoPath         string  `json:"videoPath"`
	Status            string  `json:"status"`
	BasePositionSec   float64 `json:"basePositionSec"`
	PlaybackRate      float64 `json:"playbackRate"`
	StateVersion      int64   `json:"stateVersion"`
	ServerTimestampMs int64   `json:"serverTimestampMs"`
	ControllerUserID  string  `json:"controllerUserId,omitempty"`
}

// Snapshot contains the current shared watch hub state.
type Snapshot struct {
	ID                string            `json:"id"`
	OwnerID           string            `json:"ownerId"`
	OwnerName         string            `json:"ownerName"`
	VideoPath         string            `json:"videoPath"`
	CurrentTime       float64           `json:"currentTime"`
	Playing           bool              `json:"playing"`
	UpdatedAt         int64             `json:"updatedAt"`
	Status            string            `json:"status"`
	BasePositionSec   float64           `json:"basePositionSec"`
	PlaybackRate      float64           `json:"playbackRate"`
	StateVersion      int64             `json:"stateVersion"`
	ServerTimestampMs int64             `json:"serverTimestampMs"`
	ControllerUserID  string            `json:"controllerUserId,omitempty"`
	Playback          RoomPlaybackState `json:"playback"`
	Members           []Member          `json:"members"`
	Messages          []ChatMessage     `json:"messages"`
}

// ChatMessage stores a text entry inside a watch hub.
type ChatMessage struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	Username  string `json:"username"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"createdAt"`
}

// Pong contains the server clock sample returned to a client.
type Pong struct {
	ServerTimestampMs       int64 `json:"serverTimestampMs"`
	EchoedClientTimestampMs int64 `json:"echoedClientTimestampMs,omitempty"`
}

// Event is emitted to subscribers via SSE.
type Event struct {
	Type          string       `json:"type"`
	Action        string       `json:"action,omitempty"`
	ActorID       string       `json:"actorId,omitempty"`
	ActorName     string       `json:"actorName,omitempty"`
	Reason        string       `json:"reason,omitempty"`
	ActualVersion *int64       `json:"actualVersion,omitempty"`
	Chat          *ChatMessage `json:"chat,omitempty"`
	Hub           Snapshot     `json:"hub"`
}

type hub struct {
	ID        string
	OwnerID   string
	OwnerName string

	Playback  RoomPlaybackState
	UpdatedAt time.Time

	memberRefs map[string]int
	memberInfo map[string]string
	messages   []ChatMessage

	subscribers map[string]chan Event
}

// Service stores watch hubs in memory and fan-outs room snapshots.
type Service struct {
	mu   sync.Mutex
	hubs map[string]*hub
}

// NewService creates a watch party service.
func NewService() *Service {
	return &Service{
		hubs: map[string]*hub{},
	}
}

// CreateHub creates a new watch hub with an initial canonical playback snapshot.
func (s *Service) CreateHub(ownerID, ownerName, videoPath string, currentTime float64, playing bool) (Snapshot, error) {
	ownerID = strings.TrimSpace(ownerID)
	ownerName = strings.TrimSpace(ownerName)
	videoPath = strings.TrimSpace(videoPath)
	if ownerID == "" || ownerName == "" || videoPath == "" {
		return Snapshot{}, ErrInvalidInput
	}

	hubID, err := randomID(10)
	if err != nil {
		return Snapshot{}, err
	}

	now := time.Now()
	nowMs := now.UnixMilli()
	status := StatusPaused
	if playing {
		status = StatusPlaying
	}

	h := &hub{
		ID:        hubID,
		OwnerID:   ownerID,
		OwnerName: ownerName,
		Playback: RoomPlaybackState{
			RoomID:            hubID,
			VideoPath:         videoPath,
			Status:            status,
			BasePositionSec:   normalizeTime(currentTime),
			PlaybackRate:      1,
			StateVersion:      1,
			ServerTimestampMs: nowMs,
			ControllerUserID:  ownerID,
		},
		UpdatedAt:   now,
		memberRefs:  map[string]int{},
		memberInfo:  map[string]string{},
		messages:    []ChatMessage{},
		subscribers: map[string]chan Event{},
	}

	s.mu.Lock()
	s.hubs[hubID] = h
	s.mu.Unlock()

	debugLogf("created hub=%s video=%s status=%s version=%d", hubID, videoPath, h.Playback.Status, h.Playback.StateVersion)
	return snapshotFromHub(h, nowMs), nil
}

// GetHub returns the current state snapshot for a hub.
func (s *Service) GetHub(hubID string) (Snapshot, error) {
	hubID = strings.TrimSpace(hubID)
	if hubID == "" {
		return Snapshot{}, ErrInvalidHubID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.hubs[hubID]
	if !ok {
		return Snapshot{}, ErrHubNotFound
	}

	nowMs := time.Now().UnixMilli()
	return snapshotFromHub(h, nowMs), nil
}

// Subscribe joins a hub and returns an event channel + cleanup callback.
func (s *Service) Subscribe(hubID, userID, username string) (<-chan Event, func(), error) {
	hubID = strings.TrimSpace(hubID)
	userID = strings.TrimSpace(userID)
	username = strings.TrimSpace(username)
	if hubID == "" || userID == "" || username == "" {
		return nil, nil, ErrInvalidInput
	}

	subID, err := randomID(12)
	if err != nil {
		return nil, nil, err
	}

	ch := make(chan Event, 32)
	var once sync.Once

	s.mu.Lock()
	h, ok := s.hubs[hubID]
	if !ok {
		s.mu.Unlock()
		close(ch)
		return nil, nil, ErrHubNotFound
	}

	now := time.Now()
	nowMs := now.UnixMilli()
	h.subscribers[subID] = ch
	h.memberRefs[userID]++
	h.memberInfo[userID] = username
	h.UpdatedAt = now

	snapshot := snapshotFromHub(h, nowMs)
	ch <- Event{
		Type:   "room_state",
		Action: "sync",
		Hub:    snapshot,
	}

	joinEvent := Event{
		Type:      "presence",
		Action:    "join",
		ActorID:   userID,
		ActorName: username,
		Hub:       snapshot,
	}
	s.broadcastLocked(h, joinEvent)
	s.mu.Unlock()

	debugLogf("subscribe hub=%s user=%s members=%d version=%d", hubID, userID, len(snapshot.Members), snapshot.StateVersion)

	cleanup := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()

			current, exists := s.hubs[hubID]
			if !exists {
				close(ch)
				return
			}

			delete(current.subscribers, subID)
			close(ch)

			if refs := current.memberRefs[userID]; refs > 1 {
				current.memberRefs[userID] = refs - 1
			} else {
				delete(current.memberRefs, userID)
				delete(current.memberInfo, userID)
			}

			now := time.Now()
			current.UpdatedAt = now
			leaveEvent := Event{
				Type:      "presence",
				Action:    "leave",
				ActorID:   userID,
				ActorName: username,
				Hub:       snapshotFromHub(current, now.UnixMilli()),
			}
			s.broadcastLocked(current, leaveEvent)
			debugLogf("unsubscribe hub=%s user=%s members=%d", hubID, userID, len(leaveEvent.Hub.Members))
		})
	}

	return ch, cleanup, nil
}

// Control validates and applies a playback command, then broadcasts a new canonical snapshot.
func (s *Service) Control(hubID, userID, username string, input ControlInput) (Event, error) {
	hubID = strings.TrimSpace(hubID)
	userID = strings.TrimSpace(userID)
	username = strings.TrimSpace(username)
	if hubID == "" || userID == "" || username == "" {
		return Event{}, ErrInvalidInput
	}

	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action == "" {
		return Event{}, ErrInvalidInput
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.hubs[hubID]
	if !ok {
		return Event{}, ErrHubNotFound
	}

	if input.ExpectedVersion != nil && *input.ExpectedVersion != h.Playback.StateVersion {
		debugLogf("reject hub=%s action=%s user=%s expected=%d actual=%d", hubID, action, userID, *input.ExpectedVersion, h.Playback.StateVersion)
		return Event{}, &VersionConflictError{ActualVersion: h.Playback.StateVersion}
	}

	now := time.Now()
	nowMs := now.UnixMilli()
	debugLogf(
		"command hub=%s action=%s user=%s version=%d status=%s base=%.3f rate=%.3f current=%.3f",
		hubID,
		action,
		userID,
		h.Playback.StateVersion,
		h.Playback.Status,
		h.Playback.BasePositionSec,
		h.Playback.PlaybackRate,
		input.CurrentTime,
	)

	nextPlayback, changed, err := reducePlaybackCommand(h.Playback, input, nowMs, userID)
	if err != nil {
		return Event{}, err
	}
	if !changed {
		snapshot := snapshotFromHub(h, nowMs)
		return Event{
			Type:      "room_state",
			Action:    action,
			ActorID:   userID,
			ActorName: username,
			Hub:       snapshot,
		}, nil
	}

	h.Playback = nextPlayback
	h.UpdatedAt = now

	event := Event{
		Type:      "room_state",
		Action:    action,
		ActorID:   userID,
		ActorName: username,
		Hub:       snapshotFromHub(h, nowMs),
	}
	s.broadcastLocked(h, event)

	debugLogf(
		"snapshot hub=%s version=%d status=%s base=%.3f rate=%.3f ts=%d",
		hubID,
		event.Hub.StateVersion,
		event.Hub.Status,
		event.Hub.BasePositionSec,
		event.Hub.PlaybackRate,
		event.Hub.ServerTimestampMs,
	)

	return event, nil
}

// Chat appends a chat message and broadcasts it.
func (s *Service) Chat(hubID, userID, username, text string) (Event, error) {
	hubID = strings.TrimSpace(hubID)
	userID = strings.TrimSpace(userID)
	username = strings.TrimSpace(username)
	text = strings.TrimSpace(text)
	if hubID == "" || userID == "" || username == "" || text == "" {
		return Event{}, ErrInvalidInput
	}
	if len(text) > 600 {
		return Event{}, ErrInvalidInput
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.hubs[hubID]
	if !ok {
		return Event{}, ErrHubNotFound
	}

	messageID, err := randomID(14)
	if err != nil {
		return Event{}, err
	}

	now := time.Now()
	nowMs := now.UnixMilli()
	message := ChatMessage{
		ID:        messageID,
		UserID:    userID,
		Username:  username,
		Text:      text,
		CreatedAt: nowMs,
	}

	h.messages = append(h.messages, message)
	if len(h.messages) > maxChatMessages {
		h.messages = append([]ChatMessage(nil), h.messages[len(h.messages)-maxChatMessages:]...)
	}
	h.UpdatedAt = now

	event := Event{
		Type:      "chat",
		Action:    ActionChat,
		ActorID:   userID,
		ActorName: username,
		Chat:      &message,
		Hub:       snapshotFromHub(h, nowMs),
	}
	s.broadcastLocked(h, event)

	return event, nil
}

// Ping returns a lightweight server clock sample for client-side offset estimation.
func (s *Service) Ping(clientTimestampMs int64) Pong {
	nowMs := time.Now().UnixMilli()
	return Pong{
		ServerTimestampMs:       nowMs,
		EchoedClientTimestampMs: clientTimestampMs,
	}
}

func (s *Service) broadcastLocked(h *hub, event Event) {
	for _, subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
			// Drop stale events for slow clients. A reconnect will restore the latest snapshot.
		}
	}
}

func snapshotFromHub(h *hub, nowMs int64) Snapshot {
	memberIDs := make([]string, 0, len(h.memberRefs))
	for memberID := range h.memberRefs {
		memberIDs = append(memberIDs, memberID)
	}
	sort.Strings(memberIDs)

	members := make([]Member, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		members = append(members, Member{
			ID:       memberID,
			Username: h.memberInfo[memberID],
		})
	}

	messages := make([]ChatMessage, len(h.messages))
	copy(messages, h.messages)

	playback := h.Playback
	currentTime := materializePosition(playback, nowMs)

	return Snapshot{
		ID:                h.ID,
		OwnerID:           h.OwnerID,
		OwnerName:         h.OwnerName,
		VideoPath:         playback.VideoPath,
		CurrentTime:       currentTime,
		Playing:           playback.Status == StatusPlaying,
		UpdatedAt:         maxInt64(h.UpdatedAt.UnixMilli(), playback.ServerTimestampMs),
		Status:            playback.Status,
		BasePositionSec:   playback.BasePositionSec,
		PlaybackRate:      playback.PlaybackRate,
		StateVersion:      playback.StateVersion,
		ServerTimestampMs: playback.ServerTimestampMs,
		ControllerUserID:  playback.ControllerUserID,
		Playback:          playback,
		Members:           members,
		Messages:          messages,
	}
}

func materializePosition(state RoomPlaybackState, nowMs int64) float64 {
	base := normalizeTime(state.BasePositionSec)
	if state.Status != StatusPlaying {
		return base
	}

	rate := normalizePlaybackRate(state.PlaybackRate)
	elapsedMs := nowMs - state.ServerTimestampMs
	if elapsedMs <= 0 {
		return base
	}

	return normalizeTime(base + (float64(elapsedMs)/1000)*rate)
}

func reducePlaybackCommand(prev RoomPlaybackState, command ControlInput, nowMs int64, controllerUserID string) (RoomPlaybackState, bool, error) {
	action := strings.ToLower(strings.TrimSpace(command.Action))
	if action == "" {
		return RoomPlaybackState{}, false, ErrInvalidInput
	}

	next := prev
	materializedPosition := materializePosition(prev, nowMs)
	currentRate := normalizePlaybackRate(prev.PlaybackRate)
	changed := false

	switch action {
	case ActionPlay:
		if prev.Status != StatusPlaying {
			next.Status = StatusPlaying
			next.BasePositionSec = materializedPosition
			changed = true
		}
	case ActionPause:
		if prev.Status != StatusPaused || math.Abs(prev.BasePositionSec-materializedPosition) > 0.001 {
			next.Status = StatusPaused
			next.BasePositionSec = materializedPosition
			changed = true
		}
	case ActionSeek:
		if !isFiniteTime(command.CurrentTime) {
			return RoomPlaybackState{}, false, ErrInvalidInput
		}
		target := normalizeTime(command.CurrentTime)
		if math.Abs(target-materializedPosition) > 0.001 {
			next.BasePositionSec = target
			changed = true
		}
	case ActionChangeRate:
		if !isFiniteTime(command.PlaybackRate) || command.PlaybackRate <= 0 {
			return RoomPlaybackState{}, false, ErrInvalidInput
		}
		nextRate := normalizePlaybackRate(command.PlaybackRate)
		if math.Abs(nextRate-currentRate) > 0.0001 {
			next.BasePositionSec = materializedPosition
			next.PlaybackRate = nextRate
			changed = true
		}
	case ActionVideo:
		videoPath := strings.TrimSpace(command.VideoPath)
		if videoPath == "" {
			return RoomPlaybackState{}, false, ErrInvalidInput
		}
		targetStatus := StatusPaused
		if command.Playing != nil && *command.Playing {
			targetStatus = StatusPlaying
		}
		targetBase := 0.0
		if isFiniteTime(command.CurrentTime) {
			targetBase = normalizeTime(command.CurrentTime)
		}
		next.RoomID = prev.RoomID
		next.VideoPath = videoPath
		next.Status = targetStatus
		next.BasePositionSec = targetBase
		next.PlaybackRate = 1
		changed = prev.VideoPath != videoPath ||
			prev.Status != targetStatus ||
			math.Abs(prev.BasePositionSec-targetBase) > 0.001 ||
			math.Abs(currentRate-1) > 0.0001
	default:
		return RoomPlaybackState{}, false, ErrInvalidInput
	}

	if !changed {
		return prev, false, nil
	}

	next.RoomID = prev.RoomID
	next.ControllerUserID = controllerUserID
	next.ServerTimestampMs = nowMs
	next.StateVersion = prev.StateVersion + 1
	if next.PlaybackRate == 0 {
		next.PlaybackRate = currentRate
	}
	return next, true, nil
}

func randomID(size int) (string, error) {
	randomBytes := make([]byte, size)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes)
	if len(token) > size {
		token = token[:size]
	}
	return strings.ToLower(token), nil
}

func isFiniteTime(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func normalizeTime(value float64) float64 {
	if !isFiniteTime(value) || value < 0 {
		return 0
	}
	return value
}

func normalizePlaybackRate(value float64) float64 {
	if !isFiniteTime(value) || value <= 0 {
		return 1
	}
	return value
}

func debugLogf(format string, args ...interface{}) {
	if !watchSyncDebugEnabled {
		return
	}
	log.Printf("[watch-sync] "+format, args...)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
