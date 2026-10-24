package run_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/digest"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mail/mailtest"
	"github.com/paisans-software/paisans-engagement-service/internal/mbin"
	"github.com/paisans-software/paisans-engagement-service/internal/run"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
	"github.com/paisans-software/paisans-engagement-service/internal/token"
)

var _ run.Source = (*mbin.Source)(nil)

var (
	week = time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	key  = []byte("test-key-test-key-test-key-test!")
)

// fakeSource serves three members and one thread. A member whose subject is
// in joined has already joined the thread, so their digest is empty.
type fakeSource struct {
	members []digest.Member
	joined  map[string]bool
	hotErr  error
	hots    atomic.Int32
	// viewErr fails View for the member with that subject.
	viewErr map[string]error
	// ready, when set, holds every runner in Members until all have reached
	// it, so concurrent runs overlap rather than one finishing first.
	ready *sync.WaitGroup
}

func newSource() *fakeSource {
	return &fakeSource{members: []digest.Member{
		{ID: 1, Subject: "sub-1", Email: "one@example.org", Username: "one"},
		{ID: 2, Subject: "sub-2", Email: "two@example.org", Username: "two"},
		{ID: 3, Subject: "sub-3", Email: "three@example.org", Username: "three"},
	}, joined: map[string]bool{}}
}

var thread = digest.Item{
	Key: digest.Key{Kind: digest.KindEntry, ID: 7}, Title: "grinders",
	URL:      "https://talk.example.org/m/coffee/t/7",
	Magazine: digest.Magazine{ID: 1, Name: "coffee", Title: "Coffee"},
	AuthorID: 99, Comments: 4, Participants: 3, Score: 9,
}

func (f *fakeSource) Members(context.Context) ([]digest.Member, error) {
	if f.ready != nil {
		f.ready.Done()
		f.ready.Wait()
	}
	return f.members, nil
}

func (f *fakeSource) Hot(_ context.Context, from, to time.Time) ([]digest.Item, error) {
	f.hots.Add(1)
	if !from.Equal(week.AddDate(0, 0, -7)) || !to.Equal(week) {
		return nil, errors.New("wrong window")
	}
	if f.hotErr != nil {
		return nil, f.hotErr
	}
	return []digest.Item{thread}, nil
}

func (f *fakeSource) View(_ context.Context, m digest.Member, _, _ time.Time, _ []digest.Item) (digest.View, error) {
	if err := f.viewErr[m.Subject]; err != nil {
		return digest.View{}, err
	}
	v := digest.View{Joined: map[digest.Key]bool{}}
	if f.joined[m.Subject] {
		v.Joined[thread.Key] = true
	}
	return v, nil
}

func (f *fakeSource) Stats(context.Context, time.Time, time.Time) (digest.Stats, error) {
	return digest.Stats{NewThreads: 1, Comments: 4}, nil
}

func runner(s *store.Store, src run.Source, snd mail.Sender) *run.Runner {
	return &run.Runner{
		Store: s, Source: src, Sender: snd,
		Names:   mail.Names{Instance: "Talk", Meta: "Talk · Example Community"},
		SiteURL: "https://talk.example.org/", PublicURL: "https://digest.example.org", Key: key,
	}
}

