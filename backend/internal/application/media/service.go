package media

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"evd/internal/domain/media"
)

const mp4ReadyMinBytes = 512 * 1024
const (
	hlsMarkerFile = ".transcoded"
	mp4MarkerFile = ".mp4transcoded"
)

const (
	defaultMP4Concurrency   = 1
	defaultPrewarmInterval  = 45 * time.Second
	defaultPrewarmStableFor = 40 * time.Second
	prewarmQueueSize        = 512
)

// Service handles media-related use cases.
type Service struct {
	store     VideoRepository
	converter Converter
	logger    *log.Logger
	jobs      *jobRegistry

	mp4Slots chan struct{}

	prewarmOnce     sync.Once
	prewarmQueue    chan string
	prewarmQueued   map[string]struct{}
	prewarmObserved map[string]prewarmObservation
	prewarmMu       sync.Mutex
}

// NewService creates a media use-case service with injected ports.
func NewService(store VideoRepository, converter Converter, logger *log.Logger) *Service {
	return &Service{
		store:     store,
		converter: converter,
		logger:    logger,
		jobs:      newJobRegistry(),
		mp4Slots:  make(chan struct{}, defaultMP4Concurrency),

		prewarmQueue:    make(chan string, prewarmQueueSize),
		prewarmQueued:   make(map[string]struct{}),
		prewarmObserved: make(map[string]prewarmObservation),
	}
}

type prewarmObservation struct {
	size       int64
	modifiedAt time.Time
	firstSeen  time.Time
}

// ListVideos returns discoverable media files from the library.
func (s *Service) ListVideos() ([]media.Video, error) {
	return s.store.ListVideos()
}

// DeleteVideo removes a source file from the library and clears derived artifacts.
func (s *Service) DeleteVideo(rawPath string) error {
	rel, _, err := s.store.ResolveVideoPath(rawPath)
	if err != nil {
		return err
	}
	return s.store.DeleteVideo(rel)
}

// StartMP4Prewarm periodically starts MP4 conversion for downloaded non-MP4 videos
// that stayed unchanged for a short time window.
func (s *Service) StartMP4Prewarm(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultPrewarmInterval
	}

	s.prewarmOnce.Do(func() {
		s.logger.Printf("MP4 prewarm enabled: interval=%s", interval)
		go s.runMP4PrewarmWorker(ctx)
		go s.runMP4PrewarmScanner(ctx, interval)
	})
}

func (s *Service) runMP4PrewarmScanner(ctx context.Context, interval time.Duration) {
	s.enqueuePrewarmCandidates()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.enqueuePrewarmCandidates()
		}
	}
}

func (s *Service) runMP4PrewarmWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case relPath := <-s.prewarmQueue:
			s.dequeuePrewarm(relPath)

			status, err := s.StartMP4(context.Background(), relPath)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					s.logger.Printf("MP4 prewarm skipped: %s: %v", relPath, err)
				}
				continue
			}

			// Keep prewarm conversions sequential to avoid CPU spikes.
			if status.State == media.StateProcessing {
				s.waitForJobCompletion(ctx, jobKey(media.JobMP4, relPath))
			}
		}
	}
}

func (s *Service) enqueuePrewarmCandidates() {
	videos, err := s.store.ListVideos()
	if err != nil {
		s.logger.Printf("MP4 prewarm scan failed: %v", err)
		return
	}

	now := time.Now()
	seen := make(map[string]struct{}, len(videos))

	for _, video := range videos {
		relPath := video.Path
		seen[relPath] = struct{}{}

		ext := strings.ToLower(filepath.Ext(relPath))
		if ext == ".mp4" {
			continue
		}

		outputDir, outputPath, _ := s.store.MP4Paths(relPath)
		if mp4Ready(outputDir, outputPath, s.converter.MP4MarkerVersion()) {
			continue
		}

		mp4JobKey := jobKey(media.JobMP4, relPath)
		if s.jobs.IsRunning(mp4JobKey) {
			continue
		}

		obs, stable := s.observeStability(relPath, video.Size, video.ModifiedAt, now)
		if !stable || now.Sub(obs.firstSeen) < defaultPrewarmStableFor {
			continue
		}

		s.enqueuePrewarm(relPath)
	}

	s.gcPrewarmObservations(seen)
}

