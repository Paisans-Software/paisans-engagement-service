package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
)

const (
	syncTimeout = 30 * time.Second
	minBackoff  = time.Second
	maxBackoff  = time.Minute
	// maxAttempts is how many times a batch is tried for one event that
	// fails for a reason other than an unavailable homeserver or MAS or the
	// homeserver's refusal, such as a store error, before the event is
	// skipped.
	maxAttempts = 3
)

// Command reduces a member's message to "stop", "start" or "".
func Command(body string) string {
	body = strings.ToLower(strings.TrimFunc(body, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	}))
	switch body {
	case "stop":
		return "stop"
	case "start", "resume", "restore":
		return "start"
	}
	return ""
}

// reason describes err for a log line without naming a member, a room or a
// credential.
func reason(err error) string {
	var me *matrix.Error
	switch {
	case errors.Is(err, ErrUnavailable):
		return "homeserver or MAS unavailable"
	case errors.As(err, &me):
		return me.Error()
	}
	return "internal error"
}

// Listen long-polls /sync as the bot until ctx ends, storing the sync token
// after each batch is handled. It returns only when ctx ends; errors are
// logged and retried with a wait that doubles from 1s to 1 minute.
func (b *Bot) Listen(ctx context.Context) error {
	b.attempts = map[string]int{}
	since, _, err := b.st.BotState(ctx, keySyncToken)
	for wait := minBackoff; err != nil; wait = min(wait*2, maxBackoff) {
		if err := b.pause(ctx, err, wait); err != nil {
			return err
		}
		since, _, err = b.st.BotState(ctx, keySyncToken)
	}
	initial := since == ""
	wait := minBackoff
	for ctx.Err() == nil {
		var resp *matrix.SyncResponse
		err := b.call(ctx, func() (e error) { resp, e = b.m.Sync(ctx, since, syncTimeout); return })
		if err == nil {
			err = b.handle(ctx, resp, initial)
		}
		if err == nil && resp.NextBatch != "" {
			err = b.st.SetBotState(ctx, keySyncToken, resp.NextBatch)
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if err := b.pause(ctx, err, wait); err != nil {
				return err
			}
			wait = min(wait*2, maxBackoff)
			continue
		}
		wait = minBackoff
		if resp.NextBatch != "" {
			since, initial = resp.NextBatch, false
		}
	}
	return ctx.Err()
}

