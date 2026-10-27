package store

import (
	"context"
	"fmt"
	"time"
)

// ClaimDM reserves the member's Matrix send for a week and reports whether
// this caller won it. It is independent of the email claim for the same week.
func (s *Store) ClaimDM(ctx context.Context, subject string, week time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO dm_sends (subject, week, status) VALUES ($1, $2::date, 'pending') ON CONFLICT (subject, week) DO NOTHING`,
		subject, weekDate(week))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// FinishDM records how a claimed Matrix send ended (StatusSent or
// StatusFailed) and, in the same transaction, the threads it showed. It fails
// if the send was never claimed or has already finished.
func (s *Store) FinishDM(ctx context.Context, subject string, week time.Time, status Status, shown ...Item) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx,
		`UPDATE dm_sends SET status=$3, at=now() WHERE subject=$1 AND week=$2::date AND status='pending'`,
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

// ReleaseDM gives up a claim on a Matrix send that never reached the
// homeserver. Only a pending row is removed.
func (s *Store) ReleaseDM(ctx context.Context, subject string, week time.Time) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM dm_sends WHERE subject=$1 AND week=$2::date AND status='pending'`,
		subject, weekDate(week))
	return err
}
