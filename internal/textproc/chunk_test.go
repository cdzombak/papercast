package textproc

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// parseSSML verifies payload is well-formed XML and returns its concatenated
// character data.
func parseSSML(t *testing.T, payload string) string {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(payload))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("payload is not well-formed XML: %v\npayload: %s", err, payload)
		}
		if cd, ok := tok.(xml.CharData); ok {
			b.WriteString(string(cd))
			b.WriteString(" ")
		}
	}
	return collapse(b.String())
}

// spokenText extracts the normalized spoken text from a chunk.
func spokenText(t *testing.T, c Chunk) string {
	t.Helper()
	if c.SSML {
		return parseSSML(t, c.Payload)
	}
	return collapse(c.Payload)
}

// allSpokenText concatenates the normalized spoken text of all chunks.
func allSpokenText(t *testing.T, chunks []Chunk) string {
	t.Helper()
	parts := make([]string, len(chunks))
	for i, c := range chunks {
		parts[i] = spokenText(t, c)
	}
	return collapse(strings.Join(parts, " "))
}

// wantSpokenText is the expected full spoken text for blocks+opts, built the
// same way BuildChunks builds its paragraphs.
func wantSpokenText(blocks []Block, opts RenderOptions) string {
	paras := buildParas(blocks, opts.Intro)
	texts := make([]string, len(paras))
	for i, p := range paras {
		texts[i] = p.text
	}
	return collapse(strings.Join(texts, " "))
}

func checkSizes(t *testing.T, chunks []Chunk, maxBytes int) {
	t.Helper()
	for i, c := range chunks {
		if len(c.Payload) > maxBytes {
			t.Errorf("chunk %d payload is %d bytes, exceeds limit %d", i, len(c.Payload), maxBytes)
		}
		if c.Payload == "" {
			t.Errorf("chunk %d payload is empty", i)
		}
	}
}

func TestBuildChunks_PlainSingleChunk(t *testing.T) {
	blocks := []Block{
		{KindParagraph, "First paragraph."},
		{KindParagraph, "Second paragraph."},
	}
	opts := RenderOptions{MaxChunkBytes: 4500, Intro: "My Article, from example.com."}
	chunks, err := BuildChunks(blocks, opts)
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	want := []Chunk{{Payload: "My Article, from example.com.\n\nFirst paragraph.\n\nSecond paragraph."}}
	if !reflect.DeepEqual(chunks, want) {
		t.Errorf("got %+v\nwant %+v", chunks, want)
	}
}

func TestBuildChunks_PlainNoIntro(t *testing.T) {
	blocks := []Block{{KindParagraph, "Only paragraph."}}
	chunks, err := BuildChunks(blocks, RenderOptions{MaxChunkBytes: 4500})
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	want := []Chunk{{Payload: "Only paragraph."}}
	if !reflect.DeepEqual(chunks, want) {
		t.Errorf("got %+v, want %+v", chunks, want)
	}
}

func TestBuildChunks_HeadingAndListItemPunctuation(t *testing.T) {
	blocks := []Block{
		{KindHeading, "Introduction"},
		{KindHeading, "Why bother?"},
		{KindListItem, "first item"},
		{KindParagraph, "No dot added here"},
	}
	chunks, err := BuildChunks(blocks, RenderOptions{MaxChunkBytes: 4500})
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	want := "Introduction.\n\nWhy bother?\n\nfirst item.\n\nNo dot added here"
	if len(chunks) != 1 || chunks[0].Payload != want {
		t.Errorf("got %+v, want single chunk %q", chunks, want)
	}
}

