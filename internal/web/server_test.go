package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/digest"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
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
const badBody = "This link is not valid."

func TestUnsubscribeGet(t *testing.T) {
	st := storetest.New(t)
	h := NewServer(Options{Store: st, Key: key})
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
	h := NewServer(Options{Store: st, Key: key})
	wantBody(t, do(h, "POST", "/u/"+token.Sign(key, "sub-2"), "List-Unsubscribe=One-Click"), 200, okBody)
	if !unsubscribed(t, st, "sub-2") {
		t.Fatal("sub-2 is not unsubscribed")
	}
	// A repeat is the same answer.
	wantBody(t, do(h, "POST", "/u/"+token.Sign(key, "sub-2"), ""), 200, okBody)
}

func TestBadTokenIs404(t *testing.T) {
	st := storetest.New(t)
	h := NewServer(Options{Store: st, Key: key})
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
	h := NewServer(Options{Store: storetest.New(t), Key: key})
	for _, c := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/u/"}, {"GET", "/admin"}, {"GET", "/u/a/b"}, {"POST", "/healthz"},
	} {
		if rec := do(h, c.method, c.path, ""); rec.Code != 404 {
			t.Errorf("%s %s = %d, want 404", c.method, c.path, rec.Code)
		}
	}
}

// Any method but GET, POST and HEAD on a real link is a 404 and changes
// nothing, as if the route were not there.
func TestOtherMethodsAre404(t *testing.T) {
	st := storetest.New(t)
	h := NewServer(Options{Store: st, Key: key})
	for _, m := range []string{"PUT", "DELETE", "PATCH", "OPTIONS"} {
		wantBody(t, do(h, m, "/u/"+token.Sign(key, "sub-5"), ""), 404, badBody)
	}
	if unsubscribed(t, st, "sub-5") {
		t.Fatal("another method unsubscribed")
	}
}

// Link checkers and mail scanners send HEAD; it must not unsubscribe.
func TestHeadDoesNotUnsubscribe(t *testing.T) {
	st := storetest.New(t)
	h := NewServer(Options{Store: st, Key: key})
	rec := do(h, "HEAD", "/u/"+token.Sign(key, "sub-4"), "")
	wantBody(t, rec, 200, "")
	if unsubscribed(t, st, "sub-4") {
		t.Fatal("HEAD unsubscribed sub-4")
	}
	wantBody(t, do(h, "HEAD", "/u/garbage", ""), 404, "")
}

func TestHealthz(t *testing.T) {
	st := storetest.New(t)
	h := NewServer(Options{Store: st, Key: key})
	rec := do(h, "GET", "/healthz", "")
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
	st.Close()
	if rec := do(h, "GET", "/healthz", ""); rec.Code != 503 {
		t.Fatalf("healthz with the database closed = %d, want 503", rec.Code)
	}
}

var week = time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)

func saveDigest(t *testing.T, st *store.Store, subject string) {
	t.Helper()
	d := digest.Digest{
		Stats:    digest.Stats{NewThreads: 2},
		Featured: &digest.Featured{Item: digest.Item{Key: digest.Key{Kind: digest.KindEntry, ID: 1}, Title: "grinders", URL: "https://talk.example.org/t/1", Magazine: digest.Magazine{Name: "coffee"}}},
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveDigest(context.Background(), subject, week, b); err != nil {
		t.Fatal(err)
	}
}

func webServer(st *store.Store, now time.Time) http.Handler {
	return NewServer(Options{
		Store: st, Key: key, Names: mail.Names{Instance: "Talk", Meta: "Talk Community"},
		SiteURL: "https://talk.example.org/", Now: func() time.Time { return now },
	})
}

func TestWebDigest(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	h := webServer(st, week.Add(24*time.Hour))
	rec := do(h, "GET", "/d/"+token.SignDigest(key, "sub-1", week), "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"grinders", "See everything on Talk", "Oct 12 – Oct 18"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if strings.Contains(body, "/u/") || strings.Contains(body, "Unsubscribe") {
		t.Error("page has an unsubscribe link")
	}
	for k, v := range map[string]string{
		"Referrer-Policy": "no-referrer", "X-Robots-Tag": "noindex", "Cache-Control": "private, no-store",
		"Content-Type": "text/html; charset=utf-8",
	} {
		if got := rec.Header().Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestWebDigestHead(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	h := webServer(st, week.Add(24*time.Hour))
	rec := do(h, "HEAD", "/d/"+token.SignDigest(key, "sub-1", week), "")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Fatalf("headers %v", rec.Header())
	}
	if rec := do(h, "HEAD", "/d/garbage", ""); rec.Code != 404 || rec.Body.Len() != 0 {
		t.Fatalf("bad HEAD = %d", rec.Code)
	}
}

func TestWebDigestBadTokens(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	good := token.SignDigest(key, "sub-1", week)
	payload, mac, _ := strings.Cut(good, ".")
	otherSubject := token.SignDigest(key, "sub-2", week)
	_, otherMAC, _ := strings.Cut(otherSubject, ".")
	_ = mac
	tokens := map[string]string{
		"altered":       payload + "." + mac[:len(mac)-2] + "AA",
		"other subject": payload + "." + otherMAC,
		"unsubscribe":   token.Sign(key, "sub-1"),
		"unknown week":  token.SignDigest(key, "sub-1", week.AddDate(0, 0, -7)),
		"garbage":       "garbage",
	}
	h := webServer(st, week.Add(24*time.Hour))
	want := do(h, "GET", "/u/garbage", "")
	if want.Body.String() != badBody {
		t.Fatalf("bad /u/ body %q", want.Body.String())
	}
	for name, tok := range tokens {
		rec := do(h, "GET", "/d/"+tok, "")
		wantBody(t, rec, 404, badBody)
		if rec.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
			t.Errorf("%s: content type differs from /u/", name)
		}
	}
	// Expired: four weeks after the send time.
	late := webServer(st, week.Add(28*24*time.Hour))
	wantBody(t, do(late, "GET", "/d/"+good, ""), 404, badBody)
	// Just before expiry it still reads.
	if rec := do(webServer(st, week.Add(28*24*time.Hour-time.Second)), "GET", "/d/"+good, ""); rec.Code != 200 {
		t.Fatalf("before expiry = %d", rec.Code)
	}
}

func TestWebDigestStoreError(t *testing.T) {
	st := storetest.New(t)
	h := webServer(st, week.Add(time.Hour))
	tok := token.SignDigest(key, "sub-1", week)
	st.Close()
	rec := do(h, "GET", "/d/"+tok, "")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "sub-1") || rec.Body.Len() == 0 {
		t.Fatalf("store error = %d %q", rec.Code, rec.Body.String())
	}
}

func TestWebDigestRecordsNothing(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	conn := storetest.Conn(t, st)
	count := func() string {
		var out []string
		for _, tb := range []string{"unsubscribes", "sends", "shown", "matrix_members", "dm_sends", "bot_state", "web_digests"} {
			var n int
			if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+tb).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, tb+"="+string(rune('0'+n)))
		}
		return strings.Join(out, ",")
	}
	before := count()
	h := webServer(st, week.Add(time.Hour))
	tok := token.SignDigest(key, "sub-1", week)
	for _, m := range []string{"GET", "HEAD"} {
		do(h, m, "/d/"+tok, "")
	}
	if after := count(); after != before {
		t.Fatalf("rows changed: %s -> %s", before, after)
	}
}

func TestWebDigestOtherMethods(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	h := webServer(st, week.Add(time.Hour))
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		wantBody(t, do(h, m, "/d/"+token.SignDigest(key, "sub-1", week), ""), 404, badBody)
	}
}
