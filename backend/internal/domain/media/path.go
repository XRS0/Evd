package media

import (
	"errors"
	"path"
	"strings"
)

var allowedVideoExts = map[string]bool{
	".mp4": true,
	".mkv": true,
	".avi": true,
	".mov": true,
}

// IsSupportedVideoExt reports whether extension is supported by the media domain.
func IsSupportedVideoExt(ext string) bool {
	return allowedVideoExts[strings.ToLower(strings.TrimSpace(ext))]
}

// NormalizeVideoPath validates and normalizes incoming media path.
func NormalizeVideoPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("invalid file name")
	}

	value = strings.ReplaceAll(value, "\\", "/")
	cleaned := path.Clean("/" + value)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return "", errors.New("invalid file name")
	}

	if !IsSupportedVideoExt(path.Ext(cleaned)) {
		return "", errors.New("unsupported file type")
	}

	return cleaned, nil
}
