package bot

import (
	"context"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/mas"
	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
)

// Newcomer is a member whose Matrix account is new.
type Newcomer struct {
	MASUserID, UserID, Username string
	CreatedAt                   time.Time
}

// usersPage is how many users NewMembers asks MAS for at a time, and
// maxPages how many pages one call reads at most, so a long run of
// accounts already welcomed or not linked never makes one batch unbounded.
const (
	usersPage = 100
	maxPages  = 50
)

// NewMembers is up to limit members whose Matrix account was created at or
// after since and is not in skip, oldest first: active, linked to a Pocket
// ID subject at the configured provider, and not the bot. It pages through
// MAS in creation order from since, so members past the limit are the next
// call's rather than dropped.
func (b *Bot) NewMembers(ctx context.Context, since time.Time, skip func(masUserID string) bool, limit int) ([]Newcomer, error) {
	var out []Newcomer
	after := mas.ULIDAt(since)
	for range maxPages {
		users, err := b.a.UsersAfter(ctx, after, usersPage)
		if err != nil {
			return nil, wrap(err)
		}
		for _, u := range users {
			after = u.ID
			if skip(u.ID) || u.CreatedAt.Before(since) || u.Username == b.cfg.Localpart || u.Locked || u.Deactivated {
				continue
			}
			if _, linked, err := b.a.LinkedSubject(ctx, b.cfg.Provider, u.ID); err != nil {
				return nil, wrap(err)
			} else if !linked {
				continue
			}
			out = append(out, Newcomer{MASUserID: u.ID, UserID: "@" + u.Username + ":" + b.cfg.ServerName,
				Username: u.Username, CreatedAt: u.CreatedAt})
			if len(out) == limit {
				return out, nil
			}
		}
		if len(users) < usersPage {
			break
		}
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
