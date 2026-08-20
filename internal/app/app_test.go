package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cdzombak/papercast/internal/config"
	"github.com/cdzombak/papercast/internal/instapaper"
	"github.com/cdzombak/papercast/internal/store"
	"github.com/cdzombak/papercast/internal/tts"
)

// longHTML is comfortably over the test min word count of 10.
const longHTML = `<div><p>One two three four five six seven eight nine ten.</p>
<p>Eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty.</p></div>`

const shortHTML = `<div><p>Too short.</p></div>`

type fakeInstapaper struct {
	lists    []*instapaper.ListResult // popped per ListBookmarks call; empty result when exhausted
	haveSeen [][]instapaper.HaveItem
	texts    map[int64]string
	textErr  error
	getCalls int
}

func (f *fakeInstapaper) ListBookmarks(_ context.Context, have []instapaper.HaveItem, _ int) (*instapaper.ListResult, error) {
	f.haveSeen = append(f.haveSeen, have)
	if len(f.lists) == 0 {
		return &instapaper.ListResult{}, nil
	}
	r := f.lists[0]
	f.lists = f.lists[1:]
	return r, nil
}

func (f *fakeInstapaper) GetText(_ context.Context, id int64) (string, error) {
	f.getCalls++
	if f.textErr != nil {
		return "", f.textErr
	}
	text, ok := f.texts[id]
	if !ok {
		return "", fmt.Errorf("no text for bookmark %d", id)
	}
	return text, nil
}

type fakeSynth struct {
	calls  []tts.Request
	err    error  // when set, every Synthesize call fails
	onCall func() // when set, called at the start of every Synthesize
}

func (f *fakeSynth) Synthesize(_ context.Context, req tts.Request) ([]byte, error) {
	if f.onCall != nil {
		f.onCall()
	}
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	return []byte("AUDIO[" + req.Voice + ":" + req.Payload + "]"), nil
}

func (f *fakeSynth) Close() error { return nil }

type fakeAssembler struct {
	concatErr   error
	concatCalls int
}

func (f *fakeAssembler) Concat(_ context.Context, chunkPaths []string, outPath string) error {
	f.concatCalls++
	if f.concatErr != nil {
		return f.concatErr
	}
	var out []byte
	for _, p := range chunkPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, b...)
	}
	return os.WriteFile(outPath, out, 0o644)
}

func (f *fakeAssembler) Duration(_ context.Context, _ string) (time.Duration, error) {
	return 90 * time.Second, nil
}

type fakeDescriber struct {
	result string
	err    error
	calls  int
}

func (f *fakeDescriber) Describe(_ context.Context, _ []string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.result, nil
}

type fixture struct {
	app   *App
	store *store.Store
	ip    *fakeInstapaper
	synth *fakeSynth
	asm   *fakeAssembler
	now   time.Time
	out   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		now:   time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		out:   t.TempDir(),
		ip:    &fakeInstapaper{texts: map[int64]string{}},
		synth: &fakeSynth{},
		asm:   &fakeAssembler{},
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f.store = st

	intro := true
	cfg := &config.Config{
		Processing: config.ProcessingConfig{
			MinWords:      10,
			RetryInterval: config.Duration(time.Hour),
			MaxAttempts:   3,
		},
		TTS: config.TTSConfig{
			Voices:        []string{"en-US-Chirp3-HD-Aoede"},
			Language:      "en-US",
			Speed:         1.0,
			MaxChunkBytes: 4500,
			Intro:         &intro,
		},
		Feed: config.FeedConfig{
			Title:       "Test Feed",
			Description: "Test Feed",
			Language:    "en-us",
			BaseURL:     "https://example.com/pods/",
		},
		Output: config.OutputConfig{Dir: f.out, FeedFilename: "feed.xml"},
	}
	f.app = &App{
		Cfg:        cfg,
		Store:      st,
		Instapaper: f.ip,
		Synth:      f.synth,
		Assembler:  f.asm,
		Log:        slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Now:        func() time.Time { return f.now },
		RandInt:    func(int) int { return 0 },
		Version:    "test",
	}
	return f
}

