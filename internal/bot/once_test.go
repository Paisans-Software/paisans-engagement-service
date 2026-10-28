package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// With no service running the bot, Once starts it under the bot lock, and
// the lock is free again once done is called.
func TestOnceTakesFreeLock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	b, done, err := Once(ctx, e.deps())
	if err != nil {
		t.Fatal(err)
	}
	if b.UserID() != botUser || len(e.mas.Users()) != 1 {
		t.Fatalf("bot %q, users %+v", b.UserID(), e.mas.Users())
	}
	if _, ok, err := e.st.TryBotLock(ctx); err != nil || ok {
		t.Fatalf("lock free while Once holds it: %v, %v", ok, err)
	}
	done()
	l, ok, err := e.st.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatalf("lock still held after done: %v, %v", ok, err)
	}
	l.Release()
}

// While the service holds the lock, Once acts as the running bot with its
// stored token: it creates no account, no session and no new token, and the
// service's lock is untouched.
func TestOnceSharesRunningBot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	lock, ok, err := e.st.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Release()
	tok := e.state(t, "access_token")
	b, done, err := Once(ctx, e.deps())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if len(e.mas.Users()) != 1 || len(e.mas.Sessions()) != 1 || e.state(t, "access_token") != tok {
		t.Fatalf("users %d, sessions %+v", len(e.mas.Users()), e.mas.Sessions())
	}
	link(e, "sub-1", "one")
	tg, ok, err := b.Prepare(ctx, "sub-1")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if out, err := b.Deliver(ctx, tg, "t1", note()); err != nil || out != Sent {
		t.Fatal(out, err)
	}
	done()
	select {
	case <-lock.Lost():
		t.Fatal("the service's lock was lost")
	default:
	}
}

// A shared bot never renews the token itself, since a new token would cut off
// the running service. Refused, it takes the token the service stored since,
// and with none newer the send is an outage.
func TestOnceSharedNeverRenews(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	svc, err := e.start(t)
	if err != nil {
		t.Fatal(err)
	}
	lock, ok, err := e.st.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Release()
	b, done, err := Once(ctx, e.deps())
	if err != nil {
		t.Fatal(err)
	}
	defer done()

	// The service renews; the shared bot's next call is refused and picks
	// up the stored token.
	if err := svc.renew(ctx, svc.currentToken()); err != nil {
		t.Fatal(err)
	}
	whoami := func() error { _, _, err := b.m.WhoAmI(ctx); return err }
	if err := b.call(ctx, whoami); err != nil {
		t.Fatal(err)
	}
	if b.currentToken() != e.state(t, "access_token") {
		t.Fatal("the shared bot did not take the stored token")
	}

	// The stored token is revoked and nothing newer is stored.
	sessions := len(e.mas.Sessions())
	stale := e.state(t, "access_token")
	if err := e.st.SetBotState(ctx, "access_token", "revoked"); err != nil {
		t.Fatal(err)
	}
	b.setToken("revoked")
	err = b.call(ctx, whoami)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if len(e.mas.Sessions()) != sessions || e.mas.Sessions()[0].Token != stale {
		t.Fatal("the shared bot renewed the token")
	}
}

// While the service holds the lock but has no bot account or token yet, Once
// refuses and creates nothing.
func TestOnceSharedNotStarted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	lock, ok, err := e.st.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Release()
	if _, _, err := Once(ctx, e.deps()); err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("err = %v", err)
	}
	if len(e.mas.Users()) != 0 || len(e.mas.Sessions()) != 0 {
		t.Fatal("Once created an account or a session")
	}
}

// While the service holds the lock with a token the homeserver refuses,
// Once refuses rather than renewing it.
func TestOnceSharedTokenRefused(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	lock, ok, err := e.st.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Release()
	if err := e.st.SetBotState(ctx, "access_token", "stale"); err != nil {
		t.Fatal(err)
	}
	tok := e.mas.Sessions()[0].Token
	if _, _, err := Once(ctx, e.deps()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if e.mas.Sessions()[0].Token != tok {
		t.Fatal("Once regenerated the service's token")
	}
}

// A localpart taken by someone else is refused as Start refuses it, and the
// lock is given back.
func TestOnceTakenLocalpart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.mas.Link(provider, "sub-x", e.mas.AddUser("digest-bot", time.Now().Add(-time.Hour)))
	_, _, err := Once(ctx, e.deps())
	if !errors.Is(err, ErrOff) || !strings.Contains(err.Error(), "ENGAGEMENT_MATRIX_BOT_LOCALPART") {
		t.Fatalf("err = %v", err)
	}
	l, ok, err := e.st.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatalf("lock not released: %v, %v", ok, err)
	}
	l.Release()
}
