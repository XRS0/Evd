package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds runtime settings for the server.
type Config struct {
	ServerAddr              string
	VideosDir               string
	HLSDir                  string
	MP4Dir                  string
	UsersFile               string
	SessionTTLHours         int
	TransmissionURL         string
	TransmissionUser        string
	TransmissionPass        string
	TransmissionDownloadDir string
	HlsSegmentSeconds       int
}

// Load reads environment variables and returns normalized runtime config.
func Load() Config {
	return Config{
		ServerAddr:              getEnv("SERVER_ADDR", ":8080"),
		VideosDir:               getEnv("VIDEOS_DIR", "./videos"),
		HLSDir:                  getEnv("HLS_DIR", "./hls"),
		MP4Dir:                  getEnv("MP4_DIR", "./mp4"),
		UsersFile:               getEnv("USERS_FILE", "./data/users.json"),
		SessionTTLHours:         getEnvInt("SESSION_TTL_HOURS", 72),
		TransmissionURL:         strings.TrimSpace(os.Getenv("TRANSMISSION_URL")),
		TransmissionUser:        os.Getenv("TRANSMISSION_USER"),
		TransmissionPass:        os.Getenv("TRANSMISSION_PASS"),
		TransmissionDownloadDir: getEnv("TRANSMISSION_DOWNLOAD_DIR", "/downloads"),
		HlsSegmentSeconds:       getEnvInt("HLS_SEGMENT_SECONDS", 20),
	}
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	var out int
	_, err := fmt.Sscanf(value, "%d", &out)
	if err != nil || out <= 0 {
		return fallback
	}
	return out
}