func (f *fixture) queueBookmarks(bms ...instapaper.Bookmark) {
	f.ip.lists = append(f.ip.lists, &instapaper.ListResult{Bookmarks: bms})
}

func bm(id int64, title string) instapaper.Bookmark {
	return instapaper.Bookmark{
		BookmarkID: id,
		URL:        fmt.Sprintf("https://www.example.com/articles/%d", id),
		Title:      title,
		Hash:       fmt.Sprintf("hash-%d", id),
		SavedAt:    time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
	}
}

func readFeed(t *testing.T, f *fixture) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.out, "feed.xml"))
	if err != nil {
		t.Fatalf("read feed: %v", err)
	}
	return string(b)
}

func TestRunHappyPath(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "First Article"), bm(2, "Second Article"))
	f.ip.texts[1] = longHTML
	f.ip.texts[2] = longHTML

	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("exit code = %d, want 0", code)
	}

	for _, id := range []int64{1, 2} {
		mp3 := filepath.Join(f.out, fmt.Sprintf("%d.mp3", id))
		if _, err := os.Stat(mp3); err != nil {
			t.Errorf("missing published episode: %v", err)
		}
		art, err := f.store.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !art.Published() || art.DurationSecs != 90 || art.MP3SizeBytes == 0 {
			t.Errorf("article %d not fully published: %+v", id, art)
		}
		if art.Voice != "en-US-Chirp3-HD-Aoede" {
			t.Errorf("voice = %q", art.Voice)
		}
		if _, err := os.Stat(f.app.workDir(id)); !os.IsNotExist(err) {
			t.Errorf("work dir for %d not cleaned up", id)
		}
	}

	xml := readFeed(t, f)
	for _, want := range []string{
		"<guid isPermaLink=\"false\">1</guid>",
		"<guid isPermaLink=\"false\">2</guid>",
		"https://example.com/pods/1.mp3",
		"<![CDATA[“First Article” from example.com]]>",
		"<title>Test Feed</title>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("feed missing %q", want)
		}
	}

	// No archiver configured, so no archiver link.
	if strings.Contains(xml, "Archive or Delete") {
		t.Error("archiver link present without archiver config")
	}

	// The spoken intro leads the first chunk.
	if len(f.synth.calls) == 0 || !strings.HasPrefix(f.synth.calls[0].Payload, "First Article. From example.com.") &&
		!strings.HasPrefix(f.synth.calls[0].Payload, "Second Article. From example.com.") {
		t.Errorf("first chunk missing intro: %q", f.synth.calls[0].Payload)
	}

	// The configured speaking rate reaches the synthesizer.
	if f.synth.calls[0].Speed != 1.0 {
		t.Errorf("speed = %v, want 1.0 from config", f.synth.calls[0].Speed)
	}
}

func TestSyncSendsHaveWithHashes(t *testing.T) {
	f := newFixture(t)
	if err := f.store.UpsertBookmark(store.Bookmark{BookmarkID: 5, URL: "https://example.com/5", Title: "T", Hash: "h5", SavedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkPublished(5, "5.mp3", 1, 1); err != nil {
		t.Fatal(err)
	}

	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("exit code = %d", code)
	}
	if len(f.ip.haveSeen) != 1 || len(f.ip.haveSeen[0]) != 1 {
		t.Fatalf("haveSeen = %+v", f.ip.haveSeen)
	}
	h := f.ip.haveSeen[0][0]
	if h.BookmarkID != 5 || h.Hash != "h5" {
		t.Errorf("have item = %+v", h)
	}
}

func TestMetadataUpdateFlowsToFeedWithoutResynthesis(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "Original Title"))
	f.ip.texts[1] = longHTML
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatal("first run failed")
	}
	synthCalls := len(f.synth.calls)

	updated := bm(1, "Updated Title")
	updated.Hash = "hash-1b"
	f.queueBookmarks(updated)
	f.now = f.now.Add(2 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatal("second run failed")
	}

	if len(f.synth.calls) != synthCalls {
		t.Errorf("audio was re-synthesized on metadata update")
	}
	if !strings.Contains(readFeed(t, f), "Updated Title") {
		t.Error("feed does not reflect updated title")
	}
}

