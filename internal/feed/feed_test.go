package feed

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
)

var (
	testNow  = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	testPub1 = time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	testPub2 = time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC)
)

func testMeta() Meta {
	return Meta{
		Title:       "My Papercast",
		Description: "Unread articles, read aloud.",
		Language:    "en-us",
		Author:      "Chris",
		CoverArtURL: "https://ex.com/cover.jpg",
		BaseURL:     "https://ex.com/casts",
		Generator:   "papercast 1.2.3",
	}
}

func testEpisodes() []Episode {
	return []Episode{
		{
			BookmarkID:  12345,
			Title:       "First Article",
			Link:        "https://example.org/first",
			Description: "First summary.",
			MP3Filename: "ep-12345.mp3",
			SizeBytes:   1234567,
			Duration:    time.Hour + 2*time.Minute + 3*time.Second,
			PubDate:     testPub1,
		},
		{
			BookmarkID:  67890,
			Title:       "Second Article",
			Link:        "https://example.org/second",
			Description: "Second summary.",
			MP3Filename: "ep-67890.mp3",
			SizeBytes:   7654321,
			Duration:    12*time.Minute + 34*time.Second,
			PubDate:     testPub2,
		},
	}
}

// parsed XML shapes; tags without a namespace match any namespace, so
// "author" and "duration" match the itunes-prefixed elements.
type parsedGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type parsedEnclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type parsedItem struct {
	GUID        parsedGUID      `xml:"guid"`
	Title       string          `xml:"title"`
	Link        string          `xml:"link"`
	Description string          `xml:"description"`
	PubDate     string          `xml:"pubDate"`
	Enclosure   parsedEnclosure `xml:"enclosure"`
	Duration    string          `xml:"duration"`
}

type parsedChannel struct {
	Title         string       `xml:"title"`
	Link          string       `xml:"link"`
	Language      string       `xml:"language"`
	Author        string       `xml:"author"`
	Generator     string       `xml:"generator"`
	PubDate       string       `xml:"pubDate"`
	LastBuildDate string       `xml:"lastBuildDate"`
	Items         []parsedItem `xml:"item"`
}

type parsedRSS struct {
	XMLName xml.Name      `xml:"rss"`
	Channel parsedChannel `xml:"channel"`
}

func renderAndParse(t *testing.T, meta Meta, episodes []Episode) (string, parsedRSS) {
	t.Helper()
	out, err := Render(meta, episodes, testNow)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	requireWellFormed(t, out)
	var parsed parsedRSS
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal feed: %v\n%s", err, out)
	}
	return string(out), parsed
}

// requireWellFormed walks every token to verify the document parses cleanly.
func requireWellFormed(t *testing.T, out []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(out))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("feed is not well-formed XML: %v\n%s", err, out)
		}
	}
}

func TestRenderChannelMetadata(t *testing.T) {
	raw, parsed := renderAndParse(t, testMeta(), testEpisodes())

	ch := parsed.Channel
	if ch.Title != "My Papercast" {
		t.Errorf("channel title = %q", ch.Title)
	}
	if ch.Link != "https://ex.com/casts" {
		t.Errorf("channel link = %q", ch.Link)
	}
	if ch.Language != "en-us" {
		t.Errorf("channel language = %q", ch.Language)
	}
	if ch.Generator != "papercast 1.2.3" {
		t.Errorf("channel generator = %q", ch.Generator)
	}
	if !strings.Contains(raw, "<itunes:author>Chris</itunes:author>") {
		t.Error("missing channel itunes:author")
	}
	wantNow := testNow.Format(time.RFC1123Z)
	if ch.PubDate != wantNow {
		t.Errorf("channel pubDate = %q, want %q", ch.PubDate, wantNow)
	}
	if ch.LastBuildDate != wantNow {
		t.Errorf("channel lastBuildDate = %q, want %q", ch.LastBuildDate, wantNow)
	}
	if !strings.Contains(raw, `<itunes:image href="https://ex.com/cover.jpg">`) {
		t.Error("missing itunes:image for cover art")
	}
	if !strings.Contains(raw, "<url>https://ex.com/cover.jpg</url>") {
		t.Error("missing RSS image URL for cover art")
	}
}

func TestRenderNoCoverArt(t *testing.T) {
	meta := testMeta()
	meta.CoverArtURL = ""
	raw, _ := renderAndParse(t, meta, testEpisodes())
	if strings.Contains(raw, "itunes:image") {
		t.Error("itunes:image present despite empty CoverArtURL")
	}
	if strings.Contains(raw, "<image>") {
		t.Error("RSS image present despite empty CoverArtURL")
	}
}

