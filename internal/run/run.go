package run

import (
	"context"
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
// A send the mail server refused is recorded as failed and not retried: the
// message may have gone out. A member whose digest failed before it reached
// the sender has their claim released, and Week returns an error so the next
// attempt reaches them; so does any error reading Mbin or the store.
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

	var n struct{ sent, empty, unsubscribed, failed, skipped int }
	var firstErr error
	for _, m := range members {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := r.Store.Claim(ctx, m.Subject, week)
		if err != nil {
			// Unclaimed, so the retry that the error causes reaches them.
			n.failed++
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
		if status == "" {
			n.failed++
			if firstErr == nil {
				firstErr = err
			}
			// Released even when ctx has ended, or the member would stay
			// claimed and unsent for the week.
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if rerr := r.Store.Release(rctx, m.Subject, week); rerr != nil {
				slog.Error("digest claim not released; the member is skipped this week", "member", m.ID, "error", rerr)
			}
			cancel()
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
		if err := r.Store.Finish(ctx, m.Subject, week, status); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("record a send: %w", err)
		}
	}
	slog.Info("digest run", "week", week.Format("2006-01-02"),
		"sent", n.sent, "empty", n.empty, "unsubscribed", n.unsubscribed, "failed", n.failed, "skipped", n.skipped)
	return firstErr
}

// one handles a claimed member and says how their send ended. An empty status
// means it failed before anything reached the sender, so the claim is to be
// released. The error explains a failure; it carries no address, since the
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
		return store.StatusFailed, err
	}
	return store.StatusSent, nil
}

// Loop checks every tick whether a week is due and runs it until it succeeds
// once. The weeks that succeeded are remembered only in memory: after a
// restart, the claims in the store keep a rerun from mailing anyone twice.
func Loop(ctx context.Context, r *Runner, now func() time.Time, tick time.Duration) {
	done := map[string]bool{}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if week, ok := Due(now()); ok {
			k := week.Format("2006-01-02")
			if !done[k] {
				if err := r.Week(ctx, week); err != nil {
					slog.Error("digest run failed; it is retried on the next tick until the day ends", "error", err)
				} else {
					done[k] = true
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
