package run_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
	"github.com/paisans-software/paisans-engagement-service/internal/digest"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mail/mailtest"
	"github.com/paisans-software/paisans-engagement-service/internal/run"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
	"github.com/paisans-software/paisans-engagement-service/internal/token"
)

var _ run.DM = (*bot.Bot)(nil)

type delivery struct {
	Subject, TxnID string
	Notice         bot.Notice
}

// fakeDM records every delivery. Subjects in skip are not messageable;
// outcome and err script what Deliver returns for a subject; prepareErr
// fails Prepare for a subject; block makes Deliver wait for its context;
// after, when set, is called after each delivery.
type fakeDM struct {
	mu         sync.Mutex
	skip       map[string]bool
	outcome    map[string]bot.Outcome
	err        map[string]error
	prepareErr map[string]error
	block      bool
	prepared   []string
	delivered  []delivery
	after      func(subject string)
}

func newDM() *fakeDM {
	return &fakeDM{skip: map[string]bool{}, outcome: map[string]bot.Outcome{}, err: map[string]error{}, prepareErr: map[string]error{}}
}

func (f *fakeDM) Prepare(_ context.Context, subject string) (bot.Target, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, subject)
	if err := f.prepareErr[subject]; err != nil {
		return bot.Target{}, false, err
	}
	if f.skip[subject] {
		return bot.Target{}, false, nil
	}
	return bot.Target{Subject: subject, UserID: "@" + subject + ":example.org"}, true, nil
}

func (f *fakeDM) Deliver(ctx context.Context, t bot.Target, txnID string, n bot.Notice) (bot.Outcome, error) {
	if f.block {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err[t.Subject]; err != nil {
		return 0, err
	}
	f.delivered = append(f.delivered, delivery{t.Subject, txnID, n})
	if f.after != nil {
		f.after(t.Subject)
	}
	if o, ok := f.outcome[t.Subject]; ok {
		return o, nil
	}
	return bot.Sent, nil
}

func (f *fakeDM) deliveries() []delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]delivery(nil), f.delivered...)
}

func (f *fakeDM) subjects() []string {
	var out []string
	for _, d := range f.deliveries() {
		out = append(out, d.Subject)
	}
	return out
}

// onSendDay is an hour after the send time of the shared test week.
func onSendDay() time.Time { return week.Add(time.Hour) }

// matrixRunner is runner with the Digest Bot configured and dm running.
func matrixRunner(s *store.Store, src run.Source, snd mail.Sender, dm run.DM) *run.Runner {
	r := runner(s, src, snd)
	r.MatrixConfigured = true
	r.Matrix = func() run.DM { return dm }
	r.Now = onSendDay
	return r
}

