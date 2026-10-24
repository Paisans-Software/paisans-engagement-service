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
		status, err := r.one(ctx, m, from, week, items, stats)
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
		if err := r.Store.Finish(wctx, m.Subject, week, status); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("record a send: %w", err)
		}
		cancel()
	}
	slog.Info("digest run", "week", week.Format("2006-01-02"), "sent", n.sent, "empty", n.empty,
		"unsubscribed", n.unsubscribed, "failed", n.failed, "unclaimed", n.unclaimed, "skipped", n.skipped)
	return firstErr
}

// one handles a claimed member and says how their send ended. An empty status
// means nothing was handed to the relay, so the claim is to be released. The error explains a failure; it carries no address, since the
// sender's errors are already reduced to a reason and a code.
func (r *Runner) one(ctx context.Context, m digest.Member, from, to time.Time, items []digest.Item, stats digest.Stats) (store.Status, error) {
	unsub, err := r.Store.Unsubscribed(ctx, m.Subject)
	if err != nil {
		return "", fmt.Errorf("read unsubscribes: %w", err)
	}
	if unsub {
		return store.StatusUnsubscribed, nil
	}
	v, err := r.Source.View(ctx, m, from, to, items)
	if err != nil {
		return "", fmt.Errorf("read the member's view: %w", err)
	}
	d, ok := digest.Build(items, v, stats)
	if !ok {
		return store.StatusEmpty, nil
	}
	msg, err := mail.Render(d, r.Names, r.SiteURL, r.PublicURL+"/u/"+token.Sign(r.Key, m.Subject), from, to)
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	msg.To = m.Email
	if err := r.Sender.Send(ctx, msg); err != nil {
		if errors.Is(err, mail.ErrNotSent) {
			return "", err
		}
		return store.StatusFailed, err
	}
	return store.StatusSent, nil
}

// maxBackoff is the longest Loop waits between attempts at a failing week.
const maxBackoff = 15 * time.Minute

// Loop checks every tick whether a week is due and runs it until it succeeds
// once. After a failed run the wait doubles, up to maxBackoff, so an outage
// that lasts all day does not re-read Mbin every tick. The weeks that
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
		}
		t.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
