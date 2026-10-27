package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Status is how one member's digest for one week ended.
type Status string

const (
	StatusSent         Status = "sent"
	StatusEmpty        Status = "empty"
	StatusFailed       Status = "failed"
	StatusUnsubscribed Status = "unsubscribed"
)

// Item names a thread a digest showed: kind is its source's table, ID its id
// there.
type Item struct {
	Kind string
	ID   int64
}

// Unsubscribe records that the member never wants the email digest again.
// Which threads they were shown is forgotten once no channel is left: always
// when matrix is false (the deployment has no homeserver), otherwise when the
// member's Matrix digest is off too. Calling it twice changes nothing, so a
// repeated click on the link is harmless.
func (s *Store) Unsubscribe(ctx context.Context, subject string, matrix bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO unsubscribes (subject) VALUES ($1) ON CONFLICT (subject) DO NOTHING`, subject); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM shown WHERE subject=$1 AND ($2 OR EXISTS (SELECT 1 FROM matrix_members WHERE subject=$1 AND NOT enabled))`,
		subject, !matrix); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Shown is every thread an earlier digest showed the member, in weeks strictly
// before the date of before.
func (s *Store) Shown(ctx context.Context, subject string, before time.Time) (map[Item]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT kind, item_id FROM shown WHERE subject=$1 AND week < $2::date`, subject, weekDate(before))
	if err != nil {
		return nil, err
	}
	got := map[Item]bool{}
	var it Item
	_, err = pgx.ForEachRow(rows, []any{&it.Kind, &it.ID}, func() error {
		got[it] = true
		return nil
	})
	return got, err
}

func (s *Store) Unsubscribed(ctx context.Context, subject string) (bool, error) {
	var found bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM unsubscribes WHERE subject=$1)`, subject).Scan(&found)
	return found, err
}

// Claim reserves the member's send for a week and reports whether this caller
// won it. A second call for the same member and week, from any process, gets
// false, which is what keeps a restart or a second replica from mailing twice.
//
// week is stored as the calendar date of the value in its own location (the
// send Monday), not converted to UTC, so a 08:00 local send keeps its date.
func (s *Store) Claim(ctx context.Context, subject string, week time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO sends (subject, week, status) VALUES ($1, $2::date, 'pending') ON CONFLICT (subject, week) DO NOTHING`,
		subject, weekDate(week))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Finish records how a claimed send ended and, in the same transaction, the
// threads it showed. It fails if the send was never claimed or has already
// finished, since writing a status for a row nobody holds, or over one
// already recorded, would hide a bug.
func (s *Store) Finish(ctx context.Context, subject string, week time.Time, status Status, shown ...Item) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx,
		`UPDATE sends SET status=$3, at=now() WHERE subject=$1 AND week=$2::date AND status='pending'`,
		subject, weekDate(week), string(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		// No subject in the message: it is an OIDC identifier and the
		// error reaches the log.
		return fmt.Errorf("no claimed send in the week of %s", weekDate(week))
	}
	if len(shown) > 0 {
		kinds, ids := make([]string, len(shown)), make([]int64, len(shown))
		for i, it := range shown {
			kinds[i], ids[i] = it.Kind, it.ID
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO shown (subject, week, kind, item_id)
			SELECT $1, $2::date, k, i FROM unnest($3::text[], $4::bigint[]) AS t(k, i)
			ON CONFLICT DO NOTHING`,
			subject, weekDate(week), kinds, ids); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// weekDate is the calendar date of t in t's own location, as YYYY-MM-DD.
func weekDate(t time.Time) string { return t.Format("2006-01-02") }

// Release gives up a claim on a send that never reached the mail server, so
// a later run can claim the member again. Only a pending row is removed: a
// send that finished, however it ended, stays recorded.
func (s *Store) Release(ctx context.Context, subject string, week time.Time) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM sends WHERE subject=$1 AND week=$2::date AND status='pending'`,
		subject, weekDate(week))
	return err
}
