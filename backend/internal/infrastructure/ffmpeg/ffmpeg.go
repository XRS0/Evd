package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	HLSMarkerFile = ".transcoded"
	MP4MarkerFile = ".mp4transcoded"
)

// Converter wraps ffmpeg/ffprobe calls.
type Converter struct {
	HLSVersion        string
	MP4Version        string
	HLSSegmentSeconds int
}

// NewConverter creates ffmpeg adapter with marker versions and segment duration.
func NewConverter(hlsVersion, mp4Version string, hlsSegmentSeconds int) *Converter {
	return &Converter{HLSVersion: hlsVersion, MP4Version: mp4Version, HLSSegmentSeconds: hlsSegmentSeconds}
}

// HLSMarkerVersion returns current HLS transcoding marker value.
func (c *Converter) HLSMarkerVersion() string {
	return c.HLSVersion
}

// MP4MarkerVersion returns current MP4 transcoding marker value.
func (c *Converter) MP4MarkerVersion() string {
	return c.MP4Version
}

// ConvertHLS converts a source media file into HLS playlist and segments.
func (c *Converter) ConvertHLS(ctx context.Context, inputPath, outputDir, playlistPath string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}

	gop := c.HLSSegmentSeconds * 30
	segmentPattern := filepath.Join(outputDir, "segment%05d.ts")
	args := []string{
		"-y",
		"-i", inputPath,
		"-sn",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "20",
		"-g", fmt.Sprintf("%d", gop),
		"-keyint_min", fmt.Sprintf("%d", gop),
		"-sc_threshold", "0",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", c.HLSSegmentSeconds),
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "192k",
		"-ar", "48000",
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", c.HLSSegmentSeconds),
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_filename", segmentPattern,
		playlistPath,
	}

	return run(ctx, "ffmpeg", args...)
}

// ConvertHLSFollow converts a growing file into HLS until idle timeout.
func (c *Converter) ConvertHLSFollow(ctx context.Context, inputPath, outputDir, playlistPath string, idleTimeout time.Duration) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}

	reader, err := newGrowReader(ctx, inputPath, 500*time.Millisecond, idleTimeout)
	if err != nil {
		return err
	}
	defer reader.Close()

	gop := c.HLSSegmentSeconds * 30
	segmentPattern := filepath.Join(outputDir, "segment%05d.ts")
	args := []string{
		"-y",
		"-fflags", "+genpts",
		"-i", "pipe:0",
		"-sn",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "20",
		"-g", fmt.Sprintf("%d", gop),
		"-keyint_min", fmt.Sprintf("%d", gop),
		"-sc_threshold", "0",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", c.HLSSegmentSeconds),
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "192k",
		"-ar", "48000",
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", c.HLSSegmentSeconds),
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_filename", segmentPattern,
		playlistPath,
	}

	return runWithInput(ctx, reader, "ffmpeg", args...)
}

// ConvertMP4 converts media into seekable MP4 output.
func (c *Converter) ConvertMP4(ctx context.Context, inputPath, outputPath string) error {
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}

	codec, _ := probeVideoCodec(ctx, inputPath)
	transcodeVideo := codec == "" || codec != "h264"

	tmpPath := outputPath + ".tmp.mp4"
	_ = os.Remove(tmpPath)

	args := []string{"-y", "-i", inputPath, "-sn", "-map", "0:v:0?", "-map", "0:a:0?"}
	if transcodeVideo {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "20")
	} else {
		args = append(args, "-c:v", "copy")
	}

	args = append(args,
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "192k",
		"-ar", "48000",
		"-f", "mp4",
		"-movflags", "+faststart",
		tmpPath,
	)

	if err := run(ctx, "ffmpeg", args...); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	_ = os.Remove(outputPath)
	return os.Rename(tmpPath, outputPath)
}

