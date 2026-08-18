package instapaper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gomodule/oauth1/oauth"
)

const defaultBaseURL = "https://www.instapaper.com/api/1"

// maxErrBodyLen limits how much of an error response body is included in errors.
const maxErrBodyLen = 512

// Client is an Instapaper Full API client.
type Client struct {
	oauth      oauth.Client
	token      *oauth.Credentials // nil until authenticated
	baseURL    string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL (for tests).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient overrides the HTTP client used for requests.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// NewClient creates an Instapaper client. creds may be nil, in which case
// only RequestAccessToken may be called.
func NewClient(consumerKey, consumerSecret string, creds *Credentials, opts ...Option) *Client {
	c := &Client{
		oauth: oauth.Client{
			Credentials: oauth.Credentials{Token: consumerKey, Secret: consumerSecret},
		},
		baseURL:    defaultBaseURL,
		httpClient: http.DefaultClient,
	}
	if creds != nil {
		c.token = &oauth.Credentials{Token: creds.Token, Secret: creds.TokenSecret}
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError is an error object returned by the Instapaper API.
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("instapaper API error %d: %s", e.Code, e.Message)
}

// Bookmark is one saved article as returned by bookmarks/list.
type Bookmark struct {
	BookmarkID int64
	URL        string
	Title      string
	Hash       string
	SavedAt    time.Time
}

// HaveItem identifies an already-synced bookmark for the have parameter.
type HaveItem struct {
	BookmarkID int64
	Hash       string
}

// ListResult is the parsed result of bookmarks/list.
type ListResult struct {
	Bookmarks []Bookmark
	DeleteIDs []int64
}

// RequestAccessToken exchanges a username/password for a permanent OAuth
// token pair using Instapaper's xAuth flow.
func (c *Client) RequestAccessToken(ctx context.Context, username, password string) (*Credentials, error) {
	form := url.Values{
		"x_auth_username": {username},
		"x_auth_password": {password},
		"x_auth_mode":     {"client_auth"},
	}
	body, err := c.post(ctx, &oauth.Credentials{}, "/oauth/access_token", form)
	if err != nil {
		return nil, fmt.Errorf("instapaper access token request: %w", err)
	}
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("instapaper access token request: parse response: %w", err)
	}
	creds := &Credentials{
		Token:       vals.Get("oauth_token"),
		TokenSecret: vals.Get("oauth_token_secret"),
	}
	if creds.Token == "" || creds.TokenSecret == "" {
		return nil, fmt.Errorf("instapaper access token request: response missing token pair: %q", truncate(body))
	}
	return creds, nil
}

// ListBookmarks fetches the unread folder via bookmarks/list, passing have
// for incremental sync. limit is the maximum number of bookmarks to return.
func (c *Client) ListBookmarks(ctx context.Context, have []HaveItem, limit int) (*ListResult, error) {
	form := url.Values{"limit": {strconv.Itoa(limit)}}
	if len(have) > 0 {
		parts := make([]string, len(have))
		for i, h := range have {
			parts[i] = strconv.FormatInt(h.BookmarkID, 10)
			if h.Hash != "" {
				parts[i] += ":" + h.Hash
			}
		}
		form.Set("have", strings.Join(parts, ","))
	}
	body, err := c.authedPost(ctx, "/bookmarks/list", form)
	if err != nil {
		return nil, fmt.Errorf("instapaper bookmarks/list: %w", err)
	}
	result, err := parseListResponse(body)
	if err != nil {
		return nil, fmt.Errorf("instapaper bookmarks/list: %w", err)
	}
	return result, nil
}

// GetText fetches the parsed article HTML for a bookmark via bookmarks/get_text.
func (c *Client) GetText(ctx context.Context, bookmarkID int64) (string, error) {
	form := url.Values{"bookmark_id": {strconv.FormatInt(bookmarkID, 10)}}
	body, err := c.authedPost(ctx, "/bookmarks/get_text", form)
	if err != nil {
		return "", fmt.Errorf("instapaper bookmarks/get_text (bookmark %d): %w", bookmarkID, err)
	}
	return string(body), nil
}

func (c *Client) authedPost(ctx context.Context, path string, form url.Values) ([]byte, error) {
	if c.token == nil {
		return nil, errors.New("no access token (run with -instapaper-login first)")
	}
	return c.post(ctx, c.token, path, form)
}

// post signs and sends a POST, returning the response body.
// A non-200 status becomes an error including the status and a truncated body.
func (c *Client) post(ctx context.Context, token *oauth.Credentials, path string, form url.Values) ([]byte, error) {
	ctx = context.WithValue(ctx, oauth.HTTPClient, c.httpClient)
	resp, err := c.oauth.PostContext(ctx, token, c.baseURL+path, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(body))
	}
	return body, nil
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > maxErrBodyLen {
		s = s[:maxErrBodyLen] + "..."
	}
	return s
}