func TestBuildChunks_SSMLWellFormed(t *testing.T) {
	blocks := []Block{
		{KindParagraph, "Body paragraph one."},
		{KindParagraph, "Body paragraph two."},
	}
	opts := RenderOptions{SSML: true, MaxChunkBytes: 4500, Intro: `"AT&T <3 SSML" from example.com.`}
	chunks, err := BuildChunks(blocks, opts)
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	c := chunks[0]
	if !c.SSML {
		t.Error("chunk not marked SSML")
	}
	p := c.Payload
	if !strings.HasPrefix(p, "<speak><p>") || !strings.HasSuffix(p, "</p></speak>") {
		t.Errorf("payload not wrapped in <speak><p>: %s", p)
	}
	if !strings.Contains(p, "AT&amp;T &lt;3 SSML") {
		t.Errorf("payload not escaped: %s", p)
	}
	if !strings.Contains(p, `</p><break time="750ms"/><p>`) {
		t.Errorf("missing break after intro: %s", p)
	}
	if got, want := parseSSML(t, c.Payload), wantSpokenText(blocks, opts); got != want {
		t.Errorf("spoken text = %q, want %q", got, want)
	}
}

func TestBuildChunks_SSMLNoIntroNoBreak(t *testing.T) {
	blocks := []Block{{KindParagraph, "Just a body."}}
	chunks, err := BuildChunks(blocks, RenderOptions{SSML: true, MaxChunkBytes: 4500})
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	want := []Chunk{{Payload: "<speak><p>Just a body.</p></speak>", SSML: true}}
	if !reflect.DeepEqual(chunks, want) {
		t.Errorf("got %+v, want %+v", chunks, want)
	}
}

func TestBuildChunks_PackingMultipleChunks(t *testing.T) {
	var blocks []Block
	for i := 0; i < 20; i++ {
		blocks = append(blocks, Block{KindParagraph, fmt.Sprintf("Paragraph number %d has a modest amount of text in it.", i)})
	}
	for _, ssml := range []bool{false, true} {
		opts := RenderOptions{SSML: ssml, MaxChunkBytes: 200, Intro: "An Article, from example.com."}
		chunks, err := BuildChunks(blocks, opts)
		if err != nil {
			t.Fatalf("ssml=%v: BuildChunks: %v", ssml, err)
		}
		if len(chunks) < 2 {
			t.Errorf("ssml=%v: got %d chunks, want several", ssml, len(chunks))
		}
		checkSizes(t, chunks, opts.MaxChunkBytes)
		if got, want := allSpokenText(t, chunks), wantSpokenText(blocks, opts); got != want {
			t.Errorf("ssml=%v: spoken text mismatch\ngot  %q\nwant %q", ssml, got, want)
		}
	}
}

func TestBuildChunks_OversizedParagraphSentenceSplit(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&sb, "Sentence number %d is here to add some length to the paragraph. ", i)
	}
	blocks := []Block{{KindParagraph, strings.TrimSpace(sb.String())}}
	for _, ssml := range []bool{false, true} {
		opts := RenderOptions{SSML: ssml, MaxChunkBytes: 200}
		chunks, err := BuildChunks(blocks, opts)
		if err != nil {
			t.Fatalf("ssml=%v: BuildChunks: %v", ssml, err)
		}
		if len(chunks) < 2 {
			t.Errorf("ssml=%v: got %d chunks, want the paragraph split across several", ssml, len(chunks))
		}
		checkSizes(t, chunks, opts.MaxChunkBytes)
		if got, want := allSpokenText(t, chunks), wantSpokenText(blocks, opts); got != want {
			t.Errorf("ssml=%v: spoken text mismatch\ngot  %q\nwant %q", ssml, got, want)
		}
		// Sentence boundaries respected: each chunk ends at a sentence end.
		for i, c := range chunks {
			if !strings.HasSuffix(spokenText(t, c), ".") {
				t.Errorf("ssml=%v: chunk %d does not end on a sentence boundary: %q", ssml, i, spokenText(t, c))
			}
		}
	}
}

