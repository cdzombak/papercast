package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func chatCompletionJSON(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": content}},
		},
	})
	return string(b)
}

// decodeRequest decodes a chat completions request body.
func decodeRequest(t *testing.T, r *http.Request) chatRequest {
	t.Helper()
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return req
}

func TestDescribeSuccess(t *testing.T) {
	blocks := []string{"First paragraph.", "Second paragraph."}
	wantPrompt := fmt.Sprintf(
		"Summarize the following article in 1 paragraph. Do not include any introductory text, just the summary:\n\n<article>\n%s\n</article>",
		"First paragraph.\n\nSecond paragraph.")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		req := decodeRequest(t, r)
		if req.Model != "test-model" {
			t.Errorf("model = %q, want test-model", req.Model)
		}
		if len(req.Messages) != 1 {
			t.Fatalf("got %d messages, want 1", len(req.Messages))
		}
		if req.Messages[0].Role != "user" {
			t.Errorf("role = %q, want user", req.Messages[0].Role)
		}
		if req.Messages[0].Content != wantPrompt {
			t.Errorf("prompt = %q, want %q", req.Messages[0].Content, wantPrompt)
		}
		_, _ = fmt.Fprint(w, chatCompletionJSON("  A tidy summary. \n"))
	}))
	defer ts.Close()

	d := NewDescriber(ts.URL, "test-key", "test-model", time.Minute, 0)
	got, err := d.Describe(context.Background(), blocks)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got != "A tidy summary." {
		t.Errorf("summary = %q, want %q", got, "A tidy summary.")
	}
}

func TestDescribeNoAuthHeaderWhenAPIKeyEmpty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Errorf("Authorization header present: %q", r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, chatCompletionJSON("summary"))
	}))
	defer ts.Close()

	d := NewDescriber(ts.URL, "", "test-model", time.Minute, 0)
	if _, err := d.Describe(context.Background(), []string{"text"}); err != nil {
		t.Fatalf("Describe: %v", err)
	}
}

func TestDescribeTruncatesAtBlockBoundary(t *testing.T) {
	var gotPrompt string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrompt = decodeRequest(t, r).Messages[0].Content
		_, _ = fmt.Fprint(w, chatCompletionJSON("summary"))
	}))
	defer ts.Close()

	// "aaaa\n\nbbbb" is exactly 10 runes; the third block does not fit.
	d := NewDescriber(ts.URL, "", "test-model", time.Minute, 10)
	if _, err := d.Describe(context.Background(), []string{"aaaa", "bbbb", "cccc"}); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !strings.Contains(gotPrompt, "<article>\naaaa\n\nbbbb\n</article>") {
		t.Errorf("prompt does not contain truncated article: %q", gotPrompt)
	}
	if strings.Contains(gotPrompt, "cccc") {
		t.Errorf("prompt contains dropped block: %q", gotPrompt)
	}
}

func TestDescribeTruncatesHugeFirstBlockAtRuneBoundary(t *testing.T) {
	var gotPrompt string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrompt = decodeRequest(t, r).Messages[0].Content
		_, _ = fmt.Fprint(w, chatCompletionJSON("summary"))
	}))
	defer ts.Close()

	block := strings.Repeat("héllo wörld ", 10) // multi-byte runes
	d := NewDescriber(ts.URL, "", "test-model", time.Minute, 7)
	if _, err := d.Describe(context.Background(), []string{block}); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !utf8.ValidString(gotPrompt) {
		t.Fatalf("prompt is not valid UTF-8: %q", gotPrompt)
	}
	if !strings.Contains(gotPrompt, "<article>\nhéllo w\n</article>") {
		t.Errorf("prompt does not contain rune-truncated block: %q", gotPrompt)
	}
}

func TestDescribeTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the server can detect the client disconnect.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer ts.Close()

	d := NewDescriber(ts.URL, "", "test-model", 50*time.Millisecond, 0)
	start := time.Now()
	_, err := d.Describe(context.Background(), []string{"text"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Describe returned nil error, want timeout error")
	}
	if elapsed > time.Second {
		t.Errorf("Describe took %v, want prompt failure near the 50ms timeout", elapsed)
	}
}

func TestDescribeHTTPError(t *testing.T) {
	longBody := strings.Repeat("x", 1000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, longBody, http.StatusInternalServerError)
	}))
	defer ts.Close()

	d := NewDescriber(ts.URL, "", "test-model", time.Minute, 0)
	_, err := d.Describe(context.Background(), []string{"text"})
	if err == nil {
		t.Fatal("Describe returned nil error, want HTTP error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error does not mention status 500: %v", err)
	}
	if !strings.Contains(err.Error(), "xxx") {
		t.Errorf("error does not include body snippet: %v", err)
	}
	if len(err.Error()) > 700 {
		t.Errorf("error not truncated, length %d: %v", len(err.Error()), err)
	}
}

func TestDescribeMalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "{not json")
	}))
	defer ts.Close()

	d := NewDescriber(ts.URL, "", "test-model", time.Minute, 0)
	_, err := d.Describe(context.Background(), []string{"text"})
	if err == nil {
		t.Fatal("Describe returned nil error, want parse error")
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("error does not mention parsing: %v", err)
	}
	if !strings.Contains(err.Error(), "{not json") {
		t.Errorf("error does not include body snippet: %v", err)
	}
}

func TestDescribeEmptyChoices(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"choices":[]}`)
	}))
	defer ts.Close()

	d := NewDescriber(ts.URL, "", "test-model", time.Minute, 0)
	_, err := d.Describe(context.Background(), []string{"text"})
	if err == nil {
		t.Fatal("Describe returned nil error, want no-choices error")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("error does not mention missing choices: %v", err)
	}
}

func TestDescribeEmptyContent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, chatCompletionJSON("   \n"))
	}))
	defer ts.Close()

	d := NewDescriber(ts.URL, "", "test-model", time.Minute, 0)
	_, err := d.Describe(context.Background(), []string{"text"})
	if err == nil {
		t.Fatal("Describe returned nil error, want empty-content error")
	}
	if !strings.Contains(err.Error(), "empty content") {
		t.Errorf("error does not mention empty content: %v", err)
	}
}
