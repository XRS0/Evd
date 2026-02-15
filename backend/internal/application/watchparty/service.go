package watchparty

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ActionPlay  = "play"
	ActionPause = "pause"
	ActionSeek  = "seek"
	ActionVideo = "video"
	ActionChat  = "chat"
)

var (
	ErrHubNotFound  = errors.New("watch hub not found")
	ErrInvalidHubID = errors.New("invalid hub id")
	ErrInvalidInput = errors.New("invalid control payload")
)

const maxChatMessages = 200

// ControlInput is a player update pushed by a participant.
type ControlInput struct {
	Action      string
	VideoPath   string
	CurrentTime float64
	Playing     *bool
}

// Member represents a current hub participant.
type Member struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// Snapshot contains the current shared playback state.
type Snapshot struct {
	ID          string        `json:"id"`
	OwnerID     string        `json:"ownerId"`
	OwnerName   string        `json:"ownerName"`
	VideoPath   string        `json:"videoPath"`
	CurrentTime float64       `json:"currentTime"`
	Playing     bool          `json:"playing"`
	UpdatedAt   int64         `json:"updatedAt"`
	Members     []Member      `json:"members"`
	Messages    []ChatMessage `json:"messages"`
}

// ChatMessage stores a text entry inside a watch hub.
type ChatMessage struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	Username  string `json:"username"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"createdAt"`
}

// Event is emitted to subscribers via SSE.
type Event struct {
	Type      string       `json:"type"`
	Action    string       `json:"action,omitempty"`
	ActorID   string       `json:"actorId,omitempty"`
	ActorName string       `json:"actorName,omitempty"`
	Chat      *ChatMessage `json:"chat,omitempty"`
	Hub       Snapshot     `json:"hub"`
}

type hub struct {
	ID        string
	OwnerID   string
	OwnerName string

	VideoPath   string
	CurrentTime float64
	Playing     bool
	UpdatedAt   time.Time

	memberRefs map[string]int
	memberInfo map[string]string
	messages   []ChatMessage

	subscribers map[string]chan Event
}

// Service stores hubs in memory and fan-outs control events.
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

// CreateHub creates a new watch hub.
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
	h := &hub{
		ID:          hubID,
		OwnerID:     ownerID,
		OwnerName:   ownerName,
		VideoPath:   videoPath,
		CurrentTime: normalizeTime(currentTime),
		Playing:     playing,
		UpdatedAt:   now,
		memberRefs:  map[string]int{},
		memberInfo:  map[string]string{},
		messages:    []ChatMessage{},
		subscribers: map[string]chan Event{},
	}

	s.mu.Lock()
	s.hubs[hubID] = h
	s.mu.Unlock()

	return snapshotFromHub(h), nil
}

// GetHub returns current state for a hub.
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
	return snapshotFromHub(h), nil
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

	h.subscribers[subID] = ch
	h.memberRefs[userID]++
	h.memberInfo[userID] = username
	h.UpdatedAt = time.Now()

	snapshot := snapshotFromHub(h)
	ch <- Event{
		Type:   "sync",
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
			current.UpdatedAt = time.Now()

			leaveEvent := Event{
				Type:      "presence",
				Action:    "leave",
				ActorID:   userID,
				ActorName: username,
				Hub:       snapshotFromHub(current),
			}
			s.broadcastLocked(current, leaveEvent)
		})
	}

	return ch, cleanup, nil
}

// Control applies a playback action and broadcasts it to all subscribers.
func (s *Service) Control(hubID, userID, username string, input ControlInput) (Event, error) {
	hubID = strings.TrimSpace(hubID)
	userID = strings.TrimSpace(userID)
	username = strings.TrimSpace(username)
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if hubID == "" || userID == "" || username == "" {
		return Event{}, ErrInvalidInput
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.hubs[hubID]
	if !ok {
		return Event{}, ErrHubNotFound
	}

	switch action {
	case ActionPlay:
		h.Playing = true
		if isFiniteTime(input.CurrentTime) {
			h.CurrentTime = normalizeTime(input.CurrentTime)
		}
	case ActionPause:
		h.Playing = false
		if isFiniteTime(input.CurrentTime) {
			h.CurrentTime = normalizeTime(input.CurrentTime)
		}
	case ActionSeek:
		if !isFiniteTime(input.CurrentTime) {
			return Event{}, ErrInvalidInput
		}
		h.CurrentTime = normalizeTime(input.CurrentTime)
	case ActionVideo:
		videoPath := strings.TrimSpace(input.VideoPath)
		if videoPath == "" {
			return Event{}, ErrInvalidInput
		}
		h.VideoPath = videoPath
		if isFiniteTime(input.CurrentTime) {
			h.CurrentTime = normalizeTime(input.CurrentTime)
		} else {
			h.CurrentTime = 0
		}
		if input.Playing != nil {
			h.Playing = *input.Playing
		} else {
			h.Playing = false
		}
	default:
		return Event{}, ErrInvalidInput
	}

	h.UpdatedAt = time.Now()
	event := Event{
		Type:      "control",
		Action:    action,
		ActorID:   userID,
		ActorName: username,
		Hub:       snapshotFromHub(h),
	}
	s.broadcastLocked(h, event)

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
	message := ChatMessage{
		ID:        messageID,
		UserID:    userID,
		Username:  username,
		Text:      text,
		CreatedAt: now.UnixMilli(),
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
		Hub:       snapshotFromHub(h),
	}
	s.broadcastLocked(h, event)

	return event, nil
}

func (s *Service) broadcastLocked(h *hub, event Event) {
	for _, subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
			// Drop stale events for slow clients.
		}
	}
}

func snapshotFromHub(h *hub) Snapshot {
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

	return Snapshot{
		ID:          h.ID,
		OwnerID:     h.OwnerID,
		OwnerName:   h.OwnerName,
		VideoPath:   h.VideoPath,
		CurrentTime: h.CurrentTime,
		Playing:     h.Playing,
		UpdatedAt:   h.UpdatedAt.UnixMilli(),
		Members:     members,
		Messages:    messages,
	}
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
