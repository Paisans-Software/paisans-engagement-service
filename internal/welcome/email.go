package welcome

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mbin"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/token"
)

// The welcome email goes out from SendHour local time, ranks members by
// what they did in the last ActivityWindow, and gives TopPercent of its
// places to the most active.
const (
	SendHour       = 9
	ActivityWindow = 30 * 24 * time.Hour
	TopPercent     = 80
)

// Split divides the daily limit into the most active members' share,
// rounded down, and the random share.
func Split(limit int) (top, random int) {
	top = limit * TopPercent / 100
	return top, limit - top
}

// Recipient is a member the welcome email goes to and the newcomers it
// names for them.
type Recipient struct {
	mbin.Ranked
	Newcomers []store.Welcomed
}

// Pick chooses the day's recipients: up to top of the most active members
// in ranked's order, then up to random drawn with rnd from the members
// left. A member in skip, or one of the newcomers, is never picked. A member
// is named only the newcomers whose thread they have not greeted, and a
// member with none left is passed over for the next.
func Pick(ranked []mbin.Ranked, greeted func(member, entry int64) bool, newcomers []store.Welcomed,
	top, random int, rnd *rand.Rand, skip map[string]bool) []Recipient {
	isNew := map[int64]bool{}
	for _, n := range newcomers {
		isNew[n.MbinID] = true
	}
	lines := func(m mbin.Ranked) []store.Welcomed {
		var out []store.Welcomed
		for _, n := range newcomers {
			if !greeted(m.ID, n.EntryID) {
				out = append(out, n)
			}
		}
		return out
	}
	var picked []Recipient
	var rest []mbin.Ranked
	for _, m := range ranked {
		if skip[m.Subject] || isNew[m.ID] {
			continue
		}
		if len(picked) >= top {
			rest = append(rest, m)
			continue
		}
		if l := lines(m); len(l) > 0 {
			picked = append(picked, Recipient{m, l})
		}
	}
	rnd.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	n := 0
	for _, m := range rest {
		if n >= random {
			break
		}
		if l := lines(m); len(l) > 0 {
			picked = append(picked, Recipient{m, l})
			n++
		}
	}
	return picked
}

// daySeed makes the day's random draw the same on every attempt, so a
// restart picks the same members.
func daySeed(day string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(day))
	return rand.New(rand.NewPCG(h.Sum64(), 0))
}

// DailyEmail sends the day's welcome email once it is SendHour, and never on
// a later day. The members it went to are recorded, so a restart, a retry or
// a second copy neither mails anyone twice nor goes past the limit.
func (r *Runner) DailyEmail(ctx context.Context, now time.Time) error {
	if r.Sender == nil {
		return nil
	}
	y, mo, d := now.Date()
	day := time.Date(y, mo, d, SendHour, 0, 0, 0, now.Location())
	date := day.Format("2006-01-02")
	if now.Before(day) || r.emailed == date {
		return nil
	}
	newcomers, err := r.Store.ClaimWelcomesForDay(ctx, day)
	if err != nil {
		return err
	}
	if len(newcomers) == 0 {
		r.emailed = date
		return nil
	}
	ranked, err := r.Mbin.Active(ctx, now.Add(-ActivityWindow))
	if err != nil {
		return err
	}
	entries := map[int64]bool{}
	var ids []int64
	for _, n := range newcomers {
		if !entries[n.EntryID] {
			entries[n.EntryID] = true
			ids = append(ids, n.EntryID)
		}
	}
	commenters, err := r.Mbin.Commenters(ctx, ids)
	if err != nil {
		return err
	}
	unsub, err := r.Store.WelcomeUnsubscribed(ctx)
	if err != nil {
		return err
	}
	sent, err := r.Store.WelcomeSendsOn(ctx, day)
	if err != nil {
		return err
	}
	top, random := Split(r.DailyLimit)
	greeted := func(member, entry int64) bool { return commenters[entry][member] }
	picks := Pick(ranked, greeted, newcomers, top, random, daySeed(date), unsub)

	count := len(sent)
	var firstErr error
	for _, p := range picks {
		if count >= r.DailyLimit {
			break
		}
		if sent[p.Subject] {
			continue
		}
		won, err := r.Store.ClaimWelcomeSend(ctx, p.Subject, day)
		if err != nil {
			return err
		}
		count++
		if !won {
			continue
		}
		if err := r.mailOne(ctx, p, day); err != nil {
			firstErr = cmpErr(firstErr, err)
			if errors.Is(err, mail.ErrRelayUnavailable) {
				break
			}
		}
	}
	if firstErr == nil {
		r.emailed = date
	}
	return firstErr
}

func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

// mailOne renders and sends one member's email under their claim. A message
// that never reached the relay releases the claim, so a later attempt tries
// again; one that may have reached it is recorded as failed.
func (r *Runner) mailOne(ctx context.Context, p Recipient, day time.Time) error {
	lines := make([]mail.WelcomeLine, len(p.Newcomers))
	for i, n := range p.Newcomers {
		lines[i] = mail.WelcomeLine{Name: n.Name, URL: n.URL}
	}
	unsub := r.PublicURL + "/u/" + token.SignFor(r.Key, token.Welcome, p.Subject)
	msg, err := mail.RenderWelcome(lines, r.Names, r.SiteURL+"/m/"+mbin.WelcomeMagazine, unsub)
	if err != nil {
		if rerr := r.Store.ReleaseWelcomeSend(ctx, p.Subject, day); rerr != nil {
			return rerr
		}
		return err
	}
	msg.To = p.Email
	if err := r.Sender.Send(ctx, msg); err != nil {
		if errors.Is(err, mail.ErrNotSent) {
			if rerr := r.Store.ReleaseWelcomeSend(ctx, p.Subject, day); rerr != nil {
				return rerr
			}
			return fmt.Errorf("welcome email: %w", err)
		}
		slog.Warn("a welcome email failed after the relay took it; it is not retried", "error", err)
		return r.Store.FinishWelcomeSend(ctx, p.Subject, day, store.StatusFailed)
	}
	return r.Store.FinishWelcomeSend(ctx, p.Subject, day, store.StatusSent)
}
