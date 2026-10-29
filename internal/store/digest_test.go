package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

var monday = time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)

func TestUnsubscribeIsIdempotent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if got, err := s.Unsubscribed(ctx, "sub-1"); err != nil || got {
		t.Fatalf("before: got %v, %v", got, err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Unsubscribe(ctx, "sub-1", false); err != nil {
			t.Fatalf("unsubscribe %d: %v", i, err)
		}
	}
	if got, err := s.Unsubscribed(ctx, "sub-1"); err != nil || !got {
		t.Fatalf("after: got %v, %v", got, err)
	}
	if got, err := s.Unsubscribed(ctx, "sub-2"); err != nil || got {
		t.Fatalf("another subject: got %v, %v", got, err)
	}
}

func TestClaimOnlyOnce(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if ok, err := s.Claim(ctx, "sub-1", monday); err != nil || !ok {
		t.Fatalf("first claim: %v, %v", ok, err)
	}
	if ok, err := s.Claim(ctx, "sub-1", monday); err != nil || ok {
		t.Fatalf("second claim: %v, %v", ok, err)
	}
	// A row written over a separate connection is seen as already claimed.
	c := storetest.Conn(t, s)
	if _, err := c.Exec(ctx, `INSERT INTO sends (subject, week, status) VALUES ('sub-2', '2026-10-26', 'sent')`); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Claim(ctx, "sub-2", monday); err != nil || ok {
		t.Fatalf("claim over a row from another connection: %v, %v", ok, err)
	}
}

func TestClaimNewWeek(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if ok, _ := s.Claim(ctx, "sub-1", monday); !ok {
		t.Fatal("first week not claimed")
	}
	if ok, err := s.Claim(ctx, "sub-1", monday.AddDate(0, 0, 7)); err != nil || !ok {
		t.Fatalf("next week: %v, %v", ok, err)
	}
}

func TestFinishRecordsStatus(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	c := storetest.Conn(t, s)
	for _, st := range []store.Status{store.StatusSent, store.StatusEmpty, store.StatusFailed, store.StatusUnsubscribed} {
		sub := "sub-" + string(st)
		if _, err := s.Claim(ctx, sub, monday); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, sub, monday, st); err != nil {
			t.Fatalf("finish %s: %v", st, err)
		}
		var got, week string
		if err := c.QueryRow(ctx, `SELECT status, week::text FROM sends WHERE subject=$1`, sub).Scan(&got, &week); err != nil {
			t.Fatal(err)
		}
		if got != string(st) || week != "2026-10-26" {
			t.Fatalf("got %q on %s, want %q on 2026-10-26", got, week, st)
		}
	}
	if err := s.Finish(ctx, "nobody", monday, store.StatusSent); err == nil {
		t.Fatal("finishing a send that was never claimed should fail")
	}
}

