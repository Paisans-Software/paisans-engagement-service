package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Platform is where a newcomer was welcomed.
type Platform string

const (
	PlatformMbin   Platform = "mbin"
	PlatformMatrix Platform = "matrix"
)

// WelcomeKeep is how long a welcome record is kept.
const WelcomeKeep = 21 * 24 * time.Hour

// WelcomeStart is when welcomes first ran on the platform, storing now the
// first time it is asked.
func (s *Store) WelcomeStart(ctx context.Context, p Platform, now time.Time) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx,
		`WITH ins AS (INSERT INTO welcome_starts (platform, at) VALUES ($1, $2) ON CONFLICT DO NOTHING RETURNING at)
		SELECT at FROM ins UNION ALL SELECT at FROM welcome_starts WHERE platform=$1 LIMIT 1`,
		string(p), now).Scan(&at)
	return at, err
}

// Welcomed is one newcomer welcomed. Account is the Mbin user id, as text,
// or the MAS user id; MbinID, EntryID, URL and Subscribed are Mbin's only.
type Welcomed struct {
	Platform        Platform
	Account, Name   string
	MbinID, EntryID int64
	URL             string
	Subscribed      bool
	At              time.Time
}

// WelcomedAccounts is every account welcomed on the platform and still kept.
func (s *Store) WelcomedAccounts(ctx context.Context, p Platform) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT account FROM welcomed WHERE platform=$1`, string(p))
	if err != nil {
		return nil, err
	}
	got := map[string]bool{}
	var a string
	_, err = pgx.ForEachRow(rows, []any{&a}, func() error { got[a] = true; return nil })
	return got, err
}

// RecordWelcomed records newcomers as welcomed, in one transaction. One
// already recorded is left as it is.
func (s *Store) RecordWelcomed(ctx context.Context, ws []Welcomed) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, w := range ws {
		if _, err := tx.Exec(ctx,
			`INSERT INTO welcomed (platform, account, name, mbin_id, entry_id, url, subscribed, at)
			VALUES ($1, $2, $3, NULLIF($4, 0), NULLIF($5, 0), $6, $7, $8) ON CONFLICT DO NOTHING`,
			string(w.Platform), w.Account, w.Name, w.MbinID, w.EntryID, w.URL, w.Subscribed, w.At); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const welcomedColumns = `platform, account, name, COALESCE(mbin_id, 0), COALESCE(entry_id, 0), url, subscribed, at`

func (s *Store) welcomedRows(ctx context.Context, sql string, args ...any) ([]Welcomed, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Welcomed, error) {
		var (
			w Welcomed
			p string
		)
		err := r.Scan(&p, &w.Account, &w.Name, &w.MbinID, &w.EntryID, &w.URL, &w.Subscribed, &w.At)
		w.Platform = Platform(p)
		return w, err
	})
}

// PendingSubscriptions is every Mbin newcomer welcomed and not yet
// subscribed to the magazine.
func (s *Store) PendingSubscriptions(ctx context.Context) ([]Welcomed, error) {
	return s.welcomedRows(ctx, `SELECT `+welcomedColumns+` FROM welcomed
		WHERE platform='mbin' AND NOT subscribed ORDER BY at, account`)
}

// WelcomeSubscribed records that the Mbin newcomer is subscribed.
func (s *Store) WelcomeSubscribed(ctx context.Context, account string) error {
	_, err := s.pool.Exec(ctx, `UPDATE welcomed SET subscribed=true WHERE platform='mbin' AND account=$1`, account)
	return err
}

// ClaimWelcomesForDay gives the day's welcome email its newcomers: every Mbin
// newcomer with a thread that no email has named yet becomes the day's, and
// the day's are returned. Asking again the same day returns the same ones,
// plus none recorded since: those wait for the next day.
func (s *Store) ClaimWelcomesForDay(ctx context.Context, day time.Time) ([]Welcomed, error) {
	d := weekDate(day)
	if _, err := s.pool.Exec(ctx,
		`UPDATE welcomed SET day=$1::date WHERE platform='mbin' AND day IS NULL AND entry_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM welcomed w WHERE w.platform='mbin' AND w.day=$1::date)`, d); err != nil {
		return nil, err
	}
	return s.welcomedRows(ctx, `SELECT `+welcomedColumns+` FROM welcomed
		WHERE platform='mbin' AND day=$1::date ORDER BY at, account`, d)
}

// ClaimWelcomeSend reserves the member's welcome email for the day and
// reports whether this caller won it.
func (s *Store) ClaimWelcomeSend(ctx context.Context, subject string, day time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO welcome_sends (subject, day, status) VALUES ($1, $2::date, 'pending') ON CONFLICT DO NOTHING`,
		subject, weekDate(day))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// FinishWelcomeSend records how a claimed welcome email ended: StatusSent or
