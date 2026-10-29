package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
	"github.com/paisans-software/paisans-engagement-service/internal/digest"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/token"
)

// Source is what a week's run reads from Mbin.
type Source interface {
	Members(ctx context.Context) ([]digest.Member, error)
	Hot(ctx context.Context, from, to time.Time) ([]digest.Item, error)
	Unanswered(ctx context.Context, from, to time.Time) ([]digest.Item, error)
	View(ctx context.Context, m digest.Member, from, to time.Time, items []digest.Item) (digest.View, error)
	Stats(ctx context.Context, from, to time.Time) (digest.Stats, error)
}

// DM is the Matrix bot as the run uses it.
type DM interface {
	Prepare(ctx context.Context, subject string) (bot.Target, bool, error)
	Deliver(ctx context.Context, t bot.Target, txnID string, n bot.Notice) (bot.Outcome, error)
}

// ErrBotNotReady means the Matrix bot is configured but still starting, so
// the Matrix half of the run waits for the next attempt.
var ErrBotNotReady = errors.New("the Matrix bot is not ready")

type Runner struct {
	Store  *store.Store
	Source Source
	// Sender is nil when there is no mail host, and then no email is sent.
	Sender    mail.Sender
	Names     mail.Names
	SiteURL   string
	PublicURL string
	Key       []byte

	// Matrix is nil, or returns nil, when this copy runs no Matrix bot.
	Matrix func() DM
	// MatrixConfigured is true when every Matrix variable is set.
	MatrixConfigured bool
	// MatrixPending, when set, reports whether the bot is configured here
	// but has not started yet. Nil means it never is.
	MatrixPending func() bool
	// Now is the clock the Matrix half reads; nil is time.Now.
	Now func() time.Time
	// Only, when set, limits a run to the member with that Mbin username,
	// so a test send mails one person. Nobody else is claimed, so a later
	// full run for the same week still reaches them.
	Only string
	// Rand picks the unanswered threads each digest offers; nil is the
	// random package's own source.
	Rand *rand.Rand

	// anyDay lifts the end of the send day from the Matrix pass, for a
	// Matrix send run by hand on a later day.
	anyDay bool
}

func (r *Runner) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// Week sends the digest for the week that ends at week, the send time, by
// email and as a Matrix notice. Each member's digest is built once and stored,
// so the email, the notice and the web page it links to show the same
// threads, and a rerun or a second replica reuses it rather than building
// another. A member gets a digest built when it is going to be used: an email
// they are claimed for and have not unsubscribed from, or a Matrix notice
// they have not turned off.
//
// The email pass claims every member in the store before anything is sent to
// them, so a second call, a restart or a second replica skips whoever is
// already claimed. A send the mail server refused at DATA or after is
// recorded as failed and not retried: the message may have gone out. A member
// whose email was never handed to the relay, because reading or rendering it
// failed or the relay refused it earlier, has their claim released, and Week
// returns an error so the next attempt reaches them; so does any error reading
// Mbin or the store. When the relay itself is unavailable no further email is
// attempted, since everyone after would fail the same way.
//
// The Matrix pass then sends each member with a stored digest their notice,
// under a claim of its own, until the end of the send day: a weekly notice is
// never sent the next day, and reaching midnight stops the pass without an
// error. A notice the homeserver refused is recorded as failed and not
// retried. When the homeserver is unavailable the pass stops at that member,
// releases their claim, and Week returns the error. A bot still starting
// fails the pass, so the next attempt delivers once it is up.
//
// Both passes always run; Week returns their errors joined. A copy that runs
// no Matrix bot, while another copy holds it, skips the Matrix pass without an
// error; Loop sends it later the same day if this copy takes the bot over.
func (r *Runner) Week(ctx context.Context, week time.Time) error {
	_, err := r.week(ctx, week)
	return err
}

// matrixHalf is what the Matrix pass of a week needs once the email pass is
// done, kept for a copy that had no bot when the week ran.
type matrixHalf struct {
	members  []digest.Member
	matrixOn map[string]bool
	// notBefore, when set, is the earliest time the pass is tried again:
	// the members left have a Matrix send still pending, and one left by a
	// sender that stopped is only reclaimed after store.StaleDM.
	notBefore time.Time
}