func TestBuildChunks_OversizedSentenceWordSplit(t *testing.T) {
	words := make([]string, 60)
	for i := range words {
		words[i] = fmt.Sprintf("word%02d", i)
	}
	blocks := []Block{{KindParagraph, strings.Join(words, " ")}} // one long "sentence"
	for _, ssml := range []bool{false, true} {
		opts := RenderOptions{SSML: ssml, MaxChunkBytes: 120}
		chunks, err := BuildChunks(blocks, opts)
		if err != nil {
			t.Fatalf("ssml=%v: BuildChunks: %v", ssml, err)
		}
		if len(chunks) < 2 {
			t.Errorf("ssml=%v: got %d chunks, want the sentence split across several", ssml, len(chunks))
		}
		checkSizes(t, chunks, opts.MaxChunkBytes)
		if got, want := allSpokenText(t, chunks), wantSpokenText(blocks, opts); got != want {
			t.Errorf("ssml=%v: spoken text mismatch\ngot  %q\nwant %q", ssml, got, want)
		}
	}
}

func TestBuildChunks_SSMLBudgetMeasuresRenderedPayload(t *testing.T) {
	// 30 ampersands render as 30 x "&amp;" = 150 bytes plus markup, though the
	// source text is only 59 bytes. With a 100-byte limit the paragraph must
	// split; measuring source length would (wrongly) fit it in one chunk.
	text := strings.Repeat("& ", 29) + "&"
	blocks := []Block{{KindParagraph, text}}
	opts := RenderOptions{SSML: true, MaxChunkBytes: 100}
	if len(text) >= opts.MaxChunkBytes {
		t.Fatal("test setup: source text must be under the limit")
	}
	chunks, err := BuildChunks(blocks, opts)
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	if len(chunks) < 2 {
		t.Errorf("got %d chunks, want >= 2 (rendered size must drive splitting)", len(chunks))
	}
	checkSizes(t, chunks, opts.MaxChunkBytes)
	if got, want := allSpokenText(t, chunks), wantSpokenText(blocks, opts); got != want {
		t.Errorf("spoken text mismatch\ngot  %q\nwant %q", got, want)
	}
}

func TestBuildChunks_LimitTooSmall(t *testing.T) {
	blocks := []Block{{KindParagraph, "supercalifragilisticexpialidocious"}}
	_, err := BuildChunks(blocks, RenderOptions{SSML: true, MaxChunkBytes: 40})
	if err == nil {
		t.Fatal("want error for limit too small to fit a single word, got nil")
	}
	if _, err := BuildChunks(blocks, RenderOptions{MaxChunkBytes: 0}); err == nil {
		t.Fatal("want error for MaxChunkBytes 0, got nil")
	}
}

func TestBuildChunks_EmptyInput(t *testing.T) {
	chunks, err := BuildChunks(nil, RenderOptions{MaxChunkBytes: 4500})
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("got %d chunks for empty input, want 0", len(chunks))
	}
}

func TestBuildChunks_IntroOnly(t *testing.T) {
	chunks, err := BuildChunks(nil, RenderOptions{SSML: true, MaxChunkBytes: 4500, Intro: "Only an intro."})
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	want := []Chunk{{Payload: "<speak><p>Only an intro.</p></speak>", SSML: true}}
	if !reflect.DeepEqual(chunks, want) {
		t.Errorf("got %+v, want %+v (no break when nothing follows the intro)", chunks, want)
	}
}

func TestBuildChunks_Deterministic(t *testing.T) {
	var blocks []Block
	for i := 0; i < 15; i++ {
		blocks = append(blocks, Block{KindParagraph, fmt.Sprintf("Deterministic paragraph %d with some filler text to pack.", i)})
	}
	opts := RenderOptions{SSML: true, MaxChunkBytes: 250, Intro: "Repeatable, from example.com."}
	a, err := BuildChunks(blocks, opts)
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	b, err := BuildChunks(blocks, opts)
	if err != nil {
		t.Fatalf("BuildChunks: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Error("same input produced different chunks")
	}
}

func TestEnsureTerminalPunctuation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Introduction", "Introduction."},
		{"Done.", "Done."},
		{"Really?", "Really?"},
		{"Wow!", "Wow!"},
		{"Note:", "Note:"},
		{"Wait…", "Wait…"},
		{`He said "stop!"`, `He said "stop!"`},
		{`A "quote"`, `A "quote".`},
	}
	for _, c := range cases {
		if got := ensureTerminalPunctuation(c.in); got != c.want {
			t.Errorf("ensureTerminalPunctuation(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
