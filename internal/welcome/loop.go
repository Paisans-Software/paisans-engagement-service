package welcome

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/store"
)

// BatchEvery is how often newcomers are announced.
const BatchEvery = 5 * time.Minute

// Loop runs the welcomes until ctx ends, checking every tick. The Matrix
// batch runs on the copy that runs the bot; the Mbin batch, the email and
// pruning only while held reports this copy holds the welcome lock.
func Loop(ctx context.Context, r *Runner, now func() time.Time, tick time.Duration, held func() bool) {
	var lastBatch time.Time
	var pruned string
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		n := now()
		if n.Sub(lastBatch) >= BatchEvery {
			lastBatch = n
			if err := r.MatrixBatch(ctx, n); err != nil && ctx.Err() == nil {
				slog.Warn("the Matrix welcomes failed; trying again at the next batch", "error", err)
			}
			if held() {
				if err := r.MbinBatch(ctx, n); err != nil && ctx.Err() == nil {
					slog.Warn("the Mbin welcomes failed; trying again at the next batch", "error", err)
				}
			}
		}
		if held() {
			if err := r.DailyEmail(ctx, n); err != nil && ctx.Err() == nil {
				slog.Warn("the welcome email failed; trying again", "error", err)
			}
			if d := n.Format("2006-01-02"); d != pruned {
				if err := r.Store.PruneWelcome(ctx, n); err != nil {
					slog.Warn("pruning the welcome records failed", "error", err)
				} else {
					pruned = d
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// LockHolder holds the welcome lock for this copy while it can.
type LockHolder struct{ held atomic.Bool }

// Held reports whether this copy holds the welcome lock now.
func (h *LockHolder) Held() bool { return h.held.Load() }

// Hold tries to take the welcome lock every retry and holds it while it can,
// until ctx ends; it releases the lock before it returns.
func (h *LockHolder) Hold(ctx context.Context, st *store.Store, retry time.Duration) {
	for ctx.Err() == nil {
		l, ok, err := st.TryWelcomeLock(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("the welcome lock could not be tried", "error", err)
		}
		if ok {
			h.held.Store(true)
			select {
			case <-ctx.Done():
			case <-l.Lost():
				slog.Warn("the welcome lock was lost")
			}
			h.held.Store(false)
			l.Release()
		}
		t := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