// week is Week. The matrixHalf is not nil when the Matrix pass was skipped,
// or stopped, because this copy runs no bot, or left members whose send is
// still pending.
func (r *Runner) week(ctx context.Context, week time.Time) (*matrixHalf, error) {
	from := week.AddDate(0, 0, -7)
	// Everything shared is read before anyone is claimed, so an outage of
	// Mbin leaves every member to the next attempt.
	members, err := r.Source.Members(ctx)
	if err != nil {
		return nil, fmt.Errorf("read members: %w", err)
	}
	if r.Only != "" {
		members = only(members, r.Only)
		if len(members) == 0 {
			// No username in the message: the operator typed it, and the
			// error reaches the log.
			return nil, errors.New("no member with that username gets the digest")
		}
	}
	items, err := r.Source.Hot(ctx, from, week)
	if err != nil {
		return nil, fmt.Errorf("read the week's threads: %w", err)
	}
	fresh, err := r.Source.Unanswered(ctx, from, week)
	if err != nil {
		return nil, fmt.Errorf("read the week's unanswered threads: %w", err)
	}
	stats, err := r.Source.Stats(ctx, from, week)
	if err != nil {
		return nil, fmt.Errorf("read the week's figures: %w", err)
	}
	matrixOn, emailErr := r.emailPass(ctx, members, from, week, items, fresh, stats)
	later, matrixErr := r.matrixPass(ctx, members, week, matrixOn)
	return later, errors.Join(emailErr, matrixErr)
}

// bot is the Matrix bot this copy runs, or nil.
func (r *Runner) bot() DM {
	if r.Matrix == nil {
		return nil
	}
	return r.Matrix()
}

// noBot handles a missing bot: one still starting on this copy is an error,
// so the run is tried again; one that another copy runs is not.
func (r *Runner) noBot() error {
	if r.MatrixPending != nil && r.MatrixPending() {
		slog.Warn("the Matrix bot is still starting; the Matrix notices wait for the next attempt")
		return fmt.Errorf("matrix run: %w", ErrBotNotReady)
	}
	return nil
}

// emailPass mails every member who has a verified address and builds the
// digest of every member who gets one by either channel. It returns the
// subjects whose Matrix notice is on.
func (r *Runner) emailPass(ctx context.Context, members []digest.Member, from, week time.Time, items, fresh []digest.Item, stats digest.Stats) (map[string]bool, error) {
	matrixOn := map[string]bool{}
	var n struct{ sent, empty, unsubscribed, failed, unclaimed, skipped int }
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	relayDown := false
	for _, m := range members {
		if err := ctx.Err(); err != nil {
			fail(err)
			break
		}
		emailOn := r.Sender != nil && m.Email != "" && !relayDown
		on := false
		if r.MatrixConfigured {
			mm, err := r.Store.MatrixMember(ctx, m.Subject)
			if err != nil {
				// Nothing claimed, so the retry that the error causes
				// reaches them. Only an email can be left unclaimed.
				if emailOn {
					n.unclaimed++
				}
				fail(fmt.Errorf("read the Matrix setting: %w", err))
				continue
			}
			on = mm.Enabled
		}
		if on {
			matrixOn[m.Subject] = true
		}
		if !emailOn && !on {
			continue
		}
		claimed := false
		if emailOn {
			ok, err := r.Store.Claim(ctx, m.Subject, week)
			switch {
			case err != nil:
				// Unclaimed, so the retry that the error causes reaches them.
				n.unclaimed++
				fail(fmt.Errorf("claim a send: %w", err))
			case !ok:
				n.skipped++
			default:
				claimed = true
			}
		}
		if !claimed && !on {
			continue
		}
		status, shown, err := r.one(ctx, m, claimed, on, from, week, items, fresh, stats)
		if !claimed {
			if err != nil {
				fail(err)
			}
			continue
		}
		// Released or finished even when ctx has ended, or the member would
		// stay claimed and unsent for the week.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if status == "" {
			n.unclaimed++
			fail(err)
			if rerr := r.Store.Release(wctx, m.Subject, week); rerr != nil {
				slog.Error("digest claim not released; the member is skipped this week", "member", m.ID, "error", rerr)
			}
			cancel()
			if errors.Is(err, mail.ErrRelayUnavailable) {
				slog.Warn("mail relay unavailable; the remaining members wait for the next attempt", "error", err)
				relayDown = true
			}
			continue
		}
		switch status {
		case store.StatusSent:
			n.sent++
		case store.StatusEmpty:
			n.empty++
		case store.StatusUnsubscribed:
			n.unsubscribed++
		default:
			n.failed++
			// The member id identifies the row in Mbin without naming them.
			slog.Warn("digest not sent", "member", m.ID, "error", err)
		}
		if err := r.Store.Finish(wctx, m.Subject, week, status, shown...); err != nil {
			fail(fmt.Errorf("record a send: %w", err))
		}
		cancel()
	}
	slog.Info("digest run", "week", week.Format("2006-01-02"), "sent", n.sent, "empty", n.empty,
		"unsubscribed", n.unsubscribed, "failed", n.failed, "unclaimed", n.unclaimed, "skipped", n.skipped)
	return matrixOn, firstErr
}