func TestRenderEpisodes(t *testing.T) {
	raw, parsed := renderAndParse(t, testMeta(), testEpisodes())

	items := parsed.Channel.Items
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	first := items[0]
	if first.GUID.Value != "12345" || first.GUID.IsPermaLink != "false" {
		t.Errorf("guid = %+v, want value 12345 isPermaLink false", first.GUID)
	}
	if !strings.Contains(raw, `<guid isPermaLink="false">12345</guid>`) {
		t.Error("missing verbatim guid element for first episode")
	}
	if first.Title != "First Article" {
		t.Errorf("item title = %q", first.Title)
	}
	if first.Link != "https://example.org/first" {
		t.Errorf("item link = %q", first.Link)
	}
	if !strings.Contains(raw, "<description><![CDATA[First summary.]]></description>") {
		t.Error("missing verbatim CDATA description for first episode")
	}
	if first.Description != "First summary." {
		t.Errorf("parsed description = %q", first.Description)
	}
	if first.Enclosure.URL != "https://ex.com/casts/ep-12345.mp3" {
		t.Errorf("enclosure url = %q", first.Enclosure.URL)
	}
	if first.Enclosure.Length != "1234567" {
		t.Errorf("enclosure length = %q", first.Enclosure.Length)
	}
	if first.Enclosure.Type != "audio/mpeg" {
		t.Errorf("enclosure type = %q", first.Enclosure.Type)
	}
	if !strings.Contains(raw, "<itunes:duration>1:02:03</itunes:duration>") {
		t.Error("missing itunes:duration for first episode")
	}
	wantPub := testPub1.Format(time.RFC1123Z)
	if first.PubDate != wantPub {
		t.Errorf("item pubDate = %q, want %q", first.PubDate, wantPub)
	}
	if first.PubDate == testNow.Format(time.RFC1123Z) {
		t.Error("item pubDate uses now, want the bookmark's saved date")
	}

	second := items[1]
	if second.GUID.Value != "67890" || second.GUID.IsPermaLink != "false" {
		t.Errorf("guid = %+v, want value 67890 isPermaLink false", second.GUID)
	}
	if !strings.Contains(raw, "<itunes:duration>12:34</itunes:duration>") {
		t.Error("missing itunes:duration for second episode")
	}
	if second.PubDate != testPub2.Format(time.RFC1123Z) {
		t.Errorf("second item pubDate = %q", second.PubDate)
	}
}

func TestRenderDescriptionContainingCDATAEnd(t *testing.T) {
	episodes := testEpisodes()[:1]
	episodes[0].Description = "before ]]> after"
	raw, parsed := renderAndParse(t, testMeta(), episodes)

	if parsed.Channel.Items[0].Description != "before ]]> after" {
		t.Errorf("description did not round-trip: %q", parsed.Channel.Items[0].Description)
	}
	if !strings.Contains(raw, "]]]]><![CDATA[>") {
		t.Error("missing split CDATA sections for ]]> in description")
	}
}

func TestRenderEmptyDescription(t *testing.T) {
	episodes := testEpisodes()[:1]
	episodes[0].Description = ""
	raw, parsed := renderAndParse(t, testMeta(), episodes)

	if !strings.Contains(raw, "<description><![CDATA[]]></description>") {
		t.Error("missing empty CDATA description")
	}
	if parsed.Channel.Items[0].Description != "" {
		t.Errorf("parsed description = %q, want empty", parsed.Channel.Items[0].Description)
	}
}

func TestRenderTitleSpecialChars(t *testing.T) {
	episodes := testEpisodes()[:1]
	episodes[0].Title = "Cats & Dogs <3"
	_, parsed := renderAndParse(t, testMeta(), episodes)

	if got := parsed.Channel.Items[0].Title; got != "Cats & Dogs <3" {
		t.Errorf("title did not round-trip: %q", got)
	}
}

func TestRenderEnclosureURLJoining(t *testing.T) {
	episodes := testEpisodes()[:1]
	episodes[0].MP3Filename = "my episode.mp3"

	var urls []string
	for _, base := range []string{"https://ex.com/casts", "https://ex.com/casts/"} {
		meta := testMeta()
		meta.BaseURL = base
		_, parsed := renderAndParse(t, meta, episodes)
		urls = append(urls, parsed.Channel.Items[0].Enclosure.URL)
	}

	want := "https://ex.com/casts/my%20episode.mp3"
	if urls[0] != want {
		t.Errorf("enclosure url = %q, want %q", urls[0], want)
	}
	if urls[0] != urls[1] {
		t.Errorf("enclosure urls differ by trailing slash: %q vs %q", urls[0], urls[1])
	}
}

func TestRenderInvalidBaseURL(t *testing.T) {
	meta := testMeta()
	meta.BaseURL = "://not a url"
	if _, err := Render(meta, testEpisodes(), testNow); err == nil {
		t.Fatal("Render returned nil error for invalid base URL")
	}
}
