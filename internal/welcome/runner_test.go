package welcome

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
	"github.com/paisans-software/paisans-engagement-service/internal/digest"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mail/mailtest"
	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
	"github.com/paisans-software/paisans-engagement-service/internal/mbin"
	"github.com/paisans-software/paisans-engagement-service/internal/mbinapi"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

var t0 = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

type fakeMbin struct {
	newcomers  []mbin.Newcomer
	ranked     []mbin.Ranked
	commenters map[int64]map[int64]bool
	subjects   map[string]string
}

func (f *fakeMbin) Newcomers(_ context.Context, since time.Time) ([]mbin.Newcomer, error) {
	var out []mbin.Newcomer
	for _, n := range f.newcomers {
		if !n.CreatedAt.Before(since) {
			out = append(out, n)
		}
	}
	return out, nil
}
func (f *fakeMbin) Active(context.Context, time.Time) ([]mbin.Ranked, error) { return f.ranked, nil }
func (f *fakeMbin) Commenters(context.Context, []int64) (map[int64]map[int64]bool, error) {
	if f.commenters == nil {
		return map[int64]map[int64]bool{}, nil
	}
	return f.commenters, nil
}
func (f *fakeMbin) SubjectsByEmail(context.Context, []string) (map[string]string, error) {
	return f.subjects, nil
}

type fakeAPI struct {
	magazine    *mbinapi.Magazine
	threads     []string
	created     int
	restricted  int
	subscribed  []int64
	postErr     error
	restrictErr error
}

func (f *fakeAPI) MagazineByName(context.Context, string) (mbinapi.Magazine, bool, error) {
	if f.magazine == nil {
		return mbinapi.Magazine{}, false, nil
	}
	return *f.magazine, true, nil
}
func (f *fakeAPI) CreateMagazine(_ context.Context, name, _, _ string) (mbinapi.Magazine, error) {
	f.created++
	f.magazine = &mbinapi.Magazine{ID: 3, Name: name, PostingRestricted: true}
	return *f.magazine, nil
}
func (f *fakeAPI) RestrictPosting(context.Context, int64) error {
	f.restricted++
	return f.restrictErr
}
func (f *fakeAPI) CreateThread(_ context.Context, _ int64, title, _ string) (mbinapi.Entry, error) {
	if f.postErr != nil {
		return mbinapi.Entry{}, f.postErr
	}
	f.threads = append(f.threads, title)
	return mbinapi.Entry{ID: int64(100 + len(f.threads)), Slug: "welcome"}, nil
}
func (f *fakeAPI) Subscribe(_ context.Context, user, _ int64) error {
	f.subscribed = append(f.subscribed, user)
	return nil
}

type fakeBot struct {
	members     []bot.Newcomer
	announced   []map[string]any
	announceErr error
	dms         []string
	roomCalls   int
}

func (f *fakeBot) NewMembers(_ context.Context, since time.Time) ([]bot.Newcomer, error) {
	var out []bot.Newcomer
	for _, n := range f.members {
		if !n.CreatedAt.Before(since) {
			out = append(out, n)
		}
	}
	return out, nil
}
func (f *fakeBot) CommunityRoom(context.Context, string, string) (string, error) {
	f.roomCalls++
	return "!community:example.org", nil
}
func (f *fakeBot) Announce(_ context.Context, _, _ string, c map[string]any) error {
	if f.announceErr != nil {
		return f.announceErr
	}
	f.announced = append(f.announced, c)
	return nil
}
func (f *fakeBot) Prepare(_ context.Context, subject string) (bot.Target, bool, error) {
	return bot.Target{Subject: subject}, true, nil
}
func (f *fakeBot) Deliver(_ context.Context, t bot.Target, _ string, _ bot.Notice) (bot.Outcome, error) {
	f.dms = append(f.dms, t.Subject)
	return bot.Sent, nil
}