// only is the members whose username is exactly username.
func only(members []digest.Member, username string) []digest.Member {
	var out []digest.Member
	for _, m := range members {
		if m.Username == username {
			out = append(out, m)
		}
	}
	return out
}

// one builds the member's digest when it will be used and, when claimed is
// true, says how their email ended and which threads a sent one showed. An
// empty status for a claimed member means nothing was handed to the relay, so
// the claim is to be released. The error explains a failure; it carries no
// address, since the sender's errors are already reduced to a reason and a
// code.
func (r *Runner) one(ctx context.Context, m digest.Member, claimed, matrixOn bool, from, to time.Time, items, fresh []digest.Item, stats digest.Stats) (store.Status, []store.Item, error) {
	unsub := false
	if claimed {
		var err error
		if unsub, err = r.Store.Unsubscribed(ctx, m.Subject); err != nil {
			return "", nil, fmt.Errorf("read unsubscribes: %w", err)
		}
		if unsub && !matrixOn {
			return store.StatusUnsubscribed, nil, nil
		}
	}
	d, ok, err := r.digestFor(ctx, m, from, to, items, fresh, stats)
	if err != nil || !claimed {
		return "", nil, err
	}
	if unsub {
		return store.StatusUnsubscribed, nil, nil
	}
	if !ok {
		return store.StatusEmpty, nil, nil
	}
	msg, err := mail.Render(d, r.Names, r.SiteURL, r.PublicURL+"/u/"+token.Sign(r.Key, m.Subject), from, to)
	if err != nil {
		return "", nil, fmt.Errorf("render: %w", err)
	}
	msg.To = m.Email
	if err := r.Sender.Send(ctx, msg); err != nil {
		if errors.Is(err, mail.ErrNotSent) {
			return "", nil, err
		}
		// The message may have gone out, but a thread is only remembered
		// as shown once it surely was.
		return store.StatusFailed, nil, err
	}
	return store.StatusSent, sent(d), nil
}

// digestFor is the member's digest for the week: the stored one if there is
// one, else a new one, stored before it is used so that every copy of the
// service uses the digest stored first. An empty digest is reported as false
// and not stored. Shown is read for weeks before this one only, so what this
// week's sends record does not change the digest.
func (r *Runner) digestFor(ctx context.Context, m digest.Member, from, week time.Time, items, fresh []digest.Item, stats digest.Stats) (digest.Digest, bool, error) {
	var d digest.Digest
	raw, found, err := r.Store.SavedDigest(ctx, m.Subject, week)
	if err != nil {
		return d, false, fmt.Errorf("read the stored digest: %w", err)
	}
	if !found {
		// The unanswered threads are asked about too, so the member's own
		// are among the joined.
		v, err := r.Source.View(ctx, m, from, week, slices.Concat(items, fresh))
		if err != nil {
			return d, false, fmt.Errorf("read the member's view: %w", err)
		}
		shown, err := r.Store.Shown(ctx, m.Subject, week)
		if err != nil {
			return d, false, fmt.Errorf("read what the member was shown: %w", err)
		}
		v.Shown = map[digest.Key]bool{}
		for it := range shown {
			v.Shown[digest.Key{Kind: digest.Kind(it.Kind), ID: it.ID}] = true
		}
		built, ok := digest.Build(items, v, stats)
		if !ok {
			return d, false, nil
		}
		built.AddUnanswered(fresh, v, r.Rand)
		content, err := json.Marshal(built)
		if err != nil {
			return d, false, fmt.Errorf("encode the digest: %w", err)
		}
		if raw, err = r.Store.SaveDigest(ctx, m.Subject, week, content); err != nil {
			return d, false, fmt.Errorf("store the digest: %w", err)
		}
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, false, fmt.Errorf("read the stored digest: %w", err)
	}
	return d, true, nil
}

