package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

var wnow = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

func TestStartedPerPlatform(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	a, err := s.WelcomeStart(ctx, store.PlatformMatrix, wnow)
	if err != nil || !a.Equal(wnow) {
		t.Fatalf("first start = %v, %v", a, err)
	}
	b, err := s.WelcomeStart(ctx, store.PlatformMatrix, wnow.Add(time.Hour))
	if err != nil || !b.Equal(wnow) {
		t.Fatalf("second start = %v, %v; want the first", b, err)
	}
	later := wnow.AddDate(0, 1, 0)
	c, err := s.WelcomeStart(ctx, store.PlatformMbin, later)
	if err != nil || !c.Equal(later) {
		t.Fatalf("mbin start = %v, %v; want its own", c, err)
	}
}

func mbinWelcomed(id int64, at time.Time) store.Welcomed {
	return store.Welcomed{Platform: store.PlatformMbin, Account: store.MbinAccount(id), Name: "u", MbinID: id,
		EntryID: 100, URL: "https://forum.example.org/m/welcome/t/100/-", At: at}
}

func TestClaimWelcomesForDayIsStable(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	if err := s.RecordWelcomed(ctx, []store.Welcomed{mbinWelcomed(1, wnow), mbinWelcomed(2, wnow),
		{Platform: store.PlatformMatrix, Account: "01MAS", Name: "m", At: wnow}}); err != nil {
		t.Fatal(err)
	}
	day := wnow
	a, err := s.ClaimWelcomesForDay(ctx, day)
	if err != nil || len(a) != 2 {
		t.Fatalf("first claim = %v, %v", a, err)
	}
	if err := s.RecordWelcomed(ctx, []store.Welcomed{mbinWelcomed(3, wnow.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	b, err := s.ClaimWelcomesForDay(ctx, day)
	if err != nil || len(b) != 2 {
		t.Fatalf("second claim = %v, %v; want the same two", b, err)
	}
	c, err := s.ClaimWelcomesForDay(ctx, day.AddDate(0, 0, 1))
	if err != nil || len(c) != 1 || c[0].MbinID != 3 {
		t.Fatalf("next day = %v, %v; want the one recorded later", c, err)
	}
	acc, err := s.WelcomedAccounts(ctx, store.PlatformMbin)
	if err != nil || len(acc) != 3 || !acc["1"] {
		t.Fatalf("accounts = %v, %v", acc, err)
	}
}

func TestPendingSubscriptions(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	if err := s.RecordWelcomed(ctx, []store.Welcomed{mbinWelcomed(1, wnow), mbinWelcomed(2, wnow)}); err != nil {
		t.Fatal(err)
	}
	if err := s.WelcomeSubscribed(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	p, err := s.PendingSubscriptions(ctx)
	if err != nil || len(p) != 1 || p[0].MbinID != 2 {
		t.Fatalf("pending = %v, %v", p, err)
	}
}

func TestWelcomeSendClaimOnce(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	ok, err := s.ClaimWelcomeSend(ctx, "sub", wnow)
	if err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	if ok, _ := s.ClaimWelcomeSend(ctx, "sub", wnow); ok {
		t.Fatal("second claim won")
	}
	if err := s.FinishWelcomeSend(ctx, "sub", wnow, store.StatusSent); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseWelcomeSend(ctx, "sub", wnow); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ClaimWelcomeSend(ctx, "sub", wnow); ok {
		t.Fatal("a finished send was released")
	}
	on, err := s.WelcomeSendsOn(ctx, wnow)
	if err != nil || !on["sub"] || len(on) != 1 {
		t.Fatalf("sends on = %v, %v", on, err)
	}
}

func TestAlertDueOncePerDay(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	for i, want := range []bool{true, false} {
		if got, err := s.AlertDue(ctx, "matrix-post", wnow.Add(time.Duration(i)*time.Hour)); err != nil || got != want {
			t.Fatalf("call %d = %v, %v", i, got, err)
		}
	}
	if got, _ := s.AlertDue(ctx, "mbin-post", wnow); !got {
		t.Fatal("another kind was not due")
	}
	if got, _ := s.AlertDue(ctx, "matrix-post", wnow.AddDate(0, 0, 1)); !got {
		t.Fatal("the next day was not due")
	}
}

func TestPruneWelcomeKeepsStartsAndUnsubscribes(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	old := wnow.Add(-store.WelcomeKeep - time.Hour)
	if _, err := s.WelcomeStart(ctx, store.PlatformMbin, old); err != nil {
		t.Fatal(err)
	}
	if err := s.UnsubscribeWelcome(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordWelcomed(ctx, []store.Welcomed{mbinWelcomed(1, old), mbinWelcomed(2, wnow)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AlertDue(ctx, "k", old); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneWelcome(ctx, wnow); err != nil {
		t.Fatal(err)
	}
	acc, _ := s.WelcomedAccounts(ctx, store.PlatformMbin)
	if len(acc) != 1 || !acc["2"] {
		t.Fatalf("accounts after prune = %v", acc)
	}
	if st, _ := s.WelcomeStart(ctx, store.PlatformMbin, wnow); !st.Equal(old) {
		t.Fatalf("start after prune = %v", st)
	}
	if u, _ := s.WelcomeUnsubscribed(ctx); !u["gone"] {
		t.Fatal("unsubscribe pruned")
	}
	if due, _ := s.AlertDue(ctx, "k", old); !due {
		t.Fatal("the pruned alert still holds its day")
	}
}
