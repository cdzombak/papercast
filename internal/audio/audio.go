// Package audio assembles MP3 chunks into output files via ffmpeg and writes
// ID3 tags.
package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Assembler combines MP3 chunks and inspects audio files.
type Assembler interface {
	// Concat concatenates MP3 chunk files into a single MP3 at outPath:
	// mono, 44100 Hz, 64 kbps CBR.
	Concat(ctx context.Context, chunkPaths []string, outPath string) error
	// Duration returns the audio duration of the file at path.
	Duration(ctx context.Context, path string) (time.Duration, error)
}

type ffmpegAssembler struct {
	ffmpegPath  string
	ffprobePath string
}

// NewFFmpegAssembler returns an Assembler that shells out to ffmpeg/ffprobe.
// ffmpegPath/ffprobePath "" mean: look up "ffmpeg"/"ffprobe" in PATH.
func NewFFmpegAssembler(ffmpegPath, ffprobePath string) Assembler {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}
	return &ffmpegAssembler{ffmpegPath: ffmpegPath, ffprobePath: ffprobePath}
}

func (a *ffmpegAssembler) Concat(ctx context.Context, chunkPaths []string, outPath string) error {
	if len(chunkPaths) == 0 {
		return errors.New("concat: no chunk paths given")
	}

	listPath, err := writeConcatList(chunkPaths, outPath)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(listPath) }()

	cmd := exec.CommandContext(ctx, a.ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-ac", "1", "-ar", "44100",
		"-codec:a", "libmp3lame", "-b:a", "64k",
		outPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg concat to %s: %w: %s", outPath, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (a *ffmpegAssembler) Duration(ctx context.Context, path string) (time.Duration, error) {
	cmd := exec.CommandContext(ctx, a.ffprobePath,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe duration of %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(stdout.String()), 64)
	if err != nil {
		return 0, fmt.Errorf("parse ffprobe duration of %s: %w", path, err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// writeConcatList writes an ffmpeg concat-demuxer list file next to outPath
// and returns its path.
func writeConcatList(chunkPaths []string, outPath string) (string, error) {
	var b strings.Builder
	for _, p := range chunkPaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("resolve chunk path %s: %w", p, err)
		}
		fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(abs, "'", `'\''`))
	}
	f, err := os.CreateTemp(filepath.Dir(outPath), "concat-*.txt")
	if err != nil {
		return "", fmt.Errorf("create concat list file: %w", err)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write concat list file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("close concat list file: %w", err)
	}
	return f.Name(), nil
}
