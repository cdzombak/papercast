package instapaper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	creds := &Credentials{Token: "test-token", TokenSecret: "test-token-secret"}
	return NewClient("test-consumer-key", "test-consumer-secret", creds,
		WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestRequestAccessToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/oauth/access_token" {
			t.Errorf("path = %q, want /oauth/access_token", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("x_auth_username"); got != "user@example.com" {
			t.Errorf("x_auth_username = %q", got)
		}
		if got := r.PostForm.Get("x_auth_password"); got != "hunter2" {
			t.Errorf("x_auth_password = %q", got)
		}
		if got := r.PostForm.Get("x_auth_mode"); got != "client_auth" {
			t.Errorf("x_auth_mode = %q, want client_auth", got)
		}
		w.Write([]byte("oauth_token=returned-token&oauth_token_secret=returned-secret"))
	}))
	defer srv.Close()

	c := NewClient("test-consumer-key", "test-consumer-secret", nil,
		WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	creds, err := c.RequestAccessToken(context.Background(), "user@example.com", "hunter2")
	if err != nil {
		t.Fatalf("RequestAccessToken: %v", err)
	}
	if creds.Token != "returned-token" || creds.TokenSecret != "returned-secret" {
		t.Errorf("creds = %+v", creds)
	}
	if !strings.Contains(gotAuth, "oauth_consumer_key") {
		t.Errorf("Authorization header missing oauth_consumer_key: %q", gotAuth)
	}
	if !strings.Contains(gotAuth, "oauth_signature") {
		t.Errorf("Authorization header missing oauth_signature: %q", gotAuth)
	}
}

func TestRequestAccessTokenMissingPair(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("oauth_token=only-token"))
	})
	_, err := c.RequestAccessToken(context.Background(), "u", "p")
	if err == nil || !strings.Contains(err.Error(), "missing token pair") {
		t.Errorf("err = %v, want missing token pair error", err)
	}
}

const listFixtureTail = `,
 {"type":"user","user_id":42,"username":"user@example.com"},
 {"type":"bookmark","bookmark_id":101,"url":"https://example.com/a","title":"Article A","hash":"aaAA","time":1700000000},
 {"type":"bookmark","bookmark_id":102,"url":"https://example.com/b","title":"Article B","hash":"bbBB","time":1700000100}
]`

func TestListBookmarks(t *testing.T) {
	cases := []struct {
		name    string
		meta    string
		wantIDs []int64
	}{
		{"delete_ids string", `{"type":"meta","delete_ids":"201,202"}`, []int64{201, 202}},
		{"delete_ids empty string", `{"type":"meta","delete_ids":""}`, nil},
		{"delete_ids number array", `{"type":"meta","delete_ids":[201,202]}`, []int64{201, 202}},
		{"delete_ids string array", `{"type":"meta","delete_ids":["201","202"]}`, []int64{201, 202}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bookmarks/list" {
					t.Errorf("path = %q, want /bookmarks/list", r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm: %v", err)
				}
				if got := r.PostForm.Get("limit"); got != "500" {
					t.Errorf("limit = %q, want 500", got)
				}
				if got := r.PostForm.Get("have"); got != "1:abc,2:def,3" {
					t.Errorf("have = %q, want 1:abc,2:def,3", got)
				}
				auth := r.Header.Get("Authorization")
				if !strings.Contains(auth, `oauth_token="test-token"`) {
					t.Errorf("Authorization header missing oauth_token: %q", auth)
				}
				w.Write([]byte("[" + tc.meta + listFixtureTail))
			})
			have := []HaveItem{{1, "abc"}, {2, "def"}, {3, ""}}
			res, err := c.ListBookmarks(context.Background(), have, 500)
			if err != nil {
				t.Fatalf("ListBookmarks: %v", err)
			}
			if !reflect.DeepEqual(res.DeleteIDs, tc.wantIDs) {
				t.Errorf("DeleteIDs = %v, want %v", res.DeleteIDs, tc.wantIDs)
			}
			want := []Bookmark{
				{101, "https://example.com/a", "Article A", "aaAA", time.Unix(1700000000, 0).UTC()},
				{102, "https://example.com/b", "Article B", "bbBB", time.Unix(1700000100, 0).UTC()},
			}
			if !reflect.DeepEqual(res.Bookmarks, want) {
				t.Errorf("Bookmarks = %+v, want %+v", res.Bookmarks, want)
			}
			if loc := res.Bookmarks[0].SavedAt.Location(); loc != time.UTC {
				t.Errorf("SavedAt location = %v, want UTC", loc)
			}
		})
	}
}

func TestListBookmarksEmptyHave(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if _, ok := r.PostForm["have"]; ok {
			t.Errorf("have param present, want absent: %q", r.PostForm.Get("have"))
		}
		w.Write([]byte(`[{"type":"meta","delete_ids":""}]`))
	})
	res, err := c.ListBookmarks(context.Background(), nil, 500)
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(res.Bookmarks) != 0 || len(res.DeleteIDs) != 0 {
		t.Errorf("result = %+v, want empty", res)
	}
}

func TestListBookmarksAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"type":"error","error_code":1041,"message":"Subscription required"}]`))
	})
	_, err := c.ListBookmarks(context.Background(), nil, 500)
	if err == nil {
		t.Fatal("want error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Code != 1041 || apiErr.Message != "Subscription required" {
		t.Errorf("APIError = %+v", apiErr)
	}
	if !strings.Contains(err.Error(), "1041") || !strings.Contains(err.Error(), "Subscription required") {
		t.Errorf("err = %v, want code and message in text", err)
	}
}

func TestListBookmarksHTTPError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server exploded", http.StatusInternalServerError)
	})
	_, err := c.ListBookmarks(context.Background(), nil, 500)
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "server exploded") {
		t.Errorf("err = %v, want HTTP 500 error with body", err)
	}
}

func TestGetText(t *testing.T) {
	const html = "<div><p>Article body.</p></div>"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bookmarks/get_text" {
			t.Errorf("path = %q, want /bookmarks/get_text", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("bookmark_id"); got != "101" {
			t.Errorf("bookmark_id = %q, want 101", got)
		}
		auth := r.Header.Get("Authorization")
		if !strings.Contains(auth, `oauth_token="test-token"`) {
			t.Errorf("Authorization header missing oauth_token: %q", auth)
		}
		w.Write([]byte(html))
	})
	got, err := c.GetText(context.Background(), 101)
	if err != nil {
		t.Fatalf("GetText: %v", err)
	}
	if got != html {
		t.Errorf("GetText = %q, want %q", got, html)
	}
}

func TestGetTextUnavailable(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`[{"type":"error","error_code":1550,"message":"Text not available"}]`))
	})
	_, err := c.GetText(context.Background(), 999)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("err = %v, want error mentioning 400", err)
	}
}

func TestAuthedCallWithoutToken(t *testing.T) {
	c := NewClient("k", "s", nil)
	if _, err := c.ListBookmarks(context.Background(), nil, 500); err == nil {
		t.Error("ListBookmarks with nil creds: want error")
	}
	if _, err := c.GetText(context.Background(), 1); err == nil {
		t.Error("GetText with nil creds: want error")
	}
}
