package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

// lockKeySQL computes the key the store takes for the bot lock in this test's schema.
const lockKeySQL = `SELECT ` + store.BotLockKeySQL

const testBotLock = store.BotLockID

func TestBotLockExclusive(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	l, ok, err := s.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatalf("first: ok=%v err=%v", ok, err)
	}
	l2, ok, err := s.TryBotLock(ctx)
	if err != nil || ok || l2 != nil {
		t.Fatalf("second: lock=%v ok=%v err=%v", l2, ok, err)
	}
	c := storetest.Conn(t, s)
	var got bool
	if err := c.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+lockKeySQL[len("SELECT "):]+`)`, testBotLock).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("another session took the held lock")
	}
	l.Release()
	l3, ok, err := s.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatalf("after release: ok=%v err=%v", ok, err)
	}
	l3.Release()
}

func TestBotLockLost(t *testing.T) {
	s := storetest.New(t)
	s.SetLockPing(20 * time.Millisecond)
	ctx := context.Background()
	l, ok, err := s.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	defer l.Release()
	c := storetest.Conn(t, s)
	var key int64
	if err := c.QueryRow(ctx, lockKeySQL, testBotLock).Scan(&key); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	err = c.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks
		WHERE locktype='advisory' AND granted
		AND ((classid::bigint << 32) | objid::bigint) = $1`, key).Scan(&terminated)
	if err != nil || !terminated {
		t.Fatalf("terminate: %v %v", terminated, err)
	}
	select {
	case <-l.Lost():
	case <-time.After(5 * time.Second):
		t.Fatal("Lost was not closed")
	}
}

// Release returns at once even while a ping is waiting on the connection.
func TestBotLockReleaseCutsPing(t *testing.T) {
	s := storetest.New(t)
	s.SetLockPing(time.Millisecond)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		l, ok, err := s.TryBotLock(ctx)
		if err != nil || !ok {
			t.Fatalf("attempt %d: ok=%v err=%v", i, ok, err)
		}
		time.Sleep(time.Duration(i%3) * time.Millisecond)
		start := time.Now()
		l.Release()
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("Release took %v", d)
		}
		select {
		case <-l.Lost():
			t.Fatal("Release reported the lock lost")
		default:
		}
	}
}

func TestWelcomeLockIsItsOwn(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	b, ok, err := s.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer b.Release()
	w, ok, err := s.TryWelcomeLock(ctx)
	if err != nil || !ok {
		t.Fatalf("welcome lock while the bot lock is held: %v, %v", ok, err)
	}
	if _, ok, _ := s.TryWelcomeLock(ctx); ok {
		t.Fatal("the welcome lock was taken twice")
	}
	w.Release()
	w2, ok, err := s.TryWelcomeLock(ctx)
	if err != nil || !ok {
		t.Fatalf("welcome lock after release: %v, %v", ok, err)
	}
	w2.Release()
}
