package store

import (
	"context"
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
	return s.finish(ctx, tableDMSends, subject, week, status, shown...)
}

// ReleaseDM gives up a claim on a Matrix send that never reached the
// homeserver. Only a pending row is removed.
func (s *Store) ReleaseDM(ctx context.Context, subject string, week time.Time) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM dm_sends WHERE subject=$1 AND week=$2::date AND status='pending'`,
		subject, weekDate(week))
	return err
}