// A finished send keeps the status it ended with: a second Finish fails
// rather than rewriting what happened.
func TestFinishOnlyOnce(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.Claim(ctx, "sub-1", monday); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, "sub-1", monday, store.StatusSent); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, "sub-1", monday, store.StatusFailed); err == nil {
		t.Fatal("a second finish succeeded")
	}
	var got string
	if err := storetest.Conn(t, s).QueryRow(ctx, `SELECT status FROM sends WHERE subject='sub-1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != string(store.StatusSent) {
		t.Fatalf("status became %q", got)
	}
}

// Many claims at once, as from several replicas, have exactly one winner.
func TestClaimConcurrent(t *testing.T) {
	s := storetest.New(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Claim(context.Background(), "sub-1", monday)
			errs[i] = err
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if n := wins.Load(); n != 1 {
		t.Fatalf("%d claims won", n)
	}
}

// The week is the send Monday's own date, also where 08:00 local falls on
// another time in UTC: the Sunday before in Kiritimati (UTC+14), and 15:00
// the same Monday in Los Angeles (UTC-7).
func TestClaimStoresLocalDate(t *testing.T) {
	for _, zone := range []string{"Pacific/Kiritimati", "America/Los_Angeles"} {
		t.Run(zone, func(t *testing.T) {
			loc, err := time.LoadLocation(zone)
			if err != nil {
				t.Skip(err)
			}
			s := storetest.New(t)
			at := time.Date(2026, 10, 26, 8, 0, 0, 0, loc)
			if ok, err := s.Claim(context.Background(), "sub-1", at); err != nil || !ok {
				t.Fatalf("claim: %v, %v", ok, err)
			}
			var week string
			if err := storetest.Conn(t, s).QueryRow(context.Background(),
				`SELECT week::text FROM sends WHERE subject='sub-1'`).Scan(&week); err != nil {
				t.Fatal(err)
			}
			if week != "2026-10-26" {
				t.Fatalf("stored %s, want 2026-10-26 (UTC is %s)", week, at.UTC().Format(time.DateTime))
			}
		})
	}
}

func TestReleaseOnlyPending(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	for _, sub := range []string{"sub-1", "sub-2"} {
		if _, err := s.Claim(ctx, sub, monday); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Finish(ctx, "sub-2", monday, store.StatusSent); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"sub-1", "sub-2"} {
		if err := s.Release(ctx, sub, monday); err != nil {
			t.Fatalf("release %s: %v", sub, err)
		}
	}
	// The released send can be claimed again; the one already sent cannot.
	if ok, err := s.Claim(ctx, "sub-1", monday); err != nil || !ok {
		t.Fatalf("reclaim after release: %v, %v", ok, err)
	}
	if ok, err := s.Claim(ctx, "sub-2", monday); err != nil || ok {
		t.Fatalf("a sent row was released: %v, %v", ok, err)
	}
}

func TestStoreErrorsNameNoSubject(t *testing.T) {
	s := storetest.New(t)
	err := s.Finish(context.Background(), "secret-subject", monday, store.StatusSent)
	if err == nil || strings.Contains(err.Error(), "secret-subject") {
		t.Fatalf("Finish error: %v", err)
	}
}

// A sent digest records the threads it showed, for that member alone, and a
// later week's record adds to it.
func TestFinishRecordsShown(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a, b := store.Item{Kind: "entry", ID: 1}, store.Item{Kind: "post", ID: 1}
	for i, shown := range [][]store.Item{{a, b}, {a}} {
		week := monday.AddDate(0, 0, 7*i)
		if _, err := s.Claim(ctx, "sub-1", week); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, "sub-1", week, store.StatusSent, shown...); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Shown(ctx, "sub-1", monday.AddDate(0, 0, 14))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[a] || !got[b] {
		t.Fatalf("got %v", got)
	}
	if got, err := s.Shown(ctx, "sub-2", monday.AddDate(0, 0, 14)); err != nil || len(got) != 0 {
		t.Fatalf("another subject: %v, %v", got, err)
	}
}

// A Finish that fails records nothing it was given.
func TestFinishFailureRecordsNoShown(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.Finish(ctx, "sub-1", monday, store.StatusSent, store.Item{Kind: "entry", ID: 1}); err == nil {
		t.Fatal("finishing a send that was never claimed should fail")
	}
	if got, err := s.Shown(ctx, "sub-1", monday.AddDate(0, 0, 14)); err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestUnsubscribeForgetsShown(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	for _, sub := range []string{"sub-1", "sub-2"} {
		if _, err := s.Claim(ctx, sub, monday); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, sub, monday, store.StatusSent, store.Item{Kind: "entry", ID: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Unsubscribe(ctx, "sub-1", false); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Shown(ctx, "sub-1", monday.AddDate(0, 0, 14)); err != nil || len(got) != 0 {
		t.Fatalf("unsubscribed: %v, %v", got, err)
	}
	if got, err := s.Shown(ctx, "sub-2", monday.AddDate(0, 0, 14)); err != nil || len(got) != 1 {
		t.Fatalf("another subject: %v, %v", got, err)
	}
}

func TestShownBeforeWeek(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	old, recent := store.Item{Kind: "entry", ID: 1}, store.Item{Kind: "entry", ID: 2}
	for i, it := range []store.Item{old, recent} {
		week := monday.AddDate(0, 0, 7*i-7)
		if _, err := s.Claim(ctx, "sub-1", week); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, "sub-1", week, store.StatusSent, it); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Shown(ctx, "sub-1", monday)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[old] {
		t.Fatalf("got %v", got)
	}
}