// statuses reads every send row for the week, by subject.
func statuses(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	rows, err := storetest.Conn(t, s).Query(context.Background(),
		`SELECT subject, status FROM sends WHERE week = '2026-10-26'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var sub, st string
		if err := rows.Scan(&sub, &st); err != nil {
			t.Fatal(err)
		}
		got[sub] = st
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func countTo(msgs []mail.Message) map[string]int {
	n := map[string]int{}
	for _, m := range msgs {
		n[m.To]++
	}
	return n
}

func TestWeekSendsOncePerMember(t *testing.T) {
	s := storetest.New(t)
	rec := &mailtest.Recorder{}
	r := runner(s, newSource(), rec)
	for i := 0; i < 2; i++ {
		if err := r.Week(context.Background(), week); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	n := countTo(rec.Messages)
	if len(rec.Messages) != 3 || n["one@example.org"] != 1 || n["two@example.org"] != 1 || n["three@example.org"] != 1 {
		t.Fatalf("messages by recipient: %v", n)
	}
	for sub, st := range statuses(t, s) {
		if st != string(store.StatusSent) {
			t.Fatalf("%s: %s", sub, st)
		}
	}
}

func TestTwoRunnersOneStore(t *testing.T) {
	s := storetest.New(t)
	var members []digest.Member
	for i := 1; i <= 50; i++ {
		members = append(members, digest.Member{ID: int64(i), Subject: fmt.Sprintf("sub-%d", i), Email: fmt.Sprintf("m%d@example.org", i)})
	}
	ready := &sync.WaitGroup{}
	ready.Add(2)
	srcA, srcB := newSource(), newSource()
	srcA.members, srcB.members = members, members
	srcA.ready, srcB.ready = ready, ready
	a, b := &mailtest.Recorder{}, &mailtest.Recorder{}
	ra, rb := runner(s, srcA, a), runner(s, srcB, b)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, r := range []*run.Runner{ra, rb} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = r.Week(context.Background(), week)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	n := countTo(append(append([]mail.Message{}, a.Messages...), b.Messages...))
	if len(n) != len(members) {
		t.Fatalf("recipients: %v", n)
	}
	for to, c := range n {
		if c != 1 {
			t.Fatalf("%s got %d messages", to, c)
		}
	}
}

func TestUnsubscribedGetsNothing(t *testing.T) {
	s := storetest.New(t)
	if err := s.Unsubscribe(context.Background(), "sub-2"); err != nil {
		t.Fatal(err)
	}
	rec := &mailtest.Recorder{}
	if err := runner(s, newSource(), rec).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if n := countTo(rec.Messages); n["two@example.org"] != 0 || len(rec.Messages) != 2 {
		t.Fatalf("messages by recipient: %v", n)
	}
	if st := statuses(t, s)["sub-2"]; st != string(store.StatusUnsubscribed) {
		t.Fatalf("status %q", st)
	}
}

func TestEmptyDigestNotSent(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	src.joined["sub-3"] = true
	rec := &mailtest.Recorder{}
	if err := runner(s, src, rec).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if n := countTo(rec.Messages); n["three@example.org"] != 0 || len(rec.Messages) != 2 {
		t.Fatalf("messages by recipient: %v", n)
	}
	if st := statuses(t, s)["sub-3"]; st != string(store.StatusEmpty) {
		t.Fatalf("status %q", st)
	}
}

func TestSourceErrorClaimsNobody(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	src.hotErr = errors.New("mbin is down")
	rec := &mailtest.Recorder{}
	if err := runner(s, src, rec).Week(context.Background(), week); err == nil {
		t.Fatal("Week succeeded with a failing source")
	}
	if got := statuses(t, s); len(got) != 0 {
		t.Fatalf("sends rows: %v", got)
	}
	if len(rec.Messages) != 0 {
		t.Fatalf("%d messages sent", len(rec.Messages))
	}
}

// failFor fails the send to one address and records the rest.
type failFor struct {
	addr string
	mailtest.Recorder
}

func (f *failFor) Send(ctx context.Context, m mail.Message) error {
	if m.To == f.addr {
		return errors.New("send mail: mailbox unavailable (code 550)")
	}
	return f.Recorder.Send(ctx, m)
}

func TestOneFailureDoesNotStopOthers(t *testing.T) {
	s := storetest.New(t)
	snd := &failFor{addr: "one@example.org"}
	if err := runner(s, newSource(), snd).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if n := countTo(snd.Messages); len(snd.Messages) != 2 || n["two@example.org"] != 1 || n["three@example.org"] != 1 {
		t.Fatalf("messages by recipient: %v", n)
	}
	got := statuses(t, s)
	if got["sub-1"] != string(store.StatusFailed) || got["sub-2"] != string(store.StatusSent) || got["sub-3"] != string(store.StatusSent) {
		t.Fatalf("statuses: %v", got)
	}
}

func TestUnsubscribeLinkVerifies(t *testing.T) {
	s := storetest.New(t)
	rec := &mailtest.Recorder{}
	if err := runner(s, newSource(), rec).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if len(rec.Messages) == 0 {
		t.Fatal("nothing sent")
	}
	bySubject := map[string]string{"one@example.org": "sub-1", "two@example.org": "sub-2", "three@example.org": "sub-3"}
	for _, m := range rec.Messages {
		tok, ok := strings.CutPrefix(m.Unsubscribe, "https://digest.example.org/u/")
		if !ok {
			t.Fatalf("unsubscribe URL %q", m.Unsubscribe)
		}
		sub, ok := token.Verify(key, tok)
		if !ok || sub != bySubject[m.To] {
			t.Fatalf("token for %s names %q (ok %v)", m.To, sub, ok)
		}
		if !strings.Contains(m.HTML, m.Unsubscribe) {
			t.Fatal("the link is not in the body")
		}
	}
}

// clock is a fixed time that counts how often Loop reads it, which is once a
// tick, so a test can wait for ticks to pass rather than for time to.
type clock struct {
	at    time.Time
	reads atomic.Int32
}

func (c *clock) now() time.Time { c.reads.Add(1); return c.at }

// waitTicks returns once Loop has read the clock n more times.
func (c *clock) waitTicks(t *testing.T, n int32) {
	t.Helper()
	target := c.reads.Load() + n
	deadline := time.Now().Add(5 * time.Second)
	for c.reads.Load() < target {
		if time.Now().After(deadline) {
			t.Fatalf("Loop read the clock %d times, want %d", c.reads.Load(), target)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLoopRunsOncePerWeek(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	rec := &mailtest.Recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	c := &clock{at: week.Add(30 * time.Minute)}
	go func() {
		run.Loop(ctx, runner(s, src, rec), c.now, time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.Sent()) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("%d messages", len(rec.Sent()))
		}
		time.Sleep(time.Millisecond)
	}
	c.waitTicks(t, 5)
	if h := src.hots.Load(); h != 1 {
		t.Fatalf("Week read the source %d times", h)
	}
	if n := len(rec.Sent()); n != 3 {
		t.Fatalf("%d messages", n)
	}
}

func TestLoopRetriesAfterAnError(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	src.hotErr = errors.New("mbin is down")
	rec := &mailtest.Recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		run.Loop(ctx, runner(s, src, rec), func() time.Time { return week.Add(time.Hour) }, time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for src.hots.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("Loop tried %d times", src.hots.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLoopIdleOutsideTheDay(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	c := &clock{at: week.Add(-time.Hour)}
	go func() {
		run.Loop(ctx, runner(s, src, &mailtest.Recorder{}), c.now, time.Millisecond)
		close(done)
	}()
	c.waitTicks(t, 5)
	if h := src.hots.Load(); h != 0 {
		t.Fatalf("Week ran %d times outside the day", h)
	}
}

// A failing week is retried less and less often, so an outage that lasts
// all day does not re-read Mbin every tick.
func TestLoopBacksOffAfterFailures(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	src.hotErr = errors.New("mbin is down")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c := &clock{at: week.Add(time.Hour)}
	go func() {
		run.Loop(ctx, runner(s, src, &mailtest.Recorder{}), c.now, time.Millisecond)
		close(done)
	}()
	// Doubling from 1ms, the waits before the eighth attempt add up to
	// 127ms; at one attempt a tick there would be well over a hundred.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	if h := src.hots.Load(); h < 3 || h > 12 {
		t.Fatalf("Loop tried %d times in 200ms", h)
	}
}

// An outage that begins after the shared reads leaves the member unclaimed,
// so the next attempt reaches them, and nobody already mailed gets a second.
func TestViewErrorIsRetried(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	src.viewErr = map[string]error{"sub-2": errors.New("mbin is down")}
	rec := &mailtest.Recorder{}
	r := runner(s, src, rec)
	if err := r.Week(context.Background(), week); err == nil {
		t.Fatal("Week succeeded although a member's view could not be read")
	}
	got := statuses(t, s)
	if _, ok := got["sub-2"]; ok || len(got) != 2 {
		t.Fatalf("statuses after the failure: %v", got)
	}
	src.viewErr = nil
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	n := countTo(rec.Messages)
	if len(rec.Messages) != 3 || n["one@example.org"] != 1 || n["two@example.org"] != 1 || n["three@example.org"] != 1 {
		t.Fatalf("messages by recipient: %v", n)
	}
	if st := statuses(t, s)["sub-2"]; st != string(store.StatusSent) {
		t.Fatalf("status %q", st)
	}
}

// notSentFor refuses the send to addr as never handed over, with err.
type notSentFor struct {
	addr  string
	err   error
	tries atomic.Int32
	mailtest.Recorder
}

func (f *notSentFor) Send(ctx context.Context, m mail.Message) error {
	f.tries.Add(1)
	if f.addr == "" || m.To == f.addr {
		return f.err
	}
	return f.Recorder.Send(ctx, m)
}

// A relay outage stops the run at the first member and leaves everyone
// unclaimed, so the next attempt mails them all.
func TestRelayOutageIsRetried(t *testing.T) {
	s := storetest.New(t)
	snd := &notSentFor{err: fmt.Errorf("%w: could not connect", mail.ErrRelayUnavailable)}
	r := runner(s, newSource(), snd)
	if err := r.Week(context.Background(), week); !errors.Is(err, mail.ErrNotSent) {
		t.Fatalf("Week: %v, want ErrNotSent", err)
	}
	if n := snd.tries.Load(); n != 1 {
		t.Fatalf("tried the relay %d times during an outage", n)
	}
	if got := statuses(t, s); len(got) != 0 {
		t.Fatalf("rows after the outage: %v", got)
	}
	snd.err = nil
	snd.addr = "nobody@example.org"
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if n := countTo(snd.Messages); len(snd.Messages) != 3 || n["one@example.org"] != 1 {
		t.Fatalf("messages by recipient: %v", n)
	}
	for sub, st := range statuses(t, s) {
		if st != string(store.StatusSent) {
			t.Fatalf("%s: %s", sub, st)
		}
	}
}

// One recipient the relay would not take holds up nobody else, and is
// retried on the next attempt.
func TestRecipientRefusedIsRetried(t *testing.T) {
	s := storetest.New(t)
	snd := &notSentFor{addr: "one@example.org", err: fmt.Errorf("%w: RCPT TO (code 450)", mail.ErrNotSent)}
	r := runner(s, newSource(), snd)
	if err := r.Week(context.Background(), week); !errors.Is(err, mail.ErrNotSent) {
		t.Fatalf("Week: %v, want ErrNotSent", err)
	}
	got := statuses(t, s)
	if _, ok := got["sub-1"]; ok || got["sub-2"] != string(store.StatusSent) || got["sub-3"] != string(store.StatusSent) {
		t.Fatalf("statuses: %v", got)
	}
	snd.addr = "nobody@example.org"
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if n := countTo(snd.Messages); len(snd.Messages) != 3 || n["one@example.org"] != 1 {
		t.Fatalf("messages by recipient: %v", n)
	}
}

// A row another runner holds is left exactly as it is.
func TestPendingRowIsSkipped(t *testing.T) {
	s := storetest.New(t)
	c := storetest.Conn(t, s)
	if _, err := c.Exec(context.Background(),
		`INSERT INTO sends (subject, week, status, at) VALUES ('sub-2', '2026-10-26', 'pending', '2026-10-26 08:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	rec := &mailtest.Recorder{}
	if err := runner(s, newSource(), rec).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if n := countTo(rec.Messages); len(rec.Messages) != 2 || n["two@example.org"] != 0 {
		t.Fatalf("messages by recipient: %v", n)
	}
	var st, at string
	if err := c.QueryRow(context.Background(),
		`SELECT status, at::text FROM sends WHERE subject='sub-2'`).Scan(&st, &at); err != nil {
		t.Fatal(err)
	}
	if st != "pending" || at != "2026-10-26 08:00:00+00" {
		t.Fatalf("the held row became %s at %s", st, at)
	}
}

// A store that cannot take claims mails nobody and counts them unclaimed;
// once it recovers, the retry reaches everyone.
func TestClaimFailureIsRetried(t *testing.T) {
	s := storetest.New(t)
	c := storetest.Conn(t, s)
	if _, err := c.Exec(context.Background(), `ALTER TABLE sends RENAME TO sends_away`); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)
	rec := &mailtest.Recorder{}
	r := runner(s, newSource(), rec)
	if err := r.Week(context.Background(), week); err == nil {
		t.Fatal("Week succeeded without a store to claim in")
	}
	if len(rec.Messages) != 0 {
		t.Fatalf("%d messages sent unclaimed", len(rec.Messages))
	}
	if !strings.Contains(logs.String(), "unclaimed=3") || !strings.Contains(logs.String(), "failed=0") {
		t.Fatalf("summary: %s", logs.String())
	}
	if _, err := c.Exec(context.Background(), `ALTER TABLE sends_away RENAME TO sends`); err != nil {
		t.Fatal(err)
	}
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if len(rec.Messages) != 3 {
		t.Fatalf("%d messages after the store recovered", len(rec.Messages))
	}
}