func (s *Service) observeStability(relPath string, size int64, modifiedAt time.Time, now time.Time) (prewarmObservation, bool) {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()

	prev, ok := s.prewarmObserved[relPath]
	if !ok || prev.size != size || !prev.modifiedAt.Equal(modifiedAt) {
		next := prewarmObservation{
			size:       size,
			modifiedAt: modifiedAt,
			firstSeen:  now,
		}
		s.prewarmObserved[relPath] = next
		return next, false
	}

	return prev, true
}

func (s *Service) gcPrewarmObservations(seen map[string]struct{}) {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()

	for relPath := range s.prewarmObserved {
		if _, ok := seen[relPath]; !ok {
			delete(s.prewarmObserved, relPath)
			delete(s.prewarmQueued, relPath)
		}
	}
}

func (s *Service) enqueuePrewarm(relPath string) {
	s.prewarmMu.Lock()
	if _, ok := s.prewarmQueued[relPath]; ok {
		s.prewarmMu.Unlock()
		return
	}
	s.prewarmQueued[relPath] = struct{}{}
	s.prewarmMu.Unlock()

	select {
	case s.prewarmQueue <- relPath:
	default:
		s.prewarmMu.Lock()
		delete(s.prewarmQueued, relPath)
		s.prewarmMu.Unlock()
		s.logger.Printf("MP4 prewarm queue full, skipping: %s", relPath)
	}
}

func (s *Service) dequeuePrewarm(relPath string) {
	s.prewarmMu.Lock()
	delete(s.prewarmQueued, relPath)
	s.prewarmMu.Unlock()
}

