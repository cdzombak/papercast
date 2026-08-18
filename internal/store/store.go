// Package store persists article state in a SQLite database.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Article is one Instapaper bookmark and its processing state.
type Article struct {
	BookmarkID       int64
	URL              string
	Title            string
	Hash             string
	SavedAt          time.Time
	HTML             *string
	Description      *string
	Voice            string
	MP3Filename      *string
	MP3SizeBytes     int64
	DurationSecs     int64
	RejectedTooShort bool
	Attempts         int
	LastAttemptAt    *time.Time
}

// Published reports whether the article has a published episode.
func (a *Article) Published() bool { return a.MP3Filename != nil }

// Domain returns the article URL's host with any leading "www." stripped.
func (a *Article) Domain() string {
	u, err := url.Parse(a.URL)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// HaveItem is one entry for the Instapaper bookmarks/list `have` parameter.
type HaveItem struct {
	BookmarkID int64
	Hash       string
}

// ErrNotFound is returned when an article does not exist.
var ErrNotFound = errors.New("article not found")

// Store wraps the SQLite database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if necessary) the database at path.
// now is used to stamp modification times; pass nil for time.Now.
func Open(path string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	// A single connection avoids SQLITE_BUSY between statements; this
	// application has no concurrent DB access.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: now}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate database %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 1 {
		return nil
	}
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS articles (
			bookmark_id        INTEGER PRIMARY KEY,
			url                TEXT NOT NULL,
			title              TEXT NOT NULL,
			hash               TEXT NOT NULL DEFAULT '',
			saved_at_unix      INTEGER NOT NULL,
			html               TEXT,
			description        TEXT,
			voice              TEXT NOT NULL DEFAULT '',
			mp3_filename       TEXT,
			mp3_size_bytes     INTEGER NOT NULL DEFAULT 0,
			duration_secs      INTEGER NOT NULL DEFAULT 0,
			rejected_too_short INTEGER NOT NULL DEFAULT 0,
			attempts           INTEGER NOT NULL DEFAULT 0,
			last_attempt_unix  INTEGER,
			created_at_unix    INTEGER NOT NULL,
			updated_at_unix    INTEGER NOT NULL
		) STRICT;
		PRAGMA user_version = 1;
	`)
	return err
}

const articleColumns = `bookmark_id, url, title, hash, saved_at_unix, html, description,
	voice, mp3_filename, mp3_size_bytes, duration_secs, rejected_too_short,
	attempts, last_attempt_unix`

func scanArticle(row interface{ Scan(...any) error }) (*Article, error) {
	var a Article
	var savedAt int64
	var html, description, mp3 sql.NullString
	var lastAttempt sql.NullInt64
	var rejected int
	err := row.Scan(&a.BookmarkID, &a.URL, &a.Title, &a.Hash, &savedAt, &html,
		&description, &a.Voice, &mp3, &a.MP3SizeBytes, &a.DurationSecs,
		&rejected, &a.Attempts, &lastAttempt)
	if err != nil {
		return nil, err
	}
	a.SavedAt = time.Unix(savedAt, 0).UTC()
	if html.Valid {
		a.HTML = &html.String
	}
	if description.Valid {
		a.Description = &description.String
	}
	if mp3.Valid {
		a.MP3Filename = &mp3.String
	}
	a.RejectedTooShort = rejected != 0
	if lastAttempt.Valid {
		t := time.Unix(lastAttempt.Int64, 0).UTC()
		a.LastAttemptAt = &t
	}
	return &a, nil
}

// Bookmark is the subset of Instapaper bookmark data synced into the store.
type Bookmark struct {
	BookmarkID int64
	URL        string
	Title      string
	Hash       string
	SavedAt    time.Time
}

// UpsertBookmark inserts a new article or updates the metadata (URL, title,
// hash, saved time) of an existing one, preserving its processing state.
func (s *Store) UpsertBookmark(b Bookmark) error {
	nowUnix := s.now().Unix()
	_, err := s.db.Exec(`
		INSERT INTO articles (bookmark_id, url, title, hash, saved_at_unix, created_at_unix, updated_at_unix)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bookmark_id) DO UPDATE SET
			url = excluded.url,
			title = excluded.title,
			hash = excluded.hash,
			saved_at_unix = excluded.saved_at_unix,
			updated_at_unix = excluded.updated_at_unix`,
		b.BookmarkID, b.URL, b.Title, b.Hash, b.SavedAt.Unix(), nowUnix, nowUnix)
	return err
}

// Delete removes articles by bookmark ID, returning the MP3 filenames of any
// deleted articles that had published episodes.
func (s *Store) Delete(ids []int64) ([]string, error) {
	var filenames []string
	for _, id := range ids {
		var mp3 sql.NullString
		err := s.db.QueryRow("SELECT mp3_filename FROM articles WHERE bookmark_id = ?", id).Scan(&mp3)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return filenames, err
		}
		if _, err := s.db.Exec("DELETE FROM articles WHERE bookmark_id = ?", id); err != nil {
			return filenames, err
		}
		if mp3.Valid && mp3.String != "" {
			filenames = append(filenames, mp3.String)
		}
	}
	return filenames, nil
}

// HaveList returns id/hash pairs for every article, for incremental sync.
func (s *Store) HaveList() ([]HaveItem, error) {
	rows, err := s.db.Query("SELECT bookmark_id, hash FROM articles ORDER BY bookmark_id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []HaveItem
	for rows.Next() {
		var it HaveItem
		if err := rows.Scan(&it.BookmarkID, &it.Hash); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// Get returns the article with the given bookmark ID, or ErrNotFound.
func (s *Store) Get(id int64) (*Article, error) {
	a, err := scanArticle(s.db.QueryRow(
		"SELECT "+articleColumns+" FROM articles WHERE bookmark_id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// ListAll returns all articles, newest saved first.
func (s *Store) ListAll() ([]*Article, error) {
	rows, err := s.db.Query(
		"SELECT " + articleColumns + " FROM articles ORDER BY saved_at_unix DESC, bookmark_id DESC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var articles []*Article
	for rows.Next() {
		a, err := scanArticle(rows)
		if err != nil {
			return nil, err
		}
		articles = append(articles, a)
	}
	return articles, rows.Err()
}

func (s *Store) update(id int64, query string, args ...any) error {
	args = append(args, s.now().Unix(), id)
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetHTML stores the fetched article HTML.
func (s *Store) SetHTML(id int64, html string) error {
	return s.update(id, "UPDATE articles SET html = ?, updated_at_unix = ? WHERE bookmark_id = ?", html)
}

// SetDescription stores a generated article description.
func (s *Store) SetDescription(id int64, description string) error {
	return s.update(id, "UPDATE articles SET description = ?, updated_at_unix = ? WHERE bookmark_id = ?", description)
}

// SetVoice stores the TTS voice chosen for the article.
func (s *Store) SetVoice(id int64, voice string) error {
	return s.update(id, "UPDATE articles SET voice = ?, updated_at_unix = ? WHERE bookmark_id = ?", voice)
}

// MarkRejected permanently marks the article as too short to process.
func (s *Store) MarkRejected(id int64) error {
	return s.update(id, "UPDATE articles SET rejected_too_short = 1, updated_at_unix = ? WHERE bookmark_id = ?")
}

// RecordAttempt increments the article's attempt counter and stamps the
// attempt time.
func (s *Store) RecordAttempt(id int64, at time.Time) error {
	return s.update(id,
		"UPDATE articles SET attempts = attempts + 1, last_attempt_unix = ?, updated_at_unix = ? WHERE bookmark_id = ?",
		at.Unix())
}

// RestoreAttempt resets an article's attempt counter and last-attempt time to
// the given values, undoing a RecordAttempt.
func (s *Store) RestoreAttempt(id int64, attempts int, at *time.Time) error {
	var atUnix sql.NullInt64
	if at != nil {
		atUnix = sql.NullInt64{Int64: at.Unix(), Valid: true}
	}
	return s.update(id,
		"UPDATE articles SET attempts = ?, last_attempt_unix = ?, updated_at_unix = ? WHERE bookmark_id = ?",
		attempts, atUnix)
}

// MarkPublished records a successfully published episode.
func (s *Store) MarkPublished(id int64, mp3Filename string, sizeBytes, durationSecs int64) error {
	return s.update(id,
		"UPDATE articles SET mp3_filename = ?, mp3_size_bytes = ?, duration_secs = ?, updated_at_unix = ? WHERE bookmark_id = ?",
		mp3Filename, sizeBytes, durationSecs)
}
