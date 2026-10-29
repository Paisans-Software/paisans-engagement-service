package welcome

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mbin"
	"github.com/paisans-software/paisans-engagement-service/internal/mbinapi"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
)

// MbinReader is what the welcomes read from Mbin's database.
type MbinReader interface {
	Newcomers(ctx context.Context, since time.Time, skip []int64, limit int) ([]mbin.Newcomer, error)
	WelcomeThread(ctx context.Context, title string, since time.Time) (id int64, slug string, found bool, err error)
	Active(ctx context.Context, since time.Time) ([]mbin.Ranked, error)
	Commenters(ctx context.Context, entryIDs []int64) (map[int64]map[int64]bool, error)
	SubjectsByEmail(ctx context.Context, emails []string) (map[string]string, error)
}

// MbinAPI is what the welcomes do through Mbin's API, as the agent.
type MbinAPI interface {
	MagazineByName(ctx context.Context, name string) (mbinapi.Magazine, bool, error)
	CreateMagazine(ctx context.Context, name, title, description string) (mbinapi.Magazine, error)
	RestrictPosting(ctx context.Context, magazineID int64) error
	CreateThread(ctx context.Context, magazineID int64, title, body string) (mbinapi.Entry, error)
	Subscribe(ctx context.Context, userID, magazineID int64) error
}

// MatrixBot is the Matrix bot as the welcomes use it.
type MatrixBot interface {
	NewMembers(ctx context.Context, since time.Time, skip func(masUserID string) bool, limit int) ([]bot.Newcomer, error)
	CommunityRoom(ctx context.Context, name, alias string) (string, error)
	Announce(ctx context.Context, roomID, txnID string, content map[string]any) error
	Prepare(ctx context.Context, subject string) (bot.Target, bool, error)
	Deliver(ctx context.Context, t bot.Target, txnID string, n bot.Notice) (bot.Outcome, error)
}

// The welcome magazine's title and description, set when the bot creates it.
const (
	magazineTitle       = "Welcome"
	magazineDescription = "New members, announced as they join. Say hello in the comments."
)

// Runner runs the welcomes. A nil API turns the Mbin welcomes off, an empty
// RoomAlias the Matrix ones, and a nil Sender the email; the alerts go by
// whichever channels are on.
type Runner struct {
	Store *store.Store
	Mbin  MbinReader
	API   MbinAPI
	// Bot returns the running Matrix bot, or nil when this copy runs none.
	Bot                 func() MatrixBot
	RoomName, RoomAlias string
	Sender              mail.Sender
	Names               mail.Names
	// SiteURL is Mbin's public address; PublicURL the service's, which the
	// unsubscribe links start with.
	SiteURL, PublicURL string
	Key                []byte
	DailyLimit         int
	Operators          []string

	room            string    // the community room's id, once found
	emailed         string    // the day whose email this process finished
	restrictRefused time.Time // when Mbin last refused to make the magazine moderator only
}

func (r *Runner) bot() MatrixBot {
	if r.Bot == nil {
		return nil
	}
	return r.Bot()
}

// since is the earliest creation time a newcomer on the platform may have:
// the platform's start, or WelcomeKeep ago if that is later.
func (r *Runner) since(ctx context.Context, p store.Platform, now time.Time) (time.Time, error) {
	start, err := r.Store.WelcomeStart(ctx, p, now)
	if err != nil {
		return time.Time{}, err
	}
	if cut := now.Add(-store.WelcomeKeep); cut.After(start) {
		return cut, nil
	}
	return start, nil
}

// BatchMax is the most newcomers one announcement names. A backlog larger
// than that is announced over the following batches.
const BatchMax = 50

// MbinBatch finishes the batch of Mbin newcomers whose thread is not known to
// be posted, or else welcomes up to BatchMax newcomers not yet welcomed in a
// new thread; then it subscribes every newcomer whose subscription to the
// magazine is not settled.
func (r *Runner) MbinBatch(ctx context.Context, now time.Time) error {
	if r.API == nil {
		return nil
	}
	since, err := r.since(ctx, store.PlatformMbin, now)
	if err != nil {
		return err
	}
	batch, err := r.Store.PendingWelcomes(ctx)
	if err != nil {
		return err
	}
	retry := len(batch) > 0
	if !retry {
		if batch, err = r.newBatch(ctx, since, now); err != nil {
			return err
		}
	}
	subs, err := r.Store.PendingSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(batch) == 0 && len(subs) == 0 {
		return nil
	}
	mag, err := r.ensureMagazine(ctx, now)
	if err != nil {
		return err
	}
	var errs []error
	if len(batch) > 0 {
		if err := r.postBatch(ctx, now, mag, batch, retry); err != nil {
			errs = append(errs, err)
		} else if subs, err = r.Store.PendingSubscriptions(ctx); err != nil {
			return err
		}
	}
	return errors.Join(append(errs, r.subscribe(ctx, now, mag, subs))...)
}

