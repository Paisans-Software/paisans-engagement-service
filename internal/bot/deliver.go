package bot

import (
	"context"
	"errors"
	"fmt"

	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
)

// Target is a member the bot can message.
type Target struct{ Subject, UserID, RoomID string }

// Outcome is what became of a delivery that did not fail.
type Outcome int

const (
	// Sent means the homeserver accepted the message.
	Sent Outcome = iota + 1
	// Refused means the member cannot be messaged now; the state is updated.
	Refused
)

// ErrEncrypted means a new room came out encrypted and was abandoned. It is
// also ErrUnavailable, since every new room would be the same.
var ErrEncrypted = fmt.Errorf("%w: a new room came out encrypted", ErrUnavailable)

// Prepare says whether the member gets a DM: the setting is on and a live
// Matrix account is linked to their subject at the configured provider.
func (b *Bot) Prepare(ctx context.Context, subject string) (Target, bool, error) {
	m, err := b.st.MatrixMember(ctx, subject)
	if err != nil || !m.Enabled {
		return Target{}, false, err
	}
	if m.MASUserID != "" {
		u, found, err := b.a.User(ctx, m.MASUserID)
		if err != nil {
			return Target{}, false, wrap(err)
		}
		if found {
			if u.Deactivated || u.Locked {
				return Target{}, false, nil
			}
			return Target{Subject: subject, UserID: m.UserID, RoomID: m.RoomID}, true, nil
		}
		if err := b.st.SetMatrixAccount(ctx, subject, "", ""); err != nil {
			return Target{}, false, err
		}
		if m.RoomID != "" {
			if err := b.st.ForgetMatrixRoom(ctx, subject, m.RoomID); err != nil {
				return Target{}, false, err
			}
			m.RoomID = ""
		}
	}

	id, ok, err := b.a.LinkedUserID(ctx, b.cfg.Provider, subject)
	if err != nil || !ok {
		return Target{}, false, wrap(err)
	}
	u, found, err := b.a.User(ctx, id)
	if err != nil || !found || u.Deactivated || u.Locked {
		return Target{}, false, wrap(err)
	}
	userID := "@" + u.Username + ":" + b.cfg.ServerName
	if err := b.st.SetMatrixAccount(ctx, subject, id, userID); err != nil {
		return Target{}, false, err
	}
	return Target{Subject: subject, UserID: userID, RoomID: m.RoomID}, true, nil
}

// refused reports whether err is a 4xx refusal from the homeserver.
func refused(err error) bool {
	var e *matrix.Error
	return errors.As(err, &e)
}

// Deliver makes the member's room if they have none and sends n with txnID.
// An error that is ErrUnavailable means nothing was decided: the caller
// releases its claim and tries again later.
func (b *Bot) Deliver(ctx context.Context, t Target, txnID string, n Notice) (Outcome, error) {
	room := t.RoomID
	if room == "" {
		var err error
		if room, err = b.newRoom(ctx, t); err != nil {
			if refused(err) {
				return Refused, nil
			}
			return 0, err
		}
	}

	err := b.call(ctx, func() error { return b.m.Send(ctx, room, txnID, n.Content()) })
	if err == nil {
		return Sent, nil
	}
	if !refused(err) {
		return 0, err
	}

	var state string
	merr := b.call(ctx, func() (e error) { state, e = b.m.Membership(ctx, room, t.UserID); return })
	if merr != nil && !refused(merr) {
		return 0, merr
	}
	if merr == nil && (state == "leave" || state == "ban") {
		if err := b.st.LeftMatrix(ctx, t.Subject, room); err != nil {
			return 0, err
		}
	} else if err := b.st.ForgetMatrixRoom(ctx, t.Subject, room); err != nil {
		return 0, err
	}
	return Refused, nil
}

// newRoom creates and records the member's room, or abandons it if it is encrypted.
func (b *Bot) newRoom(ctx context.Context, t Target) (string, error) {
	var room string
	if err := b.call(ctx, func() (e error) { room, e = b.m.CreateDirectRoom(ctx, b.userID, t.UserID); return }); err != nil {
		return "", err
	}
	var enc bool
	if err := b.call(ctx, func() (e error) { enc, e = b.m.Encrypted(ctx, room); return }); err != nil {
		return "", err
	}
	if enc {
		_ = b.call(ctx, func() error { return b.m.Leave(ctx, room) })
		_ = b.call(ctx, func() error { return b.m.Forget(ctx, room) })
		return "", ErrEncrypted
	}
	if err := b.st.SetMatrixRoom(ctx, t.Subject, room); err != nil {
		return "", err
	}
	return room, nil
}
