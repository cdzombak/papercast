package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/cdzombak/papercast/internal/audio"
	"github.com/cdzombak/papercast/internal/store"
	"github.com/cdzombak/papercast/internal/textproc"
	"github.com/cdzombak/papercast/internal/tts"
)

// errRejected marks an article rejected for being too short; it is a terminal
// determination, not a processing failure.
var errRejected = errors.New("article rejected: too short")

func isRejection(err error) bool { return errors.Is(err, errRejected) }

// processArticle runs the full pipeline for one article: fetch content,
// describe, synthesize, assemble, and publish. It records the attempt before
// doing any work.
func (a *App) processArticle(ctx context.Context, art *store.Article) error {
	if err := a.Store.RecordAttempt(art.BookmarkID, a.now()); err != nil {
		return fmt.Errorf("record attempt: %w", err)
	}

	blocks, err := a.prepareBlocks(ctx, art)
	if err != nil {
		return err
	}

	if wc := textproc.WordCount(blocks); wc < a.Cfg.Processing.MinWords {
		if err := a.Store.MarkRejected(art.BookmarkID); err != nil {
			return fmt.Errorf("mark rejected: %w", err)
		}
		return fmt.Errorf("%w (%d words, minimum %d)", errRejected, wc, a.Cfg.Processing.MinWords)
	}

	if a.Describer != nil && art.Description == nil {
		if err := a.describeAndStore(ctx, art); err != nil {
			a.Log.Warn("description generation failed; using fallback",
				"bookmark_id", art.BookmarkID, "title", art.Title, "error", err)
		}
	}

	voice, err := a.ensureVoice(art)
	if err != nil {
		return err
	}

	chunks, err := textproc.BuildChunks(blocks, textproc.RenderOptions{
		SSML:          a.Cfg.TTS.SSML,
		MaxChunkBytes: a.Cfg.TTS.MaxChunkBytes,
		Intro:         a.introText(art),
	})
	if err != nil {
		return fmt.Errorf("build chunks: %w", err)
	}

	workDir := a.workDir(art.BookmarkID)
	chunkPaths, err := a.synthesizeChunks(ctx, chunks, voice, workDir)
	if err != nil {
		return err
	}

	episodePath := filepath.Join(workDir, "episode.mp3")
	if err := a.Assembler.Concat(ctx, chunkPaths, episodePath); err != nil {
		return fmt.Errorf("assemble episode: %w", err)
	}
	if err := audio.WriteID3Tags(episodePath, art.Title, art.Domain()); err != nil {
		return fmt.Errorf("write ID3 tags: %w", err)
	}
	duration, err := a.Assembler.Duration(ctx, episodePath)
	if err != nil {
		return fmt.Errorf("measure episode duration: %w", err)
	}
	info, err := os.Stat(episodePath)
	if err != nil {
		return fmt.Errorf("stat episode: %w", err)
	}

	// The work dir lives under the output dir, so this rename is atomic.
	outName := EpisodeFilename(art.BookmarkID)
	if err := os.Rename(episodePath, filepath.Join(a.Cfg.Output.Dir, outName)); err != nil {
		return fmt.Errorf("publish episode: %w", err)
	}
	if err := a.Store.MarkPublished(art.BookmarkID, outName, info.Size(), int64(duration.Seconds())); err != nil {
		return fmt.Errorf("mark published: %w", err)
	}
	if err := os.RemoveAll(workDir); err != nil {
		a.Log.Warn("failed to remove work dir after publish", "bookmark_id", art.BookmarkID, "error", err)
	}
	return nil
}

// EpisodeFilename returns the deterministic output filename for a bookmark.
func EpisodeFilename(bookmarkID int64) string {
	return strconv.FormatInt(bookmarkID, 10) + ".mp3"
}

// prepareBlocks ensures article HTML is present (fetching and storing it if
// needed) and parses it into text blocks.
func (a *App) prepareBlocks(ctx context.Context, art *store.Article) ([]textproc.Block, error) {
	if art.HTML == nil {
		html, err := a.Instapaper.GetText(ctx, art.BookmarkID)
		if err != nil {
			return nil, fmt.Errorf("fetch article text: %w", err)
		}
		if err := a.Store.SetHTML(art.BookmarkID, html); err != nil {
			return nil, fmt.Errorf("store article text: %w", err)
		}
		art.HTML = &html
	}
	blocks, err := textproc.ExtractBlocks(*art.HTML)
	if err != nil {
		return nil, fmt.Errorf("parse article text: %w", err)
	}
	return blocks, nil
}

// describeAndStore generates and persists an LLM description for art,
// updating art in place on success.
func (a *App) describeAndStore(ctx context.Context, art *store.Article) error {
	blocks, err := textproc.ExtractBlocks(*art.HTML)
	if err != nil {
		return fmt.Errorf("parse article text: %w", err)
	}
	desc, err := a.Describer.Describe(ctx, blockTexts(blocks))
	if err != nil {
		return err
	}
	if err := a.Store.SetDescription(art.BookmarkID, desc); err != nil {
		return fmt.Errorf("store description: %w", err)
	}
	art.Description = &desc
	return nil
}

func blockTexts(blocks []textproc.Block) []string {
	texts := make([]string, len(blocks))
	for i, b := range blocks {
		texts[i] = b.Text
	}
	return texts
}

// ensureVoice returns the article's stored voice, choosing and persisting a
// random one from the configured list if not yet set.
func (a *App) ensureVoice(art *store.Article) (string, error) {
	if art.Voice != "" {
		return art.Voice, nil
	}
	voice := a.Cfg.TTS.Voices[a.randInt(len(a.Cfg.TTS.Voices))]
	if err := a.Store.SetVoice(art.BookmarkID, voice); err != nil {
		return "", fmt.Errorf("store voice: %w", err)
	}
	art.Voice = voice
	return voice, nil
}

// introText returns the spoken episode introduction, or "" when disabled.
func (a *App) introText(art *store.Article) string {
	if !a.Cfg.TTS.IntroEnabled() {
		return ""
	}
	return fmt.Sprintf("%s. From %s.", art.Title, art.Domain())
}

// synthesizeChunks produces (or reuses cached) MP3 files for each chunk in
// workDir, returning their paths in order.
func (a *App) synthesizeChunks(ctx context.Context, chunks []textproc.Chunk, voice, workDir string) ([]string, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	paths := make([]string, len(chunks))
	for i, chunk := range chunks {
		req := tts.Request{
			Payload:      chunk.Payload,
			SSML:         chunk.SSML,
			Voice:        voice,
			LanguageCode: a.Cfg.TTS.Language,
		}
		path := filepath.Join(workDir, tts.CacheKey(req)+".mp3")
		if _, err := os.Stat(path); err == nil {
			a.Log.Debug("reusing cached chunk", "chunk", i+1, "path", path)
			paths[i] = path
			continue
		}
		a.Log.Debug("synthesizing chunk", "chunk", i+1, "of", len(chunks), "bytes", len(chunk.Payload))
		mp3, err := a.Synth.Synthesize(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("synthesize chunk %d/%d: %w", i+1, len(chunks), err)
		}
		// Stage to a temp name so an interrupted write never leaves a
		// truncated chunk under its cacheable name.
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, mp3, 0o644); err != nil {
			return nil, fmt.Errorf("write chunk: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return nil, fmt.Errorf("finalize chunk: %w", err)
		}
		paths[i] = path
	}
	return paths, nil
}