func (s *Service) waitForJobCompletion(ctx context.Context, key string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		state, _, _ := s.jobs.Status(key)
		if state != media.StateProcessing {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// StartHLS ensures HLS conversion is scheduled for requested media file.
func (s *Service) StartHLS(ctx context.Context, rawPath string, follow bool) (media.JobStatus, error) {
	rel, full, err := s.store.ResolveVideoPath(rawPath)
	if err != nil {
		return media.JobStatus{}, err
	}

	outputDir, playlist, url := s.store.HLSPaths(rel)
	ready, segments := hlsReady(outputDir, playlist, s.converter.HLSMarkerVersion())

	jobKey := jobKey(media.JobHLS, rel)
	if s.jobs.IsRunning(jobKey) {
		return media.JobStatus{State: media.StateProcessing, Processing: true, URL: url, Segments: segments, Ready: ready}, nil
	}

	if ready {
		return media.JobStatus{State: media.StateReady, Ready: true, URL: url, Segments: segments}, nil
	}

	if err := s.prepareHLSOutput(outputDir); err != nil {
		return media.JobStatus{}, err
	}

	s.jobs.Start(jobKey)
	s.logger.Printf("HLS conversion started: %s", rel)
	go func() {
		var err error
		if follow {
			err = s.converter.ConvertHLSFollow(context.Background(), full, outputDir, playlist, 2*time.Minute)
		} else {
			err = s.converter.ConvertHLS(context.Background(), full, outputDir, playlist)
		}
		if err != nil {
			s.logger.Printf("HLS conversion failed: %s: %v", rel, err)
			_ = os.RemoveAll(outputDir)
			s.jobs.Fail(jobKey, err)
			return
		}
		s.logger.Printf("HLS conversion finished: %s", rel)
		s.jobs.Ready(jobKey)
	}()

	return media.JobStatus{State: media.StateProcessing, Processing: true, URL: url, Segments: segments}, nil
}

// HLSStatus returns current HLS conversion state for a media file.
func (s *Service) HLSStatus(rawPath string) (media.JobStatus, error) {
	rel, _, err := s.store.ResolveVideoPath(rawPath)
	if err != nil {
		return media.JobStatus{}, err
	}

	outputDir, playlist, url := s.store.HLSPaths(rel)
	ready, segments := hlsReady(outputDir, playlist, s.converter.HLSMarkerVersion())

	jobKey := jobKey(media.JobHLS, rel)
	state, jobErr, progress := s.jobs.Status(jobKey)
	if state == media.StateFailed {
		return media.JobStatus{State: media.StateFailed, Error: jobErr, URL: url, Progress: progress}, nil
	}
	if state == media.StateProcessing {
		return media.JobStatus{State: media.StateProcessing, Processing: true, URL: url, Segments: segments, Ready: ready, Progress: progress}, nil
	}

	if ready {
		return media.JobStatus{State: media.StateReady, Ready: true, URL: url, Segments: segments}, nil
	}

	return media.JobStatus{State: media.StateIdle, URL: url, Segments: segments, Ready: false}, nil
}

// StartMP4 ensures MP4 conversion is scheduled for a non-mp4 source file.
func (s *Service) StartMP4(ctx context.Context, rawPath string) (media.JobStatus, error) {
	rel, full, err := s.store.ResolveVideoPath(rawPath)
	if err != nil {
		return media.JobStatus{}, err
	}

	ext := strings.ToLower(filepath.Ext(rel))
	if ext == ".mp4" {
		return media.JobStatus{}, errors.New("unsupported file type")
	}

	outputDir, outputPath, url := s.store.MP4Paths(rel)
	ready := mp4Ready(outputDir, outputPath, s.converter.MP4MarkerVersion())

	jobKey := jobKey(media.JobMP4, rel)
	if s.jobs.IsRunning(jobKey) {
		_, _, progress := s.jobs.Status(jobKey)
		return media.JobStatus{State: media.StateProcessing, Processing: true, URL: url, Ready: ready, Progress: progress}, nil
	}

	if ready {
		return media.JobStatus{State: media.StateReady, Ready: true, URL: url}, nil
	}

	if err := s.prepareMP4Output(outputDir, outputPath); err != nil {
		return media.JobStatus{}, err
	}

	s.jobs.Start(jobKey)
	s.logger.Printf("MP4 conversion started: %s", rel)
	go func() {
		s.mp4Slots <- struct{}{}
		defer func() { <-s.mp4Slots }()

		err := s.converter.ConvertMP4WithProgress(context.Background(), full, outputPath, func(progress int) {
			s.jobs.Progress(jobKey, progress)
		})
		if err != nil {
			s.logger.Printf("MP4 conversion failed: %s: %v", rel, err)
			_ = os.Remove(outputPath)
			_ = os.Remove(filepath.Join(outputDir, mp4MarkerFile))
			s.jobs.Fail(jobKey, err)
			return
		}
		_ = os.WriteFile(filepath.Join(outputDir, mp4MarkerFile), []byte(s.converter.MP4MarkerVersion()), 0o644)
		s.logger.Printf("MP4 conversion finished: %s", rel)
		s.jobs.Ready(jobKey)
	}()

	return media.JobStatus{State: media.StateProcessing, Processing: true, URL: url, Progress: 0}, nil
}

// MP4Status returns MP4 conversion state and readiness.
func (s *Service) MP4Status(rawPath string) (media.JobStatus, error) {
	rel, _, err := s.store.ResolveVideoPath(rawPath)
	if err != nil {
		return media.JobStatus{}, err
	}

	outputDir, outputPath, url := s.store.MP4Paths(rel)
	ready := mp4Ready(outputDir, outputPath, s.converter.MP4MarkerVersion())

	jobKey := jobKey(media.JobMP4, rel)
	state, jobErr, progress := s.jobs.Status(jobKey)
	if state == media.StateFailed {
		return media.JobStatus{State: media.StateFailed, Error: jobErr, URL: url, Progress: progress}, nil
	}
	if state == media.StateProcessing {
		return media.JobStatus{State: media.StateProcessing, Processing: true, URL: url, Ready: ready, Progress: progress}, nil
	}

	if ready {
		return media.JobStatus{State: media.StateReady, Ready: true, URL: url, Progress: 100}, nil
	}

	return media.JobStatus{State: media.StateIdle, URL: url, Ready: false, Progress: progress}, nil
}

// MP4Processing reports whether MP4 conversion is currently running.
func (s *Service) MP4Processing(rawPath string) (bool, error) {
	rel, _, err := s.store.ResolveVideoPath(rawPath)
	if err != nil {
		return false, err
	}
	jobKey := jobKey(media.JobMP4, rel)
	state, _, _ := s.jobs.Status(jobKey)
	return state == media.StateProcessing, nil
}

// StreamMP4 writes an MP4 stream directly from source file (or growing file when follow=true).
func (s *Service) StreamMP4(ctx context.Context, rawPath string, follow bool, out io.Writer) error {
	_, full, err := s.store.ResolveVideoPath(rawPath)
	if err != nil {
		return err
	}
	idleTimeout := 10 * time.Minute
	if follow {
		idleTimeout = 0
	}
	return s.converter.StreamMP4(ctx, full, out, follow, idleTimeout)
}

func hlsReady(outputDir, playlistPath, version string) (bool, int) {
	if !markerMatches(outputDir, hlsMarkerFile, version) {
		return false, 0
	}

	info, err := os.Stat(playlistPath)
	if err != nil || info.Size() == 0 {
		return false, 0
	}

	segments := 0
	entries, err := os.ReadDir(outputDir)
	if err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".ts") {
				segments++
			}
		}
	}

	return segments > 0, segments
}

