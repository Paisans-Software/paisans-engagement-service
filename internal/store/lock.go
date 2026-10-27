package store

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BotLockID identifies the lock held by the one copy that runs the Digest
// Bot. The lock is keyed by the current schema as well, so stores in
// different schemas of one database each have their own bot.
const BotLockID = 0x656e676167650002 // "engage\x00\x02"

const (
	lockPingEvery   = 15 * time.Second
	lockPingTimeout = 10 * time.Second
)

// BotLockKeySQL is the bot lock's key as an SQL expression of BotLockID,
// passed as $1, and the current schema.
const BotLockKeySQL = `$1::bigint # hashtextextended(current_schema(), 0)`

// SetLockPing sets how often a held bot lock's connection is checked, for
// tests that need a lost lock noticed quickly; zero restores the default.
func (s *Store) SetLockPing(every time.Duration) { s.lockPing.Store(int64(every)) }

// Lock is the bot lock, held on a connection of its own.
type Lock struct {
	conn   *pgxpool.Conn
	every  time.Duration
	lost   chan struct{}
	stop   chan struct{}
	done   chan struct{}
	ctx    context.Context // ends the ping in flight when the lock is released
	cancel context.CancelFunc
	once   sync.Once
}

// TryBotLock takes the bot lock on a connection of its own. ok is false when
// another session holds it. The lock lasts until Release or until the
// connection fails, which closes Lost.
func (s *Store) TryBotLock(ctx context.Context) (l *Lock, ok bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+BotLockKeySQL+`)`, BotLockID).Scan(&got); err != nil {
		// The statement may have taken the lock before the error; closing the
		// connection ends the session and releases it.
		conn.Conn().Close(context.Background())
		conn.Release()
		return nil, false, err
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}
	every := time.Duration(s.lockPing.Load())
	if every <= 0 {
		every = lockPingEvery
	}
	pctx, cancel := context.WithCancel(context.Background())
	l = &Lock{conn: conn, every: every, lost: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{}),
		ctx: pctx, cancel: cancel}
	go l.watch()
	return l, true, nil
}

// watch pings the connection until it fails or the lock is released.
func (l *Lock) watch() {
	defer close(l.done)
	t := time.NewTicker(l.every)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(l.ctx, lockPingTimeout)
			_, err := l.conn.Exec(ctx, `SELECT 1`)
			cancel()
			if err != nil {
				select {
				case <-l.stop:
					// Release cut the ping short; the lock was not lost.
				default:
					close(l.lost)
				}
				return
			}
		}
	}
}

// Lost is closed when the connection holding the lock fails. The lock must
// still be released after Lost closes, to return its connection.
func (l *Lock) Lost() <-chan struct{} { return l.lost }

// Release gives the lock up and returns its connection. It ends a ping in
// flight rather than waiting for it, and is safe to call more than once,
// including after Lost has closed.
func (l *Lock) Release() {
	l.once.Do(func() {
		close(l.stop)
		l.cancel()
		<-l.done
		select {
		case <-l.lost:
			// The session is gone and took the lock with it.
			l.conn.Conn().Close(context.Background())
		default:
			if l.conn.Conn().IsClosed() {
				// The cut-short ping closed the connection, and the session
				// ended with the lock.
				break
			}
			var unlocked bool
			err := l.conn.QueryRow(context.Background(), `SELECT pg_advisory_unlock(`+BotLockKeySQL+`)`, BotLockID).Scan(&unlocked)
			if err != nil || !unlocked {
				// A session lock that is not released would stay with the
				// pooled connection; closing the connection ends the session.
				slog.Warn("could not release the bot lock; closing its connection", "error", err)
				l.conn.Conn().Close(context.Background())
			}
		}
		l.conn.Release()
	})
}