// matrixPass sends the Monday notice to every member in members whose
// notice is on and who has a stored digest for the week. The matrixHalf
// returned is not nil when the pass is to be tried again the same day: it was
// skipped or stopped because this copy runs no bot, or runs a different one
// than the pass began with, or some members' sends are claimed and not
// finished, which may be a sender that stopped before finishing.
func (r *Runner) matrixPass(ctx context.Context, members []digest.Member, week time.Time, matrixOn map[string]bool) (later *matrixHalf, err error) {
	if !r.MatrixConfigured {
		return nil, nil
	}
	// The send day ends at local midnight after week, in week's location.
	// A send by hand with anyDay has no end.
	y, mo, d := week.Date()
	end := time.Date(y, mo, d+1, 0, 0, 0, 0, week.Location())
	now := r.now()
	if !r.anyDay && !now.Before(end) {
		slog.Info("the send day is over; no Matrix notices are sent", "week", week.Format("2006-01-02"))
		return nil, nil
	}
	dm := r.bot()
	if dm == nil {
		err := r.noBot()
		if err != nil {
			return nil, err
		}
		return &matrixHalf{members: members, matrixOn: matrixOn}, nil
	}

	var n struct{ sent, refused, off, noAccount, released, skipped, pending int }
	var pending []digest.Member
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	defer func() {
		slog.Info("matrix run", "week", week.Format("2006-01-02"), "sent", n.sent, "refused", n.refused,
			"off", n.off, "no_account", n.noAccount, "released", n.released, "skipped", n.skipped, "pending", n.pending)
	}()

	// Waits for the homeserver's rate limits end with the day too. A wait
	// that would outlast the day ends early with context.DeadlineExceeded
	// while dctx itself is still live, and that too is the day ending.
	dctx, cancel := ctx, context.CancelFunc(func() {})
	if !r.anyDay {
		dctx, cancel = context.WithTimeout(ctx, end.Sub(now))
	}
	defer cancel()
	ended := false
	dayOver := func(err error) bool {
		if r.anyDay {
			return false
		}
		if ctx.Err() == nil && (dctx.Err() != nil || !r.now().Before(end) || errors.Is(err, context.DeadlineExceeded)) {
			ended = true
		}
		return ended
	}
	defer func() {
		if ended {
			slog.Info("the send day is over; the remaining Matrix notices are not sent", "week", week.Format("2006-01-02"))
		}
	}()

	saved, err := r.Store.DigestsForWeek(dctx, week)
	if err != nil {
		if dayOver(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the week's digests: %w", err)
	}

	// stop handles an error from the bot and reports whether the pass ends
	// with it. The member id identifies the row in Mbin without naming them.
	stop := func(m digest.Member, err error) bool {
		switch {
		case dayOver(err):
			return true
		case ctx.Err() != nil:
			fail(ctx.Err())
			return true
		case errors.Is(err, bot.ErrUnavailable):
			slog.Warn("homeserver unavailable; the remaining members wait for the next attempt", "member", m.ID, "error", err)
			fail(fmt.Errorf("matrix run: %w", err))
			return true
		default:
			slog.Warn("matrix notice not sent", "member", m.ID, "error", err)
			fail(fmt.Errorf("matrix run: %w", err))
			return false
		}
	}

	for _, m := range members {
		if dayOver(nil) {
			break
		}
		if err := ctx.Err(); err != nil {
			fail(err)
			break
		}
		raw, ok := saved[m.Subject]
		if !ok || !matrixOn[m.Subject] {
			continue
		}
		// The bot lock may have moved since the pass began; only the copy
		// that holds it now sends.
		if cur := r.bot(); cur != dm {
			slog.Info("the Matrix bot changed during the Matrix run; the remaining notices wait for it", "week", week.Format("2006-01-02"))
			if err := r.noBot(); err != nil {
				fail(err)
				return nil, firstErr
			}
			return &matrixHalf{members: members, matrixOn: matrixOn}, firstErr
		}
		var dg digest.Digest
		if err := json.Unmarshal(raw, &dg); err != nil {
			fail(fmt.Errorf("read the stored digest: %w", err))
			continue
		}
		t, ok, err := dm.Prepare(dctx, m.Subject)
		if err != nil {
			if stop(m, err) {
				break
			}
			continue
		}
		if !ok {
			n.noAccount++
			continue
		}
		ok, err = r.Store.ClaimDM(dctx, m.Subject, week)
		if err != nil {
			if dayOver(err) {
				break
			}
			fail(fmt.Errorf("claim a Matrix send: %w", err))
			continue
		}
		if !ok {
			// A finished send is done. A pending one may belong to a sender
			// that stopped before finishing, and is claimed again once it is
			// stale.
			held, err := r.Store.DMPending(dctx, m.Subject, week)
			switch {
			case err != nil && dayOver(err):
			case err != nil:
				fail(fmt.Errorf("read a Matrix send: %w", err))
				pending = append(pending, m)
				n.pending++
			case held:
				pending = append(pending, m)
				n.pending++
			default:
				n.skipped++
			}
			continue
		}
		link := r.PublicURL + "/d/" + token.SignDigest(r.Key, m.Subject, week)
		out, err := dm.Deliver(dctx, t, bot.TxnID(m.Subject, week), bot.Weekly(dg, r.Names.Meta, link))
		// Released or finished even when the day or ctx has ended, or the
		// member would stay claimed and unsent for the week.
		wctx, wcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err == nil && out == bot.Off {
			// Turned off since Prepare: nothing was sent, and a start later
			// today may still reach them.
			n.off++
			if rerr := r.Store.ReleaseDM(wctx, m.Subject, week); rerr != nil {
				slog.Error("matrix claim not released; the member is skipped this week", "member", m.ID, "error", rerr)
			}
			wcancel()
			continue
		}
		if err == nil && out != bot.Sent && out != bot.Refused {
			err = fmt.Errorf("unknown delivery outcome %d", out)
		}
		if err != nil {
			n.released++
			if rerr := r.Store.ReleaseDM(wctx, m.Subject, week); rerr != nil {
				slog.Error("matrix claim not released; the member is skipped this week", "member", m.ID, "error", rerr)
			}
			wcancel()
			if stop(m, err) {
				break
			}
			continue
		}
		status, shown := store.StatusSent, sent(dg)
		if out == bot.Refused {
			status, shown = store.StatusFailed, nil
			n.refused++
			slog.Warn("matrix notice refused; the member is not retried this week", "member", m.ID)
		} else {
			n.sent++
		}
		if err := r.Store.FinishDM(wctx, m.Subject, week, status, shown...); err != nil {
			fail(fmt.Errorf("record a Matrix send: %w", err))
		}
		wcancel()
	}
	if len(pending) > 0 {
		return &matrixHalf{members: pending, matrixOn: matrixOn, notBefore: r.now().Add(store.StaleDM)}, firstErr
	}
	return nil, firstErr
}

// Preview mails the member with that username the digest a brand-new member
// would get for the week: nothing joined, blocked or followed, and nothing
// shown before. A tester who has joined everything still sees a full digest,
// and no member's own activity is read. The subject is marked as a preview,
// and the unsubscribe link is the recipient's own.
//
// Nobody is claimed, nothing is recorded as shown and no digest is stored, so
// the week's real run still reaches the member and builds their own. A
// recipient who unsubscribed or has no verified address is refused, and so is
// a week with nothing for even a blank member.
func (r *Runner) Preview(ctx context.Context, week time.Time, username string) error {
	m, err := r.member(ctx, username)
	if err != nil {
		return err
	}
	if m.Email == "" {
		return errors.New("the member has no verified email address, so no preview is sent")
	}
	unsub, err := r.Store.Unsubscribed(ctx, m.Subject)
	if err != nil {
		return fmt.Errorf("read unsubscribes: %w", err)
	}
	if unsub {
		return errors.New("the member unsubscribed, so no preview is sent")
	}
	d, err := r.blankDigest(ctx, week)
	if err != nil {
		return err
	}
	from := week.AddDate(0, 0, -7)
	msg, err := mail.Render(d, r.Names, r.SiteURL, r.PublicURL+"/u/"+token.Sign(r.Key, m.Subject), from, week)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	msg.Subject = "[Preview] " + msg.Subject
	msg.To = m.Email
	if err := r.Sender.Send(ctx, msg); err != nil {
		return err
	}
	slog.Info("digest preview sent", "week", week.Format("2006-01-02"), "member", m.ID)
	return nil
}

// PreviewMatrix sends the member with that username the Monday notice built
// from the digest a brand-new member would get for the week, as Preview
// builds it, marked as a preview and with a new transaction id, into their
// room with the bot, which is made if they have none. Its link opens a copy
// of that digest stored apart from the week's digests for store.PreviewLife.
//
// Nobody is claimed, nothing is recorded as shown and the member's digest for
// the week is neither stored nor read, so the week's Matrix pass still sends
// them their own notice. A member who is unknown, stopped the bot or has no
// linked Matrix account is refused, and so is a week with nothing for even a
// blank member.
func (r *Runner) PreviewMatrix(ctx context.Context, week time.Time, username string) error {
	m, dm, err := r.matrixMember(ctx, username)
	if err != nil {
		return err
	}
	d, err := r.blankDigest(ctx, week)
	if err != nil {
		return err
	}
	t, err := r.matrixTarget(ctx, dm, m)
	if err != nil {
		return err
	}
	content, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encode the digest: %w", err)
	}
	if err := r.Store.SavePreview(ctx, m.Subject, week, content, r.now().Add(store.PreviewLife)); err != nil {
		return fmt.Errorf("store the preview: %w", err)
	}
	link := r.PublicURL + "/d/" + token.SignPreview(r.Key, m.Subject, week)
	out, err := dm.Deliver(ctx, t, bot.PreviewTxnID(), bot.WeeklyPreview(d, r.Names.Meta, link))
	switch {
	case err != nil:
		return fmt.Errorf("matrix preview: %w", err)
	case out == bot.Off:
		return errors.New("the member stopped the Matrix bot's messages, so no preview is sent")
	case out == bot.Refused:
		return errors.New("the homeserver refused the preview")
	case out != bot.Sent:
		return fmt.Errorf("unknown delivery outcome %d", out)
	}
	slog.Info("matrix preview sent", "week", week.Format("2006-01-02"), "member", m.ID)
	return nil
}

