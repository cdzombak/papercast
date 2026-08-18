// Package llm generates article descriptions via an OpenAI-compatible
// chat completions API.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const promptFormat = "Summarize the following article in 1 paragraph. Do not include any introductory text, just the summary:\n\n<article>\n%s\n</article>"

const maxErrBodyBytes = 500

// Describer summarizes article text using an OpenAI-compatible API.
type Describer struct {
	endpoint      string
	apiKey        string
	model         string
	timeout       time.Duration
	maxInputChars int
	client        *http.Client
}

// NewDescriber creates a Describer. endpoint is the base URL of an
// OpenAI-compatible API, e.g. "https://api.openai.com/v1"; requests are
// POSTed to endpoint + "/chat/completions". apiKey may be empty, in which
// case no Authorization header is sent.
func NewDescriber(endpoint, apiKey, model string, timeout time.Duration, maxInputChars int) *Describer {
	return &Describer{
		endpoint:      strings.TrimSuffix(endpoint, "/"),
		apiKey:        apiKey,
		model:         model,
		timeout:       timeout,
		maxInputChars: maxInputChars,
		client:        &http.Client{},
	}
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Describe summarizes the article. blockTexts are the article's extracted
// text blocks in order; they are joined with "\n\n" and truncated to
// maxInputChars at a block boundary if necessary.
func (d *Describer) Describe(ctx context.Context, blockTexts []string) (string, error) {
	text := joinTruncated(blockTexts, d.maxInputChars)

	body, err := json.Marshal(chatRequest{
		Model: d.model,
		Messages: []chatMessage{
			{Role: "user", Content: fmt.Sprintf(promptFormat, text)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	if d.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if d.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+d.apiKey)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: unexpected status %d: %s", resp.StatusCode, bodySnippet(respBody))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("llm: parse response: %w (body: %s)", err, bodySnippet(respBody))
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm: response contains no choices (body: %s)", bodySnippet(respBody))
	}
	summary := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if summary == "" {
		return "", fmt.Errorf("llm: response contains empty content (body: %s)", bodySnippet(respBody))
	}
	return summary, nil
}

// joinTruncated joins blocks with "\n\n", keeping the result within maxChars
// (counted in runes; no limit if maxChars <= 0). Whole trailing blocks are
// dropped; if the first block alone exceeds the limit, it is truncated at a
// rune boundary so something is sent.
func joinTruncated(blocks []string, maxChars int) string {
	joined := strings.Join(blocks, "\n\n")
	if maxChars <= 0 || utf8.RuneCountInString(joined) <= maxChars {
		return joined
	}

	var b strings.Builder
	total := 0
	for i, block := range blocks {
		n := utf8.RuneCountInString(block)
		if i > 0 {
			n += 2 // "\n\n" separator
		}
		if total+n > maxChars {
			break
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(block)
		total += n
	}
	if b.Len() == 0 && len(blocks) > 0 {
		return truncateRunes(blocks[0], maxChars)
	}
	return b.String()
}

// truncateRunes returns the first n runes of s.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}

// bodySnippet formats a response body for error messages, truncated to
// maxErrBodyBytes.
func bodySnippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > maxErrBodyBytes {
		s = s[:maxErrBodyBytes] + "..."
	}
	return s
}
