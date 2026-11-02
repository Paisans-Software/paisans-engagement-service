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
	if ok, _ := s.ClaimWelcomeSend(ctx, "m", day); !ok {
		t.Fatal("claim")
	}
	if err := s.FinishWelcomeSend(ctx, "m", day, store.StatusSent); err != nil {
		t.Fatal(err)
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

func TestAlertOncePerDayOnlyWhenRecorded(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	if sent, err := s.AlertSentToday(ctx, "matrix-post", wnow); err != nil || sent {
		t.Fatalf("before any alert = %v, %v", sent, err)
	}
	// Nothing delivered, nothing recorded: the slot is still free.
	if sent, _ := s.AlertSentToday(ctx, "matrix-post", wnow.Add(time.Hour)); sent {
		t.Fatal("an alert that was never recorded used up the day")
	}
	if err := s.RecordAlert(ctx, "matrix-post", wnow); err != nil {
		t.Fatal(err)
	}
	if sent, _ := s.AlertSentToday(ctx, "matrix-post", wnow.Add(time.Hour)); !sent {
		t.Fatal("a recorded alert did not hold the day")
	}
	if sent, _ := s.AlertSentToday(ctx, "mbin-post", wnow); sent {
		t.Fatal("another kind was held")
	}
	if sent, _ := s.AlertSentToday(ctx, "matrix-post", wnow.AddDate(0, 0, 1)); sent {
		t.Fatal("the next day was held")
	}
}

func TestPendingWelcomeGetsItsEntry(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	p := mbinWelcomed(1, wnow)
	p.EntryID, p.URL = 0, ""
	if err := s.RecordWelcomed(ctx, []store.Welcomed{p, mbinWelcomed(2, wnow)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PendingWelcomes(ctx)
	if err != nil || len(got) != 1 || got[0].MbinID != 1 {
		t.Fatalf("pending = %+v, %v", got, err)
	}
	if subs, _ := s.PendingSubscriptions(ctx); len(subs) != 1 || subs[0].MbinID != 2 {
		t.Fatalf("a newcomer without a thread is due a subscription: %+v", subs)
	}
	if c, _ := s.ClaimWelcomesForDay(ctx, wnow); len(c) != 1 {
		t.Fatalf("a newcomer without a thread was claimed for the email: %+v", c)
	}
	if err := s.SetWelcomeEntry(ctx, []string{"1"}, 77, "https://forum.example.org/m/welcome/t/77/-"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PendingWelcomes(ctx); len(got) != 0 {
		t.Fatalf("still pending: %+v", got)
	}
}

// A day whose email reached nobody hands its newcomers on to the next day;
// one that reached someone keeps them.
func TestClaimRollsOverAnUnsentDay(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	if err := s.RecordWelcomed(ctx, []store.Welcomed{mbinWelcomed(1, wnow)}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.ClaimWelcomesForDay(ctx, wnow); len(c) != 1 {
		t.Fatal("not claimed")
	}
	next := wnow.AddDate(0, 0, 1)
	if c, err := s.ClaimWelcomesForDay(ctx, next); err != nil || len(c) != 1 {
		t.Fatalf("next day after an unsent day = %+v, %v; want the newcomer again", c, err)
	}
	if ok, _ := s.ClaimWelcomeSend(ctx, "m", next); !ok {
		t.Fatal("claim")
	}
	if err := s.FinishWelcomeSend(ctx, "m", next, store.StatusSent); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.ClaimWelcomesForDay(ctx, next.AddDate(0, 0, 1)); len(c) != 0 {
		t.Fatalf("a newcomer already mailed came back: %+v", c)
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
	if err := s.RecordAlert(ctx, "k", old); err != nil {
		t.Fatal(err)
	}
	unsubscribed := mbinWelcomed(3, old)
	if err := s.RecordWelcomed(ctx, []store.Welcomed{unsubscribed}); err != nil {
		t.Fatal(err)
	}
	if err := s.WelcomeSubscribed(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneWelcome(ctx, wnow); err != nil {
		t.Fatal(err)
	}
	acc, _ := s.WelcomedAccounts(ctx, store.PlatformMbin)
	if len(acc) != 2 || !acc["2"] || !acc["3"] {
		t.Fatalf("accounts after prune = %v; want 2, and 3 kept until subscribed", acc)
	}
	if st, _ := s.WelcomeStart(ctx, store.PlatformMbin, wnow); !st.Equal(old) {
		t.Fatalf("start after prune = %v", st)
	}
	if u, _ := s.WelcomeUnsubscribed(ctx); !u["gone"] {
		t.Fatal("unsubscribe pruned")
	}
	if sent, _ := s.AlertSentToday(ctx, "k", old); sent {
		t.Fatal("the pruned alert still holds its day")
	}
}