func newRunner(t *testing.T) (*Runner, *fakeMbin, *fakeAPI, *fakeBot, *mailtest.Recorder) {
	st := storetest.New(t)
	fm, fa, fb, rec := &fakeMbin{}, &fakeAPI{}, &fakeBot{}, &mailtest.Recorder{}
	r := &Runner{Store: st, Mbin: fm, API: fa, Bot: func() MatrixBot { return fb },
		RoomName: "Riverside Community", RoomAlias: "riverside-community", Sender: rec,
		Names: mail.Names{Meta: "Riverside"}, SiteURL: "https://forum.example.org", PublicURL: "https://e.example.org",
		Key: []byte("test-key-test-key-test-key-12345"), DailyLimit: 50, Operators: []string{"op@example.org"}}
	return r, fm, fa, fb, rec
}

func newcomer(id int64, at time.Time) mbin.Newcomer {
	return mbin.Newcomer{ID: id, Subject: fmt.Sprintf("sub-%d", id), Username: fmt.Sprintf("u%d", id), CreatedAt: at}
}

func TestSplit(t *testing.T) {
	for limit, want := range map[int][2]int{50: {40, 10}, 1: {0, 1}, 7: {5, 2}, 10: {8, 2}} {
		if top, random := Split(limit); top != want[0] || random != want[1] {
			t.Errorf("Split(%d) = %d, %d; want %v", limit, top, random, want)
		}
	}
}

func ranked(n int) []mbin.Ranked {
	out := make([]mbin.Ranked, n)
	for i := range out {
		id := int64(i + 1)
		out[i] = mbin.Ranked{Member: digest.Member{ID: id, Subject: fmt.Sprintf("m-%d", id),
			Email: fmt.Sprintf("m%d@example.org", id)}, Score: n - i}
	}
	return out
}

func TestPickBackfills(t *testing.T) {
	news := []store.Welcomed{{MbinID: 900, EntryID: 1}, {MbinID: 901, EntryID: 2}}
	// Member 1 greeted both, member 2 one of them.
	greeted := func(m, e int64) bool { return m == 1 || (m == 2 && e == 1) }
	got := Pick(ranked(10), greeted, news, 3, 2, rand.New(rand.NewPCG(1, 2)), map[string]bool{"m-3": true})
	if len(got) != 5 {
		t.Fatalf("picked %d, want 5", len(got))
	}
	wantTop := []int64{2, 4, 5}
	for i, id := range wantTop {
		if got[i].ID != id {
			t.Fatalf("top %d = %d, want %d", i, got[i].ID, id)
		}
	}
	if len(got[0].Newcomers) != 1 || got[0].Newcomers[0].EntryID != 2 {
		t.Fatalf("member 2 is named %+v", got[0].Newcomers)
	}
	for _, p := range got[3:] {
		if p.ID <= 5 {
			t.Fatalf("random pick %d was not left after the top share", p.ID)
		}
	}
}

