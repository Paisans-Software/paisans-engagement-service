package run_test

import (
	"context"
	"errors"
	"fmt"
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

func TestLoopRunsOncePerWeek(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	rec := &mailtest.Recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	now := func() time.Time { return week.Add(30 * time.Minute) }
	go func() {
		run.Loop(ctx, runner(s, src, rec), now, time.Millisecond)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if h := src.hots.Load(); h != 1 {
		t.Fatalf("Week read the source %d times", h)
	}
	if len(rec.Messages) != 3 {
		t.Fatalf("%d messages", len(rec.Messages))
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
	go func() {
		run.Loop(ctx, runner(s, src, &mailtest.Recorder{}), func() time.Time { return week.Add(-time.Hour) }, time.Millisecond)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if h := src.hots.Load(); h != 0 {
		t.Fatalf("Week ran %d times outside the day", h)
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
