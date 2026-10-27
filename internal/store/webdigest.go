package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// DigestExpiry is when the digest of the week sent at week stops being
// readable on the web: 28 days on, at the same wall clock time.
func DigestExpiry(week time.Time) time.Time { return week.AddDate(0, 0, 28) }

// SaveDigest stores content as the member's digest for the week unless one is
// already stored, and returns whichever is stored. The digest expires at
// DigestExpiry(week).
func (s *Store) SaveDigest(ctx context.Context, subject string, week time.Time, content []byte) ([]byte, error) {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO web_digests (subject, week, content, expires_at) VALUES ($1, $2::date, $3, $4)
		ON CONFLICT (subject, week) DO NOTHING`,
		subject, weekDate(week), content, DigestExpiry(week)); err != nil {
		return nil, err
	}
	var got []byte
	err := s.pool.QueryRow(ctx, `SELECT content FROM web_digests WHERE subject=$1 AND week=$2::date`,
		subject, weekDate(week)).Scan(&got)
	return got, err
}

// SavedDigest is the member's stored digest for the week, whether or not it
// has expired.
func (s *Store) SavedDigest(ctx context.Context, subject string, week time.Time) ([]byte, bool, error) {
	var got []byte
	err := s.pool.QueryRow(ctx, `SELECT content FROM web_digests WHERE subject=$1 AND week=$2::date`,
		subject, weekDate(week)).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return got, err == nil, err
}

// DigestsForWeek is every member's stored digest for the week, by subject.
func (s *Store) DigestsForWeek(ctx context.Context, week time.Time) (map[string][]byte, error) {
	rows, err := s.pool.Query(ctx, `SELECT subject, content FROM web_digests WHERE week=$1::date`, weekDate(week))
	if err != nil {
		return nil, err
	}
	got := map[string][]byte{}
	var sub string
	var content []byte
	_, err = pgx.ForEachRow(rows, []any{&sub, &content}, func() error {
		got[sub] = content
		return nil
	})
	return got, err
}

// WebDigest is the stored digest for the week (YYYY-MM-DD) only while now is
// before its expiry. A weekDate that is not a date finds nothing.
func (s *Store) WebDigest(ctx context.Context, subject, weekDate string, now time.Time) ([]byte, bool, error) {
	if _, err := time.Parse(time.DateOnly, weekDate); err != nil {
		return nil, false, nil
	}
	var got []byte
	err := s.pool.QueryRow(ctx,
		`SELECT content FROM web_digests WHERE subject=$1 AND week=$2::date AND expires_at > $3`,
		subject, weekDate, now).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return got, err == nil, err
}

// PurgeDigests deletes every digest that has expired by now and reports how
// many there were.
func (s *Store) PurgeDigests(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM web_digests WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
