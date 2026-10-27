package bot

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/mas"
	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

func (e *env) deps() Deps {
	return Deps{
		Matrix: matrix.New(e.syn.URL, nil, matrix.Pacing{MessagesPerSecond: 1000, MessageBurst: 100, RoomsPerSecond: 1000, RoomBurst: 100}),
		MAS:    mas.New(e.mas.URL, e.mas.ClientID, e.mas.Secret, nil),
		Store:  e.st, Config: e.cfg, Retry: 10 * time.Millisecond,
	}
}

// supervise runs Supervise in the background and stops it when the test ends.
func supervise(t *testing.T, d Deps, h *Holder) (done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan struct{})
	go func() { defer close(ch); Supervise(ctx, d, h) }()
	t.Cleanup(func() { cancel(); <-ch })
	return ch
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func captureLog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestSuperviseRunsBot(t *testing.T) {
	e := newEnv(t)
	var h Holder
	supervise(t, e.deps(), &h)
	eventually(t, "the bot", func() bool { return h.Get() != nil })
	if h.Pending() {
		t.Error("Pending while running")
	}
	if h.Get().UserID() != botUser {
		t.Errorf("user = %s", h.Get().UserID())
	}
}

func TestSuperviseOneHolder(t *testing.T) {
	e1 := newEnv(t)
	e2 := &env{mas: e1.mas, syn: e1.syn, st: e1.st, cfg: e1.cfg}
	var h1, h2 Holder
	supervise(t, e1.deps(), &h1)
	supervise(t, e2.deps(), &h2)
	eventually(t, "a bot", func() bool { return h1.Get() != nil || h2.Get() != nil })
	eventually(t, "the second copy to learn it is second", func() bool { return !h1.Pending() && !h2.Pending() })
	time.Sleep(200 * time.Millisecond)
	if (h1.Get() != nil) == (h2.Get() != nil) {
		t.Fatalf("holders: %v and %v", h1.Get() != nil, h2.Get() != nil)
	}
	if h1.Pending() || h2.Pending() {
		t.Error("Pending with the lock decided")
	}
}

func TestSuperviseSecondCopyNotPending(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	l, ok, err := e.st.TryBotLock(ctx)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	defer l.Release()
	var h Holder
	supervise(t, e.deps(), &h)
	eventually(t, "Pending to clear", func() bool { return !h.Pending() })
	if h.Get() != nil {
		t.Error("bot started without the lock")
	}
}

func TestSuperviseOffLogsOnce(t *testing.T) {
	e := newEnv(t)
	e.mas.VersionString = "v1.4.1"
	buf := captureLog(t)
	var h Holder
	done := supervise(t, e.deps(), &h)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Supervise did not return")
	}
	if h.Get() != nil || h.Pending() {
		t.Errorf("Get=%v Pending=%v", h.Get(), h.Pending())
	}
	out := buf.String()
	if n := strings.Count(out, "Matrix is off"); n != 1 {
		t.Fatalf("%d records:\n%s", n, out)
	}
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "v1.4.1") {
		t.Errorf("log = %s", out)
	}
	// The lock was released.
	l, ok, err := e.st.TryBotLock(context.Background())
	if err != nil || !ok {
		t.Fatalf("lock after off: ok=%v err=%v", ok, err)
	}
	l.Release()
}

func TestSuperviseRetriesUnavailable(t *testing.T) {
	e := newEnv(t)
	e.mas.Down.Store(true)
	var h Holder
	supervise(t, e.deps(), &h)
	time.Sleep(100 * time.Millisecond)
	if h.Get() != nil || !h.Pending() {
		t.Fatalf("while down: Get=%v Pending=%v", h.Get(), h.Pending())
	}
	e.mas.Down.Store(false)
	eventually(t, "the bot", func() bool { return h.Get() != nil })
	if h.Pending() {
		t.Error("Pending while running")
	}
}

func TestSuperviseLockLossClearsAndRecovers(t *testing.T) {
	e := newEnv(t)
	var h Holder
	supervise(t, e.deps(), &h)
	eventually(t, "the bot", func() bool { return h.Get() != nil })
	c := storetest.Conn(t, e.st)
	// End the session holding the lock.
	if _, err := c.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND granted AND pid <> pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	// The ping runs every 15s, so the loss is noticed late; the bot is
	// back afterwards.
	eventually2(t, 40*time.Second, "the loss to clear the holder", func() bool { return h.Get() == nil })
	eventually2(t, 10*time.Second, "the bot again", func() bool { return h.Get() != nil })
}

func eventually2(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHolderZero(t *testing.T) {
	var h Holder
	if h.Get() != nil || h.Pending() {
		t.Error("zero Holder is not idle")
	}
}
