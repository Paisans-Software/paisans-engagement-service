package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
		Store: st, Key: key, Names: mail.Names{Meta: "Talk Community"},
		SiteURL: "https://talk.example.org/", Now: func() time.Time { return now },
	})
}

// doAs is do for a request the sign-in gate admitted for subject: the gate
// sets X-Auth-Request-User to the member's OIDC subject.
func doAs(h http.Handler, method, path, subject string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Auth-Request-User", subject)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const weekPath = "/d/2026-10-19"

func TestWebDigest(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	h := webServer(st, week.Add(24*time.Hour))
	rec := doAs(h, "GET", weekPath, "sub-1")
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
	rec := doAs(h, "HEAD", weekPath, "sub-1")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Fatalf("headers %v", rec.Header())
	}
	if rec := doAs(h, "HEAD", "/d/garbage", "sub-1"); rec.Code != 404 || rec.Body.Len() != 0 {
		t.Fatalf("bad HEAD = %d", rec.Code)
	}
}

// The page is the signed-in member's own: another member, another week, a
// path that is not a week, or a token link from before the gate all get the
// same 404 as any bad link.
func TestWebDigestNotFound(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	h := webServer(st, week.Add(24*time.Hour))
	want := do(h, "GET", "/u/garbage", "")
	if want.Body.String() != badBody {
		t.Fatalf("bad /u/ body %q", want.Body.String())
	}
	for name, c := range map[string]struct{ path, subject string }{
		"other member": {weekPath, "sub-2"},
		"unknown week": {"/d/2026-10-12", "sub-1"},
		"garbage":      {"/d/garbage", "sub-1"},
		"old token":    {"/d/c3ViLTEAMjAyNi0xMC0xOQ.AAAA", "sub-1"},
		"preview":      {"/d/preview", "sub-1"},
	} {
		rec := doAs(h, "GET", c.path, c.subject)
		wantBody(t, rec, 404, badBody)
		if rec.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
			t.Errorf("%s: content type differs from /u/", name)
		}
	}
	// Expired: four weeks after the send time, by the calendar.
	late := webServer(st, store.DigestExpiry(week))
	wantBody(t, doAs(late, "GET", weekPath, "sub-1"), 404, badBody)
	// Just before expiry it still reads.
	if rec := doAs(webServer(st, store.DigestExpiry(week).Add(-time.Second)), "GET", weekPath, "sub-1"); rec.Code != 200 {
		t.Fatalf("before expiry = %d", rec.Code)
	}
}

// Without the gate's header the page answers like any bad link, and the
// service warns once per request that the hostname is not gated.
func TestWebDigestWithoutGate(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	savePreview(t, st, "sub-1", week.Add(30*time.Hour))
	h := webServer(st, week.Add(24*time.Hour))
	for _, path := range []string{weekPath, "/d/preview/2026-10-19"} {
		rec := do(h, "GET", path, "")
		wantBody(t, rec, 404, badBody)
		if rec.Header().Get("X-Robots-Tag") != "noindex" || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s: headers %v", path, rec.Header())
		}
		// An empty header is no member either.
		wantBody(t, doAs(h, "GET", path, ""), 404, badBody)
		if rec := do(h, "HEAD", path, ""); rec.Code != 404 || rec.Body.Len() != 0 {
			t.Fatalf("%s: HEAD = %d", path, rec.Code)
		}
	}
	if n := strings.Count(logged.String(), "level=WARN"); n != 6 {
		t.Fatalf("%d warnings, want one per request: %q", n, logged.String())
	}
	if !strings.Contains(logged.String(), "X-Auth-Request-User") || strings.Contains(logged.String(), "sub-1") {
		t.Fatalf("log = %q", logged.String())
	}
}

func TestWebDigestStoreError(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	st := storetest.New(t)
	h := webServer(st, week.Add(time.Hour))
	st.Close()
	rec := doAs(h, "GET", weekPath, "sub-1")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "sub-1") || rec.Body.Len() == 0 {
		t.Fatalf("store error = %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(logged.String(), "sub-1") || !strings.Contains(logged.String(), "reading a web digest failed") {
		t.Fatalf("log = %q", logged.String())
	}
}

