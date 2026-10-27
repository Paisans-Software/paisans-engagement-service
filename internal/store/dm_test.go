package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

func TestClaimDMOnce(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if ok, err := s.ClaimDM(ctx, "sub-1", monday); err != nil || !ok {
		t.Fatalf("first claim: %v, %v", ok, err)
	}
	if ok, err := s.ClaimDM(ctx, "sub-1", monday); err != nil || ok {
		t.Fatalf("second claim: %v, %v", ok, err)
	}
	if ok, err := s.ClaimDM(ctx, "sub-1", monday.AddDate(0, 0, 7)); err != nil || !ok {
		t.Fatalf("next week: %v, %v", ok, err)
	}
	// The email claim for the same member and week is independent.
	if ok, err := s.Claim(ctx, "sub-1", monday); err != nil || !ok {
		t.Fatalf("email claim after DM claim: %v, %v", ok, err)
	}
	if ok, err := s.ClaimDM(ctx, "sub-2", monday); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.Claim(ctx, "sub-2", monday); err != nil || !ok {
		t.Fatalf("email claim: %v, %v", ok, err)
	}
	if ok, err := s.ClaimDM(ctx, "sub-2", monday); err != nil || ok {
		t.Fatalf("DM claim again: %v, %v", ok, err)
	}
}

func TestFinishDMRecordsShown(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a, b := store.Item{Kind: "entry", ID: 1}, store.Item{Kind: "post", ID: 1}
	for i, shown := range [][]store.Item{{a, b}, {a}} {
		week := monday.AddDate(0, 0, 7*i)
		if _, err := s.ClaimDM(ctx, "sub-1", week); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishDM(ctx, "sub-1", week, store.StatusSent, shown...); err != nil {
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
	// A second finish is refused.
	if err := s.FinishDM(ctx, "sub-1", monday, store.StatusSent); err == nil {
		t.Fatal("finishing a finished DM send should fail")
	}
	// A failed send records no items.
	if _, err := s.ClaimDM(ctx, "sub-2", monday); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishDM(ctx, "sub-2", monday, store.StatusFailed); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Shown(ctx, "sub-2", monday.AddDate(0, 0, 14)); err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	// Never claimed: refused, nothing recorded, no subject in the error.
	err = s.FinishDM(ctx, "secret-subject", monday, store.StatusSent, a)
	if err == nil || strings.Contains(err.Error(), "secret-subject") {
		t.Fatalf("unclaimed finish: %v", err)
	}
	if got, err := s.Shown(ctx, "secret-subject", monday.AddDate(0, 0, 14)); err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestReleaseDMOnlyPending(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	for _, sub := range []string{"sub-1", "sub-2"} {
		if _, err := s.ClaimDM(ctx, sub, monday); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishDM(ctx, "sub-2", monday, store.StatusSent); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"sub-1", "sub-2"} {
		if err := s.ReleaseDM(ctx, sub, monday); err != nil {
			t.Fatalf("release %s: %v", sub, err)
		}
	}
	if ok, err := s.ClaimDM(ctx, "sub-1", monday); err != nil || !ok {
		t.Fatalf("reclaim after release: %v, %v", ok, err)
	}
	if ok, err := s.ClaimDM(ctx, "sub-2", monday); err != nil || ok {
		t.Fatalf("a sent row was released: %v, %v", ok, err)
	}
}

// A pending claim older than 15 minutes is one whose sender stopped before
// finishing it; it is claimed again, and its time reset.
func TestClaimDMReclaimsStalePending(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	c := storetest.Conn(t, s)
	for _, sub := range []string{"stale", "fresh", "sent"} {
		if _, err := s.ClaimDM(ctx, sub, monday); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishDM(ctx, "sent", monday, store.StatusSent); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `UPDATE dm_sends SET at = now() - interval '16 minutes' WHERE subject IN ('stale', 'sent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `UPDATE dm_sends SET at = now() - interval '5 minutes' WHERE subject = 'fresh'`); err != nil {
		t.Fatal(err)
	}
	for sub, want := range map[string]bool{"stale": true, "fresh": false, "sent": false} {
		if ok, err := s.ClaimDM(ctx, sub, monday); err != nil || ok != want {
			t.Errorf("%s: claimed %v (%v), want %v", sub, ok, err, want)
		}
	}
	var recent bool
	if err := c.QueryRow(ctx, `SELECT at > now() - interval '1 minute' FROM dm_sends WHERE subject = 'stale'`).Scan(&recent); err != nil || !recent {
		t.Fatalf("reclaimed row's time not reset: %v %v", recent, err)
	}
	if ok, err := s.ClaimDM(ctx, "stale", monday); err != nil || ok {
		t.Fatalf("a fresh reclaim was claimed again: %v %v", ok, err)
	}
}