// parseListResponse parses the heterogeneous JSON array returned by
// bookmarks/list, distinguishing elements by their "type" field.
func parseListResponse(body []byte) (*ListResult, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	result := &ListResult{}
	for _, item := range items {
		var typed struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &typed); err != nil {
			return nil, fmt.Errorf("parse response element: %w", err)
		}
		switch typed.Type {
		case "bookmark":
			var b struct {
				BookmarkID int64       `json:"bookmark_id"`
				URL        string      `json:"url"`
				Title      string      `json:"title"`
				Hash       string      `json:"hash"`
				Time       json.Number `json:"time"`
			}
			if err := json.Unmarshal(item, &b); err != nil {
				return nil, fmt.Errorf("parse bookmark: %w", err)
			}
			bm := Bookmark{
				BookmarkID: b.BookmarkID,
				URL:        b.URL,
				Title:      b.Title,
				Hash:       b.Hash,
			}
			if b.Time != "" {
				secs, err := b.Time.Float64()
				if err != nil {
					return nil, fmt.Errorf("parse bookmark %d time %q: %w", b.BookmarkID, b.Time, err)
				}
				bm.SavedAt = time.Unix(int64(secs), 0).UTC()
			}
			result.Bookmarks = append(result.Bookmarks, bm)
		case "meta":
			var m struct {
				DeleteIDs json.RawMessage `json:"delete_ids"`
			}
			if err := json.Unmarshal(item, &m); err != nil {
				return nil, fmt.Errorf("parse meta: %w", err)
			}
			ids, err := parseDeleteIDs(m.DeleteIDs)
			if err != nil {
				return nil, fmt.Errorf("parse meta delete_ids: %w", err)
			}
			result.DeleteIDs = append(result.DeleteIDs, ids...)
		case "error":
			var e struct {
				ErrorCode int    `json:"error_code"`
				Message   string `json:"message"`
			}
			if err := json.Unmarshal(item, &e); err != nil {
				return nil, fmt.Errorf("parse error object: %w", err)
			}
			return nil, &APIError{Code: e.ErrorCode, Message: e.Message}
		default:
			// Ignore "user" and any unknown element types.
		}
	}
	return result, nil
}

// parseDeleteIDs accepts delete_ids as a comma-separated string
// ("123,456", possibly empty) or a JSON array of numbers or numeric strings.
func parseDeleteIDs(raw json.RawMessage) ([]int64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return parseIDList(strings.Split(s, ","))
	case '[':
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return nil, err
		}
		parts := make([]string, len(elems))
		for i, el := range elems {
			el = bytes.TrimSpace(el)
			if len(el) > 0 && el[0] == '"' {
				var s string
				if err := json.Unmarshal(el, &s); err != nil {
					return nil, err
				}
				parts[i] = s
			} else {
				parts[i] = string(el)
			}
		}
		return parseIDList(parts)
	default:
		return nil, fmt.Errorf("unexpected format: %s", truncate(raw))
	}
}

func parseIDList(parts []string) ([]int64, error) {
	var ids []int64
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad id %q: %w", p, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
