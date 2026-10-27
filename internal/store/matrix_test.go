package store_test

import (
	"context"
	"testing"

	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

func TestMatrixMemberDefaultsOn(t *testing.T) {
	s := storetest.New(t)
	m, err := s.MatrixMember(context.Background(), "nobody")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Enabled || m.Subject != "nobody" || m.MASUserID != "" || m.UserID != "" || m.RoomID != "" {
		t.Fatalf("got %+v", m)
	}
}

func TestMatrixAccountAndRoom(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetMatrixAccount(ctx, "sub-1", "01J0", "@one:example.org"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMatrixRoom(ctx, "sub-1", "!r:example.org"); err != nil {
		t.Fatal(err)
	}
	want := store.MatrixMember{Subject: "sub-1", Enabled: true, MASUserID: "01J0", UserID: "@one:example.org", RoomID: "!r:example.org"}
	if got, ok, err := s.MatrixMemberByRoom(ctx, "!r:example.org"); err != nil || !ok || got != want {
		t.Fatalf("by room: %+v %v %v", got, ok, err)
	}
	if got, ok, err := s.MatrixMemberByUser(ctx, "@one:example.org"); err != nil || !ok || got != want {
		t.Fatalf("by user: %+v %v %v", got, ok, err)
	}
	if _, ok, err := s.MatrixMemberByRoom(ctx, "!other"); err != nil || ok {
		t.Fatalf("unknown room: %v %v", ok, err)
	}
	if _, ok, err := s.MatrixMemberByUser(ctx, "@other:example.org"); err != nil || ok {
		t.Fatalf("unknown user: %v %v", ok, err)
	}
}

func TestForgetRoomOnlyIfCurrent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetMatrixRoom(ctx, "sub-1", "!a"); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetMatrixRoom(ctx, "sub-1", "!b"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.MatrixMember(ctx, "sub-1"); m.RoomID != "!a" {
		t.Fatalf("got %q", m.RoomID)
	}
	if err := s.ForgetMatrixRoom(ctx, "sub-1", "!a"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.MatrixMember(ctx, "sub-1"); m.RoomID != "" {
		t.Fatalf("got %q", m.RoomID)
	}
}

func TestLeftMatrix(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetMatrixRoom(ctx, "sub-1", "!a"); err != nil {
		t.Fatal(err)
	}
	if err := s.LeftMatrix(ctx, "sub-1", "!a"); err != nil {
		t.Fatal(err)
	}
	m, err := s.MatrixMember(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled || m.RoomID != "" {
		t.Fatalf("got %+v", m)
	}
}

func shownFor(t *testing.T, s *store.Store, sub string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Claim(ctx, sub, monday); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, sub, monday, store.StatusSent, store.Item{Kind: "entry", ID: 1}); err != nil {
		t.Fatal(err)
	}
}

func shownCount(t *testing.T, s *store.Store, sub string) int {
	t.Helper()
	got, err := s.Shown(context.Background(), sub, monday.AddDate(0, 0, 7))
	if err != nil {
		t.Fatal(err)
	}
	return len(got)
}

func TestShownForgottenOnlyWhenBothOff(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	for _, sub := range []string{"sub-1", "sub-2", "sub-3"} {
		shownFor(t, s, sub)
	}

	if err := s.Unsubscribe(ctx, "sub-1", true); err != nil {
		t.Fatal(err)
	}
	if n := shownCount(t, s, "sub-1"); n != 1 {
		t.Fatalf("email off only: %d", n)
	}
	if err := s.SetMatrixEnabled(ctx, "sub-1", false); err != nil {
		t.Fatal(err)
	}
	if n := shownCount(t, s, "sub-1"); n != 0 {
		t.Fatalf("both off: %d", n)
	}

	if err := s.SetMatrixEnabled(ctx, "sub-2", false); err != nil {
		t.Fatal(err)
	}
	if n := shownCount(t, s, "sub-2"); n != 1 {
		t.Fatalf("matrix off only: %d", n)
	}
	if err := s.Unsubscribe(ctx, "sub-2", true); err != nil {
		t.Fatal(err)
	}
	if n := shownCount(t, s, "sub-2"); n != 0 {
		t.Fatalf("both off: %d", n)
	}

	if err := s.Unsubscribe(ctx, "sub-3", false); err != nil {
		t.Fatal(err)
	}
	if n := shownCount(t, s, "sub-3"); n != 0 {
		t.Fatalf("no homeserver: %d", n)
	}
}

func TestLeftMatrixForgetsShownWhenUnsubscribed(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	shownFor(t, s, "sub-1")
	if err := s.Unsubscribe(ctx, "sub-1", true); err != nil {
		t.Fatal(err)
	}
	if err := s.LeftMatrix(ctx, "sub-1", "!a"); err != nil {
		t.Fatal(err)
	}
	if n := shownCount(t, s, "sub-1"); n != 0 {
		t.Fatalf("got %d", n)
	}
}

func TestBotStateRoundTrip(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, found, err := s.BotState(ctx, "k"); err != nil || found {
		t.Fatalf("missing: %v %v", found, err)
	}
	if err := s.SetBotState(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBotState(ctx, "k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, found, err := s.BotState(ctx, "k"); err != nil || !found || v != "v2" {
		t.Fatalf("got %q %v %v", v, found, err)
	}
	if err := s.DeleteBotState(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.BotState(ctx, "k"); found {
		t.Fatal("still there")
	}
}
