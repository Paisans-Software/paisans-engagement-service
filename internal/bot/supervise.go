package bot

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/mas"
	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
)

// Holder publishes the running bot to the rest of the service.
type Holder struct {
	bot     atomic.Pointer[Bot]
	pending atomic.Bool
}

// Get returns the running bot, or nil when this copy is not running one.
func (h *Holder) Get() *Bot { return h.bot.Load() }

// Pending says the run should wait for this copy's bot rather than skip the
// Matrix half. It is true while this copy may yet run the bot: from the start
// of Supervise until another copy is found to hold the lock or Matrix is off,
// and again whenever this copy holds the lock but the bot is not started. It
// is false while the bot is running and for a Holder that was never supervised.
func (h *Holder) Pending() bool { return h.bot.Load() == nil && h.pending.Load() }

// Deps is what Supervise needs.
type Deps struct {
	Matrix  *matrix.Client
	MAS     *mas.Client
	Store   *store.Store
	Members MemberChecker
	Config  Config
	// Retry is the wait between lock attempts and after a transient Start error.
	Retry time.Duration
}

// Supervise runs until ctx ends: it takes the bot lock (trying every Retry),
// starts the bot, publishes it in h and listens. On lock loss or when ctx
// ends it clears h. A bot that cannot run until the configuration or MAS
// changes is logged once at Error level, the lock is released and Supervise
// returns.
func Supervise(ctx context.Context, d Deps, h *Holder) {
	h.pending.Store(true)
	defer func() { h.bot.Store(nil); h.pending.Store(false) }()
	for ctx.Err() == nil {
		lock, ok, err := d.Store.TryBotLock(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				slog.Warn("matrix bot: the bot lock could not be tried", "error", err)
			}
		case !ok:
			h.pending.Store(false)
		default:
			h.pending.Store(true)
			if off := superviseHeld(ctx, d, h, lock); off {
				h.pending.Store(false)
				return
			}
			h.pending.Store(true)
		}
		if !sleep(ctx, d.Retry, nil) {
			return
		}
	}
}

// superviseHeld runs the bot while lock is held and releases the lock before
// it returns. off is true when the bot cannot run until something changes.
func superviseHeld(ctx context.Context, d Deps, h *Holder, lock *store.Lock) (off bool) {
	defer lock.Release()
	for {
		b, err := Start(ctx, d.Matrix, d.MAS, d.Store, d.Members, d.Config)
		if errors.Is(err, ErrOff) {
			slog.Error("Matrix is off", "reason", err.Error())
			return true
		}
		if err == nil {
			h.bot.Store(b)
			h.pending.Store(false)
			lctx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); b.Listen(lctx) }()
			select {
			case <-lock.Lost():
				slog.Warn("matrix bot: the bot lock was lost")
			case <-done:
			case <-ctx.Done():
			}
			cancel()
			<-done
			h.bot.Store(nil)
			return false
		}
		if ctx.Err() != nil {
			return false
		}
		slog.Warn("matrix bot: could not start; retrying", "reason", reason(err), "retry_in", d.Retry.String())
		if !sleep(ctx, d.Retry, lock.Lost()) {
			return false
		}
		select {
		case <-lock.Lost():
			return false
		default:
		}
	}
}

// sleep waits d. It returns false when ctx ends or lost closes first.
func sleep(ctx context.Context, d time.Duration, lost <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	case <-lost:
		return false
	}
}