// MatrixWeek sends the member with that username their Matrix notice for the
// week now, as the Matrix pass of Week would for them alone, on whatever day
// it runs: their digest is the stored one or built and stored, the send is
// claimed and recorded, and a member whose notice for the week was already
// sent is skipped. No email is sent and nobody else is claimed. A member who
// is unknown, stopped the bot or has no linked Matrix account is refused.
func (r *Runner) MatrixWeek(ctx context.Context, week time.Time, username string) error {
	m, dm, err := r.matrixMember(ctx, username)
	if err != nil {
		return err
	}
	if _, err := r.matrixTarget(ctx, dm, m); err != nil {
		return err
	}
	one := *r
	one.Sender, one.Only, one.anyDay = nil, username, true
	return one.Week(ctx, week)
}

// member is the member with that username.
func (r *Runner) member(ctx context.Context, username string) (digest.Member, error) {
	members, err := r.Source.Members(ctx)
	if err != nil {
		return digest.Member{}, fmt.Errorf("read members: %w", err)
	}
	members = only(members, username)
	if len(members) == 0 {
		// No username in the message: the operator typed it, and the
		// error reaches the log.
		return digest.Member{}, errors.New("no member with that username gets the digest")
	}
	return members[0], nil
}

// matrixMember is the member with that username and the running bot, or a
// refusal: the member is unknown or stopped the bot, or no bot runs.
func (r *Runner) matrixMember(ctx context.Context, username string) (digest.Member, DM, error) {
	m, err := r.member(ctx, username)
	if err != nil {
		return m, nil, err
	}
	dm := r.bot()
	if dm == nil {
		return m, nil, errors.New("the Matrix bot is not running, so nothing is sent")
	}
	mm, err := r.Store.MatrixMember(ctx, m.Subject)
	if err != nil {
		return m, nil, fmt.Errorf("read the Matrix setting: %w", err)
	}
	if !mm.Enabled {
		return m, nil, errors.New("the member stopped the Matrix bot's messages, so nothing is sent")
	}
	return m, dm, nil
}

