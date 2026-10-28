package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
	// Off means the member turned the messages off after Prepare; nothing
	// was sent.
	Off
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

// refused reports whether err is a 4xx refusal from the homeserver about the
// member or the room. A refusal of the bot's own token is not one.
func refused(err error) bool {
	var e *matrix.Error
	return errors.As(err, &e) && e.Status != 401 && e.Code != "M_UNKNOWN_TOKEN"
}

// lockSubject serialises the bot's work for one member, so a delivery and the
// listener's handling of the same member's command, leave or invitation do
// not interleave. It returns the unlock.
func (b *Bot) lockSubject(subject string) func() {
	v, _ := b.subjects.LoadOrStore(subject, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Deliver makes the member's room if they have none and sends n with txnID.
// It reads the member's setting and room again first, under the member's
// lock, so a stop handled since Prepare sends nothing (Off) and a room made
// since for their invitation is the one used. An error that is
// ErrUnavailable means nothing was decided: the caller releases its claim
// and tries again later.
func (b *Bot) Deliver(ctx context.Context, t Target, txnID string, n Notice) (Outcome, error) {
	defer b.lockSubject(t.Subject)()
	m, err := b.st.MatrixMember(ctx, t.Subject)
	if err != nil {
		return 0, err
	}
	if !m.Enabled {
		return Off, nil
	}
	room := m.RoomID
	if room == "" {
		var err error
		if room, err = b.newRoom(ctx, t); err != nil {
			if refused(err) {
				return Refused, nil
			}
			return 0, err
		}
	}

	err = b.call(ctx, func() error { return b.m.Send(ctx, room, txnID, n.Content()) })
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

// newRoom creates the member's room and records it before checking it, so a
// room is never left unrecorded for a later run to duplicate. A room that
// turns out encrypted, or that the homeserver refuses to describe, is
// abandoned; one whose check could not be made stays recorded and is checked
// no further, since the next delivery sends into it.
func (b *Bot) newRoom(ctx context.Context, t Target) (string, error) {
	var room string
	if err := b.call(ctx, func() (e error) { room, e = b.m.CreateDirectRoom(ctx, b.userID, t.UserID); return }); err != nil {
		return "", err
	}
	if err := b.st.SetMatrixRoom(ctx, t.Subject, room); err != nil {
		b.abandon(ctx, room)
		return "", err
	}
	var enc bool
	if err := b.call(ctx, func() (e error) { enc, e = b.m.Encrypted(ctx, room); return }); err != nil {
		if refused(err) {
			ferr := b.st.ForgetMatrixRoom(ctx, t.Subject, room)
			b.abandon(ctx, room)
			if ferr != nil {
				return "", ferr
			}
		}
		return "", err
	}
	if enc {
		// Leave the room whatever the store says, so a later send cannot reach it.
		ferr := b.st.ForgetMatrixRoom(ctx, t.Subject, room)
		b.abandon(ctx, room)
		if ferr != nil {
			return "", errors.Join(ErrEncrypted, ferr)
		}
		return "", ErrEncrypted
	}
	return room, nil
}

// leaveAttempts is how many times abandon tries to leave a room.
const leaveAttempts = 3

// abandon leaves and forgets a room. Leaving is what keeps a later send from
// reaching it, so a failed leave is tried again and, if it never succeeds,
// logged; a refusal means the bot is not in the room. Forgetting is best
// effort.
func (b *Bot) abandon(ctx context.Context, room string) {
	var err error
	for i := 0; i < leaveAttempts; i++ {
		if i > 0 {
			if b.sleep(ctx, time.Duration(i)*time.Second) != nil {
				break
			}
		}
		if err = b.call(ctx, func() error { return b.m.Leave(ctx, room) }); err == nil || refused(err) {
			err = nil
			break
		}
	}
	if err != nil {
		slog.Warn("matrix bot: could not leave an abandoned room; it stays joined", "reason", reason(err))
		return
	}
	_ = b.call(ctx, func() error { return b.m.Forget(ctx, room) })
}

// sleep waits d, or as b.wait says in tests; it returns ctx's error if ctx
// ends first.
func (b *Bot) sleep(ctx context.Context, d time.Duration) error {
	if b.wait != nil {
		return b.wait(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