// ConvertMP4WithProgress converts media into MP4 and reports conversion percentage.
func (c *Converter) ConvertMP4WithProgress(ctx context.Context, inputPath, outputPath string, onProgress func(int)) error {
	duration, _ := probeDuration(ctx, inputPath)
	totalMs := int64(duration * 1000)
	if totalMs <= 0 {
		return c.ConvertMP4(ctx, inputPath, outputPath)
	}

	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}

	codec, _ := probeVideoCodec(ctx, inputPath)
	transcodeVideo := codec == "" || codec != "h264"

	tmpPath := outputPath + ".tmp.mp4"
	_ = os.Remove(tmpPath)

	args := []string{"-y", "-i", inputPath, "-sn", "-map", "0:v:0?", "-map", "0:a:0?", "-progress", "pipe:1", "-nostats"}
	if transcodeVideo {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "20")
	} else {
		args = append(args, "-c:v", "copy")
	}

	args = append(args,
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "192k",
		"-ar", "48000",
		"-f", "mp4",
		"-movflags", "+faststart",
		tmpPath,
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	lastProgress := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		value := parts[1]
		if key == "out_time_ms" {
			ms, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				continue
			}
			percent := int(float64(ms) / float64(totalMs) * 100)
			if percent > 99 {
				percent = 99
			}
			if percent > lastProgress {
				lastProgress = percent
				if onProgress != nil {
					onProgress(percent)
				}
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	if onProgress != nil {
		onProgress(100)
	}

	_ = os.Remove(outputPath)
	return os.Rename(tmpPath, outputPath)
}

// StreamMP4 writes fragmented MP4 stream to out.
func (c *Converter) StreamMP4(ctx context.Context, inputPath string, out io.Writer, follow bool, idleTimeout time.Duration) error {
	codec, _ := probeVideoCodec(ctx, inputPath)
	transcodeVideo := codec == "" || codec != "h264"

	args := []string{"-fflags", "+genpts", "-sn", "-map", "0:v:0?", "-map", "0:a:0?"}
	if follow {
		args = append([]string{"-i", "pipe:0"}, args...)
	} else {
		args = append([]string{"-i", inputPath}, args...)
	}

	if transcodeVideo {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p")
	} else {
		args = append(args, "-c:v", "copy")
	}

	args = append(args,
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "192k",
		"-ar", "48000",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4",
		"pipe:1",
	)

	if follow {
		reader, err := newGrowReader(ctx, inputPath, 500*time.Millisecond, idleTimeout)
		if err != nil {
			return err
		}
		defer reader.Close()
		return runWithInputOutput(ctx, reader, out, "ffmpeg", args...)
	}

	return runWithOutput(ctx, out, "ffmpeg", args...)
}

func probeVideoCodec(ctx context.Context, inputPath string) (string, error) {
	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name",
		"-of", "default=nokey=1:noprint_wrappers=1",
		inputPath,
	}
	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func probeDuration(ctx context.Context, inputPath string) (float64, error) {
	args := []string{
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nokey=1:noprint_wrappers=1",
		inputPath,
	}
	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return 0, fmt.Errorf("duration missing")
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runWithInput(ctx context.Context, input io.Reader, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	cmd.Stdin = input
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runWithOutput(ctx context.Context, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runWithInputOutput(ctx context.Context, input io.Reader, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = out
	cmd.Stdin = input
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

type growReader struct {
	ctx       context.Context
	file      *os.File
	lastSize  int64
	lastGrow  time.Time
	poll      time.Duration
	idleLimit time.Duration
	closed    bool
}

func newGrowReader(ctx context.Context, path string, poll, idle time.Duration) (*growReader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &growReader{
		ctx:       ctx,
		file:      file,
		lastSize:  info.Size(),
		lastGrow:  time.Now(),
		poll:      poll,
		idleLimit: idle,
	}, nil
}

func (g *growReader) Read(p []byte) (int, error) {
	for {
		n, err := g.file.Read(p)
		if n > 0 {
			g.lastGrow = time.Now()
			return n, nil
		}
		if err == io.EOF {
			info, statErr := g.file.Stat()
			if statErr != nil {
				return 0, statErr
			}
			if info.Size() > g.lastSize {
				g.lastSize = info.Size()
				continue
			}
			if g.idleLimit > 0 && time.Since(g.lastGrow) >= g.idleLimit {
				return 0, io.EOF
			}
			select {
			case <-g.ctx.Done():
				return 0, g.ctx.Err()
			case <-time.After(g.poll):
			}
			continue
		}
		if err != nil {
			return 0, err
		}
	}
}

func (g *growReader) Close() error {
	if g.closed {
		return nil
	}
	g.closed = true
	return g.file.Close()
}
