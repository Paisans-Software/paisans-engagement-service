package bot

import (
	"context"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
)

// Newcomer is a member whose Matrix account is new.
type Newcomer struct {
	MASUserID, UserID, Username string
	CreatedAt                   time.Time
}

// newestUsers is how many of MAS's newest users NewMembers reads. Five
// minutes of signups never come near it.
const newestUsers = 200

// NewMembers is every member whose Matrix account was created at or after
// since: active, linked to a Pocket ID subject at the configured provider,
// and not the bot.
func (b *Bot) NewMembers(ctx context.Context, since time.Time) ([]Newcomer, error) {
	users, err := b.a.NewestUsers(ctx, newestUsers)
	if err != nil {
		return nil, wrap(err)
	}
	var out []Newcomer
	for _, u := range users {
		if u.CreatedAt.Before(since) || u.Username == b.cfg.Localpart || u.Locked || u.Deactivated {
			continue
		}
		if _, linked, err := b.a.LinkedSubject(ctx, b.cfg.Provider, u.ID); err != nil {
			return nil, wrap(err)
		} else if !linked {
			continue
		}
		out = append(out, Newcomer{MASUserID: u.ID, UserID: "@" + u.Username + ":" + b.cfg.ServerName,
			Username: u.Username, CreatedAt: u.CreatedAt})
	}
	return out, nil
}

// CommunityRoom finds the room under #alias:<server_name> and joins it, or
// creates it with the name when there is none. A room someone else made is
// joined as it is: the bot changes nothing in it.
func (b *Bot) CommunityRoom(ctx context.Context, name, alias string) (string, error) {
	full := "#" + alias + ":" + b.cfg.ServerName
	var (
		room  string
		found bool
	)
	if err := b.call(ctx, func() (e error) { room, found, e = b.m.ResolveAlias(ctx, full); return }); err != nil {
		return "", err
	}
	if !found {
		err := b.call(ctx, func() (e error) { room, e = b.m.CreateCommunityRoom(ctx, name, alias); return })
		if err == nil {
			return room, nil
		}
		// Another creator took the alias since it was resolved: join theirs.
		if !matrix.IsCode(err, "M_ROOM_IN_USE") {
			return "", err
		}
		room = full
	}
	if err := b.call(ctx, func() (e error) { room, e = b.m.Join(ctx, room); return }); err != nil {
		return "", err
	}
	return room, nil
}

// Announce sends content to the room with txnID.
func (b *Bot) Announce(ctx context.Context, roomID, txnID string, content map[string]any) error {
	return b.call(ctx, func() error { return b.m.Send(ctx, roomID, txnID, content) })
}

// IsRefusal reports whether err is the homeserver refusing the request, as
// opposed to being unavailable or refusing the bot's token.
func IsRefusal(err error) bool { return refused(err) }
