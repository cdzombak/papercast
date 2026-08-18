package audio

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	id3v2 "github.com/bogem/id3v2/v2"
)

func TestWriteID3Tags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.mp3")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0xFF}, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	const title = "Café — résumé"
	const artist = "Ærø Ñandú"
	if err := WriteID3Tags(path, title, artist); err != nil {
		t.Fatalf("WriteID3Tags: %v", err)
	}

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("reopen tagged file: %v", err)
	}
	defer func() { _ = tag.Close() }()
	if got := tag.Title(); got != title {
		t.Errorf("title = %q, want %q", got, title)
	}
	if got := tag.Artist(); got != artist {
		t.Errorf("artist = %q, want %q", got, artist)
	}
}

func TestWriteID3Tags_MissingFile(t *testing.T) {
	err := WriteID3Tags(filepath.Join(t.TempDir(), "nope.mp3"), "t", "a")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH", bin)
		}
	}
}

// makeSineMP3 generates a short sine-tone MP3 at path.
func makeSineMP3(t *testing.T, path string, seconds float64) {
	t.Helper()
	dur := strconv.FormatFloat(seconds, 'f', -1, 64)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+dur,
		"-ac", "1", "-ar", "44100", "-b:a", "64k", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generate %s: %v: %s", path, err, stderr.String())
	}
}

func TestConcatAndDuration(t *testing.T) {
	requireFFmpeg(t)
	// Directory name exercises single-quote and space escaping in the
	// concat list file.
	dir := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	chunk1 := filepath.Join(dir, "chunk 1.mp3")
	chunk2 := filepath.Join(dir, "chunk 2.mp3")
	makeSineMP3(t, chunk1, 0.3)
	makeSineMP3(t, chunk2, 0.3)

	a := NewFFmpegAssembler("", "")
	outPath := filepath.Join(dir, "out.mp3")
	if err := a.Concat(context.Background(), []string{chunk1, chunk2}, outPath); err != nil {
		t.Fatalf("Concat: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() == 0 {
		t.Error("output file is empty")
	}

	d, err := a.Duration(context.Background(), outPath)
	if err != nil {
		t.Fatalf("Duration: %v", err)
	}
	if d < 200*time.Millisecond || d > 1200*time.Millisecond {
		t.Errorf("duration = %v, want ~0.6s (0.2s-1.2s)", d)
	}

	// Concat list file was removed.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "concat-") {
			t.Errorf("leftover concat list file: %s", e.Name())
		}
	}
}

func TestConcat_EmptyChunkList(t *testing.T) {
	a := NewFFmpegAssembler("", "")
	err := a.Concat(context.Background(), nil, filepath.Join(t.TempDir(), "out.mp3"))
	if err == nil {
		t.Fatal("expected error for empty chunk list")
	}
}

func TestConcat_MissingInputFile(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	a := NewFFmpegAssembler("", "")
	err := a.Concat(context.Background(),
		[]string{filepath.Join(dir, "does-not-exist.mp3")},
		filepath.Join(dir, "out.mp3"))
	if err == nil {
		t.Fatal("expected error for missing input file")
	}
	if !strings.Contains(err.Error(), "No such file") {
		t.Errorf("err = %v, want ffmpeg stderr mentioning missing file", err)
	}
}

func TestDuration_MissingFile(t *testing.T) {
	requireFFmpeg(t)
	a := NewFFmpegAssembler("", "")
	_, err := a.Duration(context.Background(), filepath.Join(t.TempDir(), "nope.mp3"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
