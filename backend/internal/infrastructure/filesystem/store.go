package filesystem

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"evd/internal/domain/media"
)

// Store manages media files and output paths.
type Store struct {
	VideosDir string
	HLSDir    string
	MP4Dir    string
	VideoDirs []string

	metaMu             sync.Mutex
	torrentTitles      map[string]string
	torrentTitlesReady bool
}

// NewStore creates filesystem adapter with configured roots.
func NewStore(videosDir, hlsDir, mp4Dir string, extraVideoDirs ...string) *Store {
	videoDirs := make([]string, 0, 1+len(extraVideoDirs))
	for _, dir := range append([]string{videosDir}, extraVideoDirs...) {
		cleanDir := strings.TrimSpace(dir)
		if cleanDir == "" {
			continue
		}
		duplicate := false
		for _, existing := range videoDirs {
			if samePath(existing, cleanDir) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			videoDirs = append(videoDirs, cleanDir)
		}
	}

	return &Store{
		VideosDir: videosDir,
		HLSDir:    hlsDir,
		MP4Dir:    mp4Dir,
		VideoDirs: videoDirs,
	}
}

// EnsureDirs creates filesystem roots used by service.
func (s *Store) EnsureDirs() error {
	for _, dir := range s.VideoDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(s.HLSDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(s.MP4Dir, 0o755); err != nil {
		return err
	}
	return nil
}

// VideosRoot returns the root directory that stores source media files.
func (s *Store) VideosRoot() string {
	return s.VideosDir
}

// ListVideos scans media library and returns normalized entries.
func (s *Store) ListVideos() ([]media.Video, error) {
	videos := make([]media.Video, 0)
	seen := make(map[string]struct{})
	for _, baseDir := range s.videoRoots() {
		_ = filepath.WalkDir(baseDir, func(filePath string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			if !media.IsSupportedVideoExt(filepath.Ext(entry.Name())) {
				return nil
			}

			info, err := entry.Info()
			if err != nil {
				return nil
			}

			rel, err := filepath.Rel(baseDir, filePath)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if _, exists := seen[rel]; exists {
				return nil
			}
			seen[rel] = struct{}{}

			videos = append(videos, media.Video{
				Name:       entry.Name(),
				Path:       rel,
				Size:       info.Size(),
				ModifiedAt: info.ModTime(),
			})
			return nil
		})
	}

	sort.Slice(videos, func(i, j int) bool {
		return videos[i].ModifiedAt.After(videos[j].ModifiedAt)
	})

	return videos, nil
}

// ResolveVideoPath validates a request path and returns relative/absolute forms.
func (s *Store) ResolveVideoPath(raw string) (string, string, error) {
	rel, err := media.NormalizeVideoPath(raw)
	if err != nil {
		return "", "", err
	}

	for _, baseDir := range s.videoRoots() {
		full := filepath.Join(baseDir, filepath.FromSlash(rel))
		if !isWithinDir(baseDir, full) {
			continue
		}
		if info, statErr := os.Stat(full); statErr == nil && !info.IsDir() {
			return rel, full, nil
		}
	}

	full := filepath.Join(s.VideosDir, filepath.FromSlash(rel))
	if !isWithinDir(s.VideosDir, full) {
		return "", "", errors.New("invalid file path")
	}
	return rel, full, os.ErrNotExist
}

// DeleteVideo removes a source media file and associated derived outputs when present.
func (s *Store) DeleteVideo(relPath string) error {
	var (
		full string
		info os.FileInfo
		err  error
	)
	for _, baseDir := range s.videoRoots() {
		candidate := filepath.Join(baseDir, filepath.FromSlash(relPath))
		if !isWithinDir(baseDir, candidate) {
			continue
		}
		info, err = os.Stat(candidate)
		if err == nil && !info.IsDir() {
			full = candidate
			break
		}
	}
	if full == "" {
		if err != nil {
			return err
		}
		return os.ErrNotExist
	}

	if err := os.Remove(full); err != nil {
		return err
	}

	hlsDir, _, _ := s.HLSPaths(relPath)
	_ = os.RemoveAll(hlsDir)

	_, mp4Path, _ := s.MP4Paths(relPath)
	_ = os.Remove(mp4Path)

	return nil
}

// HLSPaths builds output paths and URL for HLS artifacts.
func (s *Store) HLSPaths(relPath string) (string, string, string) {
	base := strings.TrimSuffix(relPath, path.Ext(relPath))
	outputDir := filepath.Join(s.HLSDir, filepath.FromSlash(base))
	outputPath := filepath.Join(outputDir, "index.m3u8")
	urlPath := "/hls/" + base + "/index.m3u8"
	return outputDir, outputPath, urlPath
}

// MP4Paths builds output paths and URL for MP4 artifacts.
func (s *Store) MP4Paths(relPath string) (string, string, string) {
	base := strings.TrimSuffix(relPath, path.Ext(relPath))
	outputPath := filepath.Join(s.MP4Dir, filepath.FromSlash(base)+".mp4")
	outputDir := filepath.Dir(outputPath)
	urlPath := "/api/stream-mp4/" + relPath
	return outputDir, outputPath, urlPath
}

// FileExists checks if a media file exists in source library.
func (s *Store) FileExists(relPath string) bool {
	for _, baseDir := range s.videoRoots() {
		full := filepath.Join(baseDir, filepath.FromSlash(relPath))
		if !isWithinDir(baseDir, full) {
			continue
		}
		info, err := os.Stat(full)
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// TorrentDisplayName returns a stored custom display title for the torrent hash.
func (s *Store) TorrentDisplayName(hash string) (string, error) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()

	if err := s.loadTorrentTitlesLocked(); err != nil {
		return "", err
	}
	return strings.TrimSpace(s.torrentTitles[strings.TrimSpace(hash)]), nil
}

// SaveTorrentDisplayName persists a custom display title for the torrent hash.
func (s *Store) SaveTorrentDisplayName(hash, title string) error {
	cleanHash := strings.TrimSpace(hash)
	cleanTitle := strings.TrimSpace(title)
	if cleanHash == "" || cleanTitle == "" {
		return nil
	}

	s.metaMu.Lock()
	defer s.metaMu.Unlock()

	if err := s.loadTorrentTitlesLocked(); err != nil {
		return err
	}

	s.torrentTitles[cleanHash] = cleanTitle
	return s.persistTorrentTitlesLocked()
}

func (s *Store) loadTorrentTitlesLocked() error {
	if s.torrentTitlesReady {
		return nil
	}

	s.torrentTitles = make(map[string]string)
	data, err := os.ReadFile(s.torrentTitlesPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.torrentTitlesReady = true
			return nil
		}
		return err
	}

	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.torrentTitles); err != nil {
			return err
		}
	}

	s.torrentTitlesReady = true
	return nil
}

