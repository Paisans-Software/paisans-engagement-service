// Package bot runs the Digest Bot: its Matrix account, its token and, in
// later files, the rooms and messages it keeps.
package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/mas"
	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
)

const (
	// DisplayName is the bot's profile name.
	DisplayName = "Digest Bot"
	// Device is the bot's fixed device id. Synapse keys its persisted send
	// deduplication by device, so a retried send survives a new token.
	Device = "DIGESTBOT"
	// Scope is the bot token's scope. The device scope makes MAS provision
	// the device in Synapse; without one, PUT /send returns 500.
	Scope = "urn:matrix:client:api:* urn:matrix:client:device:" + Device
)

// Bot state keys.
const (
	keyMASUser      = "mas_user_id"
	keyLocalpart    = "localpart"
	keySession      = "session_id"
	keyAccessToken  = "access_token"
	keyCreateIntent = "create_intent" // "<localpart> <RFC3339 time>"
	keySyncToken    = "sync_token"
)

// intentSlack is how far before the recorded intent a user's creation time
// may fall and still count as the answer to that intent.
const intentSlack = 5 * time.Minute

// Config is what Start needs to know about this deployment.
type Config struct{ ServerName, Provider, Localpart string }

// MemberChecker reports whether an OIDC subject is a current member.
type MemberChecker interface {
	IsMember(ctx context.Context, subject string) (bool, error)
}

var (
	// ErrOff wraps a reason the bot cannot run until the configuration or MAS changes.
	ErrOff = errors.New("matrix is off")
	// ErrUnavailable wraps matrix.ErrUnavailable and mas.ErrUnavailable for callers.
	ErrUnavailable = errors.New("matrix unavailable")
)

// Bot is a started Digest Bot.
type Bot struct {
	m       *matrix.Client
	a       *mas.Client
	st      *store.Store
	members MemberChecker
	cfg     Config
	userID  string

	mu sync.Mutex // serialises token renewal
}

// UserID is "@<localpart>:<server_name>".
func (b *Bot) UserID() string { return b.userID }

// wrap marks an unavailable homeserver or MAS as ErrUnavailable and keeps the
// inner chain; any other error is returned as is.
func wrap(err error) error {
	if errors.Is(err, matrix.ErrUnavailable) || errors.Is(err, mas.ErrUnavailable) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}

func off(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrOff, fmt.Sprintf(format, args...))
}

