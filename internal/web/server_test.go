package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
	"github.com/paisans-software/paisans-engagement-service/internal/token"
)

var key = []byte("0123456789abcdef0123456789abcdef")

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func unsubscribed(t *testing.T, st *store.Store, subject string) bool {
	t.Helper()
	ok, err := st.Unsubscribed(context.Background(), subject)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d", rec.Code, code)
	}
	if got := rec.Body.String(); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
}

const okBody = "You're unsubscribed from the weekly digest."
const badBody = "This unsubscribe link is not valid."

func TestUnsubscribeGet(t *testing.T) {
	st := storetest.New(t)
	h := NewServer(st, key)
	rec := do(h, "GET", "/u/"+token.Sign(key, "sub-1"), "")
	wantBody(t, rec, 200, okBody)
	if !unsubscribed(t, st, "sub-1") {
		t.Fatal("sub-1 is not unsubscribed")
	}
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Cache-Control"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
}

func TestUnsubscribePost(t *testing.T) {
	st := storetest.New(t)
	h := NewServer(st, key)
	wantBody(t, do(h, "POST", "/u/"+token.Sign(key, "sub-2"), "List-Unsubscribe=One-Click"), 200, okBody)
	if !unsubscribed(t, st, "sub-2") {
		t.Fatal("sub-2 is not unsubscribed")
	}
	// A repeat is the same answer.
	wantBody(t, do(h, "POST", "/u/"+token.Sign(key, "sub-2"), ""), 200, okBody)
}

func TestBadTokenIs404(t *testing.T) {
	st := storetest.New(t)
	h := NewServer(st, key)
	other := token.Sign([]byte("another key another key another!"), "sub-3")
	for _, tok := range []string{"garbage", other, token.Sign(key, ""), "a.b"} {
		for _, m := range []string{"GET", "POST"} {
			wantBody(t, do(h, m, "/u/"+tok, ""), 404, badBody)
		}
	}
	if unsubscribed(t, st, "sub-3") || unsubscribed(t, st, "") {
		t.Fatal("a bad token unsubscribed someone")
	}
}

func TestOtherPathsAre404(t *testing.T) {
	h := NewServer(storetest.New(t), key)
	for _, c := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/u/"}, {"GET", "/admin"}, {"PUT", "/u/x"}, {"DELETE", "/u/x"}, {"GET", "/u/a/b"},
	} {
		rec := do(h, c.method, c.path, "")
		if rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%s %s = %d, want 404 or 405", c.method, c.path, rec.Code)
		}
	}
	if rec := do(h, "GET", "/", ""); rec.Code != 404 {
		t.Errorf("GET / = %d, want 404", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	st := storetest.New(t)
	h := NewServer(st, key)
	rec := do(h, "GET", "/healthz", "")
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
	st.Close()
	if rec := do(h, "GET", "/healthz", ""); rec.Code != 503 {
		t.Fatalf("healthz with the database closed = %d, want 503", rec.Code)
	}
}