// pause logs why the bot is waiting and waits d; it returns ctx's error if
// ctx ends first.
func (b *Bot) pause(ctx context.Context, cause error, d time.Duration) error {
	slog.Warn("matrix bot: retrying", "reason", reason(cause), "retry_in", d.String())
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

// replyTxn is the transaction id of a reply to event id, so that a replayed
// batch is deduplicated by the homeserver.
func replyTxn(id string) string {
	h := sha256.Sum256([]byte(id))
	return "reply-" + hex.EncodeToString(h[:])[:16]
}

func (b *Bot) reply(ctx context.Context, room, eventID, text string) error {
	err := b.call(ctx, func() error { return b.m.Send(ctx, room, replyTxn(eventID), Reply(text).Content()) })
	if refused(err) {
		slog.Warn("matrix bot: a reply was refused", "reason", reason(err))
		return nil
	}
	return err
}

// cleanup runs a leave or forget whose refusal (already gone) is not worth a retry.
func (b *Bot) cleanup(ctx context.Context, fn func() error) error {
	if err := b.call(ctx, fn); err != nil && !refused(err) {
		return err
	}
	return nil
}

func (b *Bot) leaveAndForget(ctx context.Context, room string) error {
	if err := b.cleanup(ctx, func() error { return b.m.Leave(ctx, room) }); err != nil {
		return err
	}
	return b.cleanup(ctx, func() error { return b.m.Forget(ctx, room) })
}

// guard keeps one event that cannot be handled from stopping the listener.
// An unavailable homeserver or MAS, or the end of ctx, is returned for the
// batch to be tried again, as often as it takes. Any other error but the
// homeserver's refusal, a store error say, is returned too, until the batch
// has been tried maxAttempts times for the event under key. A refusal, or an
// error that outlasts those attempts, is logged and the event skipped.
func (b *Bot) guard(ctx context.Context, key string, err error) error {
	if err == nil {
		delete(b.attempts, key)
		return nil
	}
	if ctx.Err() != nil || errors.Is(err, ErrUnavailable) {
		return err
	}
	if !refused(err) {
		b.attempts[key]++
		if b.attempts[key] < maxAttempts {
			return err
		}
	}
	delete(b.attempts, key)
	slog.Warn("matrix bot: an event was skipped", "reason", reason(err))
	return nil
}

// handle acts on one /sync batch. With initial set only invitations are
// answered: the timeline of a first sync is history.
func (b *Bot) handle(ctx context.Context, r *matrix.SyncResponse, initial bool) error {
	for room, inv := range r.Rooms.Invite {
		if err := b.guard(ctx, "invite\x00"+room, b.invite(ctx, room, inv.InviteState.Events)); err != nil {
			return err
		}
	}
	if initial {
		return nil
	}
	for room, j := range r.Rooms.Join {
		for _, ev := range j.Timeline.Events {
			if err := b.guard(ctx, "event\x00"+room+"\x00"+ev.EventID, b.event(ctx, room, ev)); err != nil {
				return err
			}
		}
	}
	for room := range r.Rooms.Leave {
		if err := b.guard(ctx, "leave\x00"+room, b.left(ctx, room)); err != nil {
			return err
		}
	}
	return nil
}

// left forgets a room the bot is no longer in.
func (b *Bot) left(ctx context.Context, room string) error {
	m, ok, err := b.st.MatrixMemberByRoom(ctx, room)
	if err != nil || !ok {
		return err
	}
	return b.st.ForgetMatrixRoom(ctx, m.Subject, room)
}

// event handles one timeline event of a joined room.
func (b *Bot) event(ctx context.Context, room string, ev matrix.Event) error {
	if ev.Sender == b.userID {
		return nil
	}
	switch ev.Type {
	case "m.room.message":
		m, ok, err := b.st.MatrixMemberByRoom(ctx, room)
		if err != nil || !ok || m.UserID == "" || ev.Sender != m.UserID {
			return err
		}
		var raw struct {
			MsgType string `json:"msgtype"`
			Body    string `json:"body"`
		}
		if json.Unmarshal(ev.Content, &raw) != nil || raw.MsgType == "m.notice" {
			return nil
		}
		switch Command(raw.Body) {
		case "stop":
			if err := b.st.SetMatrixEnabled(ctx, m.Subject, false); err != nil {
				return err
			}
			return b.reply(ctx, room, ev.EventID, ReplyStopped)
		case "start":
			if err := b.st.SetMatrixEnabled(ctx, m.Subject, true); err != nil {
				return err
			}
			return b.reply(ctx, room, ev.EventID, ReplyStarted)
		}
		return b.reply(ctx, room, ev.EventID, ReplyHelp)

	case "m.room.member":
		m, ok, err := b.st.MatrixMemberByRoom(ctx, room)
		if err != nil || !ok || m.UserID == "" || ev.StateKey == nil || *ev.StateKey != m.UserID {
			return err
		}
		var c struct {
			Membership string `json:"membership"`
		}
		if json.Unmarshal(ev.Content, &c) != nil || (c.Membership != "leave" && c.Membership != "ban") {
			return nil
		}
		if err := b.st.LeftMatrix(ctx, m.Subject, room); err != nil {
			return err
		}
		return b.leaveAndForget(ctx, room)
	}
	return nil
}

// inviter is the sender of the invitation for the bot in an invite's stripped
// state, and the id of that event when the homeserver sent one.
func (b *Bot) inviter(events []matrix.Event) (sender, eventID string) {
	for _, ev := range events {
		if ev.Type != "m.room.member" || ev.StateKey == nil || *ev.StateKey != b.userID {
			continue
		}
		var c struct {
			Membership string `json:"membership"`
		}
		if json.Unmarshal(ev.Content, &c) == nil && c.Membership == "invite" {
			return ev.Sender, ev.EventID
		}
	}
	return "", ""
}

// invite answers an invitation: a member's gets a fresh room, any other is
// rejected. The eligibility lookups come first, so a failure of MAS or Mbin is
// retried with the invitation still pending. The invitation is rejected
// before the room is made; after that a failure can only lose the greeting,
// and a replayed batch cannot invite the member a second time.
func (b *Bot) invite(ctx context.Context, room string, state []matrix.Event) error {
	sender, eventID := b.inviter(state)
	if eventID == "" {
		eventID = room // stripped state carries no event id
	}
	subject, masUser, ok, err := b.inviterMember(ctx, sender)
	if err != nil && (ctx.Err() != nil || errors.Is(err, ErrUnavailable)) {
		return err
	}
	if err != nil {
		slog.Warn("matrix bot: an invitation was rejected", "reason", reason(err))
		ok = false
	}
	if ok {
		if err := b.prepareAnswer(ctx, subject, masUser, sender); err != nil {
			return err
		}
	}
	if err := b.cleanup(ctx, func() error { return b.m.Leave(ctx, room) }); err != nil || !ok {
		return err
	}
	fresh, err := b.newRoom(ctx, Target{Subject: subject, UserID: sender})
	if err == nil {
		err = b.reply(ctx, fresh, eventID, ReplyStarted)
	}
	if err != nil {
		slog.Warn("matrix bot: a member's room could not be greeted", "reason", reason(err))
	}
	return nil
}

// inviterMember finds the current member behind a local Matrix id.
func (b *Bot) inviterMember(ctx context.Context, sender string) (subject, masUser string, ok bool, err error) {
	local, cut := strings.CutSuffix(strings.TrimPrefix(sender, "@"), ":"+b.cfg.ServerName)
	if !cut || !strings.HasPrefix(sender, "@") || local == "" || strings.Contains(local, ":") || b.members == nil {
		return "", "", false, nil
	}
	u, found, err := b.a.UserByUsername(ctx, local)
	if err != nil || !found || u.Deactivated || u.Locked {
		return "", "", false, wrap(err)
	}
	subject, found, err = b.a.LinkedSubject(ctx, b.cfg.Provider, u.ID)
	if err != nil || !found {
		return "", "", false, wrap(err)
	}
	member, err := b.members.IsMember(ctx, subject)
	if err != nil || !member {
		return "", "", false, err
	}
	return subject, u.ID, true, nil
}

// prepareAnswer drops the member's old room and records them as on.
func (b *Bot) prepareAnswer(ctx context.Context, subject, masUser, userID string) error {
	old, err := b.st.MatrixMember(ctx, subject)
	if err != nil {
		return err
	}
	if old.RoomID != "" {
		if err := b.leaveAndForget(ctx, old.RoomID); err != nil {
			return err
		}
		if err := b.st.ForgetMatrixRoom(ctx, subject, old.RoomID); err != nil {
			return err
		}
	}
	if err := b.st.SetMatrixAccount(ctx, subject, masUser, userID); err != nil {
		return err
	}
	return b.st.SetMatrixEnabled(ctx, subject, true)
}
