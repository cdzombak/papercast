package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), func() time.Time { return fixedNow })
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustUpsert(t *testing.T, s *Store, b Bookmark) {
	t.Helper()
	if err := s.UpsertBookmark(b); err != nil {
		t.Fatalf("UpsertBookmark: %v", err)
	}
}

func bookmark(id int64) Bookmark {
	return Bookmark{
		BookmarkID: id,
		URL:        "https://www.example.com/article",
		Title:      "Test Article",
		Hash:       "hash1",
		SavedAt:    time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestUpsertAndGet(t *testing.T) {
	s := openTest(t)
	mustUpsert(t, s, bookmark(1))

	a, err := s.Get(1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if a.Title != "Test Article" || a.Hash != "hash1" || a.URL != "https://www.example.com/article" {
		t.Errorf("unexpected article: %+v", a)
	}
	if !a.SavedAt.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("SavedAt = %v", a.SavedAt)
	}
	if a.Published() || a.RejectedTooShort || a.Attempts != 0 || a.HTML != nil {
		t.Errorf("new article has unexpected state: %+v", a)
	}
}

func TestGetNotFound(t *testing.T) {
	s := openTest(t)
	if _, err := s.Get(42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUpsertPreservesProcessingState(t *testing.T) {
	s := openTest(t)
	mustUpsert(t, s, bookmark(1))
	if err := s.SetHTML(1, "<p>hi</p>"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVoice(1, "en-US-Chirp3-HD-Aoede"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublished(1, "1.mp3", 1000, 60); err != nil {
		t.Fatal(err)
	}

	updated := bookmark(1)
	updated.Title = "New Title"
	updated.Hash = "hash2"
	mustUpsert(t, s, updated)

	a, err := s.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "New Title" || a.Hash != "hash2" {
		t.Errorf("metadata not updated: %+v", a)
	}
	if a.HTML == nil || *a.HTML != "<p>hi</p>" {
		t.Error("HTML lost on upsert")
	}
	if a.Voice != "en-US-Chirp3-HD-Aoede" {
		t.Error("voice lost on upsert")
	}
	if !a.Published() || *a.MP3Filename != "1.mp3" || a.MP3SizeBytes != 1000 || a.DurationSecs != 60 {
		t.Errorf("published state lost: %+v", a)
	}
}

func TestFieldUpdates(t *testing.T) {
	s := openTest(t)
	mustUpsert(t, s, bookmark(1))

	if err := s.SetDescription(1, "a summary"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRejected(1); err != nil {
		t.Fatal(err)
	}
	attemptAt := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	if err := s.RecordAttempt(1, attemptAt); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAttempt(1, attemptAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	a, err := s.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if a.Description == nil || *a.Description != "a summary" {
		t.Error("description not stored")
	}
	if !a.RejectedTooShort {
		t.Error("rejection not stored")
	}
	if a.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", a.Attempts)
	}
	if a.LastAttemptAt == nil || !a.LastAttemptAt.Equal(attemptAt.Add(time.Hour)) {
		t.Errorf("LastAttemptAt = %v", a.LastAttemptAt)
	}
}

func TestUpdateMissingArticle(t *testing.T) {
	s := openTest(t)
	if err := s.SetHTML(99, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetHTML on missing article: want ErrNotFound, got %v", err)
	}
}

func TestHaveList(t *testing.T) {
	s := openTest(t)
	mustUpsert(t, s, bookmark(2))
	b := bookmark(1)
	b.Hash = "otherhash"
	mustUpsert(t, s, b)

	have, err := s.HaveList()
	if err != nil {
		t.Fatal(err)
	}
	if len(have) != 2 || have[0].BookmarkID != 1 || have[0].Hash != "otherhash" || have[1].BookmarkID != 2 {
		t.Errorf("unexpected have list: %+v", have)
	}
}

func TestDelete(t *testing.T) {
	s := openTest(t)
	mustUpsert(t, s, bookmark(1))
	mustUpsert(t, s, bookmark(2))
	mustUpsert(t, s, bookmark(3))
	if err := s.MarkPublished(1, "1.mp3", 10, 5); err != nil {
		t.Fatal(err)
	}

	removed, err := s.Delete([]int64{1, 2, 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "1.mp3" {
		t.Errorf("removed = %v, want [1.mp3]", removed)
	}
	if _, err := s.Get(1); !errors.Is(err, ErrNotFound) {
		t.Error("article 1 not deleted")
	}
	if _, err := s.Get(3); err != nil {
		t.Error("article 3 should remain")
	}
}

func TestListAllOrder(t *testing.T) {
	s := openTest(t)
	old := bookmark(1)
	old.SavedAt = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	mustUpsert(t, s, old)
	newer := bookmark(2)
	newer.SavedAt = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	mustUpsert(t, s, newer)

	all, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].BookmarkID != 2 || all[1].BookmarkID != 1 {
		t.Errorf("unexpected order: %v, %v", all[0].BookmarkID, all[1].BookmarkID)
	}
}

func TestDomain(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://www.example.com/a/b", "example.com"},
		{"https://daringfireball.net/2026/x", "daringfireball.net"},
		{"http://WWW.Example.COM/", "example.com"},
		{"not a url", ""},
	}
	for _, tc := range cases {
		a := Article{URL: tc.url}
		if got := a.Domain(); got != tc.want {
			t.Errorf("Domain(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s1, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.UpsertBookmark(bookmark(1)); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if _, err := s2.Get(1); err != nil {
		t.Errorf("data lost across reopen: %v", err)
	}
}
