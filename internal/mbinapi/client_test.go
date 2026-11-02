package mbinapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type call struct {
	Method, Path, Auth, Host string
	Body                     map[string]any
}

type fake struct {
	*httptest.Server
	mu      sync.Mutex
	hosts   []string
	tokens  []url.Values
	calls   []call
	answer  func(c call) (int, any)
	expired bool
}

func newFake(t *testing.T, answer func(c call) (int, any)) *fake {
	f := &fake{answer: answer}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	f.hosts = append(f.hosts, r.Host)
	if r.URL.Path == "/token" {
		r.ParseForm()
		f.tokens = append(f.tokens, r.PostForm)
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok" + string(rune('0'+len(f.tokens))), "expires_in": 3600})
		return
	}
	c := call{Method: r.Method, Path: r.URL.EscapedPath(), Auth: r.Header.Get("Authorization")}
	raw, _ := io.ReadAll(r.Body)
	json.Unmarshal(raw, &c.Body)
	f.calls = append(f.calls, c)
	if f.expired {
		f.expired = false
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	status, out := f.answer(c)
	w.WriteHeader(status)
	if out != nil {
		json.NewEncoder(w).Encode(out)
	}
}

func TestTokenRequestAndCache(t *testing.T) {
	f := newFake(t, func(call) (int, any) { return 204, nil })
	c := New(f.URL, "forum.example.org", "engagement-agent", "s3cret", nil)
	ctx := context.Background()
	for range 2 {
		if err := c.Subscribe(ctx, 7, 3); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.tokens) != 1 {
		t.Fatalf("token requests = %d, want 1", len(f.tokens))
	}
	tv := f.tokens[0]
	if tv.Get("grant_type") != "client_credentials" || tv.Get("client_id") != "engagement-agent" ||
		tv.Get("client_secret") != "s3cret" || tv.Get("scope") != Scopes {
		t.Fatalf("token form = %v", tv)
	}
	if f.calls[0].Auth != "Bearer tok1" {
		t.Fatalf("auth = %q", f.calls[0].Auth)
	}
	for _, h := range f.hosts {
		if h != "forum.example.org" {
			t.Fatalf("Host = %q, want Mbin's hostname on every request", h)
		}
	}
}

func TestUnauthorizedRenewsOnce(t *testing.T) {
	f := newFake(t, func(call) (int, any) { return 204, nil })
	c := New(f.URL, "", "id", "s", nil)
	ctx := context.Background()
	if err := c.Subscribe(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	f.expired = true
	if err := c.Subscribe(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	if len(f.tokens) != 2 || f.calls[len(f.calls)-1].Auth != "Bearer tok2" {
		t.Fatalf("tokens = %d, last auth %q", len(f.tokens), f.calls[len(f.calls)-1].Auth)
	}
}

func TestRequests(t *testing.T) {
	f := newFake(t, func(c call) (int, any) {
		switch c.Path {
		case "/api/magazine/name/welcome":
			return 200, map[string]any{"magazineId": 3, "name": "welcome", "isPostingRestrictedToMods": false}
		case "/api/agent/magazines":
			return 201, map[string]any{"magazineId": 4, "name": "welcome", "isPostingRestrictedToMods": true}
		case "/api/magazine/3/article":
			return 200, map[string]any{"entryId": 99, "slug": "welcome-alice"}
		}
		return 204, nil
	})
	c := New(f.URL, "", "id", "s", nil)
	ctx := context.Background()
	m, ok, err := c.MagazineByName(ctx, "welcome")
	if err != nil || !ok || m != (Magazine{ID: 3, Name: "welcome"}) {
		t.Fatalf("MagazineByName = %+v, %v, %v", m, ok, err)
	}
	m, err = c.CreateMagazine(ctx, "welcome", "Welcome", "Say hello")
	if err != nil || m.ID != 4 || !m.PostingRestricted {
		t.Fatalf("CreateMagazine = %+v, %v", m, err)
	}
	if err := c.RestrictPosting(ctx, 3); err != nil {
		t.Fatal(err)
	}
	e, err := c.CreateThread(ctx, 3, "Welcome @alice", "Say hello to @alice")
	if err != nil || e != (Entry{ID: 99, Slug: "welcome-alice"}) {
		t.Fatalf("CreateThread = %+v, %v", e, err)
	}
	if err := c.Subscribe(ctx, 7, 3); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		method, path string
		body         map[string]any
	}{
		{"GET", "/api/magazine/name/welcome", nil},
		{"POST", "/api/agent/magazines", map[string]any{"name": "welcome", "title": "Welcome", "description": "Say hello",
			"isAdult": false, "isPostingRestrictedToMods": true}},
		{"PUT", "/api/moderate/magazine/3", map[string]any{"isPostingRestrictedToMods": true}},
		{"POST", "/api/magazine/3/article", map[string]any{"title": "Welcome @alice", "body": "Say hello to @alice",
			"lang": "en", "isAdult": false, "isOc": false}},
		{"PUT", "/api/agent/users/7/magazines/3/subscription", nil},
	}
	if len(f.calls) != len(want) {
		t.Fatalf("calls = %+v", f.calls)
	}
	for i, w := range want {
		got := f.calls[i]
		gb, _ := json.Marshal(got.Body)
		wb, _ := json.Marshal(w.body)
		if got.Method != w.method || got.Path != w.path || string(gb) != string(wb) {
			t.Errorf("call %d = %s %s %s, want %s %s %s", i, got.Method, got.Path, gb, w.method, w.path, wb)
		}
	}
}

func TestErrors(t *testing.T) {
	for status, want := range map[int]error{403: ErrForbidden, 401: ErrForbidden, 400: ErrRefused, 503: ErrUnavailable} {
		f := newFake(t, func(call) (int, any) { return status, map[string]any{"detail": "no"} })
		err := New(f.URL, "", "id", "s", nil).Subscribe(context.Background(), 1, 2)
		if !errors.Is(err, want) {
			t.Errorf("status %d: err = %v, want %v", status, err, want)
		}
	}
	f := newFake(t, func(call) (int, any) { return 404, nil })
	if _, ok, err := New(f.URL, "", "id", "s", nil).MagazineByName(context.Background(), "welcome"); ok || err != nil {
		t.Fatalf("missing magazine = %v, %v", ok, err)
	}
	f.Close()
	if err := New(f.URL, "", "id", "s", nil).Subscribe(context.Background(), 1, 2); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("down: %v", err)
	}
}

func TestRefusalCarriesMbinsReason(t *testing.T) {
	f := newFake(t, func(call) (int, any) {
		return 400, map[string]any{"type": "x", "title": "Bad Request", "detail": "Title is too\nlong"}
	})
	_, err := New(f.URL, "", "id", "s", nil).CreateThread(context.Background(), 3, "t", "b")
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "Title is too long") {
		t.Fatalf("err = %v", err)
	}
}

func TestSubscribeNotFoundIsRefused(t *testing.T) {
	f := newFake(t, func(call) (int, any) { return 404, nil })
	if err := New(f.URL, "", "id", "s", nil).Subscribe(context.Background(), 1, 2); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want a refusal", err)
	}
}
