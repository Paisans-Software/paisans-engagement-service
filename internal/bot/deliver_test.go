package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/digest"
)

func started(t *testing.T) (*env, *Bot) {
	t.Helper()
	e := newEnv(t)
	b, err := e.start(t)
	if err != nil {
		t.Fatal(err)
	}
	return e, b
}

func link(e *env, subject, username string) string {
	id := e.mas.AddUser(username, e.mas.Users()[0].CreatedAt)
	e.mas.Link(provider, subject, id)
	return id
}

func note() Notice { return Weekly(digest.Digest{}, "Talk", "L") }

func TestPrepareLooksUpAndCaches(t *testing.T) {
	e, b := started(t)
	link(e, "sub-1", "one")
	ctx := context.Background()
	tg, ok, err := b.Prepare(ctx, "sub-1")
	if err != nil || !ok || tg.UserID != "@one:example.org" || tg.Subject != "sub-1" || tg.RoomID != "" {
		t.Fatalf("target = %+v ok=%v err=%v", tg, ok, err)
	}
	n := e.mas.LinkLookups()
	if _, ok, err := b.Prepare(ctx, "sub-1"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if e.mas.LinkLookups() != n {
		t.Fatal("second Prepare looked the link up again")
	}
}

func TestPrepareUnlinked(t *testing.T) {
	_, b := started(t)
	if _, ok, err := b.Prepare(context.Background(), "nobody"); ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestPrepareDeactivated(t *testing.T) {
	e, b := started(t)
	e.mas.Deactivate(link(e, "sub-1", "one"))
	if _, ok, err := b.Prepare(context.Background(), "sub-1"); ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestPrepareOff(t *testing.T) {
	e, b := started(t)
	link(e, "sub-1", "one")
	if err := e.st.SetMatrixEnabled(context.Background(), "sub-1", false); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := b.Prepare(context.Background(), "sub-1"); ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestPrepareCachedUserGone(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	if err := e.st.SetMatrixAccount(ctx, "sub-1", "01GONE", "@old:example.org"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := b.Prepare(ctx, "sub-1"); ok || err != nil {
		t.Fatal(ok, err)
	}
	m, _ := e.st.MatrixMember(ctx, "sub-1")
	if m.MASUserID != "" || m.UserID != "" {
		t.Fatalf("member = %+v", m)
	}
	link(e, "sub-1", "new")
	tg, ok, err := b.Prepare(ctx, "sub-1")
	if err != nil || !ok || tg.UserID != "@new:example.org" {
		t.Fatal(tg, ok, err)
	}
}

// Two members whose accounts vanished are both cleared: an unknown account
// is stored as none, not as an empty id the second would collide with.
func TestPrepareTwoCachedUsersGone(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	for _, sub := range []string{"sub-1", "sub-2"} {
		if err := e.st.SetMatrixAccount(ctx, sub, "01GONE"+sub, "@old-"+sub+":example.org"); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := b.Prepare(ctx, sub); ok || err != nil {
			t.Fatalf("%s: %v %v", sub, ok, err)
		}
	}
}

func prepared(t *testing.T, e *env, b *Bot) Target {
	t.Helper()
	link(e, "sub-1", "one")
	tg, ok, err := b.Prepare(context.Background(), "sub-1")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	return tg
}

func TestDeliverCreatesRoomOnce(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	tg := prepared(t, e, b)
	if out, err := b.Deliver(ctx, tg, "t1", note()); err != nil || out != Sent {
		t.Fatal(out, err)
	}
	tg, _, _ = b.Prepare(ctx, "sub-1")
	if out, err := b.Deliver(ctx, tg, "t2", note()); err != nil || out != Sent {
		t.Fatal(out, err)
	}
	if len(e.syn.Rooms()) != 1 || len(e.syn.Sends()) != 2 {
		t.Fatalf("rooms=%d sends=%d", len(e.syn.Rooms()), len(e.syn.Sends()))
	}
	m, _ := e.st.MatrixMember(ctx, "sub-1")
	if m.RoomID != e.syn.Rooms()[0].ID {
		t.Fatalf("room = %q", m.RoomID)
	}
}

func TestDeliverAbandonsEncryptedRoom(t *testing.T) {
	e, b := started(t)
	e.syn.EncryptNewRooms(true)
	tg := prepared(t, e, b)
	_, err := b.Deliver(context.Background(), tg, "t1", note())
	if !errors.Is(err, ErrEncrypted) || !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	rs := e.syn.Rooms()
	if len(rs) != 1 || !rs[0].Left || !rs[0].Forgotten || len(e.syn.Sends()) != 0 {
		t.Fatalf("rooms=%+v", rs)
	}
	if m, _ := e.st.MatrixMember(context.Background(), "sub-1"); m.RoomID != "" {
		t.Fatalf("room stored: %q", m.RoomID)
	}
}

func TestDeliverVanishedRoom(t *testing.T) {
	for _, tc := range []struct {
		membership string
		enabled    bool
	}{{"join", true}, {"leave", false}, {"ban", false}, {"", true}} {
		t.Run(tc.membership, func(t *testing.T) {
			e, b := started(t)
			ctx := context.Background()
			tg := prepared(t, e, b)
			if _, err := b.Deliver(ctx, tg, "t1", note()); err != nil {
				t.Fatal(err)
			}
			room := e.syn.Rooms()[0].ID
			e.syn.RefuseSend(room, 403, "M_FORBIDDEN")
			if tc.membership != "" {
				e.syn.SetMembership(room, tg.UserID, tc.membership)
			}
			tg.RoomID = room
			out, err := b.Deliver(ctx, tg, "t2", note())
			if err != nil || out != Refused {
				t.Fatal(out, err)
			}
			m, _ := e.st.MatrixMember(ctx, "sub-1")
			if m.RoomID != "" || m.Enabled != tc.enabled {
				t.Fatalf("member = %+v", m)
			}
		})
	}
}

func TestDeliverRetryReusesTxnID(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	tg := prepared(t, e, b)
	for i := 0; i < 2; i++ {
		tg, _, _ = b.Prepare(ctx, "sub-1")
		if out, err := b.Deliver(ctx, tg, "same", note()); err != nil || out != Sent {
			t.Fatal(out, err)
		}
	}
	if e.syn.SendAttempts() != 2 || len(e.syn.Sends()) != 1 {
		t.Fatalf("attempts=%d sends=%d", e.syn.SendAttempts(), len(e.syn.Sends()))
	}
	p := e.syn.SendPaths()
	if len(p) != 2 || p[0] != p[1] {
		t.Fatalf("paths = %v", p)
	}
}

func TestDeliverUnavailable(t *testing.T) {
	e, b := started(t)
	tg := prepared(t, e, b)
	e.syn.Down.Store(true)
	_, err := b.Deliver(context.Background(), tg, "t1", note())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if m, _ := e.st.MatrixMember(context.Background(), "sub-1"); m.RoomID != "" {
		t.Fatalf("room stored: %q", m.RoomID)
	}
}

func TestNewRoomStateReadUnavailableKeepsRoom(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	tg := prepared(t, e, b)
	e.syn.FailState(503, "")
	if _, err := b.Deliver(ctx, tg, "t1", note()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	m, _ := e.st.MatrixMember(ctx, "sub-1")
	if m.RoomID == "" || len(e.syn.Rooms()) != 1 || e.syn.Rooms()[0].Left {
		t.Fatalf("member=%+v rooms=%+v", m, e.syn.Rooms())
	}
	e.syn.ClearFailState()
	tg, _, _ = b.Prepare(ctx, "sub-1")
	if out, err := b.Deliver(ctx, tg, "t1", note()); err != nil || out != Sent {
		t.Fatal(out, err)
	}
	if len(e.syn.Rooms()) != 1 || len(e.syn.Sends()) != 1 || e.syn.Sends()[0].Room != m.RoomID {
		t.Fatalf("rooms=%d sends=%+v", len(e.syn.Rooms()), e.syn.Sends())
	}
}

func TestNewRoomStateReadRefusedAbandonsRoom(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	tg := prepared(t, e, b)
	e.syn.FailState(403, "M_FORBIDDEN")
	out, err := b.Deliver(ctx, tg, "t1", note())
	if err != nil || out != Refused {
		t.Fatal(out, err)
	}
	m, _ := e.st.MatrixMember(ctx, "sub-1")
	rs := e.syn.Rooms()
	if m.RoomID != "" || len(rs) != 1 || !rs[0].Left || !rs[0].Forgotten {
		t.Fatalf("member=%+v rooms=%+v", m, rs)
	}
}

func TestRejectedBotTokenIsNotARefusal(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	tg := prepared(t, e, b)
	if _, err := b.Deliver(ctx, tg, "t1", note()); err != nil {
		t.Fatal(err)
	}
	tg, _, _ = b.Prepare(ctx, "sub-1")
	e.syn.Accept(func(string) (string, string, bool) { return "", "", false })
	out, err := b.Deliver(ctx, tg, "t2", note())
	if !errors.Is(err, ErrUnavailable) || out != 0 {
		t.Fatal(out, err)
	}
	m, _ := e.st.MatrixMember(ctx, "sub-1")
	if m.RoomID == "" || !m.Enabled {
		t.Fatalf("member = %+v", m)
	}
}

func TestRepeatedUnauthorizedIsUnavailable(t *testing.T) {
	for name, setup := range map[string]func(*env){
		"missing token code": func(e *env) { e.syn.UnauthCode.Store("M_MISSING_TOKEN") },
		"renewal refused":    func(e *env) { e.mas.RefuseSessions.Store(403) },
	} {
		t.Run(name, func(t *testing.T) {
			e, b := started(t)
			ctx := context.Background()
			tg := prepared(t, e, b)
			if _, err := b.Deliver(ctx, tg, "t1", note()); err != nil {
				t.Fatal(err)
			}
			tg, _, _ = b.Prepare(ctx, "sub-1")
			e.syn.Accept(func(string) (string, string, bool) { return "", "", false })
			setup(e)
			out, err := b.Deliver(ctx, tg, "t2", note())
			if !errors.Is(err, ErrUnavailable) || out != 0 {
				t.Fatal(out, err)
			}
			if m, _ := e.st.MatrixMember(ctx, "sub-1"); m.RoomID == "" || !m.Enabled {
				t.Fatalf("member = %+v", m)
			}
		})
	}
}

func TestPrepareLocked(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	id := link(e, "sub-1", "one")
	if _, ok, err := b.Prepare(ctx, "sub-1"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Locked once the account is known, and locked before it is.
	e.mas.Lock(id)
	if _, ok, err := b.Prepare(ctx, "sub-1"); ok || err != nil {
		t.Fatalf("cached: %v %v", ok, err)
	}
	e.mas.Lock(link(e, "sub-2", "two"))
	if _, ok, err := b.Prepare(ctx, "sub-2"); ok || err != nil {
		t.Fatalf("looked up: %v %v", ok, err)
	}
}

// A stop handled after Prepare keeps Deliver from sending.
func TestDeliverAfterStop(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	tg := prepared(t, e, b)
	if err := e.st.SetMatrixEnabled(ctx, "sub-1", false); err != nil {
		t.Fatal(err)
	}
	out, err := b.Deliver(ctx, tg, "t1", note())
	if err != nil || out != Off {
		t.Fatal(out, err)
	}
	if len(e.syn.Rooms()) != 0 || len(e.syn.Sends()) != 0 {
		t.Fatalf("rooms=%d sends=%d", len(e.syn.Rooms()), len(e.syn.Sends()))
	}
}

// A room the listener made for the member after Prepare is the one Deliver
// sends into; it makes no second room.
func TestDeliverUsesRoomMadeMeanwhile(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	tg := prepared(t, e, b)
	e.syn.AddRoom("!invited:example.org")
	if err := e.st.SetMatrixRoom(ctx, "sub-1", "!invited:example.org"); err != nil {
		t.Fatal(err)
	}
	if out, err := b.Deliver(ctx, tg, "t1", note()); err != nil || out != Sent {
		t.Fatal(out, err)
	}
	if s := e.syn.Sends(); len(s) != 1 || s[0].Room != "!invited:example.org" || len(e.syn.Rooms()) != 1 {
		t.Fatalf("sends=%+v rooms=%d", s, len(e.syn.Rooms()))
	}
}

// An encrypted room whose first leave fails is still left.
func TestEncryptedRoomLeaveRetried(t *testing.T) {
	e, b := started(t)
	b.wait = func(context.Context, time.Duration) error { return nil }
	e.syn.EncryptNewRooms(true)
	e.syn.FailLeaves.Store(1)
	tg := prepared(t, e, b)
	if _, err := b.Deliver(context.Background(), tg, "t1", note()); !errors.Is(err, ErrEncrypted) {
		t.Fatal(err)
	}
	if rs := e.syn.Rooms(); len(rs) != 1 || !rs[0].Left {
		t.Fatalf("rooms=%+v", rs)
	}
}

// One that cannot be left at all is logged, not passed over in silence.
func TestEncryptedRoomLeaveFailureLogged(t *testing.T) {
	buf := captureLog(t)
	e, b := started(t)
	b.wait = func(context.Context, time.Duration) error { return nil }
	e.syn.EncryptNewRooms(true)
	e.syn.FailLeaves.Store(100)
	tg := prepared(t, e, b)
	if _, err := b.Deliver(context.Background(), tg, "t1", note()); !errors.Is(err, ErrEncrypted) {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "could not leave") {
		t.Fatalf("log: %s", buf.String())
	}
	if strings.Contains(buf.String(), "!room") {
		t.Fatalf("log names the room: %s", buf.String())
	}
}
