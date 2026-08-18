package app

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/cdzombak/papercast/internal/store"
	"github.com/cdzombak/papercast/internal/textproc"
)

// Debug processes a single article without touching its retry budget, the
// output directory, or the feed. It writes chunk audio, the assembled episode,
// and an HTML report into the article's work directory, all of which are left
// in place afterward.
func (a *App) Debug(ctx context.Context, bookmarkID int64) error {
	art, err := a.Store.Get(bookmarkID)
	if err != nil {
		return fmt.Errorf("article %d: %w", bookmarkID, err)
	}

	blocks, err := a.prepareBlocks(ctx, art)
	if err != nil {
		return err
	}
	wordCount := textproc.WordCount(blocks)

	description := "(LLM descriptions disabled)"
	if a.Describer != nil {
		desc, err := a.Describer.Describe(ctx, blockTexts(blocks))
		if err != nil {
			description = fmt.Sprintf("(generation failed: %v)", err)
		} else {
			description = desc
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

	// Debug runs always start from freshly synthesized chunks.
	workDir := a.workDir(bookmarkID)
	if err := os.RemoveAll(workDir); err != nil {
		return fmt.Errorf("clear work dir: %w", err)
	}

	chunkPaths, err := a.synthesizeChunks(ctx, chunks, voice, workDir)
	if err != nil {
		return err
	}

	episodePath := filepath.Join(workDir, "episode.mp3")
	if err := a.Assembler.Concat(ctx, chunkPaths, episodePath); err != nil {
		return fmt.Errorf("assemble episode: %w", err)
	}

	reportPath := filepath.Join(workDir, "debug.html")
	report := debugReport(art, voice, wordCount, description, chunks, chunkPaths)
	if err := os.WriteFile(reportPath, []byte(report), 0o644); err != nil {
		return fmt.Errorf("write debug report: %w", err)
	}

	a.Log.Info("debug run complete", "bookmark_id", bookmarkID,
		"chunks", len(chunks), "report", reportPath)
	fmt.Printf("Debug report: %s\n", reportPath)
	return nil
}

func debugReport(art *store.Article, voice string, wordCount int, description string,
	chunks []textproc.Chunk, chunkPaths []string) string {
	var b strings.Builder
	esc := html.EscapeString

	b.WriteString("<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\">\n")
	fmt.Fprintf(&b, "<title>papercast debug: %s</title>\n", esc(art.Title))
	b.WriteString(`<style>
body { font-family: system-ui, sans-serif; margin: 2rem auto; max-width: 60rem; padding: 0 1rem; }
.chunk { border: 1px solid #999; border-radius: 6px; margin: 1.5rem 0; padding: 1rem; }
.chunk h3 { margin-top: 0; }
pre { white-space: pre-wrap; word-wrap: break-word; background: #f4f4f4; padding: 0.75rem; border-radius: 4px; }
audio { width: 100%; margin-top: 0.5rem; }
dt { font-weight: bold; }
</style></head><body>
`)
	fmt.Fprintf(&b, "<h1>%s</h1>\n", esc(art.Title))
	b.WriteString("<dl>\n")
	fmt.Fprintf(&b, "<dt>Bookmark ID</dt><dd>%d</dd>\n", art.BookmarkID)
	fmt.Fprintf(&b, "<dt>URL</dt><dd><a href=\"%s\">%s</a></dd>\n", esc(art.URL), esc(art.URL))
	fmt.Fprintf(&b, "<dt>Voice</dt><dd>%s</dd>\n", esc(voice))
	fmt.Fprintf(&b, "<dt>Word count</dt><dd>%d</dd>\n", wordCount)
	fmt.Fprintf(&b, "<dt>LLM description</dt><dd>%s</dd>\n", esc(description))
	b.WriteString("</dl>\n")

	b.WriteString("<h2>Assembled episode</h2>\n<audio controls src=\"episode.mp3\"></audio>\n")

	fmt.Fprintf(&b, "<h2>Chunks (%d)</h2>\n", len(chunks))
	for i, chunk := range chunks {
		kind := "text"
		if chunk.SSML {
			kind = "SSML"
		}
		b.WriteString("<div class=\"chunk\">\n")
		fmt.Fprintf(&b, "<h3>Chunk %d of %d (%s, %d bytes)</h3>\n", i+1, len(chunks), kind, len(chunk.Payload))
		fmt.Fprintf(&b, "<pre>%s</pre>\n", esc(chunk.Payload))
		fmt.Fprintf(&b, "<audio controls src=\"%s\"></audio>\n", esc(filepath.Base(chunkPaths[i])))
		b.WriteString("</div>\n")
	}
	b.WriteString("</body></html>\n")
	return b.String()
}
