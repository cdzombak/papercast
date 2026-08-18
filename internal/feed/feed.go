// Package feed renders the podcast RSS feed.
package feed

import (
	"bytes"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eduncan911/podcast"
)

// Meta holds channel-level feed metadata.
type Meta struct {
	Title       string
	Description string
	Language    string
	Author      string // itunes:author; may be ""
	CoverArtURL string // may be ""
	BaseURL     string // absolute URL under which the feed and MP3s are served
	Generator   string // e.g. "papercast x.y.z"
}

// Episode is one item in the feed.
type Episode struct {
	BookmarkID  int64
	Title       string
	Link        string // the article's URL
	Description string // plain text; will be CDATA-wrapped
	MP3Filename string // filename within the output dir
	SizeBytes   int64
	Duration    time.Duration
	PubDate     time.Time // the bookmark's saved date
}

// Render produces the complete RSS XML. Episodes are emitted in the order
// given (caller sorts). now is used for the channel's pubDate/lastBuildDate.
func Render(meta Meta, episodes []Episode, now time.Time) ([]byte, error) {
	base, err := url.Parse(meta.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("feed: parse base URL %q: %w", meta.BaseURL, err)
	}

	p := podcast.New(meta.Title, meta.BaseURL, meta.Description, &now, &now)
	p.Language = meta.Language
	p.IAuthor = meta.Author
	p.Generator = meta.Generator
	if meta.CoverArtURL != "" {
		p.AddImage(meta.CoverArtURL)
	}

	for i := range episodes {
		ep := &episodes[i]
		item := podcast.Item{
			GUID:  strconv.FormatInt(ep.BookmarkID, 10),
			Title: ep.Title,
			Link:  ep.Link,
			// The podcast library cannot emit CDATA descriptions, and
			// requires Description to be non-empty; use a marker that is
			// swapped for the CDATA-wrapped text after encoding.
			Description: descMarker(ep.BookmarkID),
		}
		pubDate := ep.PubDate
		item.AddPubDate(&pubDate)
		item.AddEnclosure(base.JoinPath(url.PathEscape(ep.MP3Filename)).String(), podcast.MP3, ep.SizeBytes)
		item.AddDuration(int64(ep.Duration.Seconds()))
		if _, err := p.AddItem(item); err != nil {
			return nil, fmt.Errorf("feed: add item for bookmark %d: %w", ep.BookmarkID, err)
		}
	}

	var buf bytes.Buffer
	if err := p.Encode(&buf); err != nil {
		return nil, fmt.Errorf("feed: encode: %w", err)
	}

	out := buf.String()
	for i := range episodes {
		ep := &episodes[i]
		needle := "<description>" + descMarker(ep.BookmarkID) + "</description>"
		if !strings.Contains(out, needle) {
			return nil, fmt.Errorf("feed: description marker for bookmark %d not found in encoded XML", ep.BookmarkID)
		}
		replacement := "<description><![CDATA[" + cdataEscape(ep.Description) + "]]></description>"
		out = strings.Replace(out, needle, replacement, 1)
	}

	// The podcast library does not emit isPermaLink on item GUIDs, and it
	// emits no channel-level <guid>, so a global replace is safe.
	out = strings.ReplaceAll(out, "<guid>", `<guid isPermaLink="false">`)

	return []byte(out), nil
}

// descMarker returns a collision-proof placeholder for an episode's
// description.
func descMarker(bookmarkID int64) string {
	return fmt.Sprintf("PAPERCAST-DESC-%d-MARKER", bookmarkID)
}

// cdataEscape makes s safe for embedding in a CDATA section by splitting
// any "]]>" across two sections.
func cdataEscape(s string) string {
	return strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
}
