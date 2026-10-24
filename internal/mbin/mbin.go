// Package mbin reads the week's threads and each member's activity from
// Mbin's database, through a role that may read only Allowlist. It never
// writes, and the role could not if it tried.
package mbin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/paisans-software/paisans-engagement-service/internal/digest"
)

// titleRunes is how much of a microblog post stands in for its title.
const titleRunes = 120

type Source struct {
	pool    *pgxpool.Pool
	siteURL string
}

// Open connects to Mbin's database. siteURL is the address members reach
// Mbin at, which every item's link starts with.
func Open(ctx context.Context, url, siteURL string) (*Source, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open mbin database: %w", err)
	}
	return newSource(pool, siteURL), nil
}

func newSource(pool *pgxpool.Pool, siteURL string) *Source {
	return &Source{pool: pool, siteURL: strings.TrimRight(siteURL, "/")}
}

func (s *Source) Close() { s.pool.Close() }

// Members is everyone who may receive the digest, by Mbin user id.
func (s *Source) Members(ctx context.Context) ([]digest.Member, error) {
	rows, err := s.pool.Query(ctx, membersSQL)
	if err != nil {
		return nil, fmt.Errorf("members: %w", err)
	}
	ms, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (digest.Member, error) {
		var m digest.Member
		err := r.Scan(&m.ID, &m.Subject, &m.Email, &m.Username)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("members: %w", err)
	}
	return ms, nil
}

// Hot is the week's candidate threads, best first by score plus comments.
// Per member rules (blocks, subscriptions, what they joined) are the digest
// package's, applied to View.
func (s *Source) Hot(ctx context.Context, from, to time.Time) ([]digest.Item, error) {
	rows, err := s.pool.Query(ctx, hotSQL, from, to)
	if err != nil {
		return nil, fmt.Errorf("hot: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (digest.Item, error) {
		var (
			it       digest.Item
			kind     string
			slug     *string
			text     *string
			comments []byte
			newTop   []byte
		)
		err := r.Scan(&kind, &it.Key.ID, &it.AuthorID, &slug, &text, &it.Score, &it.Comments, &it.Federated,
			&it.Magazine.ID, &it.Magazine.Name, &it.Magazine.Title, &it.Participants, &it.NewComments,
			&comments, &newTop)
		if err != nil {
			return it, err
		}
		it.Key.Kind = digest.Kind(kind)
		if text != nil {
			it.Title = *text
		}
		if it.Key.Kind == digest.KindPost {
			it.Title = digest.Plain(it.Title, titleRunes)
		} else {
			it.Title = oneLine(it.Title)
		}
		it.Magazine.Title = oneLine(it.Magazine.Title)
		it.URL = s.url(it, slug)
		if it.TopComments, err = topComments(comments); err != nil {
			return it, err
		}
		if it.TopNew, err = topComments(newTop); err != nil {
			return it, err
		}
		return it, nil
	})
	if err != nil {
		return nil, fmt.Errorf("hot: %w", err)
	}
	return items, nil
}

// topComments decodes a JSON list of comments from hotSQL; nil is none.
func topComments(b []byte) ([]digest.Comment, error) {
	if b == nil {
		return nil, nil
	}
	var top []struct {
		Author int64  `json:"author"`
		Body   string `json:"body"`
		Votes  int    `json:"votes"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, err
	}
	var out []digest.Comment
	for _, c := range top {
		out = append(out, digest.Comment{AuthorID: c.Author, Body: c.Body, Votes: c.Votes})
	}
	return out, nil
}

// oneLine collapses every run of whitespace, newlines included, to a single
// space, since a title with a line break would break the email's text part.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// url is the item's page on Mbin: /m/<magazine>/t/<id>/<slug> for an entry,
// /p/ for a post, with "-" standing in for a missing slug as Mbin does. A
// remote magazine's name already carries its @host.
func (s *Source) url(it digest.Item, slug *string) string {
	kind := "/t/"
	if it.Key.Kind == digest.KindPost {
		kind = "/p/"
	}
	sl := "-"
	if slug != nil && *slug != "" {
		sl = *slug
	}
	return s.siteURL + "/m/" + it.Magazine.Name + kind + strconv.FormatInt(it.Key.ID, 10) + "/" + sl
}

// View is the member's choices and, among items, what they already joined.
// Visited is true when the member's last visit is at or after from. Mbin keeps
// only the latest visit, so one after to also counts: nothing tells whether
// there was another inside the window.
func (s *Source) View(ctx context.Context, m digest.Member, from, to time.Time, items []digest.Item) (digest.View, error) {
	v := digest.View{
		Subscribed:       map[int64]bool{},
		BlockedMagazines: map[int64]bool{},
		BannedMagazines:  map[int64]bool{},
		BlockedUsers:     map[int64]bool{},
		Joined:           map[digest.Key]bool{},
	}
	rows, err := s.pool.Query(ctx, choicesSQL, m.ID, from)
	if err != nil {
		return v, fmt.Errorf("view of %d: %w", m.ID, err)
	}
	sets := map[string]map[int64]bool{
		"subscribed":       v.Subscribed,
		"blocked_magazine": v.BlockedMagazines,
		"banned":           v.BannedMagazines,
		"blocked_user":     v.BlockedUsers,
	}
	var (
		set string
		id  int64
	)
	_, err = pgx.ForEachRow(rows, []any{&set, &id}, func() error {
		if set == "visited" {
			v.Visited = true
		} else {
			sets[set][id] = true
		}
		return nil
	})
	if err != nil {
		return v, fmt.Errorf("view of %d: %w", m.ID, err)
	}

	if len(items) == 0 {
		return v, nil
	}
	entries, posts := []int64{}, []int64{}
	for _, it := range items {
		if it.Key.Kind == digest.KindPost {
			posts = append(posts, it.Key.ID)
		} else {
			entries = append(entries, it.Key.ID)
		}
	}
	rows, err = s.pool.Query(ctx, joinedSQL, m.ID, entries, posts)
	if err != nil {
		return v, fmt.Errorf("joined of %d: %w", m.ID, err)
	}
	var kind string
	_, err = pgx.ForEachRow(rows, []any{&kind, &id}, func() error {
		v.Joined[digest.Key{Kind: digest.Kind(kind), ID: id}] = true
		return nil
	})
	if err != nil {
		return v, fmt.Errorf("joined of %d: %w", m.ID, err)
	}
	return v, nil
}

// Stats is the week's local figures for the email's header.
func (s *Source) Stats(ctx context.Context, from, to time.Time) (digest.Stats, error) {
	var st digest.Stats
	if err := s.pool.QueryRow(ctx, statsSQL, from, to).Scan(&st.NewThreads, &st.Comments, &st.NewMembers); err != nil {
		return st, fmt.Errorf("stats: %w", err)
	}
	return st, nil
}