// dmStatuses reads every Matrix send row for the week date, by subject.
func dmStatuses(t *testing.T, s *store.Store, date string) map[string]string {
	t.Helper()
	rows, err := storetest.Conn(t, s).Query(context.Background(),
		`SELECT subject, status FROM dm_sends WHERE week = $1::date`, date)
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

func countRows(t *testing.T, s *store.Store, query string) int {
	t.Helper()
	var n int
	if err := storetest.Conn(t, s).QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// link is the web digest link in a notice.
func link(t *testing.T, n bot.Notice) string {
	t.Helper()
	for _, line := range strings.Split(n.Body, "\n") {
		if l, ok := strings.CutPrefix(line, "Read the whole digest: "); ok {
			return l
		}
	}
	t.Fatalf("no link in %q", n.Body)
	return ""
}

func TestDMSentOnceInSameRun(t *testing.T) {
	s := storetest.New(t)
	rec, dm := &mailtest.Recorder{}, newDM()
	r := matrixRunner(s, newSource(), rec, dm)
	for i := 0; i < 2; i++ {
		if err := r.Week(context.Background(), week); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(rec.Messages) != 3 {
		t.Fatalf("%d emails", len(rec.Messages))
	}
	got := dm.deliveries()
	if len(got) != 3 {
		t.Fatalf("deliveries: %v", dm.subjects())
	}
	for _, d := range got {
		if d.TxnID != bot.TxnID(d.Subject, week) {
			t.Fatalf("txn id %q for %s", d.TxnID, d.Subject)
		}
	}
	st := dmStatuses(t, s, "2026-10-26")
	if len(st) != 3 {
		t.Fatalf("dm_sends: %v", st)
	}
	for sub, v := range st {
		if v != string(store.StatusSent) {
			t.Fatalf("%s: %s", sub, v)
		}
	}
}

func TestDMAndEmailShowSameDigest(t *testing.T) {
	s := storetest.New(t)
	rec, dm := &mailtest.Recorder{}, newDM()
	if err := matrixRunner(s, newSource(), rec, dm).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	var email mail.Message
	for _, m := range rec.Messages {
		if m.To == "one@example.org" {
			email = m
		}
	}
	var notice bot.Notice
	for _, d := range dm.deliveries() {
		if d.Subject == "sub-1" {
			notice = d.Notice
		}
	}
	raw, ok, err := s.SavedDigest(context.Background(), "sub-1", week)
	if err != nil || !ok {
		t.Fatalf("saved digest: %v %v", ok, err)
	}
	var d digest.Digest
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Featured == nil {
		t.Fatal("no featured thread")
	}
	title := d.Featured.Item.Title
	if !strings.Contains(email.Text, title) {
		t.Fatalf("email does not feature %q", title)
	}
	if lines := strings.Split(notice.Body, "\n"); len(lines) < 3 || lines[2] != "• "+title {
		t.Fatalf("notice: %q", notice.Body)
	}
	tok, ok := strings.CutPrefix(link(t, notice), "https://digest.example.org/d/")
	if !ok {
		t.Fatalf("link %q", link(t, notice))
	}
	sub, date, ok := token.VerifyDigest(key, tok)
	if !ok || sub != "sub-1" || date != "2026-10-26" {
		t.Fatalf("token names %q %q (ok %v)", sub, date, ok)
	}
	if _, found, err := s.WebDigest(context.Background(), sub, date, onSendDay()); err != nil || !found {
		t.Fatalf("web digest: %v %v", found, err)
	}
}

func TestDMWithoutVerifiedEmail(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	src.members = []digest.Member{{ID: 4, Subject: "sub-4", Username: "four"}}
	rec, dm := &mailtest.Recorder{}, newDM()
	if err := matrixRunner(s, src, rec, dm).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if len(rec.Messages) != 0 {
		t.Fatalf("%d emails", len(rec.Messages))
	}
	if got := dm.subjects(); len(got) != 1 || got[0] != "sub-4" {
		t.Fatalf("deliveries: %v", got)
	}
	if got := statuses(t, s); len(got) != 0 {
		t.Fatalf("sends rows: %v", got)
	}
}

func TestDMIgnoresEmailUnsubscribe(t *testing.T) {
	s := storetest.New(t)
	if err := s.Unsubscribe(context.Background(), "sub-2", true); err != nil {
		t.Fatal(err)
	}
	src := newSource()
	src.members = src.members[1:2]
	rec, dm := &mailtest.Recorder{}, newDM()
	if err := matrixRunner(s, src, rec, dm).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if len(rec.Messages) != 0 {
		t.Fatalf("%d emails", len(rec.Messages))
	}
	if st := statuses(t, s)["sub-2"]; st != string(store.StatusUnsubscribed) {
		t.Fatalf("status %q", st)
	}
	if got := dm.subjects(); len(got) != 1 || got[0] != "sub-2" {
		t.Fatalf("deliveries: %v", got)
	}
}

// An unsubscribed member with no Matrix digest has nothing to read a digest
// for, so none is built.
func TestUnsubscribedWithoutMatrixBuildsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    func(*store.Store) *run.Runner
	}{
		{"no homeserver", func(s *store.Store) *run.Runner { return runner(s, newSource(), &mailtest.Recorder{}) }},
		{"matrix off", func(s *store.Store) *run.Runner {
			if err := s.SetMatrixEnabled(context.Background(), "sub-2", false); err != nil {
				t.Fatal(err)
			}
			return matrixRunner(s, newSource(), &mailtest.Recorder{}, newDM())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.New(t)
			if err := s.Unsubscribe(context.Background(), "sub-2", true); err != nil {
				t.Fatal(err)
			}
			if err := tc.r(s).Week(context.Background(), week); err != nil {
				t.Fatal(err)
			}
			if st := statuses(t, s)["sub-2"]; st != string(store.StatusUnsubscribed) {
				t.Fatalf("status %q", st)
			}
			if n := countRows(t, s, `SELECT count(*) FROM web_digests WHERE subject = 'sub-2'`); n != 0 {
				t.Fatalf("%d digests stored", n)
			}
		})
	}
}

func TestMatrixOffSkipsDM(t *testing.T) {
	s := storetest.New(t)
	if err := s.SetMatrixEnabled(context.Background(), "sub-1", false); err != nil {
		t.Fatal(err)
	}
	src := newSource()
	src.members = src.members[:1]
	rec, dm := &mailtest.Recorder{}, newDM()
	if err := matrixRunner(s, src, rec, dm).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if len(rec.Messages) != 1 {
		t.Fatalf("%d emails", len(rec.Messages))
	}
	if got := dm.subjects(); len(got) != 0 {
		t.Fatalf("deliveries: %v", got)
	}
}

func TestEmptyDigestNoDM(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	src.members = src.members[2:]
	src.joined["sub-3"] = true
	rec, dm := &mailtest.Recorder{}, newDM()
	if err := matrixRunner(s, src, rec, dm).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if len(rec.Messages) != 0 || len(dm.deliveries()) != 0 {
		t.Fatalf("%d emails, deliveries %v", len(rec.Messages), dm.subjects())
	}
	if n := countRows(t, s, `SELECT count(*) FROM web_digests`); n != 0 {
		t.Fatalf("%d digests stored", n)
	}
}

func TestHomeserverDownStopsMatrixOnly(t *testing.T) {
	s := storetest.New(t)
	rec, dm := &mailtest.Recorder{}, newDM()
	dm.err["sub-2"] = fmt.Errorf("%w: connection refused", bot.ErrUnavailable)
	r := matrixRunner(s, newSource(), rec, dm)
	if err := r.Week(context.Background(), week); !errors.Is(err, bot.ErrUnavailable) {
		t.Fatalf("Week: %v, want ErrUnavailable", err)
	}
	if len(rec.Messages) != 3 {
		t.Fatalf("%d emails", len(rec.Messages))
	}
	st := dmStatuses(t, s, "2026-10-26")
	if len(st) != 1 || st["sub-1"] != string(store.StatusSent) {
		t.Fatalf("dm_sends: %v", st)
	}
	for _, sub := range dm.prepared {
		if sub == "sub-3" {
			t.Fatal("member 3 was attempted after the homeserver went down")
		}
	}
	dm.err = map[string]error{}
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if got := dm.subjects(); strings.Join(got, ",") != "sub-1,sub-2,sub-3" {
		t.Fatalf("deliveries: %v", got)
	}
	if len(rec.Messages) != 3 {
		t.Fatalf("%d emails after the retry", len(rec.Messages))
	}
}

// Prepare failing the same way stops the pass too.
func TestHomeserverDownAtPrepare(t *testing.T) {
	s := storetest.New(t)
	dm := newDM()
	dm.prepareErr["sub-1"] = fmt.Errorf("%w: connection refused", bot.ErrUnavailable)
	if err := matrixRunner(s, newSource(), &mailtest.Recorder{}, dm).Week(context.Background(), week); !errors.Is(err, bot.ErrUnavailable) {
		t.Fatalf("Week: %v, want ErrUnavailable", err)
	}
	if len(dm.prepared) != 1 || len(dm.deliveries()) != 0 {
		t.Fatalf("prepared %v, delivered %v", dm.prepared, dm.subjects())
	}
	if st := dmStatuses(t, s, "2026-10-26"); len(st) != 0 {
		t.Fatalf("dm_sends: %v", st)
	}
}

func TestRefusedIsNotRetried(t *testing.T) {
	s := storetest.New(t)
	dm := newDM()
	dm.outcome["sub-1"] = bot.Refused
	r := matrixRunner(s, newSource(), &mailtest.Recorder{}, dm)
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if st := dmStatuses(t, s, "2026-10-26")["sub-1"]; st != string(store.StatusFailed) {
		t.Fatalf("status %q", st)
	}
	before := len(dm.deliveries())
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if n := len(dm.deliveries()); n != before {
		t.Fatalf("%d more deliveries on the second run", n-before)
	}
}

// A member who stopped the messages between Prepare and the send is left
// unclaimed, so a start later the same day still reaches them.
func TestOffAtDeliveryLeavesNoRow(t *testing.T) {
	s := storetest.New(t)
	dm := newDM()
	dm.outcome["sub-2"] = bot.Off
	if err := matrixRunner(s, newSource(), &mailtest.Recorder{}, dm).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	st := dmStatuses(t, s, "2026-10-26")
	if _, ok := st["sub-2"]; ok || len(st) != 2 {
		t.Fatalf("dm_sends: %v", st)
	}
}

func TestPrepareFalseLeavesNoRow(t *testing.T) {
	s := storetest.New(t)
	dm := newDM()
	dm.skip["sub-2"] = true
	if err := matrixRunner(s, newSource(), &mailtest.Recorder{}, dm).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	st := dmStatuses(t, s, "2026-10-26")
	if _, ok := st["sub-2"]; ok || len(st) != 2 {
		t.Fatalf("dm_sends: %v", st)
	}
}

func TestNoBotOnThisCopy(t *testing.T) {
	s := storetest.New(t)
	rec := &mailtest.Recorder{}
	r := matrixRunner(s, newSource(), rec, nil)
	r.Matrix = func() run.DM { return nil }
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if len(rec.Messages) != 3 {
		t.Fatalf("%d emails", len(rec.Messages))
	}
	if n := countRows(t, s, `SELECT count(*) FROM dm_sends`); n != 0 {
		t.Fatalf("%d dm_sends rows", n)
	}
}

// A copy that had no bot when its run went out still sends the Matrix
// notices if it takes the bot over later the same day, without reading Mbin
// or mailing anyone again.
func TestLoopSendsDMsWhenBotArrivesLater(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	rec, dm := &mailtest.Recorder{}, newDM()
	r := matrixRunner(s, src, rec, dm)
	var here atomic.Bool
	r.Matrix = func() run.DM {
		if !here.Load() {
			return nil
		}
		return dm
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	c := newClock(onSendDay())
	go func() {
		run.Loop(ctx, r, c.now, time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.Sent()) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("%d emails", len(rec.Sent()))
		}
		time.Sleep(time.Millisecond)
	}
	c.waitTicks(t, 5)
	if n := len(dm.deliveries()); n != 0 {
		t.Fatalf("%d deliveries without a bot", n)
	}
	here.Store(true)
	for len(dm.deliveries()) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("deliveries: %v", dm.subjects())
		}
		time.Sleep(time.Millisecond)
	}
	c.waitTicks(t, 5)
	if n := len(dm.deliveries()); n != 3 {
		t.Fatalf("%d deliveries", n)
	}
	if h := src.hots.Load(); h != 1 {
		t.Fatalf("Mbin read %d times", h)
	}
	if n := len(rec.Sent()); n != 3 {
		t.Fatalf("%d emails", n)
	}
}

