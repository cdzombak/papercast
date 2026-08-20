// Package app orchestrates papercast's sync/process/publish pipeline.
package app

import (
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cdzombak/papercast/internal/audio"
	"github.com/cdzombak/papercast/internal/config"
	"github.com/cdzombak/papercast/internal/feed"
	"github.com/cdzombak/papercast/internal/instapaper"
	"github.com/cdzombak/papercast/internal/store"
	"github.com/cdzombak/papercast/internal/tts"
)

// Exit codes per the spec.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitPartial = 8
)

// WorkDirName is the chunk-staging directory created under the output dir.
const WorkDirName = ".papercast-work"

const listLimit = 500

// InstapaperAPI is the subset of the Instapaper client used by the app.
type InstapaperAPI interface {
	ListBookmarks(ctx context.Context, have []instapaper.HaveItem, limit int) (*instapaper.ListResult, error)
	GetText(ctx context.Context, bookmarkID int64) (string, error)
}

// DescriberAPI generates article descriptions.
type DescriberAPI interface {
	Describe(ctx context.Context, blockTexts []string) (string, error)
}

// App wires together the papercast pipeline. All fields are required unless
// noted otherwise.
type App struct {
	Cfg        *config.Config
	Store      *store.Store
	Instapaper InstapaperAPI
	Synth      tts.Synthesizer
	Assembler  audio.Assembler
	Describer  DescriberAPI // nil when LLM descriptions are disabled
	Log        *slog.Logger
	Now        func() time.Time // nil = time.Now
	RandInt    func(n int) int  // nil = math/rand/v2; used for voice selection
	Version    string
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *App) randInt(n int) int {
	if a.RandInt != nil {
		return a.RandInt(n)
	}
	return rand.IntN(n)
}

func (a *App) workDir(bookmarkID int64) string {
	return filepath.Join(a.Cfg.Output.Dir, WorkDirName, strconv.FormatInt(bookmarkID, 10))
}

// sync updates the local database from Instapaper's unread folder and removes
// output files for deleted bookmarks.
func (a *App) sync(ctx context.Context) error {
	have, err := a.Store.HaveList()
	if err != nil {
		return fmt.Errorf("load have list: %w", err)
	}
	ipHave := make([]instapaper.HaveItem, len(have))
	for i, h := range have {
		ipHave[i] = instapaper.HaveItem{BookmarkID: h.BookmarkID, Hash: h.Hash}
	}

	result, err := a.Instapaper.ListBookmarks(ctx, ipHave, listLimit)
	if err != nil {
		return fmt.Errorf("list bookmarks: %w", err)
	}
	a.Log.Info("instapaper sync", "new_or_changed", len(result.Bookmarks), "deleted", len(result.DeleteIDs))

	for _, b := range result.Bookmarks {
		err := a.Store.UpsertBookmark(store.Bookmark{
			BookmarkID: b.BookmarkID,
			URL:        b.URL,
			Title:      b.Title,
			Hash:       b.Hash,
			SavedAt:    b.SavedAt,
		})
		if err != nil {
			return fmt.Errorf("upsert bookmark %d: %w", b.BookmarkID, err)
		}
	}

	removedFiles, err := a.Store.Delete(result.DeleteIDs)
	if err != nil {
		return fmt.Errorf("delete bookmarks: %w", err)
	}
	for _, name := range removedFiles {
		path := filepath.Join(a.Cfg.Output.Dir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			a.Log.Warn("failed to remove episode file for deleted bookmark", "path", path, "error", err)
		} else {
			a.Log.Info("removed episode for deleted bookmark", "file", name)
		}
	}
	for _, id := range result.DeleteIDs {
		if err := os.RemoveAll(a.workDir(id)); err != nil {
			a.Log.Warn("failed to remove work dir for deleted bookmark", "bookmark_id", id, "error", err)
		}
	}
	return nil
}

// ListArticles syncs with Instapaper and writes one line per article to w.
func (a *App) ListArticles(ctx context.Context, w io.Writer) error {
	if err := a.sync(ctx); err != nil {
		return err
	}
	articles, err := a.Store.ListAll()
	if err != nil {
		return err
	}
	for _, art := range articles {
		if _, err := fmt.Fprintf(w, "%d\t%s\t%s\n", art.BookmarkID, art.Domain(), art.Title); err != nil {
			return err
		}
	}
	return nil
}

// Run executes a full sync + process + publish cycle and returns the process
// exit code.
func (a *App) Run(ctx context.Context) int {
	if err := a.sync(ctx); err != nil {
		a.Log.Error("instapaper sync failed", "error", err)
		return ExitFailure
	}

	a.retryDescriptions(ctx)

	articles, err := a.Store.ListAll()
	if err != nil {
		a.Log.Error("list articles", "error", err)
		return ExitFailure
	}

	var succeeded, rejected, failed int
	for _, art := range articles {
		if ctx.Err() != nil {
			a.Log.Warn("run interrupted; remaining articles will be processed next run",
				"error", ctx.Err())
			break
		}
		switch a.considerArticle(art) {
		case decisionSkip:
			continue
		case decisionProcess:
		}
		log := a.Log.With("bookmark_id", art.BookmarkID, "title", art.Title)
		err := a.processArticle(ctx, art)
		switch {
		case err == nil:
			log.Info("article published")
			succeeded++
		case isRejection(err):
			log.Info("article rejected as too short")
			rejected++
		case ctx.Err() != nil:
			// Interrupted part-way through, which says nothing about the
			// article: give back the attempt it just consumed, leave its
			// cached chunks in place, and don't count it as a failure.
			log.Warn("article processing interrupted; attempt not counted", "error", err)
			if err := a.Store.RestoreAttempt(art.BookmarkID, art.Attempts, art.LastAttemptAt); err != nil {
				log.Warn("failed to restore attempt counter", "error", err)
			}
		default:
			log.Error("article processing failed", "error", err, "attempts", art.Attempts+1)
			failed++
			if art.Attempts+1 >= a.Cfg.Processing.MaxAttempts {
				log.Warn("article failed permanently; removing cached chunks")
				if err := os.RemoveAll(a.workDir(art.BookmarkID)); err != nil {
					log.Warn("failed to remove work dir", "error", err)
				}
			}
		}
	}

	if err := a.writeFeed(); err != nil {
		a.Log.Error("write feed", "error", err)
		return ExitFailure
	}

	a.Log.Info("run complete", "published", succeeded, "rejected", rejected, "failed", failed)
	switch {
	case failed == 0:
		return ExitSuccess
	case succeeded+rejected > 0:
		return ExitPartial
	default:
		return ExitFailure
	}
}

