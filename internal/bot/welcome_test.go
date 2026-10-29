package bot

import (
	"context"
	"fmt"
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
	got, err := b.NewMembers(ctx, since, func(string) bool { return false }, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MASUserID != newID || got[0].UserID != "@newbie:example.org" {
		t.Fatalf("new members = %+v", got)
	}
}

// A backlog larger than a page and than the limit is worked through over
// several calls, oldest first, and nobody is dropped.
func TestNewMembersBacklog(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	var ids []string
	for i := range 250 {
		id := e.mas.AddUser(fmt.Sprintf("n%03d", i), since.Add(time.Duration(i)*time.Second))
		e.mas.Link(provider, fmt.Sprintf("sub-%d", i), id)
		ids = append(ids, id)
	}
	done := map[string]bool{}
	for range 4 {
		got, err := b.NewMembers(ctx, since, func(id string) bool { return done[id] }, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range got {
			if done[n.MASUserID] {
				t.Fatalf("%s returned twice", n.Username)
			}
			done[n.MASUserID] = true
		}
	}
	for i, id := range ids {
		if !done[id] {
			t.Fatalf("member %d was dropped", i)
		}
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