// newBatch is up to BatchMax newcomers not yet welcomed, oldest first.
func (r *Runner) newBatch(ctx context.Context, since, now time.Time) ([]store.Welcomed, error) {
	done, err := r.Store.WelcomedAccounts(ctx, store.PlatformMbin)
	if err != nil {
		return nil, err
	}
	skip := make([]int64, 0, len(done))
	for a := range done {
		if id, err := strconv.ParseInt(a, 10, 64); err == nil {
			skip = append(skip, id)
		}
	}
	ns, err := r.Mbin.Newcomers(ctx, since, skip, BatchMax)
	if err != nil {
		return nil, err
	}
	batch := make([]store.Welcomed, len(ns))
	for i, n := range ns {
		batch[i] = store.Welcomed{Platform: store.PlatformMbin, Account: store.MbinAccount(n.ID), Name: n.Username,
			MbinID: n.ID, At: now}
	}
	return batch, nil
}

// postBatch posts the batch's thread. A new batch is recorded first, without
// a thread, so a post whose answer is lost, or whose record fails after it,
// is found again rather than posted twice: a batch left without a thread is
// looked for in the welcome magazine by its title before it is posted again.
func (r *Runner) postBatch(ctx context.Context, now time.Time, mag int64, batch []store.Welcomed, retry bool) error {
	slices.SortFunc(batch, func(a, b store.Welcomed) int { return cmp.Compare(a.MbinID, b.MbinID) })
	names := make([]string, len(batch))
	accounts := make([]string, len(batch))
	recorded := now
	for i, w := range batch {
		names[i], accounts[i] = w.Name, w.Account
		if w.At.Before(recorded) {
			recorded = w.At
		}
	}
	title := ThreadTitle(names)
	var e mbinapi.Entry
	found := false
	if retry {
		id, slug, ok, err := r.Mbin.WelcomeThread(ctx, title, recorded.Add(-time.Minute))
		if err != nil {
			return err
		}
		e, found = mbinapi.Entry{ID: id, Slug: slug}, ok
	} else if err := r.Store.RecordWelcomed(ctx, batch); err != nil {
		return err
	}
	if !found {
		var err error
		e, err = r.API.CreateThread(ctx, mag, title, ThreadBody(names, r.Names.Meta))
		if errors.Is(err, mbinapi.ErrForbidden) || errors.Is(err, mbinapi.ErrRefused) {
			r.alert(ctx, now, "mbin-post", "The community bot cannot post the welcome thread in /m/"+mbin.WelcomeMagazine+
				", and tries again every five minutes. Mbin said: "+err.Error())
		}
		if err != nil {
			return fmt.Errorf("post the welcome thread: %w", err)
		}
	}
	slug := cmp.Or(e.Slug, "-")
	url := r.SiteURL + "/m/" + mbin.WelcomeMagazine + "/t/" + strconv.FormatInt(e.ID, 10) + "/" + slug
	if err := r.Store.SetWelcomeEntry(ctx, accounts, e.ID, url); err != nil {
		return err
	}
	slog.Info("welcomed new members on Mbin", "count", len(batch))
	return nil
}

// subscribe subscribes each newcomer to the magazine, one at a time, so one
// that fails holds up none of the others. A refusal settles the newcomer,
// who Mbin will not subscribe; any other failure is tried again next batch,
// and once a newcomer has waited more than a day the operators are told.
func (r *Runner) subscribe(ctx context.Context, now time.Time, mag int64, subs []store.Welcomed) error {
	var first error
	for _, w := range subs {
		err := r.API.Subscribe(ctx, w.MbinID, mag)
		switch {
		case err == nil:
		case errors.Is(err, mbinapi.ErrRefused):
			slog.Warn("Mbin refused to subscribe a newcomer to the welcome magazine; not asking again", "reason", err.Error())
		default:
			if first == nil {
				first = fmt.Errorf("subscribe a newcomer: %w", err)
			}
			if now.Sub(w.At) > 24*time.Hour {
				r.alert(ctx, now, "mbin-subscribe", "The community bot has not been able to subscribe a new member to /m/"+
					mbin.WelcomeMagazine+" for more than a day. Check that its Mbin account holds the agent role and "+
					"its client the agent:magazine:subscribe scope. Mbin said: "+err.Error())
			}
			continue
		}
		if err := r.Store.WelcomeSubscribed(ctx, w.Account); err != nil {
			return err
		}
	}
	return first
}

