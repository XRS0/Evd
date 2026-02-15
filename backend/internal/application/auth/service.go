package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	passwordSaltBytes = 16
	passwordRounds    = 100000
	userIDBytes       = 12
	sessionIDBytes    = 32
)

var (
	ErrUnauthorized       = errors.New("unauthorized")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUserExists         = errors.New("username already exists")
	ErrInvalidInput       = errors.New("invalid username or password format")

	usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,32}$`)
)

// User is a public account model returned to the client.
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	CreatedAt int64  `json:"createdAt"`
}

type storedUser struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	UsernameKey  string `json:"usernameKey"`
	PasswordHash string `json:"passwordHash"`
	CreatedAt    int64  `json:"createdAt"`
}

type session struct {
	User      User
	ExpiresAt time.Time
}

// Service manages user accounts and active sessions.
type Service struct {
	mu sync.RWMutex

	usersByKey map[string]storedUser
	usersByID  map[string]storedUser
	sessions   map[string]session

	usersFile  string
	sessionTTL time.Duration
}

// NewService creates an auth service and loads persisted users from disk.
func NewService(usersFile string, sessionTTL time.Duration) (*Service, error) {
	if sessionTTL <= 0 {
		sessionTTL = 72 * time.Hour
	}

	svc := &Service{
		usersByKey: map[string]storedUser{},
		usersByID:  map[string]storedUser{},
		sessions:   map[string]session{},
		usersFile:  strings.TrimSpace(usersFile),
		sessionTTL: sessionTTL,
	}

	if err := svc.loadUsers(); err != nil {
		return nil, err
	}

	return svc, nil
}

// SessionTTL returns the configured session lifetime.
func (s *Service) SessionTTL() time.Duration {
	return s.sessionTTL
}

// Register creates a new user account and immediately returns a fresh session.
func (s *Service) Register(username, password string) (User, string, error) {
	normalizedUsername, usernameKey, err := validateCredentials(username, password)
	if err != nil {
		return User{}, "", err
	}

	passwordHash, err := hashPassword(password)
	if err != nil {
		return User{}, "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupExpiredSessionsLocked(time.Now())

	if _, exists := s.usersByKey[usernameKey]; exists {
		return User{}, "", ErrUserExists
	}

	now := time.Now().UnixMilli()
	userID, err := randomToken(userIDBytes)
	if err != nil {
		return User{}, "", err
	}

	user := storedUser{
		ID:           userID,
		Username:     normalizedUsername,
		UsernameKey:  usernameKey,
		PasswordHash: passwordHash,
		CreatedAt:    now,
	}

	s.usersByKey[usernameKey] = user
	s.usersByID[userID] = user

	if err := s.saveUsersLocked(); err != nil {
		delete(s.usersByKey, usernameKey)
		delete(s.usersByID, userID)
		return User{}, "", err
	}

	publicUser := user.toPublic()
	token, err := s.createSessionLocked(publicUser)
	if err != nil {
		return User{}, "", err
	}

	return publicUser, token, nil
}

// Login authenticates user credentials and returns a fresh session token.
func (s *Service) Login(username, password string) (User, string, error) {
	normalized := strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if normalized == "" || password == "" {
		return User{}, "", ErrInvalidCredentials
	}
	usernameKey := strings.ToLower(normalized)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupExpiredSessionsLocked(time.Now())

	user, exists := s.usersByKey[usernameKey]
	if !exists {
		return User{}, "", ErrInvalidCredentials
	}
	if !verifyPassword(password, user.PasswordHash) {
		return User{}, "", ErrInvalidCredentials
	}

	publicUser := user.toPublic()
	token, err := s.createSessionLocked(publicUser)
	if err != nil {
		return User{}, "", err
	}

	return publicUser, token, nil
}

// LoginGuest creates an anonymous guest session without user registration.
func (s *Service) LoginGuest() (User, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupExpiredSessionsLocked(time.Now())

	guestID, err := randomToken(userIDBytes)
	if err != nil {
		return User{}, "", err
	}

	guestUser := User{
		ID:        "guest_" + guestID,
		Username:  "guest",
		CreatedAt: time.Now().UnixMilli(),
	}

	token, err := s.createSessionLocked(guestUser)
	if err != nil {
		return User{}, "", err
	}

	return guestUser, token, nil
}

// Authenticate resolves a session token into a user.
func (s *Service) Authenticate(token string) (User, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return User{}, ErrUnauthorized
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.cleanupExpiredSessionsLocked(now)

	record, exists := s.sessions[token]
	if !exists || now.After(record.ExpiresAt) {
		delete(s.sessions, token)
		return User{}, ErrUnauthorized
	}

	if record.User.ID == "" {
		delete(s.sessions, token)
		return User{}, ErrUnauthorized
	}

	return record.User, nil
}

// Logout removes an active session token.
func (s *Service) Logout(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

func (s *Service) createSessionLocked(user User) (string, error) {
	token, err := randomToken(sessionIDBytes)
	if err != nil {
		return "", err
	}

	s.sessions[token] = session{
		User:      user,
		ExpiresAt: time.Now().Add(s.sessionTTL),
	}

	return token, nil
}

func (s *Service) cleanupExpiredSessionsLocked(now time.Time) {
	for token, entry := range s.sessions {
		if now.After(entry.ExpiresAt) {
			delete(s.sessions, token)
		}
	}
}

func (s *Service) loadUsers() error {
	if s.usersFile == "" {
		return nil
	}

	raw, err := os.ReadFile(s.usersFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var list []storedUser
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("decode users file: %w", err)
	}

	for _, item := range list {
		if item.ID == "" || item.PasswordHash == "" {
			continue
		}
		if item.UsernameKey == "" {
			item.UsernameKey = strings.ToLower(strings.TrimSpace(item.Username))
		}
		if item.UsernameKey == "" {
			continue
		}
		s.usersByKey[item.UsernameKey] = item
		s.usersByID[item.ID] = item
	}

	return nil
}

func (s *Service) saveUsersLocked() error {
	if s.usersFile == "" {
		return nil
	}

	out := make([]storedUser, 0, len(s.usersByKey))
	for _, item := range s.usersByKey {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UsernameKey < out[j].UsernameKey
	})

	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.usersFile), 0o755); err != nil {
		return err
	}

	tmpPath := s.usersFile + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
		return err
	}

	return os.Rename(tmpPath, s.usersFile)
}

func validateCredentials(username, password string) (string, string, error) {
	cleanUsername := strings.TrimSpace(username)
	cleanPassword := strings.TrimSpace(password)

	if !usernamePattern.MatchString(cleanUsername) {
		return "", "", ErrInvalidInput
	}
	if len(cleanPassword) < 6 || len(cleanPassword) > 128 {
		return "", "", ErrInvalidInput
	}

	usernameKey := strings.ToLower(cleanUsername)
	return cleanUsername, usernameKey, nil
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	sum := sha256.Sum256(append(salt, []byte(password)...))
	current := sum[:]
	for i := 0; i < passwordRounds; i++ {
		next := sha256.Sum256(append(current, salt...))
		current = next[:]
	}

	return hex.EncodeToString(salt) + ":" + hex.EncodeToString(current), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, ":")
	if len(parts) != 2 {
		return false
	}

	salt, err := hex.DecodeString(parts[0])
	if err != nil || len(salt) == 0 {
		return false
	}
	expected, err := hex.DecodeString(parts[1])
	if err != nil || len(expected) == 0 {
		return false
	}

	sum := sha256.Sum256(append(salt, []byte(password)...))
	current := sum[:]
	for i := 0; i < passwordRounds; i++ {
		next := sha256.Sum256(append(current, salt...))
		current = next[:]
	}

	return subtle.ConstantTimeCompare(current, expected) == 1
}

func (u storedUser) toPublic() User {
	return User{
		ID:        u.ID,
		Username:  u.Username,
		CreatedAt: u.CreatedAt,
	}
}