func TestDeleteRemovesEpisodeAndFeedEntry(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "Doomed"), bm(2, "Keeper"))
	f.ip.texts[1] = longHTML
	f.ip.texts[2] = longHTML
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatal("first run failed")
	}

	f.ip.lists = append(f.ip.lists, &instapaper.ListResult{DeleteIDs: []int64{1}})
	f.now = f.now.Add(2 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatal("second run failed")
	}

	if _, err := os.Stat(filepath.Join(f.out, "1.mp3")); !os.IsNotExist(err) {
		t.Error("deleted bookmark's MP3 still present")
	}
	if _, err := f.store.Get(1); err != store.ErrNotFound {
		t.Errorf("deleted bookmark still in DB: %v", err)
	}
	xml := readFeed(t, f)
	if strings.Contains(xml, "Doomed") {
		t.Error("feed still contains deleted episode")
	}
	if !strings.Contains(xml, "Keeper") {
		t.Error("feed lost remaining episode")
	}
}

func TestTooShortArticleRejectedPermanently(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "Tiny"))
	f.ip.texts[1] = shortHTML

	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("exit code = %d, want 0 (rejection is not a failure)", code)
	}
	art, err := f.store.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if !art.RejectedTooShort || art.Published() {
		t.Errorf("article state: %+v", art)
	}
	if len(f.synth.calls) != 0 {
		t.Error("TTS called for rejected article")
	}

	// Later runs never retry it.
	getCalls := f.ip.getCalls
	f.now = f.now.Add(24 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatal("second run failed")
	}
	if f.ip.getCalls != getCalls || len(f.synth.calls) != 0 {
		t.Error("rejected article was retried")
	}
}

func TestPartialFailureExitCode(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "Good"), bm(2, "Bad"))
	f.ip.texts[1] = longHTML
	// Article 2 has no text -> GetText fails -> article fails.

	if code := f.app.Run(context.Background()); code != ExitPartial {
		t.Fatalf("exit code = %d, want %d", code, ExitPartial)
	}
	// The successful article still made it into the feed.
	if !strings.Contains(readFeed(t, f), "Good") {
		t.Error("successful article missing from feed")
	}
}

func TestAllFailedExitCode(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "Bad"))
	// No text for the article: GetText fails.
	if code := f.app.Run(context.Background()); code != ExitFailure {
		t.Fatalf("exit code = %d, want %d", code, ExitFailure)
	}
}

func TestSyncFailureIsFatal(t *testing.T) {
	f := newFixture(t)
	f.app.Instapaper = failingInstapaper{}
	if code := f.app.Run(context.Background()); code != ExitFailure {
		t.Fatalf("exit code = %d, want %d", code, ExitFailure)
	}
}

type failingInstapaper struct{}

func (failingInstapaper) ListBookmarks(context.Context, []instapaper.HaveItem, int) (*instapaper.ListResult, error) {
	return nil, fmt.Errorf("instapaper is down")
}
func (failingInstapaper) GetText(context.Context, int64) (string, error) {
	return "", fmt.Errorf("instapaper is down")
}

func TestRetryIntervalGate(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "Flaky"))
	f.ip.texts[1] = longHTML
	f.synth.err = fmt.Errorf("tts unavailable")

	if code := f.app.Run(context.Background()); code != ExitFailure {
		t.Fatalf("first run exit = %d, want 1", code)
	}
	synthCalls := len(f.synth.calls)

	// Too soon: skipped, and the skip is not an error.
	f.now = f.now.Add(30 * time.Minute)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("second run exit = %d, want 0 (skip is not an error)", code)
	}
	if len(f.synth.calls) != synthCalls {
		t.Error("article retried before retry interval elapsed")
	}

	// After the interval: retried and succeeds.
	f.synth.err = nil
	f.now = f.now.Add(time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("third run exit = %d, want 0", code)
	}
	art, _ := f.store.Get(1)
	if !art.Published() {
		t.Error("article not published after retry")
	}
}