// A copy that loses the bot during its Matrix pass stops sending at once;
// the members left are reached by the copy that holds the bot now.
func TestBotLostDuringPass(t *testing.T) {
	s := storetest.New(t)
	dm := newDM()
	r := matrixRunner(s, newSource(), &mailtest.Recorder{}, dm)
	var lost atomic.Bool
	r.Matrix = func() run.DM {
		if lost.Load() {
			return nil
		}
		return dm
	}
	dm.after = func(string) { lost.Store(true) }
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if got := dm.subjects(); len(got) != 1 || got[0] != "sub-1" {
		t.Fatalf("deliveries: %v", got)
	}
	if st := dmStatuses(t, s, "2026-10-26"); len(st) != 1 {
		t.Fatalf("dm_sends: %v", st)
	}
	other := newDM()
	r.Matrix = func() run.DM { return other }
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if got := other.subjects(); strings.Join(got, ",") != "sub-2,sub-3" {
		t.Fatalf("deliveries by the new holder: %v", got)
	}
}

// A rate limit wait that would outlast the send day ends the pass as the
// day does: no error and no warning about the member.
func TestRateWaitPastDayEnd(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)
	s := storetest.New(t)
	dm := newDM()
	dm.err["sub-1"] = fmt.Errorf("matrix PUT /send: the rate limit wait outlasts the deadline: %w", context.DeadlineExceeded)
	if err := matrixRunner(s, newSource(), &mailtest.Recorder{}, dm).Week(context.Background(), week); err != nil {
		t.Fatalf("Week: %v", err)
	}
	if len(dm.prepared) != 1 {
		t.Fatalf("prepared %v", dm.prepared)
	}
	if n := countRows(t, s, `SELECT count(*) FROM dm_sends`); n != 0 {
		t.Fatalf("%d dm_sends rows", n)
	}
	if strings.Contains(logs.String(), "matrix notice not sent") || !strings.Contains(logs.String(), "the send day is over") {
		t.Fatalf("logs: %s", logs.String())
	}
}

