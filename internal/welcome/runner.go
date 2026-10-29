package welcome

import (
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
	Newcomers(ctx context.Context, since time.Time) ([]mbin.Newcomer, error)
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
	NewMembers(ctx context.Context, since time.Time) ([]bot.Newcomer, error)
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

	magazine int64  // the welcome magazine's id, once known
	room     string // the community room's id, once found
	emailed  string // the day whose email this process finished
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

// MbinBatch welcomes the Mbin newcomers not yet welcomed in one thread, and
// subscribes every newcomer welcomed and not yet subscribed to the magazine.
func (r *Runner) MbinBatch(ctx context.Context, now time.Time) error {
	if r.API == nil {
		return nil
	}
	since, err := r.since(ctx, store.PlatformMbin, now)
	if err != nil {
		return err
	}
	all, err := r.Mbin.Newcomers(ctx, since)
	if err != nil {
		return err
	}
	done, err := r.Store.WelcomedAccounts(ctx, store.PlatformMbin)
	if err != nil {
		return err
	}
	var fresh []mbin.Newcomer
	for _, n := range all {
		if !done[store.MbinAccount(n.ID)] {
			fresh = append(fresh, n)
		}
	}
	// Oldest first, so the thread names them in the order they joined.
	slices.SortFunc(fresh, func(a, b mbin.Newcomer) int { return a.CreatedAt.Compare(b.CreatedAt) })
	pending, err := r.Store.PendingSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(fresh) == 0 && len(pending) == 0 {
		return nil
	}
	mag, err := r.ensureMagazine(ctx, now)
	if err != nil {
		return err
	}
	if len(fresh) > 0 {
		names := make([]string, len(fresh))
		for i, n := range fresh {
			names[i] = n.Username
		}
		e, err := r.API.CreateThread(ctx, mag, ThreadTitle(names), ThreadBody(names, r.Names.Meta))
		if errors.Is(err, mbinapi.ErrForbidden) || errors.Is(err, mbinapi.ErrRefused) {
			r.alert(ctx, now, "mbin-post", "The community bot cannot post the welcome thread in /m/"+mbin.WelcomeMagazine+
				". Check that its Mbin account owns or moderates the magazine and holds the agent role.")
		}
		if err != nil {
			return fmt.Errorf("post the welcome thread: %w", err)
		}
		slug := e.Slug
		if slug == "" {
			slug = "-"
		}
		url := r.SiteURL + "/m/" + mbin.WelcomeMagazine + "/t/" + strconv.FormatInt(e.ID, 10) + "/" + slug
		ws := make([]store.Welcomed, len(fresh))
		for i, n := range fresh {
			ws[i] = store.Welcomed{Platform: store.PlatformMbin, Account: store.MbinAccount(n.ID), Name: n.Username,
				MbinID: n.ID, EntryID: e.ID, URL: url, At: now}
		}
		if err := r.Store.RecordWelcomed(ctx, ws); err != nil {
			return err
		}
		slog.Info("welcomed new members on Mbin", "count", len(fresh))
		if pending, err = r.Store.PendingSubscriptions(ctx); err != nil {
			return err
		}
	}
	for _, w := range pending {
		err := r.API.Subscribe(ctx, w.MbinID, mag)
		switch {
		case err == nil:
		case errors.Is(err, mbinapi.ErrRefused):
			// Mbin will not subscribe this member, such as one banned since
			// they joined; asking again changes nothing.
			slog.Warn("Mbin refused to subscribe a newcomer to the welcome magazine")
		case errors.Is(err, mbinapi.ErrForbidden):
			r.alert(ctx, now, "mbin-subscribe", "The community bot cannot subscribe new members to /m/"+mbin.WelcomeMagazine+
				". Check that its Mbin account holds the agent role and its client the agent:magazine:subscribe scope.")
			return fmt.Errorf("subscribe a newcomer: %w", err)
		default:
			return fmt.Errorf("subscribe a newcomer: %w", err)
		}
		if err := r.Store.WelcomeSubscribed(ctx, w.Account); err != nil {
			return err
		}
	}
	return nil
}

// ensureMagazine finds the welcome magazine, creating it when there is none,
// and restricts its new threads to moderators. Once per process.
func (r *Runner) ensureMagazine(ctx context.Context, now time.Time) (int64, error) {
	if r.magazine != 0 {
		return r.magazine, nil
	}
	m, found, err := r.API.MagazineByName(ctx, mbin.WelcomeMagazine)
	if err != nil {
		return 0, err
	}
	if !found {
		m, err = r.API.CreateMagazine(ctx, mbin.WelcomeMagazine, magazineTitle, magazineDescription)
	} else if !m.PostingRestricted {
		err = r.API.RestrictPosting(ctx, m.ID)
	}
	if errors.Is(err, mbinapi.ErrForbidden) || errors.Is(err, mbinapi.ErrRefused) {
		r.alert(ctx, now, "mbin-magazine", "The community bot cannot create /m/"+mbin.WelcomeMagazine+
			" or make it moderator-only. If the magazine exists, make the bot's Mbin account a moderator; "+
			"otherwise check that it holds the agent role.")
	}
	if err != nil {
		return 0, fmt.Errorf("the welcome magazine: %w", err)
	}
	r.magazine = m.ID
	return m.ID, nil
}

// MatrixBatch announces the Matrix newcomers not yet welcomed in one message
// in the community room. It does nothing while this copy runs no bot.
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
	all, err := b.NewMembers(ctx, since)
	if err != nil {
		return err
	}
	done, err := r.Store.WelcomedAccounts(ctx, store.PlatformMatrix)
	if err != nil {
		return err
	}
	var fresh []bot.Newcomer
	var ids []string
	for _, n := range all {
		if !done[n.MASUserID] {
			fresh = append(fresh, n)
			ids = append(ids, n.MASUserID)
		}
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
