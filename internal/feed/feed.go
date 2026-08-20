// Package feed renders the podcast RSS feed.
package feed

import (
	"bytes"
	"errors"
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
	Description string // HTML fragment; will be CDATA-wrapped
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
	Description string // HTML fragment; will be CDATA-wrapped
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

	// The podcast library cannot emit CDATA descriptions; every description
	// is encoded as a marker and swapped for CDATA-wrapped HTML afterwards.
	p := podcast.New(meta.Title, meta.BaseURL, channelDescMarker, &now, &now)
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
			// The library also requires Description to be non-empty, which
			// the marker satisfies.
			Description: itemDescMarker(ep.BookmarkID),
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

	out, err := replaceDesc(buf.String(), channelDescMarker, meta.Description)
	if err != nil {
		return nil, fmt.Errorf("feed: channel description: %w", err)
	}
	for i := range episodes {
		ep := &episodes[i]
		out, err = replaceDesc(out, itemDescMarker(ep.BookmarkID), ep.Description)
		if err != nil {
			return nil, fmt.Errorf("feed: description for bookmark %d: %w", ep.BookmarkID, err)
		}
	}

	// The podcast library does not emit isPermaLink on item GUIDs, and it
	// emits no channel-level <guid>, so a global replace is safe.
	out = strings.ReplaceAll(out, "<guid>", `<guid isPermaLink="false">`)

	return []byte(out), nil
}

// channelDescMarker is the placeholder for the channel description.
const channelDescMarker = "PAPERCAST-CHANNEL-DESC-MARKER"

// itemDescMarker returns a collision-proof placeholder for an episode's
// description.
func itemDescMarker(bookmarkID int64) string {
	return fmt.Sprintf("PAPERCAST-DESC-%d-MARKER", bookmarkID)
}

// replaceDesc swaps the <description> element holding marker for one holding
// desc, CDATA-wrapped.
func replaceDesc(xml, marker, desc string) (string, error) {
	needle := "<description>" + marker + "</description>"
	if !strings.Contains(xml, needle) {
		return "", errors.New("marker not found in encoded XML")
	}
	replacement := "<description><![CDATA[" + cdataEscape(desc) + "]]></description>"
	return strings.Replace(xml, needle, replacement, 1), nil
}

// cdataEscape makes s safe for embedding in a CDATA section by splitting
// any "]]>" across two sections.
func cdataEscape(s string) string {
	return strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
}
