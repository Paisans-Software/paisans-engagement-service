package store

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// botLock is held by the one copy that runs the Digest Bot. The lock is keyed
// by the current schema as well, so stores in different schemas of one
// database each have their own bot.
const botLock = 0x656e676167650002 // "engage\x00\x02"

const (
	lockPingEvery   = 15 * time.Second
	lockPingTimeout = 10 * time.Second
)

// Lock is the bot lock, held on a connection of its own.
type Lock struct {
	conn *pgxpool.Conn
	lost chan struct{}
	stop chan struct{}
	done chan struct{}
	once sync.Once
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
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bigint # hashtextextended(current_schema(), 0))`, botLock).Scan(&got); err != nil {
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
	l = &Lock{conn: conn, lost: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	go l.watch()
	return l, true, nil
}

// watch pings the connection until it fails or the lock is released.
func (l *Lock) watch() {
	defer close(l.done)
	t := time.NewTicker(lockPingEvery)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), lockPingTimeout)
			_, err := l.conn.Exec(ctx, `SELECT 1`)
			cancel()
			if err != nil {
				close(l.lost)
				return
			}
		}
	}
}

// Lost is closed when the connection holding the lock fails.
func (l *Lock) Lost() <-chan struct{} { return l.lost }

// Release gives the lock up. It is safe to call more than once.
func (l *Lock) Release() {
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		select {
		case <-l.lost:
			// The session is gone and took the lock with it.
			l.conn.Conn().Close(context.Background())
		default:
			var unlocked bool
			err := l.conn.QueryRow(context.Background(), `SELECT pg_advisory_unlock($1::bigint # hashtextextended(current_schema(), 0))`, botLock).Scan(&unlocked)
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