func TestWebDigestRecordsNothing(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	savePreview(t, st, "sub-1", week.Add(30*time.Hour))
	conn := storetest.Conn(t, st)
	count := func() string {
		var out []string
		for _, tb := range []string{"unsubscribes", "sends", "shown", "matrix_members", "dm_sends", "bot_state", "web_digests", "preview_digests"} {
			var n int
			if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+tb).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, tb+"="+fmt.Sprint(n))
		}
		return strings.Join(out, ",")
	}
	before := count()
	h := webServer(st, week.Add(time.Hour))
	for _, m := range []string{"GET", "HEAD"} {
		doAs(h, m, weekPath, "sub-1")
		doAs(h, m, "/d/preview/2026-10-19", "sub-1")
	}
	if after := count(); after != before {
		t.Fatalf("rows changed: %s -> %s", before, after)
	}
}

func TestWebDigestOtherMethods(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	savePreview(t, st, "sub-1", week.Add(30*time.Hour))
	h := webServer(st, week.Add(time.Hour))
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		for _, path := range []string{weekPath, "/d/preview/2026-10-19"} {
			rec := doAs(h, m, path, "sub-1")
			wantBody(t, rec, 404, badBody)
			if rec.Header().Get("X-Robots-Tag") != "noindex" {
				t.Errorf("%s %s: X-Robots-Tag %q", m, path, rec.Header().Get("X-Robots-Tag"))
			}
		}
	}
}

// A path whose week is not a calendar day as YYYY-MM-DD gets the same 404
// as any bad link, and the digest headers once, before it reaches Postgres.
func TestWebDigestBadDate(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	h := webServer(st, week.Add(time.Hour))
	for _, date := range []string{"2026-02-30", "2026-10-19x", "19-10-2026", "2026-1-19", "%202026-10-19"} {
		for _, path := range []string{"/d/" + date, "/d/preview/" + date} {
			rec := doAs(h, "GET", path, "sub-1")
			wantBody(t, rec, 404, badBody)
			if got := rec.Header().Values("X-Robots-Tag"); len(got) != 1 || got[0] != "noindex" {
				t.Errorf("%s: X-Robots-Tag %q", path, got)
			}
			if got := rec.Header().Values("Cache-Control"); len(got) != 1 || got[0] != "private, no-store" {
				t.Errorf("%s: Cache-Control %q", path, got)
			}
		}
	}
}

func savePreview(t *testing.T, st *store.Store, subject string, expires time.Time) {
	t.Helper()
	d := digest.Digest{
		Stats:    digest.Stats{NewThreads: 2},
		Featured: &digest.Featured{Item: digest.Item{Key: digest.Key{Kind: digest.KindEntry, ID: 2}, Title: "kettles", URL: "https://talk.example.org/t/2", Magazine: digest.Magazine{Name: "coffee"}}},
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SavePreview(context.Background(), subject, week, b, expires); err != nil {
		t.Fatal(err)
	}
}

// The preview path opens the member's stored preview, not the week's digest,
// until the preview expires; the week's path still opens the week's digest.
func TestPreviewPage(t *testing.T) {
	st := storetest.New(t)
	saveDigest(t, st, "sub-1")
	expires := week.Add(30 * time.Hour)
	savePreview(t, st, "sub-1", expires)
	h := webServer(st, week.Add(24*time.Hour))
	const previewPath = "/d/preview/2026-10-19"
	rec := doAs(h, "GET", previewPath, "sub-1")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "kettles") || strings.Contains(rec.Body.String(), "grinders") {
		t.Fatalf("preview = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Robots-Tag") != "noindex" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("headers %v", rec.Header())
	}
	if rec := doAs(h, "HEAD", previewPath, "sub-1"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = %d", rec.Code)
	}
	rec = doAs(h, "GET", weekPath, "sub-1")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "grinders") || strings.Contains(rec.Body.String(), "kettles") {
		t.Fatalf("week = %d", rec.Code)
	}
	wantBody(t, doAs(webServer(st, expires), "GET", previewPath, "sub-1"), 404, badBody)
	// A member with no preview finds nothing, even though their week's
	// digest is stored, and another member's preview is not theirs.
	saveDigest(t, st, "sub-2")
	wantBody(t, doAs(h, "GET", previewPath, "sub-2"), 404, badBody)
	wantBody(t, doAs(h, "GET", "/d/preview/2026-10-12", "sub-1"), 404, badBody)
}
