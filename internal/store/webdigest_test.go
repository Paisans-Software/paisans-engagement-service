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
	if _, ok, err := s.WebDigest(ctx, "sub-1", day, monday.Add(store.DigestLifetime)); err != nil || ok {
		t.Fatalf("day 28: %v, %v", ok, err)
	}
	if _, ok, err := s.WebDigest(ctx, "sub-1", "2026-10-19", monday); err != nil || ok {
		t.Fatalf("another week: %v, %v", ok, err)
	}
	if n, err := s.PurgeDigests(ctx, monday.Add(27*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("early purge: %d, %v", n, err)
	}
	if n, err := s.PurgeDigests(ctx, monday.Add(store.DigestLifetime)); err != nil || n != 1 {
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
