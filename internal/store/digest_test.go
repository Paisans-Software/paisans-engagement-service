package store_test

import (
	"context"
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
		if err := s.Unsubscribe(ctx, "sub-1"); err != nil {
			t.Fatalf("unsubscribe %d: %v", i, err)
		}
	}
	if got, err := s.Unsubscribed(ctx, "sub-1"); err != nil || !got {
		t.Fatalf("after: got %v, %v", got, err)
	}
	if got, _ := s.Unsubscribed(ctx, "sub-2"); got {
		t.Fatal("another subject reads as unsubscribed")
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
	if _, err := s.Claim(ctx, "sub-1", monday); err != nil {
		t.Fatal(err)
	}
	for _, st := range []store.Status{store.StatusSent, store.StatusEmpty, store.StatusFailed, store.StatusUnsubscribed} {
		if err := s.Finish(ctx, "sub-1", monday, st); err != nil {
			t.Fatalf("finish %s: %v", st, err)
		}
		var got, week string
		c := storetest.Conn(t, s)
		if err := c.QueryRow(ctx, `SELECT status, week::text FROM sends WHERE subject='sub-1'`).Scan(&got, &week); err != nil {
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
