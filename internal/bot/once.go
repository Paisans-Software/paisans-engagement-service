package bot

import (
	"context"
	"errors"
	"fmt"

	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
)

// Once gives a process that does not supervise the bot, such as the send
// command, the Matrix bot for one send. done must be called once the send is
// over.
//
// When no copy of the service holds the bot lock, Once takes it, starts the
// bot as Supervise would, and done releases the lock. The bot does not
// listen, so a member's command waits in the room for the service's
// listener.
//
// When the service holds the lock, Once acts as the bot it runs: it reads the
// account and the access token the service stored and checks the token with
// the homeserver. It never creates an account, a session or a token, and
// never renews one, since a new token would cut the service off; a refused
// token is replaced only by a newer one the service has stored. The lock is
// not taken, so the service's listener keeps running. done does nothing.
func Once(ctx context.Context, d Deps) (b *Bot, done func(), err error) {
	lock, ok, err := d.Store.TryBotLock(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("try the bot lock: %w", err)
	}
	if ok {
		b, err := Start(ctx, d.Matrix, d.MAS, d.Store, d.Members, d.Config)
		if err != nil {
			lock.Release()
			return nil, nil, err
		}
		return b, lock.Release, nil
	}
	b, err = attach(ctx, d)
	if err != nil {
		return nil, nil, err
	}
	return b, func() {}, nil
}

// errNotStarted is the refusal while the running service's bot has no
// account or token under the configured localpart.
func errNotStarted(localpart string) error {
	return fmt.Errorf("the service holds the Matrix bot but has not started it as %q; see the service's log", localpart)
}

// attach is the bot the running service holds, acting with its stored token.
func attach(ctx context.Context, d Deps) (*Bot, error) {
	b := &Bot{m: d.Matrix, a: d.MAS, st: d.Store, members: d.Members, cfg: d.Config, shared: true,
		userID: "@" + d.Config.Localpart + ":" + d.Config.ServerName}
	endpoint, err := b.m.AuthMetadata(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	b.a.SetTokenEndpoint(endpoint)
	state := map[string]string{}
	for _, k := range []string{keyMASUser, keyLocalpart, keyAccessToken} {
		v, ok, err := b.st.BotState(ctx, k)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errNotStarted(d.Config.Localpart)
		}
		state[k] = v
	}
	if state[keyLocalpart] != d.Config.Localpart {
		return nil, errNotStarted(d.Config.Localpart)
	}
	tok := state[keyAccessToken]
	b.setToken(tok)
	user, device, err := b.m.WhoAmI(ctx)
	if err != nil {
		if matrix.IsCode(err, "M_UNKNOWN_TOKEN") || unauthorized(err) {
			return nil, fmt.Errorf("%w: the homeserver refuses the running Matrix bot's token; try again once the service has renewed it", ErrUnavailable)
		}
		return nil, wrap(err)
	}
	if user != b.userID || device != Device {
		return nil, fmt.Errorf("the stored bot token belongs to %s device %s, not %s device %s", user, device, b.userID, Device)
	}
	return b, nil
}

// adoptStored takes the token the service stored, when it is newer than
// stale. It is how a shared bot renews.
func (b *Bot) adoptStored(ctx context.Context, stale string) error {
	tok, ok, err := b.st.BotState(ctx, keyAccessToken)
	if err != nil {
		return err
	}
	if !ok || tok == stale {
		return errors.New("the token is the running service's to renew, and it has stored no newer one")
	}
	b.tok = tok
	b.m.SetToken(tok)
	return nil
}