// StatusFailed.
func (s *Store) FinishWelcomeSend(ctx context.Context, subject string, day time.Time, status Status) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE welcome_sends SET status=$3, at=now() WHERE subject=$1 AND day=$2::date AND status='pending'`,
		subject, weekDate(day), string(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("no claimed welcome email on %s", weekDate(day))
	}
	return nil
}

// ReleaseWelcomeSend gives up a claim on a welcome email that never reached
// the mail server.
func (s *Store) ReleaseWelcomeSend(ctx context.Context, subject string, day time.Time) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM welcome_sends WHERE subject=$1 AND day=$2::date AND status='pending'`, subject, weekDate(day))
	return err
}

// WelcomeSendsOn is every member claimed for the day's welcome email.
func (s *Store) WelcomeSendsOn(ctx context.Context, day time.Time) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT subject FROM welcome_sends WHERE day=$1::date`, weekDate(day))
	if err != nil {
		return nil, err
	}
	got := map[string]bool{}
	var sub string
	_, err = pgx.ForEachRow(rows, []any{&sub}, func() error { got[sub] = true; return nil })
	return got, err
}

// UnsubscribeWelcome records that the member wants no more welcome emails. It
// leaves the digest alone.
func (s *Store) UnsubscribeWelcome(ctx context.Context, subject string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO welcome_unsubscribes (subject) VALUES ($1) ON CONFLICT DO NOTHING`, subject)
	return err
}

// WelcomeUnsubscribed is every member who unsubscribed from welcome emails.
func (s *Store) WelcomeUnsubscribed(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT subject FROM welcome_unsubscribes`)
	if err != nil {
		return nil, err
	}
	got := map[string]bool{}
	var sub string
	_, err = pgx.ForEachRow(rows, []any{&sub}, func() error { got[sub] = true; return nil })
	return got, err
}

// AlertDue reports whether an alert of the kind may go out now, and records
// it if so: at most one per kind per calendar day of now.
func (s *Store) AlertDue(ctx context.Context, kind string, now time.Time) (bool, error) {
	var k string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO welcome_alerts (kind, day, at) VALUES ($1, $2::date, $3)
		ON CONFLICT (kind) DO UPDATE SET day=$2::date, at=$3 WHERE welcome_alerts.day < $2::date
		RETURNING kind`, kind, weekDate(now), now).Scan(&k)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// PruneWelcome deletes the welcome records older than WelcomeKeep. The starts
// and the unsubscribes stay.
func (s *Store) PruneWelcome(ctx context.Context, now time.Time) error {
	cut := now.Add(-WelcomeKeep)
	for _, q := range []string{
		`DELETE FROM welcomed WHERE at < $1`,
		`DELETE FROM welcome_sends WHERE at < $1`,
		`DELETE FROM welcome_alerts WHERE at < $1`,
	} {
		if _, err := s.pool.Exec(ctx, q, cut); err != nil {
			return err
		}
	}
	return nil
}

// MbinAccount is the Mbin user id as a welcome record's account.
func MbinAccount(id int64) string { return strconv.FormatInt(id, 10) }
