package media

import (
	"context"
	"io"
	"time"

	mediadomain "evd/internal/domain/media"
)

// VideoRepository is an application port for media file discovery and path resolution.
type VideoRepository interface {
	ListVideos() ([]mediadomain.Video, error)
	ResolveVideoPath(raw string) (string, string, error)
	DeleteVideo(relPath string) error
	HLSPaths(relPath string) (string, string, string)
	MP4Paths(relPath string) (string, string, string)
}

// Converter is an application port for media transcoding and streaming operations.
type Converter interface {
	HLSMarkerVersion() string
	MP4MarkerVersion() string
	ConvertHLS(ctx context.Context, inputPath, outputDir, playlistPath string) error
	ConvertHLSFollow(ctx context.Context, inputPath, outputDir, playlistPath string, idleTimeout time.Duration) error
	ConvertMP4WithProgress(ctx context.Context, inputPath, outputPath string, onProgress func(int)) error
	StreamMP4(ctx context.Context, inputPath string, out io.Writer, follow bool, idleTimeout time.Duration) error
}