func TestAttemptsExhaustedCountsOnceThenQuiet(t *testing.T) {
	f := newFixture(t)
	f.app.Cfg.Processing.MaxAttempts = 2
	f.queueBookmarks(bm(1, "Cursed"))
	f.ip.texts[1] = longHTML
	f.synth.err = fmt.Errorf("tts permanently broken")

	if code := f.app.Run(context.Background()); code != ExitFailure {
		t.Fatalf("first run exit = %d, want 1", code)
	}
	f.now = f.now.Add(2 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitFailure {
		t.Fatalf("second run exit = %d, want 1", code)
	}
	// Cached chunks are removed after the final failure.
	if _, err := os.Stat(f.app.workDir(1)); !os.IsNotExist(err) {
		t.Error("work dir not removed after final failure")
	}

	// Exhausted: quiet skip, exit 0.
	f.now = f.now.Add(2 * time.Hour)
	synthCalls := len(f.synth.calls)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("third run exit = %d, want 0", code)
	}
	if len(f.synth.calls) != synthCalls {
		t.Error("exhausted article was attempted again")
	}
	art, _ := f.store.Get(1)
	if art.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", art.Attempts)
	}
}

func TestInterruptedRunDoesNotConsumeAnAttempt(t *testing.T) {
	f := newFixture(t)
	first, second := bm(1, "Interrupted"), bm(2, "Untouched")
	second.SavedAt = first.SavedAt.Add(-time.Hour) // sorts after the first
	f.queueBookmarks(first, second)
	f.ip.texts[1] = longHTML
	f.ip.texts[2] = longHTML

	// Interrupt the run part-way through the first article's synthesis.
	ctx, cancel := context.WithCancel(context.Background())
	f.synth.onCall = cancel
	f.synth.err = context.Canceled

	if code := f.app.Run(ctx); code != ExitSuccess {
		t.Fatalf("exit code = %d, want 0 (an interrupt is not an article failure)", code)
	}

	art, err := f.store.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if art.Attempts != 0 || art.LastAttemptAt != nil {
		t.Errorf("interrupted article consumed an attempt: attempts=%d last_attempt=%v",
			art.Attempts, art.LastAttemptAt)
	}
	// Cached chunks survive so the next run can resume.
	if _, err := os.Stat(f.app.workDir(1)); err != nil {
		t.Errorf("work dir removed after interrupt: %v", err)
	}
	// The run stops instead of marching through the remaining articles.
	if len(f.synth.calls) != 1 {
		t.Errorf("synth calls = %d, want 1", len(f.synth.calls))
	}
	if f.ip.getCalls != 1 {
		t.Errorf("get_text calls = %d, want 1", f.ip.getCalls)
	}
}

func TestChunkCacheReusedOnRetry(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "Cachey"))
	f.ip.texts[1] = longHTML
	f.asm.concatErr = fmt.Errorf("disk full")

	if code := f.app.Run(context.Background()); code != ExitFailure {
		t.Fatalf("first run exit = %d, want 1", code)
	}
	synthCalls := len(f.synth.calls)
	if synthCalls == 0 {
		t.Fatal("no synthesis happened")
	}
	getCalls := f.ip.getCalls

	f.asm.concatErr = nil
	f.now = f.now.Add(2 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("second run exit = %d, want 0", code)
	}
	if len(f.synth.calls) != synthCalls {
		t.Errorf("cached chunks not reused: %d synth calls on retry", len(f.synth.calls)-synthCalls)
	}
	if f.ip.getCalls != getCalls {
		t.Error("article content re-fetched despite being stored")
	}
	art, _ := f.store.Get(1)
	if !art.Published() {
		t.Error("article not published on retry")
	}
}