func (s *Store) persistTorrentTitlesLocked() error {
	data, err := json.MarshalIndent(s.torrentTitles, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := s.torrentTitlesPath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.torrentTitlesPath())
}

func (s *Store) torrentTitlesPath() string {
	return filepath.Join(s.MP4Dir, ".evd-torrent-titles.json")
}

func (s *Store) videoRoots() []string {
	if len(s.VideoDirs) > 0 {
		return s.VideoDirs
	}
	return []string{s.VideosDir}
}

func samePath(left, right string) bool {
	leftClean := filepath.Clean(strings.TrimSpace(left))
	rightClean := filepath.Clean(strings.TrimSpace(right))
	if leftClean == rightClean {
		return true
	}

	leftAbs, leftErr := filepath.Abs(leftClean)
	rightAbs, rightErr := filepath.Abs(rightClean)
	return leftErr == nil && rightErr == nil && leftAbs == rightAbs
}

func isWithinDir(basePath, targetPath string) bool {
	baseAbs, err := filepath.Abs(basePath)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return false
	}
	sep := string(os.PathSeparator)
	if rel == ".." || strings.HasPrefix(rel, ".."+sep) {
		return false
	}
	return true
}

// FormatDate converts time into unix seconds used by HTTP DTOs.
func FormatDate(t time.Time) int64 {
	return t.Unix()
}

// SanitizeUploadName validates incoming upload file names.
func SanitizeUploadName(raw string) (string, error) {
	return media.NormalizeVideoPath(raw)
}