// restrictRetry is how long after Mbin refused to make the magazine
// moderator only the bot asks again.
const restrictRetry = 24 * time.Hour

// ensureMagazine finds the welcome magazine, creating it when there is none,
// and makes its new threads moderator only. It looks every batch, so a
// magazine deleted since is made again. Mbin lets only the owner change the
// magazine: refused, the bot tells the operators and does not ask again for a
// day, and posts anyway if it may.
func (r *Runner) ensureMagazine(ctx context.Context, now time.Time) (int64, error) {
	m, found, err := r.API.MagazineByName(ctx, mbin.WelcomeMagazine)
	if err != nil {
		return 0, err
	}
	if !found {
		m, err = r.API.CreateMagazine(ctx, mbin.WelcomeMagazine, magazineTitle, magazineDescription)
		if errors.Is(err, mbinapi.ErrForbidden) || errors.Is(err, mbinapi.ErrRefused) {
			r.alert(ctx, now, "mbin-magazine", "The community bot cannot create /m/"+mbin.WelcomeMagazine+
				". Check that its Mbin account holds the agent role. Mbin said: "+err.Error())
		}
		if err != nil {
			return 0, fmt.Errorf("create the welcome magazine: %w", err)
		}
		return m.ID, nil
	}
	if !m.PostingRestricted && now.Sub(r.restrictRefused) >= restrictRetry {
		err := r.API.RestrictPosting(ctx, m.ID)
		if errors.Is(err, mbinapi.ErrForbidden) {
			r.restrictRefused = now
			r.alert(ctx, now, "mbin-magazine", "The community bot cannot manage /m/"+mbin.WelcomeMagazine+
				": only its owner may make it moderator only, and the bot is not the owner. Make the bot's Mbin "+
				"account the owner, or make the magazine moderator only by hand.")
		} else if err != nil {
			return 0, fmt.Errorf("make the welcome magazine moderator only: %w", err)
		}
	}
	return m.ID, nil
}

// MatrixBatch announces up to BatchMax Matrix newcomers not yet welcomed in
// one message in the community room. It does nothing while this copy runs no
// bot. A retried announcement names the same newcomers with the same
// transaction id, so the homeserver delivers it once.
func (r *Runner) MatrixBatch(ctx context.Context, now time.Time) error {
	b := r.bot()
	if b == nil || r.RoomAlias == "" {
		return nil
	}
	since, err := r.since(ctx, store.PlatformMatrix, now)
	if err != nil {
		return err
	}
	if r.room == "" {
		room, err := b.CommunityRoom(ctx, r.RoomName, r.RoomAlias)
		if bot.IsRefusal(err) {
			r.alert(ctx, now, "matrix-room", "The community bot cannot find, join or create the community room #"+
				r.RoomAlias+". If the room exists, invite the bot or let anyone join it.")
		}
		if err != nil {
			return fmt.Errorf("the community room: %w", err)
		}
		r.room = room
	}
	done, err := r.Store.WelcomedAccounts(ctx, store.PlatformMatrix)
	if err != nil {
		return err
	}
	fresh, err := b.NewMembers(ctx, since, func(id string) bool { return done[id] }, BatchMax)
	if err != nil {
		return err
	}
	ids := make([]string, len(fresh))
	for i, n := range fresh {
		ids[i] = n.MASUserID
	}
	if len(fresh) == 0 {
		return nil
	}
	slices.SortFunc(fresh, func(a, b bot.Newcomer) int { return a.CreatedAt.Compare(b.CreatedAt) })
	if err := b.Announce(ctx, r.room, BatchTxnID(ids), Announcement(fresh, r.Names.Meta)); err != nil {
		if bot.IsRefusal(err) {
			r.alert(ctx, now, "matrix-post", "The community bot cannot post in the community room #"+r.RoomAlias+
				". Give it permission to send messages there.")
			// The room may have changed; find it again next time.
			r.room = ""
		}
		return fmt.Errorf("announce Matrix newcomers: %w", err)
	}
	ws := make([]store.Welcomed, len(fresh))
	for i, n := range fresh {
		ws[i] = store.Welcomed{Platform: store.PlatformMatrix, Account: n.MASUserID, Name: n.Username, At: now}
	}
	if err := r.Store.RecordWelcomed(ctx, ws); err != nil {
		return err
	}
	slog.Info("welcomed new members on Matrix", "count", len(fresh))
	return nil
}
