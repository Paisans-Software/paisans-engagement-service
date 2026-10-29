package mbin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/paisans-software/paisans-engagement-service/internal/digest"
)

// Newcomer is a member who joined recently.
type Newcomer struct {
	ID                int64
	Subject, Username string
	CreatedAt         time.Time
}

// Newcomers is every approved member created at or after since, newest
// first, at most 500.
func (s *Source) Newcomers(ctx context.Context, since time.Time) ([]Newcomer, error) {
	rows, err := s.pool.Query(ctx, newcomersSQL, since)
	if err != nil {
		return nil, fmt.Errorf("newcomers: %w", err)
	}
	ns, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Newcomer, error) {
		var n Newcomer
		err := r.Scan(&n.ID, &n.Subject, &n.Username, &n.CreatedAt)
		return n, err
	})
	if err != nil {
		return nil, fmt.Errorf("newcomers: %w", err)
	}
	return ns, nil
}

// Ranked is a member with a verified address and how active they were.
type Ranked struct {
	digest.Member
	Score int
}

// Active is every member with a verified address, most active since since
// first, then by id.
func (s *Source) Active(ctx context.Context, since time.Time) ([]Ranked, error) {
	rows, err := s.pool.Query(ctx, activeSQL, since)
	if err != nil {
		return nil, fmt.Errorf("active: %w", err)
	}
	rs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Ranked, error) {
		var m Ranked
		err := r.Scan(&m.ID, &m.Subject, &m.Email, &m.Username, &m.Score)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("active: %w", err)
	}
	return rs, nil
}

// Commenters is, for each of the threads, the members who left a visible
// comment on it.
func (s *Source) Commenters(ctx context.Context, entryIDs []int64) (map[int64]map[int64]bool, error) {
	got := map[int64]map[int64]bool{}
	if len(entryIDs) == 0 {
		return got, nil
	}
	rows, err := s.pool.Query(ctx, commentersSQL, entryIDs)
	if err != nil {
		return nil, fmt.Errorf("commenters: %w", err)
	}
	var entry, user int64
	_, err = pgx.ForEachRow(rows, []any{&entry, &user}, func() error {
		if got[entry] == nil {
			got[entry] = map[int64]bool{}
		}
		got[entry][user] = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("commenters: %w", err)
	}
	return got, nil
}

// SubjectsByEmail is the subject of each member whose address is among
// emails, keyed by the address in lower case.
func (s *Source) SubjectsByEmail(ctx context.Context, emails []string) (map[string]string, error) {
	lower := make([]string, len(emails))
	for i, e := range emails {
		lower[i] = strings.ToLower(e)
	}
	rows, err := s.pool.Query(ctx, subjectsByEmailSQL, lower)
	if err != nil {
		return nil, fmt.Errorf("subjects by email: %w", err)
	}
	got := map[string]string{}
	var email, subject string
	_, err = pgx.ForEachRow(rows, []any{&email, &subject}, func() error { got[email] = subject; return nil })
	if err != nil {
		return nil, fmt.Errorf("subjects by email: %w", err)
	}
	return got, nil
}
