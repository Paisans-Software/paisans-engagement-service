package welcome

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mbin"
	"github.com/paisans-software/paisans-engagement-service/internal/token"
)

// welcomeOne posts one Mbin newcomer's thread, member id 900.
func welcomeOne(t *testing.T, r *Runner, fm *fakeMbin) {
	t.Helper()
	ctx := context.Background()
	if err := r.MbinBatch(ctx, t0.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	fm.newcomers = []mbin.Newcomer{newcomer(900, t0.Add(-time.Hour))}
	if err := r.MbinBatch(ctx, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestEmailWaitsForSendHour(t *testing.T) {
	r, fm, _, _, rec := newRunner(t)
	welcomeOne(t, r, fm)
	fm.ranked = ranked(3)
	early := time.Date(2026, 10, 29, 8, 59, 0, 0, time.UTC)
	if err := r.DailyEmail(context.Background(), early); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.Sent()); n != 0 {
		t.Fatalf("sent %d before 09:00", n)
	}
	if err := r.DailyEmail(context.Background(), t0); err != nil {
		t.Fatal(err)
	}
	m := rec.Sent()
	if len(m) != 3 || m[0].Subject != "Say hello to 1 new person on Riverside" ||
		!strings.Contains(m[0].Text, "https://forum.example.org/m/welcome/t/101/welcome") {
		t.Fatalf("sent %+v", m)
	}
	if !strings.HasPrefix(m[0].Unsubscribe, "https://e.example.org/u/w.") {
		t.Fatalf("unsubscribe = %q", m[0].Unsubscribe)
	}
	p, sub, ok := token.VerifyFor(r.Key, strings.TrimPrefix(m[0].Unsubscribe, "https://e.example.org/u/"))
	if !ok || p != token.Welcome || sub != "m-1" {
		t.Fatalf("token = %v %q %v", p, sub, ok)
	}
}

func TestEmailSkipsUnsubscribedAndNewcomers(t *testing.T) {
	r, fm, _, _, rec := newRunner(t)
	welcomeOne(t, r, fm)
	fm.ranked = ranked(3)
	fm.ranked[1].ID = 900 // the newcomer is a member too
	fm.commenters = map[int64]map[int64]bool{101: {3: true}}
	if err := r.Store.UnsubscribeWelcome(context.Background(), "m-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.DailyEmail(context.Background(), t0); err != nil {
		t.Fatal(err)
	}
	if m := rec.Sent(); len(m) != 0 {
		t.Fatalf("sent to %v; want nobody", m)
	}
}

// A copy that stops part way, and starts again the same day, sends to the
// members left and no more than the limit in all.
func TestEmailRestartKeepsCap(t *testing.T) {
	r, fm, _, _, rec := newRunner(t)
	r.DailyLimit = 5
	welcomeOne(t, r, fm)
	fm.ranked = ranked(20)
	rec.Err = nil
	failing := &failAfter{n: 2, next: rec}
	r.Sender = failing
	if err := r.DailyEmail(context.Background(), t0); err == nil {
		t.Fatal("no error from a relay that went away")
	}
	if n := len(rec.Sent()); n != 2 {
		t.Fatalf("sent %d before the relay went away", n)
	}
	// A new process: nothing kept in memory.
	r2 := *r
	r2.emailed = ""
	r2.Sender = rec
	if err := r2.DailyEmail(context.Background(), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, m := range rec.Sent() {
		seen[m.To]++
	}
	if len(rec.Sent()) != 5 || len(seen) != 5 {
		t.Fatalf("sent %d to %d people; want 5 and 5", len(rec.Sent()), len(seen))
	}
	if err := r2.DailyEmail(context.Background(), t0.Add(2*time.Minute)); err != nil || len(rec.Sent()) != 5 {
		t.Fatalf("a third run sent more: %d, %v", len(rec.Sent()), err)
	}
}

// failAfter passes n messages to next, then refuses as an unavailable relay.
type failAfter struct {
	n    int
	next mail.Sender
}

func (f *failAfter) Send(ctx context.Context, m mail.Message) error {
	if f.n == 0 {
		return mail.ErrRelayUnavailable
	}
	f.n--
	return f.next.Send(ctx, m)
}