func TestDescriptionsStoredAndRetried(t *testing.T) {
	f := newFixture(t)
	desc := &fakeDescriber{err: fmt.Errorf("llm down")}
	f.app.Describer = desc
	f.queueBookmarks(bm(1, "Described"))
	f.ip.texts[1] = longHTML

	// Description fails; article still publishes with the fallback.
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("first run exit = %d, want 0", code)
	}
	if !strings.Contains(readFeed(t, f), "<![CDATA[“Described” from example.com]]>") {
		t.Error("feed missing fallback description")
	}
	art, _ := f.store.Get(1)
	if art.Description != nil {
		t.Error("failed description should not be stored")
	}

	// Next run: description retried for the already-published episode and the
	// feed picks it up.
	desc.err = nil
	desc.result = "A thoughtful summary."
	f.now = f.now.Add(2 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("second run exit = %d, want 0", code)
	}
	art, _ = f.store.Get(1)
	if art.Description == nil || *art.Description != "A thoughtful summary." {
		t.Errorf("description not stored on retry: %v", art.Description)
	}
	if !strings.Contains(readFeed(t, f), "<![CDATA[A thoughtful summary.]]>") {
		t.Error("feed missing retried description")
	}
}

func TestDescriptionsNotRetriedForExhaustedArticles(t *testing.T) {
	f := newFixture(t)
	f.app.Cfg.Processing.MaxAttempts = 1
	desc := &fakeDescriber{err: fmt.Errorf("llm down")}
	f.app.Describer = desc
	f.queueBookmarks(bm(1, "Cursed"))
	f.ip.texts[1] = longHTML
	f.synth.err = fmt.Errorf("tts permanently broken")

	if code := f.app.Run(context.Background()); code != ExitFailure {
		t.Fatalf("first run exit = %d, want 1", code)
	}
	describeCalls := desc.calls
	if describeCalls == 0 {
		t.Fatal("description was never attempted")
	}

	// The article has given up; its description is not attempted again.
	f.now = f.now.Add(24 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("second run exit = %d, want 0", code)
	}
	if desc.calls != describeCalls {
		t.Errorf("description retried for an exhausted article: %d extra calls",
			desc.calls-describeCalls)
	}
}

func TestDescriptionsRetriedForPublishedArticleThatUsedAllAttempts(t *testing.T) {
	f := newFixture(t)
	f.app.Cfg.Processing.MaxAttempts = 2
	desc := &fakeDescriber{err: fmt.Errorf("llm down")}
	f.app.Describer = desc
	f.queueBookmarks(bm(1, "Eventually Fine"))
	f.ip.texts[1] = longHTML
	f.asm.concatErr = fmt.Errorf("disk full")

	if code := f.app.Run(context.Background()); code != ExitFailure {
		t.Fatalf("first run exit = %d, want 1", code)
	}
	// Second run publishes on the article's last attempt.
	f.asm.concatErr = nil
	f.now = f.now.Add(2 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("second run exit = %d, want 0", code)
	}
	art, _ := f.store.Get(1)
	if !art.Published() || art.Attempts != 2 {
		t.Fatalf("article state: published=%v attempts=%d", art.Published(), art.Attempts)
	}

	// Published, so its description is still worth retrying.
	desc.err = nil
	desc.result = "A thoughtful summary."
	f.now = f.now.Add(2 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("third run exit = %d, want 0", code)
	}
	if !strings.Contains(readFeed(t, f), "<![CDATA[A thoughtful summary.]]>") {
		t.Error("feed missing description retried for a published episode")
	}
}

