package textproc

import (
	"reflect"
	"testing"
)

func TestExtractBlocks_Basic(t *testing.T) {
	src := `<html><body>
		<h1>Title Here</h1>
		<p>First paragraph.</p>
		<ul><li>Item one</li><li>Item two</li></ul>
		<blockquote><p>Quoted paragraph.</p><p>Second quoted.</p></blockquote>
		<blockquote>Bare quote text</blockquote>
		<p>Last paragraph.</p>
	</body></html>`
	got, err := ExtractBlocks(src)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{
		{KindHeading, "Title Here"},
		{KindParagraph, "First paragraph."},
		{KindListItem, "Item one"},
		{KindListItem, "Item two"},
		{KindBlockquote, "Quoted paragraph."},
		{KindBlockquote, "Second quoted."},
		{KindBlockquote, "Bare quote text"},
		{KindParagraph, "Last paragraph."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestExtractBlocks_AllHeadingLevels(t *testing.T) {
	got, err := ExtractBlocks("<h1>A</h1><h2>B</h2><h3>C</h3><h4>D</h4><h5>E</h5><h6>F</h6>")
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d blocks, want 6", len(got))
	}
	for i, b := range got {
		if b.Kind != KindHeading {
			t.Errorf("block %d kind = %d, want KindHeading", i, b.Kind)
		}
	}
}

func TestExtractBlocks_NestedDivs(t *testing.T) {
	src := `<div><div><section><p>Deeply nested.</p></section></div></div>`
	got, err := ExtractBlocks(src)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{{KindParagraph, "Deeply nested."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractBlocks_LooseTextInDiv(t *testing.T) {
	src := `<div>Loose text here<p>A real paragraph.</p>trailing text</div>`
	got, err := ExtractBlocks(src)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{
		{KindParagraph, "Loose text here"},
		{KindParagraph, "A real paragraph."},
		{KindParagraph, "trailing text"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestExtractBlocks_WhitespaceCollapsed(t *testing.T) {
	src := "<p>  lots \n\t of   <em> spaced </em>  text  </p>"
	got, err := ExtractBlocks(src)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{{KindParagraph, "lots of spaced text"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractBlocks_SkipsScriptStyleNoscriptIframe(t *testing.T) {
	src := `<head><title>Ignored</title><style>p{color:red}</style></head>
		<body><p>Kept.</p><script>var x = "ignored";</script>
		<noscript>ignored</noscript><iframe>ignored</iframe></body>`
	got, err := ExtractBlocks(src)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{{KindParagraph, "Kept."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractBlocks_BrIsSpace(t *testing.T) {
	got, err := ExtractBlocks("<p>line one<br>line two</p><div>loose one<br/>loose two</div>")
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{
		{KindParagraph, "line one line two"},
		{KindParagraph, "loose one loose two"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractBlocks_EntitiesDecoded(t *testing.T) {
	got, err := ExtractBlocks("<p>AT&amp;T &lt;3 &quot;SSML&quot;</p>")
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{{KindParagraph, `AT&T <3 "SSML"`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractBlocks_EmptyBlocksSkipped(t *testing.T) {
	got, err := ExtractBlocks("<p>   </p><p></p><h2>\n\t</h2><p>Real.</p><li> </li>")
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{{KindParagraph, "Real."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractBlocks_ListItemIncludesNestedP(t *testing.T) {
	src := `<ol><li><p>First part.</p><p>Second part.</p></li><li>Plain item</li></ol>`
	got, err := ExtractBlocks(src)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{
		{KindListItem, "First part. Second part."},
		{KindListItem, "Plain item"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestExtractBlocks_BlockquoteWithHeadingAndListItems(t *testing.T) {
	src := `<blockquote><h3>Quote heading</h3><ul><li>Quoted item</li></ul></blockquote>`
	got, err := ExtractBlocks(src)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{
		{KindBlockquote, "Quote heading"},
		{KindBlockquote, "Quoted item"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestExtractBlocks_FigureImageSkipped(t *testing.T) {
	src := `<p>Before.</p><figure><img src="x.jpg" alt="alt text ignored"></figure><p>After.</p>`
	got, err := ExtractBlocks(src)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{
		{KindParagraph, "Before."},
		{KindParagraph, "After."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractBlocks_InlineMarkupKeptInline(t *testing.T) {
	src := `<p>Read <a href="/x">the <strong>full</strong> story</a> now.</p>`
	got, err := ExtractBlocks(src)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	want := []Block{{KindParagraph, "Read the full story now."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestWordCount(t *testing.T) {
	blocks := []Block{
		{KindHeading, "A Title"},
		{KindParagraph, "one two three"},
		{KindListItem, "four"},
	}
	if got := WordCount(blocks); got != 6 {
		t.Errorf("WordCount = %d, want 6", got)
	}
	if got := WordCount(nil); got != 0 {
		t.Errorf("WordCount(nil) = %d, want 0", got)
	}
}
