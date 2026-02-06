package filesystem

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"evd/internal/domain/media"
)

// Store manages media files and output paths.
type Store struct {
	VideosDir string
	HLSDir    string
	MP4Dir    string
}

// NewStore creates filesystem adapter with configured roots.
func NewStore(videosDir, hlsDir, mp4Dir string) *Store {
	return &Store{VideosDir: videosDir, HLSDir: hlsDir, MP4Dir: mp4Dir}
}

// EnsureDirs creates filesystem roots used by service.
func (s *Store) EnsureDirs() error {
	if err := os.MkdirAll(s.VideosDir, 0o755); err != nil {
		return err
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
	_ = filepath.WalkDir(s.VideosDir, func(filePath string, entry fs.DirEntry, err error) error {
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

		rel, err := filepath.Rel(s.VideosDir, filePath)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		videos = append(videos, media.Video{
			Name:       entry.Name(),
			Path:       rel,
			Size:       info.Size(),
			ModifiedAt: info.ModTime(),
		})
		return nil
	})

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
	full := filepath.Join(s.VideosDir, filepath.FromSlash(rel))
	if !isWithinDir(s.VideosDir, full) {
		return "", "", errors.New("invalid file path")
	}
	return rel, full, nil
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
	full := filepath.Join(s.VideosDir, filepath.FromSlash(relPath))
	if !isWithinDir(s.VideosDir, full) {
		return false
	}
	info, err := os.Stat(full)
	if err != nil {
		return false
	}
	return !info.IsDir()
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
