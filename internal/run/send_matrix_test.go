package run_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mail/mailtest"
	"github.com/paisans-software/paisans-engagement-service/internal/run"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
	"github.com/paisans-software/paisans-engagement-service/internal/web"
)

// get fetches a digest link from the service's own pages at now, as the
// sign-in gate passes it on for the member subject.
func get(t *testing.T, s *store.Store, subject, link string, now time.Time) *httptest.ResponseRecorder {
	t.Helper()
	path, ok := strings.CutPrefix(link, "https://digest.example.org")
	if !ok {
		t.Fatalf("link %q is not on the engagement hostname", link)
	}
	h := web.NewServer(web.Options{Store: s, Key: key, Names: mail.Names{Meta: "Talk · Example Community"},
		SiteURL: "https://talk.example.org/", Now: func() time.Time { return now }})
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Auth-Request-User", subject)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A Matrix preview sends the member a notice marked as a preview, built from
// a blank member's digest, whose link opens for about a day. It claims,
// records and stores nothing of the week's, so the week's run still sends
// the member their own notice.
func TestMatrixPreview(t *testing.T) {
	s := storetest.New(t)
	src := newSource()
	src.joined["sub-1"] = true
	rec, dm := &mailtest.Recorder{}, newDM()
	r := matrixRunner(s, src, rec, dm)
	ctx := context.Background()
	if err := r.PreviewMatrix(ctx, week, "one"); err != nil {
		t.Fatal(err)
	}
	got := dm.deliveries()
	if len(got) != 1 || got[0].Subject != "sub-1" || len(rec.Messages) != 0 {
		t.Fatalf("deliveries %v, emails %d", dm.subjects(), len(rec.Messages))
	}
	d := got[0]
	if !strings.HasPrefix(d.TxnID, "preview-") || d.TxnID == bot.TxnID("sub-1", week) {
		t.Fatalf("txn id %q", d.TxnID)
	}
	if !strings.HasPrefix(d.Notice.Body, "[Preview] Your weekly digest from Talk · Example Community") ||
		!strings.Contains(d.Notice.Body, "• grinders") {
		t.Fatalf("body %q", d.Notice.Body)
	}
	for table, n := range map[string]int{"sends": 0, "dm_sends": 0, "shown": 0, "web_digests": 0, "preview_digests": 1} {
		if got := countRows(t, s, "SELECT count(*) FROM "+table); got != n {
			t.Errorf("%s has %d rows, want %d", table, got, n)
		}
	}
	l := link(t, d.Notice)
	if l != "https://digest.example.org/d/preview/2026-10-26" {
		t.Fatalf("preview link %q", l)
	}
	if page := get(t, s, "sub-1", l, onSendDay().Add(23*time.Hour)); page.Code != 200 || !strings.Contains(page.Body.String(), "grinders") {
		t.Fatalf("preview page = %d", page.Code)
	}
	if page := get(t, s, "sub-2", l, onSendDay().Add(23*time.Hour)); page.Code != 404 {
		t.Fatalf("another member's preview page = %d", page.Code)
	}
	if page := get(t, s, "sub-1", l, onSendDay().Add(24*time.Hour)); page.Code != 404 {
		t.Fatalf("preview page after a day = %d", page.Code)
	}

	// The week's run sends every member their own notice, with the week's
	// transaction id and link.
	src.joined["sub-1"] = false
	if err := r.Week(ctx, week); err != nil {
		t.Fatal(err)
	}
	after := dm.deliveries()[1:]
	if len(after) != 3 {
		t.Fatalf("week deliveries: %v", dm.subjects())
	}
	for _, d := range after {
		if d.TxnID != bot.TxnID(d.Subject, week) {
			t.Fatalf("txn id %q for %s", d.TxnID, d.Subject)
		}
		if strings.Contains(d.Notice.Body, "Preview") {
			t.Fatalf("week notice marked as a preview: %q", d.Notice.Body)
		}
	}
	if st := dmStatuses(t, s, "2026-10-26"); st["sub-1"] != string(store.StatusSent) {
		t.Fatalf("dm_sends: %v", st)
	}
	if page := get(t, s, after[0].Subject, link(t, after[0].Notice), onSendDay()); page.Code != 200 {
		t.Fatalf("week page = %d", page.Code)
	}
}

// A preview is refused, and nothing is sent or stored, for a member who is
// unknown, stopped the bot or has no linked Matrix account, while no bot
// runs, and in a week with nothing for even a blank member.
func TestMatrixPreviewRefusals(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetMatrixEnabled(ctx, "sub-2", false); err != nil {
		t.Fatal(err)
	}
	federated := thread
	federated.Federated = true
	cases := []struct {
		name, to, want string
		src            run.Source
		noBot          bool
	}{
		{name: "unknown", to: "nobody", want: "no member", src: newSource()},
		{name: "stopped", to: "two", want: "stopped", src: newSource()},
		{name: "no account", to: "three", want: "no Matrix account", src: newSource()},
		{name: "no bot", to: "one", want: "not running", src: newSource(), noBot: true},
		{name: "empty week", to: "one", want: "nothing", src: laterSource{newSource(), federated}},
	}
	for _, c := range cases {
		dm := newDM()
		dm.skip["sub-3"] = true
		r := matrixRunner(s, c.src, &mailtest.Recorder{}, dm)
		if c.noBot {
			r.Matrix = func() run.DM { return nil }
		}
		err := r.PreviewMatrix(ctx, week, c.to)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if len(dm.deliveries()) != 0 {
			t.Errorf("%s: delivered %v", c.name, dm.subjects())
		}
	}
	if n := countRows(t, s, "SELECT count(*) FROM preview_digests"); n != 0 {
		t.Fatalf("%d previews stored", n)
	}
}

// A preview the homeserver refused, or a member who stopped the bot after
// the check, is an error.
func TestMatrixPreviewNotDelivered(t *testing.T) {
	s := storetest.New(t)
	for _, out := range []bot.Outcome{bot.Refused, bot.Off} {
		dm := newDM()
		dm.outcome["sub-1"] = out
		if err := matrixRunner(s, newSource(), nil, dm).PreviewMatrix(context.Background(), week, "one"); err == nil {
			t.Errorf("outcome %d: no error", out)
		}
	}
}

// A Matrix send for one member sends their real notice for the week, on any
// day, claimed and recorded as the Matrix pass does, and no email. A second
// send skips them, and the week's run still reaches everyone else and mails
// everyone.
func TestMatrixSendOne(t *testing.T) {
	s := storetest.New(t)
	rec, dm := &mailtest.Recorder{}, newDM()
	r := matrixRunner(s, newSource(), rec, dm)
	r.Now = func() time.Time { return week.AddDate(0, 0, 3) }
	ctx := context.Background()
	if err := r.MatrixWeek(ctx, week, "one"); err != nil {
		t.Fatal(err)
	}
	got := dm.deliveries()
	if len(got) != 1 || got[0].Subject != "sub-1" || got[0].TxnID != bot.TxnID("sub-1", week) || len(rec.Messages) != 0 {
		t.Fatalf("deliveries %v, emails %d", dm.subjects(), len(rec.Messages))
	}
	if st := dmStatuses(t, s, "2026-10-26"); len(st) != 1 || st["sub-1"] != string(store.StatusSent) {
		t.Fatalf("dm_sends: %v", st)
	}
	if st := statuses(t, s); len(st) != 0 {
		t.Fatalf("sends: %v", st)
	}
	shown, err := s.Shown(ctx, "sub-1", week.AddDate(0, 0, 7))
	if err != nil || len(shown) != 1 {
		t.Fatalf("shown %v, %v", shown, err)
	}
	if l := link(t, got[0].Notice); l != "https://digest.example.org/d/2026-10-26" {
		t.Fatalf("link %q", l)
	}
	if page := get(t, s, "sub-1", link(t, got[0].Notice), week.AddDate(0, 0, 3)); page.Code != 200 {
		t.Fatalf("page = %d", page.Code)
	}

	if err := r.MatrixWeek(ctx, week, "one"); err != nil {
		t.Fatal(err)
	}
	if n := len(dm.deliveries()); n != 1 {
		t.Fatalf("second send delivered again: %v", dm.subjects())
	}

	r.Now = onSendDay
	if err := r.Week(ctx, week); err != nil {
		t.Fatal(err)
	}
	if subs := dm.subjects(); len(subs) != 3 || subs[1] == "sub-1" || subs[2] == "sub-1" {
		t.Fatalf("week deliveries: %v", subs)
	}
	if len(rec.Messages) != 3 {
		t.Fatalf("%d emails", len(rec.Messages))
	}
}

// A Matrix send is refused, and nothing is claimed or sent, for a member who
// is unknown, stopped the bot or has no linked Matrix account, and while no
// bot runs.
func TestMatrixSendOneRefusals(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetMatrixEnabled(ctx, "sub-2", false); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, to, want string
		noBot          bool
	}{
		{name: "unknown", to: "nobody", want: "no member"},
		{name: "stopped", to: "two", want: "stopped"},
		{name: "no account", to: "three", want: "no Matrix account"},
		{name: "no bot", to: "one", want: "not running", noBot: true},
	}
	for _, c := range cases {
		dm := newDM()
		dm.skip["sub-3"] = true
		r := matrixRunner(s, newSource(), &mailtest.Recorder{}, dm)
		if c.noBot {
			r.Matrix = func() run.DM { return nil }
		}
		err := r.MatrixWeek(ctx, week, c.to)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if len(dm.deliveries()) != 0 {
			t.Errorf("%s: delivered %v", c.name, dm.subjects())
		}
	}
	if st := dmStatuses(t, s, "2026-10-26"); len(st) != 0 {
		t.Fatalf("dm_sends: %v", st)
	}
}