// A bot that is still starting is waited for: the run fails so it is tried
// again, and the email already sent is not sent twice.
func TestBotStillStarting(t *testing.T) {
	s := storetest.New(t)
	rec, dm := &mailtest.Recorder{}, newDM()
	r := matrixRunner(s, newSource(), rec, dm)
	var ready bool
	r.Matrix = func() run.DM {
		if !ready {
			return nil
		}
		return dm
	}
	r.MatrixPending = func() bool { return !ready }
	if err := r.Week(context.Background(), week); !errors.Is(err, run.ErrBotNotReady) {
		t.Fatalf("Week: %v, want ErrBotNotReady", err)
	}
	if len(rec.Messages) != 3 || len(dm.deliveries()) != 0 {
		t.Fatalf("%d emails, deliveries %v", len(rec.Messages), dm.subjects())
	}
	ready = true
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if len(rec.Messages) != 3 || len(dm.deliveries()) != 3 {
		t.Fatalf("%d emails, deliveries %v", len(rec.Messages), dm.subjects())
	}
}

// Once the send day is over a bot still starting no longer fails the run:
// there is nothing left for it to send.
func TestBotStillStartingAfterDayEnd(t *testing.T) {
	s := storetest.New(t)
	r := matrixRunner(s, newSource(), &mailtest.Recorder{}, nil)
	r.Matrix = func() run.DM { return nil }
	r.MatrixPending = func() bool { return true }
	r.Now = func() time.Time { return time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC) }
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatalf("Week: %v", err)
	}
}

