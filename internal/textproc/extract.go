// Package textproc extracts structured text blocks from article HTML and
// renders them into size-limited TTS request payloads.
package textproc

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// BlockKind identifies the structural role of an extracted block.
type BlockKind int

const (
	KindParagraph BlockKind = iota
	KindHeading
	KindListItem
	KindBlockquote
)

// Block is one unit of article text with paragraph-level structure preserved.
type Block struct {
	Kind BlockKind
	Text string // trimmed inline text, internal whitespace collapsed to single spaces
}

// skipTags are elements whose content is never spoken.
var skipTags = map[string]bool{
	"script":   true,
	"style":    true,
	"head":     true,
	"noscript": true,
	"iframe":   true,
}

// inlineTags are elements whose text flows within the enclosing block rather
// than starting a new one.
var inlineTags = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "bdo": true,
	"cite": true, "code": true, "data": true, "del": true, "dfn": true,
	"em": true, "i": true, "ins": true, "kbd": true, "mark": true,
	"q": true, "s": true, "samp": true, "small": true, "span": true,
	"strong": true, "sub": true, "sup": true, "time": true, "u": true,
	"var": true, "wbr": true,
}

// ExtractBlocks parses HTML (as returned by Instapaper bookmarks/get_text)
// into an ordered list of blocks. Paragraph boundaries are preserved.
func ExtractBlocks(htmlSrc string) ([]Block, error) {
	doc, err := html.Parse(strings.NewReader(htmlSrc))
	if err != nil {
		return nil, fmt.Errorf("parse article HTML: %w", err)
	}
	var blocks []Block
	emit := func(kind BlockKind, text string) {
		text = collapse(text)
		if text != "" {
			blocks = append(blocks, Block{Kind: kind, Text: text})
		}
	}
	walkContainer(doc, false, emit)
	return blocks, nil
}

// WordCount counts whitespace-separated words across all block text.
func WordCount(blocks []Block) int {
	n := 0
	for _, b := range blocks {
		n += len(strings.Fields(b.Text))
	}
	return n
}

// walkContainer walks the children of a container node, emitting blocks.
// Loose inline content between block elements is emitted as a paragraph.
// Inside a blockquote, every emitted block gets KindBlockquote.
func walkContainer(n *html.Node, inBlockquote bool, emit func(BlockKind, string)) {
	kindOf := func(k BlockKind) BlockKind {
		if inBlockquote {
			return KindBlockquote
		}
		return k
	}
	var pending strings.Builder
	flush := func() {
		emit(kindOf(KindParagraph), pending.String())
		pending.Reset()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			pending.WriteString(c.Data)
		case html.ElementNode:
			switch {
			case skipTags[c.Data]:
				// ignore entirely
			case c.Data == "br":
				pending.WriteString(" ")
			case inlineTags[c.Data]:
				writeInlineText(c, &pending)
			case c.Data == "p":
				flush()
				emit(kindOf(KindParagraph), inlineText(c))
			case isHeading(c.Data):
				flush()
				emit(kindOf(KindHeading), inlineText(c))
			case c.Data == "li":
				flush()
				emit(kindOf(KindListItem), inlineText(c))
			case c.Data == "blockquote":
				flush()
				if hasBlockDescendant(c) {
					walkContainer(c, true, emit)
				} else {
					emit(KindBlockquote, inlineText(c))
				}
			default:
				// container: div, article, section, ul, ol, table, figure, etc.
				flush()
				walkContainer(c, inBlockquote, emit)
			}
		}
	}
	flush()
}

func isHeading(tag string) bool {
	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return true
	}
	return false
}

// inlineText returns the full flattened text of a node's subtree.
func inlineText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writeInlineText(c, &b)
	}
	return b.String()
}

// writeInlineText appends the flattened text of n's subtree to b.
// <br> becomes a space; block-level descendants are padded with spaces so
// their text does not run together; skipped elements contribute nothing.
func writeInlineText(n *html.Node, b *strings.Builder) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(n.Data)
	case html.ElementNode:
		if skipTags[n.Data] {
			return
		}
		if n.Data == "br" {
			b.WriteString(" ")
			return
		}
		block := !inlineTags[n.Data]
		if block {
			b.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeInlineText(c, b)
		}
		if block {
			b.WriteString(" ")
		}
	}
}

// hasBlockDescendant reports whether n contains a p, heading, or li element.
func hasBlockDescendant(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			if c.Data == "p" || c.Data == "li" || isHeading(c.Data) {
				return true
			}
			if !skipTags[c.Data] && hasBlockDescendant(c) {
				return true
			}
		}
	}
	return false
}

// collapse trims s and collapses internal whitespace runs to single spaces.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