func mp4Ready(outputDir, outputPath, version string) bool {
	if !markerMatches(outputDir, mp4MarkerFile, version) {
		return false
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		return false
	}

	return info.Size() >= mp4ReadyMinBytes
}

func markerMatches(outputDir, markerFile, version string) bool {
	data, err := os.ReadFile(filepath.Join(outputDir, markerFile))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == version
}

func (s *Service) prepareHLSOutput(outputDir string) error {
	_ = os.RemoveAll(outputDir)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, hlsMarkerFile), []byte(s.converter.HLSMarkerVersion()), 0o644)
}

func (s *Service) prepareMP4Output(outputDir, outputPath string) error {
	_ = os.Remove(outputPath)
	_ = os.Remove(filepath.Join(outputDir, mp4MarkerFile))
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	return nil
}

type jobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*jobState
}

type jobState struct {
	state    media.JobState
	err      string
	progress int
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{jobs: make(map[string]*jobState)}
}

func (j *jobRegistry) IsRunning(key string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	state, ok := j.jobs[key]
	return ok && state.state == media.StateProcessing
}

func (j *jobRegistry) Start(key string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.jobs[key] = &jobState{state: media.StateProcessing}
}

func (j *jobRegistry) Ready(key string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	state := j.jobs[key]
	if state == nil {
		state = &jobState{}
	}
	state.state = media.StateReady
	state.progress = 100
	j.jobs[key] = state
}

func (j *jobRegistry) Fail(key string, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	state := j.jobs[key]
	if state == nil {
		state = &jobState{}
	}
	state.state = media.StateFailed
	state.err = err.Error()
	j.jobs[key] = state
}

func (j *jobRegistry) Status(key string) (media.JobState, string, int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	state, ok := j.jobs[key]
	if !ok {
		return media.StateIdle, "", 0
	}
	return state.state, state.err, state.progress
}

func (j *jobRegistry) Progress(key string, value int) {
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	state := j.jobs[key]
	if state == nil {
		state = &jobState{}
	}
	if value > state.progress {
		state.progress = value
	}
	j.jobs[key] = state
}

func jobKey(jobType media.JobType, relPath string) string {
	return string(jobType) + ":" + relPath
}