func TestNoMailHostStillDMs(t *testing.T) {
	s := storetest.New(t)
	dm := newDM()
	if err := matrixRunner(s, newSource(), nil, dm).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if n := len(dm.deliveries()); n != 3 {
		t.Fatalf("%d deliveries", n)
	}
	if got := statuses(t, s); len(got) != 0 {
		t.Fatalf("sends rows: %v", got)
	}
}

// The digest is built once: a restart that finds the source changed still
// delivers the digest the email showed.
func TestFrozenDigestSurvivesShown(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	src.members = src.members[:1]
	first := matrixRunner(s, src, &mailtest.Recorder{}, nil)
	first.Matrix = func() run.DM { return nil }
	if err := first.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM shown WHERE week = '2026-10-26'`); n == 0 {
		t.Fatal("the email recorded nothing as shown")
	}
	raw, ok, err := s.SavedDigest(context.Background(), "sub-1", week)
	if err != nil || !ok {
		t.Fatalf("saved digest: %v %v", ok, err)
	}
	var stored digest.Digest
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}

	renamed := thread
	renamed.Title = "burr grinders"
	dm := newDM()
	if err := matrixRunner(s, laterSource{src, renamed}, &mailtest.Recorder{}, dm).Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	got := dm.deliveries()
	if len(got) != 1 {
		t.Fatalf("deliveries: %v", dm.subjects())
	}
	want := bot.Weekly(stored, "Talk · Example Community", link(t, got[0].Notice))
	if got[0].Notice != want {
		t.Fatalf("delivered\n%q\nwant\n%q", got[0].Notice.Body, want.Body)
	}
}

func TestWeekDateAgreesInLocalZone(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	// 00:30 on the Monday is still Sunday in UTC.
	for _, monday := range []time.Time{
		time.Date(2026, 3, 30, 8, 0, 0, 0, berlin),
		time.Date(2026, 3, 30, 0, 30, 0, 0, berlin),
	} {
		t.Run(monday.Format("15:04"), func(t *testing.T) {
			s := storetest.New(t)
			src := newSource()
			src.members = src.members[:1]
			dm := newDM()
			r := matrixRunner(s, laterSource{src, thread}, &mailtest.Recorder{}, dm)
			r.Now = func() time.Time { return monday.Add(time.Hour) }
			if err := r.Week(context.Background(), monday); err != nil {
				t.Fatal(err)
			}
			if st := dmStatuses(t, s, "2026-03-30"); st["sub-1"] != string(store.StatusSent) {
				t.Fatalf("dm_sends: %v", st)
			}
			if n := countRows(t, s, `SELECT count(*) FROM web_digests WHERE week = '2026-03-30'`); n != 1 {
				t.Fatalf("%d web digests dated 2026-03-30", n)
			}
			got := dm.deliveries()
			if len(got) != 1 {
				t.Fatalf("deliveries: %v", dm.subjects())
			}
			tok, _ := strings.CutPrefix(link(t, got[0].Notice), "https://digest.example.org/d/")
			if _, date, ok := token.VerifyDigest(key, tok); !ok || date != "2026-03-30" {
				t.Fatalf("token date %q (ok %v)", date, ok)
			}
		})
	}
}

// A relay outage holds up the email only: the Matrix notices still go out,
// and every email is left for the next attempt.
func TestRelayOutageWithMatrixOn(t *testing.T) {
	s := storetest.New(t)
	snd := &notSentFor{err: fmt.Errorf("%w: could not connect", mail.ErrRelayUnavailable)}
	dm := newDM()
	if err := matrixRunner(s, newSource(), snd, dm).Week(context.Background(), week); !errors.Is(err, mail.ErrNotSent) {
		t.Fatalf("Week: %v, want ErrNotSent", err)
	}
	if n := len(dm.deliveries()); n != 3 {
		t.Fatalf("%d deliveries", n)
	}
	if got := statuses(t, s); len(got) != 0 {
		t.Fatalf("sends rows: %v", got)
	}
}

// A Matrix setting that cannot be read claims nobody, by either channel, and
// counts as unclaimed only a member who had an email to claim.
func TestMatrixSettingUnreadable(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)
	s := storetest.New(t)
	if _, err := storetest.Conn(t, s).Exec(context.Background(), `ALTER TABLE matrix_members RENAME TO matrix_members_away`); err != nil {
		t.Fatal(err)
	}
	src := newSource()
	src.members = append(src.members, digest.Member{ID: 4, Subject: "sub-4", Username: "four"})
	rec, dm := &mailtest.Recorder{}, newDM()
	if err := matrixRunner(s, src, rec, dm).Week(context.Background(), week); err == nil {
		t.Fatal("Week succeeded without the Matrix settings")
	}
	if len(rec.Messages) != 0 || len(dm.deliveries()) != 0 {
		t.Fatalf("%d emails, deliveries %v", len(rec.Messages), dm.subjects())
	}
	if !strings.Contains(logs.String(), "unclaimed=3") {
		t.Fatalf("summary: %s", logs.String())
	}
}

// A weekly notice is never sent after its own day.
func TestSendDayOver(t *testing.T) {
	s := storetest.New(t)
	rec, dm := &mailtest.Recorder{}, newDM()
	r := matrixRunner(s, newSource(), rec, dm)
	r.Now = func() time.Time { return time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC) }
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if len(dm.prepared) != 0 || len(dm.deliveries()) != 0 {
		t.Fatalf("prepared %v, delivered %v", dm.prepared, dm.subjects())
	}
	if n := countRows(t, s, `SELECT count(*) FROM dm_sends`); n != 0 {
		t.Fatalf("%d dm_sends rows", n)
	}
}

// A delivery still waiting when the day ends is given up, and the member is
// left unclaimed.
func TestDayEndCutsWait(t *testing.T) {
	s := storetest.New(t)
	dm := newDM()
	dm.block = true
	r := matrixRunner(s, newSource(), &mailtest.Recorder{}, dm)
	r.Now = func() time.Time { return time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC).Add(-50 * time.Millisecond) }
	start := time.Now()
	if err := r.Week(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Week took %v", d)
	}
	if len(dm.prepared) != 1 {
		t.Fatalf("prepared %v", dm.prepared)
	}
	if n := countRows(t, s, `SELECT count(*) FROM dm_sends`); n != 0 {
		t.Fatalf("%d dm_sends rows", n)
	}
}

// A Matrix send left pending by a sender that stopped before finishing is not
// done: Loop skips the member while the claim is fresh, then, once it is
// stale and the send day is not over, delivers once.
func TestLoopRetriesPendingDMAfterStaleWindow(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	rec, dm := &mailtest.Recorder{}, newDM()
	r := matrixRunner(s, src, rec, dm)
	c := newClock(onSendDay())
	r.Now = c.now
	conn := storetest.Conn(t, s)
	if _, err := conn.Exec(context.Background(),
		`INSERT INTO dm_sends (subject, week, status) VALUES ('sub-2', '2026-10-26', 'pending')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		run.Loop(ctx, r, c.now, time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(dm.deliveries()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("deliveries: %v", dm.subjects())
		}
		time.Sleep(time.Millisecond)
	}
	c.waitTicks(t, 5)
	for _, d := range dm.deliveries() {
		if d.Subject == "sub-2" {
			t.Fatal("a fresh pending claim was sent over")
		}
	}
	// The stopped sender's claim ages past the stale window.
	if _, err := conn.Exec(context.Background(),
		`UPDATE dm_sends SET at = now() - interval '20 minutes' WHERE subject = 'sub-2'`); err != nil {
		t.Fatal(err)
	}
	c.set(onSendDay().Add(16 * time.Minute))
	for len(dm.deliveries()) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("deliveries: %v", dm.subjects())
		}
		time.Sleep(time.Millisecond)
	}
	c.waitTicks(t, 5)
	if n := len(dm.deliveries()); n != 3 {
		t.Fatalf("%d deliveries", n)
	}
	if st := dmStatuses(t, s, "2026-10-26")["sub-2"]; st != string(store.StatusSent) {
		t.Fatalf("sub-2: %q", st)
	}
	if h := src.hots.Load(); h != 1 {
		t.Fatalf("Mbin read %d times", h)
	}
}

// A finished Matrix send, sent or failed, is done and is not tried again.
func TestLoopFinishedDMIsNotRetried(t *testing.T) {
	s := storetest.New(t)
	dm := newDM()
	dm.outcome["sub-1"] = bot.Refused
	r := matrixRunner(s, newSource(), &mailtest.Recorder{}, dm)
	c := newClock(onSendDay())
	r.Now = c.now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		run.Loop(ctx, r, c.now, time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(dm.deliveries()) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("deliveries: %v", dm.subjects())
		}
		time.Sleep(time.Millisecond)
	}
	c.set(onSendDay().Add(time.Hour))
	c.waitTicks(t, 10)
	if n := len(dm.deliveries()); n != 3 {
		t.Fatalf("%d deliveries", n)
	}
}
