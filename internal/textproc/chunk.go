package textproc

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/neurosnap/sentences"
	"github.com/neurosnap/sentences/english"
)

// RenderOptions controls how blocks are rendered into TTS chunks.
type RenderOptions struct {
	SSML          bool
	MaxChunkBytes int    // max size in bytes of each rendered chunk payload
	Intro         string // spoken intro text; "" disables the intro
}

// Chunk is one TTS request payload.
type Chunk struct {
	Payload string // exactly what will be sent to the TTS API
	SSML    bool
}

// para is one renderable paragraph: the intro or a block's text.
type para struct {
	text  string
	intro bool
}

var sentenceTokenizer = sync.OnceValues(func() (*sentences.DefaultSentenceTokenizer, error) {
	return english.NewSentenceTokenizer(nil)
})

var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

// BuildChunks renders blocks into TTS request payloads no larger than
// MaxChunkBytes each, measured over the rendered payload (including SSML
// markup and escaping when SSML is on). Packing is greedy on paragraph
// boundaries; oversized paragraphs fall back to sentence, then word, splits.
func BuildChunks(blocks []Block, opts RenderOptions) ([]Chunk, error) {
	if opts.MaxChunkBytes <= 0 {
		return nil, fmt.Errorf("MaxChunkBytes must be positive, got %d", opts.MaxChunkBytes)
	}

	paras := buildParas(blocks, opts.Intro)

	var chunks []Chunk
	var cur []para
	fits := func(ps []para) bool {
		return len(renderChunk(ps, opts.SSML)) <= opts.MaxChunkBytes
	}
	flush := func() {
		if len(cur) > 0 {
			chunks = append(chunks, Chunk{Payload: renderChunk(cur, opts.SSML), SSML: opts.SSML})
			cur = nil
		}
	}
	withCur := func(p para) []para {
		return append(cur[:len(cur):len(cur)], p)
	}

	for _, p := range paras {
		if cand := withCur(p); fits(cand) {
			cur = cand
			continue
		}
		flush()
		if fits([]para{p}) {
			cur = []para{p}
			continue
		}
		// Fallback: the paragraph alone exceeds the limit. Split it into
		// sentences (then words if needed) and pack those greedily; a chunk
		// may end with a partial paragraph, continued in the next chunk.
		units, err := splitOversized(p, opts)
		if err != nil {
			return nil, err
		}
		acc := ""
		for _, u := range units {
			joined := u
			if acc != "" {
				joined = acc + " " + u
			}
			if cand := withCur(para{text: joined, intro: p.intro}); fits(cand) {
				acc = joined
				continue
			}
			if acc != "" {
				cur = append(cur, para{text: acc, intro: p.intro})
			}
			flush()
			acc = u // each unit fits alone, by construction
		}
		if acc != "" {
			cur = append(cur, para{text: acc, intro: p.intro})
		}
	}
	flush()
	return chunks, nil
}

// buildParas converts the intro and blocks into the paragraph sequence to
// render. Heading and list-item text gets terminal punctuation for natural
// speech.
func buildParas(blocks []Block, intro string) []para {
	var paras []para
	if t := collapse(intro); t != "" {
		paras = append(paras, para{text: t, intro: true})
	}
	for _, b := range blocks {
		t := b.Text
		if b.Kind == KindHeading || b.Kind == KindListItem {
			t = ensureTerminalPunctuation(t)
		}
		if t != "" {
			paras = append(paras, para{text: t})
		}
	}
	return paras
}

// renderChunk renders paragraphs into a single chunk payload. In SSML mode a
// break follows the intro paragraph when body text shares the chunk.
func renderChunk(ps []para, ssml bool) string {
	if !ssml {
		texts := make([]string, len(ps))
		for i, p := range ps {
			texts[i] = p.text
		}
		return strings.Join(texts, "\n\n")
	}
	var b strings.Builder
	b.WriteString("<speak>")
	for i, p := range ps {
		b.WriteString("<p>")
		b.WriteString(xmlEscaper.Replace(p.text))
		b.WriteString("</p>")
		if p.intro && i < len(ps)-1 {
			b.WriteString(`<break time="750ms"/>`)
		}
	}
	b.WriteString("</speak>")
	return b.String()
}

// splitOversized splits an over-limit paragraph into units that each fit in a
// chunk by themselves: sentences where possible, single words as a last
// resort.
func splitOversized(p para, opts RenderOptions) ([]string, error) {
	tok, err := sentenceTokenizer()
	if err != nil {
		return nil, fmt.Errorf("build sentence tokenizer: %w", err)
	}
	fitsAlone := func(text string) bool {
		rendered := renderChunk([]para{{text: text, intro: p.intro}}, opts.SSML)
		return len(rendered) <= opts.MaxChunkBytes
	}
	var units []string
	for _, s := range tok.Tokenize(p.text) {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		if fitsAlone(text) {
			units = append(units, text)
			continue
		}
		for _, w := range strings.Fields(text) {
			if !fitsAlone(w) {
				return nil, fmt.Errorf("MaxChunkBytes %d is too small to fit the single word %q once rendered", opts.MaxChunkBytes, w)
			}
			units = append(units, w)
		}
	}
	return units, nil
}

// terminalPunctuation are rune values that already end a spoken phrase.
const terminalPunctuation = ".!?:;…—"

// closingMarks are trailing quote/bracket runes to look past when checking
// for terminal punctuation.
const closingMarks = "\"')]»”’"

// ensureTerminalPunctuation appends "." unless the text already ends with
// terminal punctuation (possibly followed by closing quotes or brackets).
func ensureTerminalPunctuation(s string) string {
	t := strings.TrimRight(s, closingMarks)
	if t == "" {
		return s
	}
	r, _ := utf8.DecodeLastRuneInString(t)
	if strings.ContainsRune(terminalPunctuation, r) {
		return s
	}
	return s + "."
}