// matrixTarget is the member's Matrix target, or a refusal when MAS links no
// usable Matrix account to them.
func (r *Runner) matrixTarget(ctx context.Context, dm DM, m digest.Member) (bot.Target, error) {
	t, ok, err := dm.Prepare(ctx, m.Subject)
	if err != nil {
		return t, fmt.Errorf("look up the member's Matrix account: %w", err)
	}
	if !ok {
		return t, errors.New("the member has no Matrix account linked through MAS, or it is locked or deactivated, so nothing is sent")
	}
	return t, nil
}

// blankDigest is the digest a brand-new member would get for the week, or an
// error when the week has nothing for even them.
func (r *Runner) blankDigest(ctx context.Context, week time.Time) (digest.Digest, error) {
	from := week.AddDate(0, 0, -7)
	items, err := r.Source.Hot(ctx, from, week)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("read the week's threads: %w", err)
	}
	fresh, err := r.Source.Unanswered(ctx, from, week)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("read the week's unanswered threads: %w", err)
	}
	stats, err := r.Source.Stats(ctx, from, week)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("read the week's figures: %w", err)
	}
	d, ok := digest.Build(items, digest.View{}, stats)
	if !ok {
		return d, errors.New("the week has nothing for even a new member, so there is no digest to preview")
	}
	d.AddUnanswered(fresh, digest.View{}, r.Rand)
	return d, nil
}

