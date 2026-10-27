package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// MatrixMember is one member's Matrix setting and what is known of their
// account and room.
type MatrixMember struct {
	Subject   string
	Enabled   bool   // a member with no row is on
	MASUserID string // "" until looked up
	UserID    string // @localpart:server_name, "" until looked up
	RoomID    string // "" when there is no current room
}

const matrixColumns = `subject, enabled, COALESCE(mas_user_id, ''), COALESCE(user_id, ''), COALESCE(room_id, '')`

func scanMatrix(row pgx.Row) (MatrixMember, error) {
	var m MatrixMember
	err := row.Scan(&m.Subject, &m.Enabled, &m.MASUserID, &m.UserID, &m.RoomID)
	return m, err
}

// MatrixMember is the member's row, or the defaults (on, nothing known) when
// there is none.
func (s *Store) MatrixMember(ctx context.Context, subject string) (MatrixMember, error) {
	m, err := scanMatrix(s.pool.QueryRow(ctx, `SELECT `+matrixColumns+` FROM matrix_members WHERE subject=$1`, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		return MatrixMember{Subject: subject, Enabled: true}, nil
	}
	return m, err
}

func (s *Store) matrixBy(ctx context.Context, column, value string) (MatrixMember, bool, error) {
	m, err := scanMatrix(s.pool.QueryRow(ctx, `SELECT `+matrixColumns+` FROM matrix_members WHERE `+column+`=$1`, value))
	if errors.Is(err, pgx.ErrNoRows) {
		return MatrixMember{}, false, nil
	}
	return m, err == nil, err
}

// MatrixMemberByRoom is the member whose current room is roomID.
func (s *Store) MatrixMemberByRoom(ctx context.Context, roomID string) (MatrixMember, bool, error) {
	return s.matrixBy(ctx, "room_id", roomID)
}

// MatrixMemberByUser is the member whose Matrix id is userID.
func (s *Store) MatrixMemberByUser(ctx context.Context, userID string) (MatrixMember, bool, error) {
	return s.matrixBy(ctx, "user_id", userID)
}

// SetMatrixAccount records the member's MAS user id and Matrix id. Empty
// ids clear them: they are stored as NULL, since the Matrix id is unique and
// any number of members may have none.
func (s *Store) SetMatrixAccount(ctx context.Context, subject, masUserID, userID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO matrix_members (subject, mas_user_id, user_id) VALUES ($1, NULLIF($2, ''), NULLIF($3, ''))
		ON CONFLICT (subject) DO UPDATE SET mas_user_id=NULLIF($2, ''), user_id=NULLIF($3, ''), at=now()`,
		subject, masUserID, userID)
	return err
}

// SetMatrixRoom records the member's current room; an empty id clears it.
func (s *Store) SetMatrixRoom(ctx context.Context, subject, roomID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO matrix_members (subject, room_id) VALUES ($1, NULLIF($2, ''))
		ON CONFLICT (subject) DO UPDATE SET room_id=NULLIF($2, ''), at=now()`,
		subject, roomID)
	return err
}

// ForgetMatrixRoom clears the member's room, only if it is still roomID.
func (s *Store) ForgetMatrixRoom(ctx context.Context, subject, roomID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE matrix_members SET room_id=NULL, at=now() WHERE subject=$1 AND room_id=$2`,
		subject, roomID)
	return err
}

// SetMatrixEnabled turns the member's Matrix digest on or off. Turning it off
// for a member who has also unsubscribed from email forgets which threads
// they were shown.
func (s *Store) SetMatrixEnabled(ctx context.Context, subject string, on bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := setEnabled(ctx, tx, subject, on); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LeftMatrix records that the member left roomID: the digest is off and, if
// roomID is still their room, it is forgotten. One transaction.
func (s *Store) LeftMatrix(ctx context.Context, subject, roomID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := setEnabled(ctx, tx, subject, false); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE matrix_members SET room_id=NULL WHERE subject=$1 AND room_id=$2`,
		subject, roomID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func setEnabled(ctx context.Context, tx pgx.Tx, subject string, on bool) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO matrix_members (subject, enabled) VALUES ($1, $2)
		ON CONFLICT (subject) DO UPDATE SET enabled=$2, at=now()`,
		subject, on); err != nil {
		return err
	}
	if on {
		return nil
	}
	_, err := tx.Exec(ctx,
		`DELETE FROM shown WHERE subject=$1 AND EXISTS (SELECT 1 FROM unsubscribes WHERE subject=$1)`,
		subject)
	return err
}

// BotState is the value kept under key, and whether there is one.
func (s *Store) BotState(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM bot_state WHERE key=$1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) SetBotState(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO bot_state (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value=$2`,
		key, value)
	return err
}

func (s *Store) DeleteBotState(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM bot_state WHERE key=$1`, key)
	return err
}
