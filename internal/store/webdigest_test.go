package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

func sameJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestSaveDigestKeepsFirst(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a, b := `{"title": "a"}`, `{"title": "b"}`
	got, err := s.SaveDigest(ctx, "sub-1", monday, []byte(a))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, a)
	got, err = s.SaveDigest(ctx, "sub-1", monday, []byte(b))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, a)
	got, ok, err := s.SavedDigest(ctx, "sub-1", monday)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	sameJSON(t, got, a)
	if _, ok, err := s.SavedDigest(ctx, "sub-2", monday); err != nil || ok {
		t.Fatalf("another subject: %v, %v", ok, err)
	}
}

func TestWebDigestExpires(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.SaveDigest(ctx, "sub-1", monday, []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	day := monday.Format("2006-01-02")
	got, ok, err := s.WebDigest(ctx, "sub-1", day, monday.Add(27*24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("day 27: %v, %v", ok, err)
	}
	sameJSON(t, got, `{"n":1}`)
	if _, ok, err := s.WebDigest(ctx, "sub-1", day, store.DigestExpiry(monday)); err != nil || ok {
		t.Fatalf("day 28: %v, %v", ok, err)
	}
	if _, ok, err := s.WebDigest(ctx, "sub-1", "2026-10-19", monday); err != nil || ok {
		t.Fatalf("another week: %v, %v", ok, err)
	}
	if n, err := s.PurgeDigests(ctx, monday.Add(27*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("early purge: %d, %v", n, err)
	}
	if n, err := s.PurgeDigests(ctx, store.DigestExpiry(monday)); err != nil || n != 1 {
		t.Fatalf("purge: %d, %v", n, err)
	}
	if _, ok, err := s.SavedDigest(ctx, "sub-1", monday); err != nil || ok {
		t.Fatalf("after purge: %v, %v", ok, err)
	}
}

func TestDigestsForWeek(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	prev := monday.AddDate(0, 0, -7)
	for _, c := range []struct {
		sub  string
		week time.Time
		body string
	}{{"sub-1", monday, `{"n":1}`}, {"sub-2", monday, `{"n":2}`}, {"sub-1", prev, `{"n":0}`}} {
		if _, err := s.SaveDigest(ctx, c.sub, c.week, []byte(c.body)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.DigestsForWeek(ctx, monday)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	sameJSON(t, got["sub-1"], `{"n":1}`)
	sameJSON(t, got["sub-2"], `{"n":2}`)
}

// A date that is not a date finds no digest; it is not a store error.
func TestWebDigestMalformedDate(t *testing.T) {
	s := storetest.New(t)
	for _, d := range []string{"2026-13-45", "not a date", ""} {
		if got, ok, err := s.WebDigest(context.Background(), "sub-1", d, monday); err != nil || ok || got != nil {
			t.Errorf("%q: %q %v %v", d, got, ok, err)
		}
	}
}

// Expiry is 28 calendar days on, at the same wall clock time across a change
// of daylight saving time.
func TestDigestExpiryKeepsWallClock(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	week := time.Date(2026, 3, 23, 8, 0, 0, 0, berlin)
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.SaveDigest(ctx, "sub-1", week, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	var exp time.Time
	if err := storetest.Conn(t, s).QueryRow(ctx, `SELECT expires_at FROM web_digests`).Scan(&exp); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 4, 20, 8, 0, 0, 0, berlin); !exp.Equal(want) {
		t.Fatalf("expires %v, want %v", exp.In(berlin), want)
	}
}

// A preview is readable until its own expiry, a later preview of the same
// week replaces it, and the purge deletes it once expired.
func TestPreviewDigest(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	day := monday.Format("2006-01-02")
	at := monday.Add(50 * time.Hour)
	if err := s.SavePreview(ctx, "sub-1", monday, []byte(`{"n":1}`), at); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePreview(ctx, "sub-1", monday, []byte(`{"n":2}`), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.PreviewDigest(ctx, "sub-1", day, at)
	if err != nil || !ok {
		t.Fatalf("before expiry: %v, %v", ok, err)
	}
	sameJSON(t, got, `{"n":2}`)
	if _, ok, err := s.PreviewDigest(ctx, "sub-1", day, at.Add(time.Hour)); err != nil || ok {
		t.Fatalf("at expiry: %v, %v", ok, err)
	}
	if _, ok, err := s.PreviewDigest(ctx, "sub-1", "not a date", at); err != nil || ok {
		t.Fatalf("bad date: %v, %v", ok, err)
	}
	if n, err := s.PurgeDigests(ctx, at); err != nil || n != 0 {
		t.Fatalf("early purge: %d, %v", n, err)
	}
	if n, err := s.PurgeDigests(ctx, at.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("purge: %d, %v", n, err)
	}
	if n := countPreviews(t, s); n != 0 {
		t.Fatalf("%d previews after purge", n)
	}
}

// A preview is not the member's digest for the week: the week's own reads
// never see it, and saving the week's digest is not blocked by it.
func TestPreviewIsNotTheWeek(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SavePreview(ctx, "sub-1", monday, []byte(`{"preview":true}`), monday.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.SavedDigest(ctx, "sub-1", monday); err != nil || ok {
		t.Fatalf("SavedDigest sees the preview: %v, %v", ok, err)
	}
	if got, err := s.DigestsForWeek(ctx, monday); err != nil || len(got) != 0 {
		t.Fatalf("DigestsForWeek sees the preview: %v, %v", got, err)
	}
	if _, ok, err := s.WebDigest(ctx, "sub-1", monday.Format("2006-01-02"), monday); err != nil || ok {
		t.Fatalf("WebDigest sees the preview: %v, %v", ok, err)
	}
	got, err := s.SaveDigest(ctx, "sub-1", monday, []byte(`{"real":true}`))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, `{"real":true}`)
}

func countPreviews(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := storetest.Conn(t, s).QueryRow(context.Background(), `SELECT count(*) FROM preview_digests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