// Start checks MAS, finds or creates the bot's account, makes sure it holds a
// working token and sets its display name. An ErrOff result lasts until the
// configuration or MAS changes; anything else is worth retrying.
func Start(ctx context.Context, m *matrix.Client, a *mas.Client, st *store.Store, members MemberChecker, cfg Config) (*Bot, error) {
	b := &Bot{m: m, a: a, st: st, members: members, cfg: cfg,
		userID: "@" + cfg.Localpart + ":" + cfg.ServerName}

	vs, err := a.Version(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	if v, ok := mas.ParseVersion(vs); !ok || v.Less(mas.MinVersion) {
		return nil, off("MAS %s is older than 1.5.0", vs)
	}
	endpoint, err := m.AuthMetadata(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	a.SetTokenEndpoint(endpoint)

	if err := b.account(ctx); err != nil {
		return nil, wrap(err)
	}
	if err := b.token(ctx); err != nil {
		return nil, wrap(err)
	}
	name, err := m.DisplayName(ctx, b.userID)
	if err != nil {
		return nil, wrap(err)
	}
	if name != DisplayName {
		if err := m.SetDisplayName(ctx, b.userID, DisplayName); err != nil {
			return nil, wrap(err)
		}
	}
	return b, nil
}

func (b *Bot) takenErr() error {
	return off("the MAS username %q is not the Digest Bot's; set ENGAGEMENT_MATRIX_BOT_LOCALPART "+
		"(the engagement app's bot_localpart setting) to a name no one uses", b.cfg.Localpart)
}

// account finds or creates the MAS user. It never adopts a user it did not create.
func (b *Bot) account(ctx context.Context) error {
	if id, ok, err := b.st.BotState(ctx, keyMASUser); err != nil {
		return err
	} else if ok {
		if lp, _, err := b.st.BotState(ctx, keyLocalpart); err != nil {
			return err
		} else if lp == b.cfg.Localpart {
			u, found, err := b.a.User(ctx, id)
			if err != nil {
				return err
			}
			if found && !u.Deactivated {
				return nil
			}
		}
	}

	u, found, err := b.a.UserByUsername(ctx, b.cfg.Localpart)
	if err != nil {
		return err
	}
	if !found {
		intent := b.cfg.Localpart + " " + time.Now().UTC().Format(time.RFC3339)
		if err := b.st.SetBotState(ctx, keyCreateIntent, intent); err != nil {
			return err
		}
		u, err = b.a.CreateUser(ctx, b.cfg.Localpart)
		if errors.Is(err, mas.ErrTaken) {
			return b.takenErr()
		}
		if err != nil {
			return err
		}
	} else {
		ours, err := b.lostCreate(ctx, u)
		if err != nil {
			return err
		}
		if !ours {
			return b.takenErr()
		}
	}
	return b.adopt(ctx, u.ID)
}

// lostCreate reports whether u is the answer to a create this service made
// and never saw the reply to.
func (b *Bot) lostCreate(ctx context.Context, u mas.User) (bool, error) {
	intent, ok, err := b.st.BotState(ctx, keyCreateIntent)
	if err != nil || !ok {
		return false, err
	}
	lp, ts, _ := strings.Cut(intent, " ")
	at, perr := time.Parse(time.RFC3339, ts)
	if lp != b.cfg.Localpart || perr != nil || u.CreatedAt.Before(at.Add(-intentSlack)) {
		return false, nil
	}
	linked, err := b.a.HasLinks(ctx, u.ID)
	return !linked, err
}

// adopt records the account as ours and forgets the state of any earlier one.
func (b *Bot) adopt(ctx context.Context, masUserID string) error {
	for k, v := range map[string]string{keyMASUser: masUserID, keyLocalpart: b.cfg.Localpart} {
		if err := b.st.SetBotState(ctx, k, v); err != nil {
			return err
		}
	}
	for _, k := range []string{keyCreateIntent, keySession, keyAccessToken} {
		if err := b.st.DeleteBotState(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// token makes the Matrix client hold a token that belongs to the bot.
func (b *Bot) token(ctx context.Context) error {
	if tok, ok, err := b.st.BotState(ctx, keyAccessToken); err != nil {
		return err
	} else if ok {
		b.m.SetToken(tok)
		user, device, err := b.m.WhoAmI(ctx)
		if err == nil && user == b.userID && device == Device {
			return nil
		}
		if err != nil && !matrix.IsCode(err, "M_UNKNOWN_TOKEN") {
			return err
		}
	}
	if err := b.renew(ctx); err != nil {
		return err
	}
	user, device, err := b.m.WhoAmI(ctx)
	if err != nil {
		return err
	}
	if user != b.userID || device != Device {
		return fmt.Errorf("the new bot token belongs to %s device %s, not %s device %s", user, device, b.userID, Device)
	}
	return nil
}

// renew obtains a new token (regenerate, else create) and stores it.
func (b *Bot) renew(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var tok string
	sid, ok, err := b.st.BotState(ctx, keySession)
	if err != nil {
		return err
	}
	if ok {
		tok, err = b.a.RegenerateSession(ctx, sid)
		if errors.Is(err, mas.ErrSessionGone) {
			ok = false
		} else if err != nil {
			return err
		}
	}
	if !ok {
		masUser, _, err := b.st.BotState(ctx, keyMASUser)
		if err != nil {
			return err
		}
		s, err := b.a.CreateSession(ctx, masUser, DisplayName, Scope)
		if err != nil {
			return err
		}
		sid, tok = s.ID, s.Token
		if err := b.st.SetBotState(ctx, keySession, sid); err != nil {
			return err
		}
	}
	if err := b.st.SetBotState(ctx, keyAccessToken, tok); err != nil {
		return err
	}
	b.m.SetToken(tok)
	return nil
}

// call runs fn and, when the homeserver refuses the token, renews it and runs
// fn once more.
func (b *Bot) call(ctx context.Context, fn func() error) error {
	err := fn()
	if !matrix.IsCode(err, "M_UNKNOWN_TOKEN") {
		return wrap(err)
	}
	if err := b.renew(ctx); err != nil {
		return wrap(err)
	}
	err = fn()
	if matrix.IsCode(err, "M_UNKNOWN_TOKEN") {
		// A fresh token is refused too: the trouble is the bot's, not the member's.
		return fmt.Errorf("%w: the homeserver refuses the bot's token", ErrUnavailable)
	}
	return wrap(err)
}
