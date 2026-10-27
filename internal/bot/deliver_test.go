package bot

import (
	"context"
	"errors"
	"testing"

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
