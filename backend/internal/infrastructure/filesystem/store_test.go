package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreListsAndResolvesVideosAcrossConfiguredRoots(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	libraryDir := filepath.Join(rootDir, "library")
	downloadsDir := filepath.Join(rootDir, "downloads")
	hlsDir := filepath.Join(rootDir, "hls")
	mp4Dir := filepath.Join(rootDir, "mp4")

	store := NewStore(libraryDir, hlsDir, mp4Dir, downloadsDir)
	if err := store.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}

	downloadedRelPath := filepath.Join("show", "episode-01.mkv")
	downloadedFullPath := filepath.Join(downloadsDir, downloadedRelPath)
	if err := os.MkdirAll(filepath.Dir(downloadedFullPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(downloadedFullPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	videos, err := store.ListVideos()
	if err != nil {
		t.Fatalf("ListVideos() error = %v", err)
	}
	if len(videos) != 1 {
		t.Fatalf("len(ListVideos()) = %d, want 1", len(videos))
	}
	if videos[0].Path != "show/episode-01.mkv" {
		t.Fatalf("videos[0].Path = %q, want %q", videos[0].Path, "show/episode-01.mkv")
	}

	rel, full, err := store.ResolveVideoPath("show/episode-01.mkv")
	if err != nil {
		t.Fatalf("ResolveVideoPath() error = %v", err)
	}
	if rel != "show/episode-01.mkv" {
		t.Fatalf("ResolveVideoPath() rel = %q, want %q", rel, "show/episode-01.mkv")
	}
	if full != downloadedFullPath {
		t.Fatalf("ResolveVideoPath() full = %q, want %q", full, downloadedFullPath)
	}
	if !store.FileExists("show/episode-01.mkv") {
		t.Fatal("FileExists() = false, want true")
	}
}

func TestStorePrefersPrimaryLibraryForDuplicateRelativePaths(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	libraryDir := filepath.Join(rootDir, "library")
	downloadsDir := filepath.Join(rootDir, "downloads")

	store := NewStore(libraryDir, filepath.Join(rootDir, "hls"), filepath.Join(rootDir, "mp4"), downloadsDir)
	if err := store.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}

	relPath := filepath.Join("movie", "cut.mp4")
	primaryPath := filepath.Join(libraryDir, relPath)
	secondaryPath := filepath.Join(downloadsDir, relPath)
	for _, fullPath := range []string{primaryPath, secondaryPath} {
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(fullPath), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	_, full, err := store.ResolveVideoPath("movie/cut.mp4")
	if err != nil {
		t.Fatalf("ResolveVideoPath() error = %v", err)
	}
	if full != primaryPath {
		t.Fatalf("ResolveVideoPath() full = %q, want primary path %q", full, primaryPath)
	}

	videos, err := store.ListVideos()
	if err != nil {
		t.Fatalf("ListVideos() error = %v", err)
	}
	if len(videos) != 1 {
		t.Fatalf("len(ListVideos()) = %d, want 1", len(videos))
	}
}
