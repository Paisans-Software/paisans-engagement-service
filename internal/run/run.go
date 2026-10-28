package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/digest"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/token"
)

// Source is what a week's run reads from Mbin.
type Source interface {
	Members(ctx context.Context) ([]digest.Member, error)
	Hot(ctx context.Context, from, to time.Time) ([]digest.Item, error)
	View(ctx context.Context, m digest.Member, from, to time.Time, items []digest.Item) (digest.View, error)
	Stats(ctx context.Context, from, to time.Time) (digest.Stats, error)
}

type Runner struct {
	Store     *store.Store
	Source    Source
	Sender    mail.Sender
	Names     mail.Names
	SiteURL   string
	PublicURL string
	Key       []byte
	// Only, when set, limits a run to the member with that Mbin username,
	// so a test send mails one person. Nobody else is claimed, so a later
	// full run for the same week still reaches them.
	Only string
}

// Week sends the digest for the week that ends at week, the send time. Every
// member is claimed in the store before anything is sent to them, so a second
// call, a restart or a second replica skips whoever is already claimed.
//
// A send the mail server refused at DATA or after is recorded as failed and
// not retried: the message may have gone out. A member whose digest was never
// handed to the relay, because reading or rendering it failed or the relay
// refused it earlier, has their claim released, and Week returns an error so
// the next attempt reaches them; so does any error reading Mbin or the store.
// When the relay itself is unavailable the run stops at that member, since
// everyone after them would fail the same way.
func (r *Runner) Week(ctx context.Context, week time.Time) error {
	from := week.AddDate(0, 0, -7)
	// Everything shared is read before anyone is claimed, so an outage of
	// Mbin leaves every member to the next attempt.
	members, err := r.Source.Members(ctx)
	if err != nil {
		return fmt.Errorf("read members: %w", err)
	}
	if r.Only != "" {
		members = only(members, r.Only)
		if len(members) == 0 {
			// No username in the message: the operator typed it, and the
			// error reaches the log.
			return errors.New("no member with that username gets the digest")
		}
	}
	items, err := r.Source.Hot(ctx, from, week)
	if err != nil {
		return fmt.Errorf("read the week's threads: %w", err)
	}
	stats, err := r.Source.Stats(ctx, from, week)
	if err != nil {
		return fmt.Errorf("read the week's figures: %w", err)
	}

	var n struct{ sent, empty, unsubscribed, failed, unclaimed, skipped int }
	var firstErr error
	for _, m := range members {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := r.Store.Claim(ctx, m.Subject, week)
		if err != nil {
			// Unclaimed, so the retry that the error causes reaches them.
			n.unclaimed++
			if firstErr == nil {
				firstErr = fmt.Errorf("claim a send: %w", err)
			}
			continue
		}
		if !ok {
			n.skipped++
			continue
		}
		status, shown, err := r.one(ctx, m, from, week, items, stats)
		// Released or finished even when ctx has ended, or the member would
		// stay claimed and unsent for the week.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if status == "" {
			n.unclaimed++
			if firstErr == nil {
				firstErr = err
			}
			if rerr := r.Store.Release(wctx, m.Subject, week); rerr != nil {
				slog.Error("digest claim not released; the member is skipped this week", "member", m.ID, "error", rerr)
			}
			cancel()
			if errors.Is(err, mail.ErrRelayUnavailable) {
				slog.Warn("mail relay unavailable; the remaining members wait for the next attempt", "error", err)
				break
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
		if err := r.Store.Finish(wctx, m.Subject, week, status, shown...); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("record a send: %w", err)
		}
		cancel()
	}
	slog.Info("digest run", "week", week.Format("2006-01-02"), "sent", n.sent, "empty", n.empty,
		"unsubscribed", n.unsubscribed, "failed", n.failed, "unclaimed", n.unclaimed, "skipped", n.skipped)
	return firstErr
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

// one handles a claimed member and says how their send ended, and which
// threads a sent digest showed. An empty status means nothing was handed to
// the relay, so the claim is to be released. The error explains a failure; it
// carries no address, since the sender's errors are already reduced to a
// reason and a code.
func (r *Runner) one(ctx context.Context, m digest.Member, from, to time.Time, items []digest.Item, stats digest.Stats) (store.Status, []store.Item, error) {
	unsub, err := r.Store.Unsubscribed(ctx, m.Subject)
	if err != nil {
		return "", nil, fmt.Errorf("read unsubscribes: %w", err)
	}
	if unsub {
		return store.StatusUnsubscribed, nil, nil
	}
	d, ok, err := r.build(ctx, m, from, to, items, stats)
	if err != nil {
		return "", nil, err
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

// build is the member's digest for the week, and false when it has nothing
// in it. It only reads.
func (r *Runner) build(ctx context.Context, m digest.Member, from, to time.Time, items []digest.Item, stats digest.Stats) (digest.Digest, bool, error) {
	v, err := r.Source.View(ctx, m, from, to, items)
	if err != nil {
		return digest.Digest{}, false, fmt.Errorf("read the member's view: %w", err)
	}
	shown, err := r.Store.Shown(ctx, m.Subject)
	if err != nil {
		return digest.Digest{}, false, fmt.Errorf("read what the member was shown: %w", err)
	}
	v.Shown = map[digest.Key]bool{}
	for it := range shown {
		v.Shown[digest.Key{Kind: digest.Kind(it.Kind), ID: it.ID}] = true
	}
	d, ok := digest.Build(items, v, stats)
	return d, ok, nil
}

// Preview mails the member with username to the digest that the member with
// username as would get for the week, so a tester who has joined everything
// still sees a full one. The subject is marked as a preview, and the
// unsubscribe link is the recipient's own, never the other member's.
//
// Nobody is claimed and nothing is recorded as shown, so the week's real run
// still reaches both members with the same digest. A member who would get no
// digest, because they unsubscribed or have nothing left, is refused, and so
// is a recipient who unsubscribed.
func (r *Runner) Preview(ctx context.Context, week time.Time, to, as string) error {
	from := week.AddDate(0, 0, -7)
	members, err := r.Source.Members(ctx)
	if err != nil {
		return fmt.Errorf("read members: %w", err)
	}
	// No usernames in the messages: the operator typed them, and the
	// errors reach the log.
	recipient, viewer := only(members, to), only(members, as)
	if len(recipient) == 0 {
		return errors.New("no member with the --member username gets the digest")
	}
	if len(viewer) == 0 {
		return errors.New("no member with the --as username gets the digest")
	}
	rm, vm := recipient[0], viewer[0]
	for _, c := range []struct {
		m    digest.Member
		flag string
	}{{rm, "--member"}, {vm, "--as"}} {
		unsub, err := r.Store.Unsubscribed(ctx, c.m.Subject)
		if err != nil {
			return fmt.Errorf("read unsubscribes: %w", err)
		}
		if unsub {
			return fmt.Errorf("the %s member unsubscribed, so nothing is sent", c.flag)
		}
	}
	items, err := r.Source.Hot(ctx, from, week)
	if err != nil {
		return fmt.Errorf("read the week's threads: %w", err)
	}
	stats, err := r.Source.Stats(ctx, from, week)
	if err != nil {
		return fmt.Errorf("read the week's figures: %w", err)
	}
	d, ok, err := r.build(ctx, vm, from, week, items, stats)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the --as member has nothing to read this week, so there is no digest to preview")
	}
	msg, err := mail.Render(d, r.Names, r.SiteURL, r.PublicURL+"/u/"+token.Sign(r.Key, rm.Subject), from, week)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	msg.Subject = "[Preview as " + vm.Username + "] " + msg.Subject
	msg.To = rm.Email
	if err := r.Sender.Send(ctx, msg); err != nil {
		return err
	}
	slog.Info("digest preview sent", "week", week.Format("2006-01-02"), "member", rm.ID, "as", vm.ID)
	return nil
}

// sent is every thread the digest shows.
func sent(d digest.Digest) []store.Item {
	var out []store.Item
	if d.Featured != nil {
		out = append(out, store.Item{Kind: string(d.Featured.Item.Key.Kind), ID: d.Featured.Item.Key.ID})
	}
	for _, it := range d.More {
		out = append(out, store.Item{Kind: string(it.Key.Kind), ID: it.Key.ID})
	}
	return out
}

// maxBackoff is the longest Loop waits between attempts at a failing week.
const maxBackoff = 15 * time.Minute

// Loop checks every tick whether a week is due and runs it until it succeeds
// once. After a failed run the wait doubles, up to maxBackoff, so an outage
// that lasts all day does not re-read Mbin every tick; it is back to one tick
// once no week is due. The weeks that
// succeeded are remembered only in memory: after a restart, the claims in the
// store keep a rerun from mailing anyone twice.
func Loop(ctx context.Context, r *Runner, now func() time.Time, tick time.Duration) {
	done := map[string]bool{}
	backoff := tick
	t := time.NewTimer(tick)
	defer t.Stop()
	for {
		wait := tick
		if week, ok := Due(now()); ok {
			k := week.Format("2006-01-02")
			if !done[k] {
				if err := r.Week(ctx, week); err != nil {
					slog.Error("digest run failed; it is retried until the day ends", "error", err, "retry_in", backoff.String())
					wait = backoff
					backoff = min(2*backoff, maxBackoff)
				} else {
					done[k] = true
					backoff = tick
				}
			}
		} else {
			// The day is over, so the next one starts from a short wait
			// whether this one ended in success or not.
			backoff = tick
		}
		t.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