// sent is every thread the digest shows.
func sent(d digest.Digest) []store.Item {
	var out []store.Item
	if d.Featured != nil {
		out = append(out, store.Item{Kind: string(d.Featured.Item.Key.Kind), ID: d.Featured.Item.Key.ID})
	}
	for _, it := range slices.Concat(d.More, d.Unanswered) {
		out = append(out, store.Item{Kind: string(it.Key.Kind), ID: it.Key.ID})
	}
	return out
}

// maxBackoff is the longest Loop waits between attempts at a failing week.
const maxBackoff = 15 * time.Minute

// purgeEvery is how often Loop deletes expired web digests.
const purgeEvery = time.Hour

// purge deletes the web digests expired by now. A failure is logged and left
// to the next purge.
func purge(ctx context.Context, st *store.Store, now time.Time) {
	n, err := st.PurgeDigests(ctx, now)
	if err != nil {
		slog.Error("expired web digests not deleted", "error", err)
		return
	}
	if n > 0 {
		slog.Info("expired web digests deleted", "count", n)
	}
}

// Loop checks every tick whether a week is due and runs it until it succeeds
// once. After a failed run the wait doubles, up to maxBackoff, so an outage
// that lasts all day does not re-read Mbin every tick; it is back to one tick
// once no week is due. A week whose Matrix pass found no bot on this copy is
// not done: once this copy runs the bot, the same day, Loop sends the Matrix
// notices, without reading Mbin or running the email pass again. A member
// whose Matrix send is still pending is tried again once it can be reclaimed,
// store.StaleDM later, while the send day lasts. The weeks
// that succeeded are remembered only in memory: after a restart, the claims
// in the store keep a rerun from mailing anyone twice. At most once an hour it
// also deletes the web digests that have expired.
func Loop(ctx context.Context, r *Runner, now func() time.Time, tick time.Duration) {
	done := map[string]bool{}
	later := map[string]*matrixHalf{}
	backoff := tick
	var purged time.Time
	t := time.NewTimer(tick)
	defer t.Stop()
	for {
		at := now()
		if purged.IsZero() || at.Sub(purged) >= purgeEvery {
			purged = at
			purge(ctx, r.Store, at)
		}
		wait := tick
		if week, ok := Due(at); ok {
			k := week.Format("2006-01-02")
			var (
				half *matrixHalf
				err  error
				ran  = true
			)
			switch {
			case done[k]:
				ran = false
			case later[k] != nil:
				if r.bot() == nil {
					ran = false
					break
				}
				if at.Before(later[k].notBefore) {
					ran = false
					break
				}
				half, err = r.matrixPass(ctx, later[k].members, week, later[k].matrixOn)
				if half != nil {
					later[k] = half
				}
			default:
				half, err = r.week(ctx, week)
			}
			switch {
			case !ran:
			case err != nil:
				slog.Error("digest run failed; it is retried until the day ends", "error", err, "retry_in", backoff.String())
				wait = backoff
				backoff = min(2*backoff, maxBackoff)
			case half != nil:
				later[k] = half
				backoff = tick
			default:
				done[k] = true
				delete(later, k)
				backoff = tick
			}
		} else {
			// The day is over, so the next one starts from a short wait
			// whether this one ended in success or not.
			backoff = tick
			clear(later)
		}
		t.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