func TestMbinBatchOneThreadForAll(t *testing.T) {
	r, fm, fa, _, _ := newRunner(t)
	ctx := context.Background()
	if err := r.MbinBatch(ctx, t0); err != nil {
		t.Fatal(err)
	}
	fm.newcomers = []mbin.Newcomer{newcomer(2, t0.Add(2*time.Minute)), newcomer(1, t0.Add(time.Minute))}
	if err := r.MbinBatch(ctx, t0.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if fa.created != 1 || len(fa.threads) != 1 || fa.threads[0] != "Welcome @u1 and @u2" {
		t.Fatalf("created %d, threads %v", fa.created, fa.threads)
	}
	if len(fa.subscribed) != 2 {
		t.Fatalf("subscribed %v", fa.subscribed)
	}
	if err := r.MbinBatch(ctx, t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(fa.threads) != 1 || len(fa.subscribed) != 2 {
		t.Fatalf("a second batch posted %v and subscribed %v", fa.threads, fa.subscribed)
	}
}

func TestMbinBatchSkipsMembersBeforeStart(t *testing.T) {
	r, fm, fa, fb, _ := newRunner(t)
	ctx := context.Background()
	// Matrix welcomes start a month before Mbin's.
	fb.members = nil
	if err := r.MatrixBatch(ctx, t0.AddDate(0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	fm.newcomers = []mbin.Newcomer{newcomer(1, t0.Add(-time.Hour))}
	if err := r.MbinBatch(ctx, t0); err != nil {
		t.Fatal(err)
	}
	if len(fa.threads) != 0 || len(fa.subscribed) != 0 {
		t.Fatalf("a member from before Mbin's start was welcomed: %v %v", fa.threads, fa.subscribed)
	}
}

func TestMbinBatchAlertsWhenNotModerator(t *testing.T) {
	r, fm, fa, fb, rec := newRunner(t)
	ctx := context.Background()
	fa.magazine = &mbinapi.Magazine{ID: 3, Name: "welcome"}
	fa.restrictErr = fmt.Errorf("%w: 403", mbinapi.ErrForbidden)
	fm.subjects = map[string]string{"op@example.org": "sub-op"}
	if err := r.MbinBatch(ctx, t0); err != nil {
		t.Fatal(err)
	}
	fm.newcomers = []mbin.Newcomer{newcomer(1, t0.Add(time.Minute))}
	for i := range 2 {
		if err := r.MbinBatch(ctx, t0.Add(time.Duration(5*(i+1))*time.Minute)); err == nil {
			t.Fatal("no error")
		}
	}
	if len(fa.threads) != 0 {
		t.Fatal("posted in a magazine it cannot manage")
	}
	if m := rec.Sent(); len(m) != 1 || m[0].To != "op@example.org" || m[0].Unsubscribe != "" {
		t.Fatalf("alerts = %+v, want one email", m)
	}
	if len(fb.dms) != 1 || fb.dms[0] != "sub-op" {
		t.Fatalf("alert DMs = %v", fb.dms)
	}
}

func TestAlertOncePerDay(t *testing.T) {
	r, _, _, _, rec := newRunner(t)
	ctx := context.Background()
	r.alert(ctx, t0, "k", "broken")
	r.alert(ctx, t0.Add(time.Hour), "k", "broken")
	r.alert(ctx, t0.AddDate(0, 0, 1), "k", "broken")
	if n := len(rec.Sent()); n != 2 {
		t.Fatalf("alerts sent = %d, want 2", n)
	}
}

func TestMatrixBatchOneMessage(t *testing.T) {
	r, _, _, fb, _ := newRunner(t)
	ctx := context.Background()
	if err := r.MatrixBatch(ctx, t0); err != nil {
		t.Fatal(err)
	}
	fb.members = []bot.Newcomer{
		{MASUserID: "01B", UserID: "@b:example.org", Username: "b", CreatedAt: t0.Add(2 * time.Minute)},
		{MASUserID: "01A", UserID: "@a:example.org", Username: "a", CreatedAt: t0.Add(time.Minute)},
		{MASUserID: "01OLD", UserID: "@old:example.org", Username: "old", CreatedAt: t0.Add(-time.Minute)},
	}
	for i := 1; i <= 2; i++ {
		if err := r.MatrixBatch(ctx, t0.Add(time.Duration(5*i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if len(fb.announced) != 1 || fb.roomCalls != 1 {
		t.Fatalf("announced %d, room calls %d", len(fb.announced), fb.roomCalls)
	}
	if body := fb.announced[0]["body"].(string); body != "Please welcome a and b to Riverside! Say hello." {
		t.Fatalf("body = %q", body)
	}
	r.Bot = func() MatrixBot { return nil }
	if err := r.MatrixBatch(ctx, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestMatrixBatchAlertsWhenRefused(t *testing.T) {
	r, _, _, fb, rec := newRunner(t)
	ctx := context.Background()
	if err := r.MatrixBatch(ctx, t0); err != nil {
		t.Fatal(err)
	}
	fb.members = []bot.Newcomer{{MASUserID: "01A", UserID: "@a:example.org", Username: "a", CreatedAt: t0.Add(time.Minute)}}
	fb.announceErr = &matrix.Error{Status: 403, Code: "M_FORBIDDEN"}
	if err := r.MatrixBatch(ctx, t0.Add(5*time.Minute)); err == nil {
		t.Fatal("no error")
	}
	if n := len(rec.Sent()); n != 1 || !strings.Contains(rec.Sent()[0].Text, "#riverside-community") {
		t.Fatalf("alerts = %+v", rec.Sent())
	}
	fb.announceErr = nil
	if err := r.MatrixBatch(ctx, t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(fb.announced) != 1 || fb.roomCalls != 2 {
		t.Fatalf("announced %d, room calls %d; want the room found again", len(fb.announced), fb.roomCalls)
	}
}