func TestVoicePersistsAcrossRetries(t *testing.T) {
	f := newFixture(t)
	f.app.Cfg.TTS.Voices = []string{"en-US-Chirp3-HD-Aoede", "en-US-Chirp3-HD-Puck"}
	pick := 1
	f.app.RandInt = func(n int) int { return pick }
	f.queueBookmarks(bm(1, "Voiced"))
	f.ip.texts[1] = longHTML
	f.asm.concatErr = fmt.Errorf("boom")

	if code := f.app.Run(context.Background()); code != ExitFailure {
		t.Fatal("first run should fail")
	}
	if f.synth.calls[0].Voice != "en-US-Chirp3-HD-Puck" {
		t.Fatalf("voice = %q", f.synth.calls[0].Voice)
	}

	// Even if the RNG would now pick differently, the stored voice is used.
	pick = 0
	f.asm.concatErr = nil
	f.now = f.now.Add(2 * time.Hour)
	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatal("second run failed")
	}
	last := f.synth.calls[len(f.synth.calls)-1]
	if last.Voice != "en-US-Chirp3-HD-Puck" {
		t.Errorf("retry used voice %q, want stored voice", last.Voice)
	}
}

func TestIntroDisabled(t *testing.T) {
	f := newFixture(t)
	off := false
	f.app.Cfg.TTS.Intro = &off
	f.queueBookmarks(bm(1, "No Intro"))
	f.ip.texts[1] = longHTML

	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatal("run failed")
	}
	if strings.Contains(f.synth.calls[0].Payload, "No Intro. From example.com.") {
		t.Error("intro present despite being disabled")
	}
}

func TestArchiverLinkInDescriptions(t *testing.T) {
	f := newFixture(t)
	f.app.Cfg.Archiver.BaseURL = "https://archiver.example.com"
	f.queueBookmarks(bm(1, "First Article"))
	f.ip.texts[1] = longHTML

	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("exit code = %d, want 0", code)
	}

	want := `<![CDATA[“First Article” from example.com<br><br>` +
		`<a href="https://archiver.example.com/1?domain=example.com&amp;title=First+Article">Archive or Delete this article in Instapaper</a>]]>`
	if xml := readFeed(t, f); !strings.Contains(xml, want) {
		t.Errorf("feed missing archiver link %q", want)
	}
}

func TestDescriptionHTMLEscaping(t *testing.T) {
	f := newFixture(t)
	f.app.Cfg.Archiver.BaseURL = "https://archiver.example.com"
	f.app.Describer = &fakeDescriber{result: "Fish & <chips>.\n\nA second paragraph."}
	f.queueBookmarks(bm(1, "Tom & Jerry <Reviewed>"))
	f.ip.texts[1] = longHTML

	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("exit code = %d, want 0", code)
	}
	xml := readFeed(t, f)

	// The LLM description is escaped and its blank line becomes <br><br>.
	want := "<![CDATA[Fish &amp; &lt;chips&gt;.<br><br>A second paragraph.<br><br>" +
		`<a href="https://archiver.example.com/1?domain=example.com&amp;title=Tom+%26+Jerry+%3CReviewed%3E">`
	if !strings.Contains(xml, want) {
		t.Errorf("feed missing escaped description %q, got:\n%s", want, xml)
	}
	// The title reaches <title> XML-escaped, not HTML-escaped twice.
	if !strings.Contains(xml, "<title>Tom &amp; Jerry &lt;Reviewed&gt;</title>") {
		t.Error("item title not XML-escaped as expected")
	}
}

func TestFallbackDescriptionEscapedInFeed(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "Tom & Jerry <Reviewed>"))
	f.ip.texts[1] = longHTML

	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("exit code = %d, want 0", code)
	}

	want := `<![CDATA[“Tom &amp; Jerry &lt;Reviewed&gt;” from example.com]]>`
	if xml := readFeed(t, f); !strings.Contains(xml, want) {
		t.Errorf("feed missing escaped fallback description %q", want)
	}
}

func TestChannelDescriptionEscaped(t *testing.T) {
	f := newFixture(t)
	f.app.Cfg.Feed.Description = "Articles & essays,\nread aloud."
	f.queueBookmarks(bm(1, "First Article"))
	f.ip.texts[1] = longHTML

	if code := f.app.Run(context.Background()); code != ExitSuccess {
		t.Fatalf("exit code = %d, want 0", code)
	}

	want := "<description><![CDATA[Articles &amp; essays,<br>read aloud.]]></description>"
	if xml := readFeed(t, f); !strings.Contains(xml, want) {
		t.Errorf("feed missing escaped channel description %q", want)
	}
}

func TestTextToHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{`"quoted" & <tagged>`, `&#34;quoted&#34; &amp; &lt;tagged&gt;`},
		{"one\ntwo\r\nthree\rfour", "one<br>two<br>three<br>four"},
		{"  padded\n", "padded"},
		{"already &amp; escaped", "already &amp;amp; escaped"},
	}
	for _, tc := range cases {
		if got := textToHTML(tc.in); got != tc.want {
			t.Errorf("textToHTML(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestArchiverLinkEscaping(t *testing.T) {
	art := &store.Article{
		BookmarkID: 42,
		Title:      `Ampersands & "Quotes"`,
		URL:        "https://sub.example.com/a?x=1",
	}
	got := archiverLink("https://archiver.example.com/prefix", art)
	want := `<a href="https://archiver.example.com/prefix/42?domain=sub.example.com&amp;title=Ampersands+%26+%22Quotes%22">Archive or Delete this article in Instapaper</a>`
	if got != want {
		t.Errorf("archiverLink =\n%s\nwant\n%s", got, want)
	}
}

func TestFallbackDescriptionQuoting(t *testing.T) {
	art := &store.Article{
		Title: `The "Best" C:\Path Article`,
		URL:   "https://www.example.com/a",
	}
	// The title's own straight quotes and backslashes pass through as-is;
	// only the wrapping quotes are curly.
	want := `“The "Best" C:\Path Article” from example.com`
	if got := FallbackDescription(art); got != want {
		t.Errorf("FallbackDescription = %s, want %s", got, want)
	}
}

func TestListArticles(t *testing.T) {
	f := newFixture(t)
	f.queueBookmarks(bm(1, "Listed Article"))
	var buf strings.Builder
	if err := f.app.ListArticles(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	want := "1\texample.com\tListed Article\n"
	if buf.String() != want {
		t.Errorf("output = %q, want %q", buf.String(), want)
	}
	if len(f.synth.calls) != 0 {
		t.Error("list-articles should not process articles")
	}
}

func TestDebugMode(t *testing.T) {
	f := newFixture(t)
	desc := &fakeDescriber{result: "Debug summary."}
	f.app.Describer = desc
	if err := f.store.UpsertBookmark(store.Bookmark{
		BookmarkID: 7, URL: "https://example.com/7", Title: "Debugged", Hash: "h", SavedAt: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	f.ip.texts[7] = longHTML

	if err := f.app.Debug(context.Background(), 7); err != nil {
		t.Fatalf("Debug: %v", err)
	}

	workDir := f.app.workDir(7)
	report, err := os.ReadFile(filepath.Join(workDir, "debug.html"))
	if err != nil {
		t.Fatalf("debug report missing: %v", err)
	}
	for _, want := range []string{"Debugged", "Debug summary.", "episode.mp3", "<pre>"} {
		if !strings.Contains(string(report), want) {
			t.Errorf("debug report missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, "episode.mp3")); err != nil {
		t.Error("debug episode.mp3 missing")
	}

	art, _ := f.store.Get(7)
	if art.Published() {
		t.Error("debug mode must not publish")
	}
	if art.Attempts != 0 || art.LastAttemptAt != nil {
		t.Error("debug mode must not consume the retry budget")
	}
	if art.Description != nil {
		t.Error("debug mode must not store the description")
	}
	if art.HTML == nil {
		t.Error("debug mode should store fetched HTML")
	}
	if _, err := os.Stat(filepath.Join(f.out, "7.mp3")); !os.IsNotExist(err) {
		t.Error("debug mode must not write to the output dir")
	}
}

func TestDebugModeUnknownArticle(t *testing.T) {
	f := newFixture(t)
	if err := f.app.Debug(context.Background(), 999); err == nil {
		t.Fatal("expected error for unknown article")
	}
}
