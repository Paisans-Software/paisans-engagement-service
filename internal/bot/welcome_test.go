package bot

import (
	"context"
	"testing"
	"time"
)

func TestNewMembers(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	newID := e.mas.AddUser("newbie", since.Add(time.Minute))
	e.mas.Link(provider, "sub-new", newID)
	oldID := e.mas.AddUser("oldie", since.Add(-time.Minute))
	e.mas.Link(provider, "sub-old", oldID)
	e.mas.AddUser("unlinked", since.Add(time.Minute))
	goneID := e.mas.AddUser("gone", since.Add(time.Minute))
	e.mas.Link(provider, "sub-gone", goneID)
	e.mas.Deactivate(goneID)
	got, err := b.NewMembers(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MASUserID != newID || got[0].UserID != "@newbie:example.org" {
		t.Fatalf("new members = %+v", got)
	}
}

func TestCommunityRoomCreatesThenJoinsExisting(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	room, err := b.CommunityRoom(ctx, "Riverside Community", "riverside-community")
	if err != nil || room == "" {
		t.Fatalf("CommunityRoom = %q, %v", room, err)
	}
	if rs := e.syn.Rooms(); len(rs) != 1 || rs[0].Request["name"] != "Riverside Community" {
		t.Fatalf("rooms = %+v", rs)
	}
	e.syn.AddAlias("#lobby:example.org", "!human:example.org")
	got, err := b.CommunityRoom(ctx, "Lobby", "lobby")
	if err != nil || got != "!human:example.org" {
		t.Fatalf("existing room = %q, %v", got, err)
	}
	if len(e.syn.Rooms()) != 1 {
		t.Fatal("a room was created for an alias that exists")
	}
}

func TestAnnounceRefused(t *testing.T) {
	e, b := started(t)
	e.syn.RefuseSend("!human:example.org", 403, "M_FORBIDDEN")
	err := b.Announce(context.Background(), "!human:example.org", "txn", map[string]any{"msgtype": "m.text", "body": "hi"})
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Announce = %v, want a refusal", err)
	}
	if err := b.Announce(context.Background(), "!other:example.org", "txn", map[string]any{"msgtype": "m.text", "body": "hi"}); err != nil {
		t.Fatal(err)
	}
}