type decision int

const (
	decisionProcess decision = iota
	decisionSkip
)

// considerArticle decides whether an article needs a processing attempt now.
func (a *App) considerArticle(art *store.Article) decision {
	if art.Published() || art.RejectedTooShort {
		return decisionSkip
	}
	if art.Attempts >= a.Cfg.Processing.MaxAttempts {
		a.Log.Warn("skipping article that exhausted its attempts",
			"bookmark_id", art.BookmarkID, "title", art.Title, "attempts", art.Attempts)
		return decisionSkip
	}
	if art.LastAttemptAt != nil {
		elapsed := a.now().Sub(*art.LastAttemptAt)
		if elapsed < a.Cfg.Processing.RetryInterval.Std() {
			a.Log.Info("skipping article attempted too recently",
				"bookmark_id", art.BookmarkID, "last_attempt", art.LastAttemptAt, "retry_in",
				a.Cfg.Processing.RetryInterval.Std()-elapsed)
			return decisionSkip
		}
	}
	return decisionProcess
}

// retryDescriptions attempts LLM descriptions for any article that has content
// but no stored description (including already-published episodes, whose feed
// entries are updated once a description succeeds).
func (a *App) retryDescriptions(ctx context.Context) {
	if a.Describer == nil {
		return
	}
	articles, err := a.Store.ListAll()
	if err != nil {
		a.Log.Warn("list articles for description retry", "error", err)
		return
	}
	for _, art := range articles {
		if art.RejectedTooShort || art.HTML == nil || art.Description != nil {
			continue
		}
		// An unpublished article that exhausted its attempts will never make
		// it into the feed, so its description would never be read.
		if !art.Published() && art.Attempts >= a.Cfg.Processing.MaxAttempts {
			continue
		}
		if err := a.describeAndStore(ctx, art); err != nil {
			a.Log.Warn("description generation failed; will retry next run",
				"bookmark_id", art.BookmarkID, "title", art.Title, "error", err)
		}
	}
}

// writeFeed regenerates the RSS feed from all published articles and writes it
// atomically into the output directory.
func (a *App) writeFeed() error {
	articles, err := a.Store.ListAll()
	if err != nil {
		return err
	}
	var episodes []feed.Episode
	for _, art := range articles {
		if !art.Published() {
			continue
		}
		episodes = append(episodes, feed.Episode{
			BookmarkID:  art.BookmarkID,
			Title:       art.Title,
			Link:        art.URL,
			Description: a.episodeDescription(art),
			MP3Filename: *art.MP3Filename,
			SizeBytes:   art.MP3SizeBytes,
			Duration:    time.Duration(art.DurationSecs) * time.Second,
			PubDate:     art.SavedAt,
		})
	}

	xml, err := feed.Render(feed.Meta{
		Title:       a.Cfg.Feed.Title,
		Description: a.Cfg.Feed.Description,
		Language:    a.Cfg.Feed.Language,
		Author:      a.Cfg.Feed.Author,
		CoverArtURL: a.Cfg.Feed.CoverArtURL,
		BaseURL:     a.Cfg.Feed.BaseURL,
		Generator:   "papercast " + a.Version,
	}, episodes, a.now())
	if err != nil {
		return fmt.Errorf("render feed: %w", err)
	}

	dest := filepath.Join(a.Cfg.Output.Dir, a.Cfg.Output.FeedFilename)
	tmp, err := os.CreateTemp(a.Cfg.Output.Dir, ".feed-*.xml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(xml); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// episodeDescription returns the stored LLM description, or the fallback,
// with the archiver link appended when the integration is configured.
func (a *App) episodeDescription(art *store.Article) string {
	desc := FallbackDescription(art)
	if art.Description != nil && *art.Description != "" {
		desc = *art.Description
	}
	if a.Cfg.Archiver.BaseURL != "" {
		desc += "\n\n" + archiverLink(a.Cfg.Archiver.BaseURL, art)
	}
	return desc
}

// archiverLink returns an HTML anchor to the papercast-archiver page for art.
// Descriptions are CDATA-wrapped in the feed, so the anchor reaches podcast
// clients intact.
func archiverLink(baseURL string, art *store.Article) string {
	q := url.Values{}
	if art.Title != "" {
		q.Set("title", art.Title)
	}
	if d := art.Domain(); d != "" {
		q.Set("domain", d)
	}
	u := baseURL + "/" + strconv.FormatInt(art.BookmarkID, 10)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return `<a href="` + html.EscapeString(u) + `">Archive or Delete this article in Instapaper</a>`
}

// FallbackDescription is used when no LLM description is available.
func FallbackDescription(art *store.Article) string {
	// Not %q: that escapes quotes and backslashes in the title Go-style,
	// which would show up verbatim in podcast clients.
	return fmt.Sprintf(`"%s" from %s`, art.Title, art.Domain())
}
