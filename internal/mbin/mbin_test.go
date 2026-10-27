package mbin

import (
	"context"
	_ "embed"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/paisans-software/paisans-engagement-service/internal/digest"
)

//go:embed testdata/schema.sql
var schemaSQL string

const site = "https://forum.example.org"

var (
	to   = time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	from = to.AddDate(0, 0, -7)
	mid  = from.Add(72 * time.Hour)
	old  = from.AddDate(-1, 0, 0)
)

// fixture is one test's copy of the allowlisted schema: db writes rows behind
// the source's back, url reaches the same schema for a second pool.
type fixture struct {
	db     *pgx.Conn
	url    string
	schema string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := os.Getenv("ENGAGEMENT_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("ENGAGEMENT_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("mbin_%d", rand.Uint64())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	if _, err := db.Exec(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	return &fixture{db: db, url: u.String(), schema: schema}
}

// source opens a Source on the fixture's schema as its owner.
func (f *fixture) source(t *testing.T) *Source {
	t.Helper()
	s, err := Open(context.Background(), f.url, site+"/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

type row map[string]any

// insert writes one row, with fields overriding the defaults.
func (f *fixture) insert(t *testing.T, table string, defaults, fields row) {
	t.Helper()
	r := row{}
	for k, v := range defaults {
		r[k] = v
	}
	for k, v := range fields {
		r[k] = v
	}
	cols := make([]string, 0, len(r))
	for k := range r {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	args := make([]any, len(cols))
	marks := make([]string, len(cols))
	for i, c := range cols {
		args[i] = r[c]
		marks[i] = fmt.Sprintf("$%d", i+1)
		cols[i] = pgx.Identifier{c}.Sanitize()
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		pgx.Identifier{table}.Sanitize(), strings.Join(cols, ", "), strings.Join(marks, ", "))
	if _, err := f.db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("insert %s %v: %v", table, r, err)
	}
}

// user is a local member unless fields say otherwise.
func (f *fixture) user(t *testing.T, id int, fields row) {
	f.insert(t, "user", row{
		"id": id, "username": fmt.Sprintf("u%d", id), "email": fmt.Sprintf("u%d@example.org", id),
		"is_verified": true, "visibility": "visible", "type": "Person",
		"oauth_oidc_id": fmt.Sprintf("sub-%d", id), "created_at": old, "last_active": old,
	}, fields)
}

func (f *fixture) magazine(t *testing.T, id int, fields row) {
	f.insert(t, "magazine", row{
		"id": id, "name": fmt.Sprintf("mag%d", id), "title": fmt.Sprintf("Magazine %d", id),
	}, fields)
}

// entry is a visible local thread created and active inside the window.
func (f *fixture) entry(t *testing.T, id, author, mag int, fields row) {
	f.insert(t, "entry", row{
		"id": id, "user_id": author, "magazine_id": mag, "slug": fmt.Sprintf("entry-%d", id),
		"title": fmt.Sprintf("Entry %d", id), "created_at": mid, "last_active": mid,
	}, fields)
}

func (f *fixture) post(t *testing.T, id, author, mag int, fields row) {
	f.insert(t, "post", row{
		"id": id, "user_id": author, "magazine_id": mag, "slug": fmt.Sprintf("post-%d", id),
		"body": fmt.Sprintf("Post %d", id), "created_at": mid, "last_active": mid,
	}, fields)
}

func (f *fixture) entryComment(t *testing.T, id, author, entry, mag int, fields row) {
	f.insert(t, "entry_comment", row{
		"id": id, "user_id": author, "entry_id": entry, "magazine_id": mag,
		"body": fmt.Sprintf("Comment %d", id), "created_at": mid,
	}, fields)
}

func (f *fixture) postComment(t *testing.T, id, author, post, mag int, fields row) {
	f.insert(t, "post_comment", row{
		"id": id, "user_id": author, "post_id": post, "magazine_id": mag,
		"body": fmt.Sprintf("Reply %d", id), "created_at": mid,
	}, fields)
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func keys(items []digest.Item) []digest.Key {
	ks := make([]digest.Key, len(items))
	for i, it := range items {
		ks[i] = it.Key
	}
	return ks
}

func entryKey(id int64) digest.Key { return digest.Key{Kind: digest.KindEntry, ID: id} }
func postKey(id int64) digest.Key  { return digest.Key{Kind: digest.KindPost, ID: id} }

func TestAllowlistExcludesCredentials(t *testing.T) {
	tables := []string{"sessions", "rememberme_token", "oauth2_access_token", "oauth2_refresh_token",
		"oauth2_authorization_code", "oauth2_client", "oauth2_user_consent", "reset_password_request",
		"user_push_subscription", "site", "settings", "messenger_messages", "message", "message_thread",
		"user_note", "ap_activity", "activity"}
	columns := []string{"password", "totp_secret", "totp_backup_codes", "private_key", "old_private_key", "ip"}
	oauthID := regexp.MustCompile(`^oauth_.*_id$`)
	for _, tb := range tables {
		if _, ok := Allowlist[tb]; ok {
			t.Errorf("allowlist names credential table %q", tb)
		}
	}
	for tb, cols := range Allowlist {
		for _, c := range cols {
			if slices.Contains(columns, c) || (oauthID.MatchString(c) && c != "oauth_oidc_id") {
				t.Errorf("allowlist names credential column %s.%s", tb, c)
			}
		}
	}
}

// The fixture is only a fair test of the allowlist if it holds exactly the
// allowlisted columns.
func TestFixtureMatchesAllowlist(t *testing.T) {
	f := newFixture(t)
	rows, err := f.db.Query(context.Background(),
		`SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = $1`, f.schema)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for rows.Next() {
		var tb, c string
		if err := rows.Scan(&tb, &c); err != nil {
			t.Fatal(err)
		}
		got[tb] = append(got[tb], c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(Allowlist) {
		t.Errorf("fixture has %d tables, allowlist %d", len(got), len(Allowlist))
	}
	for tb, cols := range Allowlist {
		want := slices.Sorted(slices.Values(cols))
		have := slices.Sorted(slices.Values(got[tb]))
		if !slices.Equal(want, have) {
			t.Errorf("%s: fixture %v, allowlist %v", tb, have, want)
		}
	}
}

func TestQueriesStayInsideAllowlist(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	role := fmt.Sprintf("mbin_ro_%d", rand.Uint64())
	f.exec(t, "CREATE ROLE "+role)
	t.Cleanup(func() {
		// DROP OWNED revokes the grants, so the role can go before its schema.
		f.db.Exec(context.Background(), "DROP OWNED BY "+role)
		f.db.Exec(context.Background(), "DROP ROLE "+role)
	})
	f.exec(t, fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", f.schema, role))
	for tb, cols := range Allowlist {
		quoted := make([]string, len(cols))
		for i, c := range cols {
			quoted[i] = pgx.Identifier{c}.Sanitize()
		}
		f.exec(t, fmt.Sprintf("GRANT SELECT (%s) ON %s.%s TO %s",
			strings.Join(quoted, ", "), f.schema, pgx.Identifier{tb}.Sanitize(), role))
	}

	// Rows in every table, so no branch of a query is skipped as empty.
	f.user(t, 1, nil)
	f.user(t, 2, nil)
	f.magazine(t, 10, nil)
	f.magazine(t, 11, row{"name": "remote@remote.example.org", "ap_id": "https://remote.example.org/m/remote"})
	f.entry(t, 100, 1, 10, nil)
	f.entry(t, 101, 2, 11, row{"ap_id": "https://remote.example.org/t/1"})
	f.post(t, 200, 2, 10, nil)
	f.entryComment(t, 300, 2, 100, 10, nil)
	f.postComment(t, 400, 1, 200, 10, nil)
	f.exec(t, `INSERT INTO entry_vote VALUES (1, 100, 1)`)
	f.exec(t, `INSERT INTO post_vote VALUES (1, 200, 1)`)
	f.exec(t, `INSERT INTO favourite VALUES (1, 100, NULL)`)
	f.exec(t, `INSERT INTO bookmark VALUES (1, NULL, 200)`)
	f.exec(t, `INSERT INTO notification VALUES (1, 'read', NULL, 300, NULL, NULL)`)
	f.exec(t, `INSERT INTO magazine_subscription VALUES (1, 11)`)
	f.exec(t, `INSERT INTO magazine_block VALUES (1, 10)`)
	f.exec(t, `INSERT INTO magazine_ban VALUES (1, 10, NULL)`)
	f.exec(t, `INSERT INTO user_block VALUES (1, 2)`)

	cfg, err := pgxpool.ParseConfig(f.url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+role)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := newSource(pool, site)
	t.Cleanup(s.Close)

	// A sanity check that the role really is confined.
	if _, err := pool.Exec(ctx, `SELECT 1 FROM "user" WHERE created_at IS NULL AND id = 0 AND username = ''`); err != nil {
		t.Fatalf("allowlisted read refused: %v", err)
	}
	f.exec(t, `ALTER TABLE "user" ADD COLUMN password TEXT`)
	if _, err := pool.Exec(ctx, `SELECT password FROM "user"`); err == nil {
		t.Fatal("role read a column outside the allowlist")
	}

	members, err := s.Members(ctx)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	items, err := s.Hot(ctx, from, to)
	if err != nil {
		t.Fatalf("Hot: %v", err)
	}
	if len(members) == 0 || len(items) == 0 {
		t.Fatalf("expected rows, got %d members and %d items", len(members), len(items))
	}
	if _, err := s.View(ctx, members[0], from, to, items); err != nil {
		t.Fatalf("View: %v", err)
	}
	if _, err := s.Stats(ctx, from, to); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if _, err := s.IsMember(ctx, "sub-1"); err != nil {
		t.Fatalf("IsMember: %v", err)
	}
}

func TestMembersRule(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.user(t, 2, row{"ap_id": "https://remote.example.org/u/2"})
	f.user(t, 3, row{"is_verified": false})
	f.user(t, 4, row{"is_banned": true})
	f.user(t, 5, row{"is_deleted": true})
	f.user(t, 6, row{"marked_for_deletion_at": mid})
	f.user(t, 7, row{"type": "Service"})
	f.user(t, 8, row{"email": nil})
	f.user(t, 9, row{"oauth_oidc_id": nil})
	f.user(t, 10, row{"visibility": "trashed"})

	got, err := f.source(t).Members(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Users 3 and 8 are members who cannot be mailed: no verified address.
	want := []digest.Member{
		{ID: 1, Subject: "sub-1", Email: "u1@example.org", Username: "u1"},
		{ID: 3, Subject: "sub-3", Email: "", Username: "u3"},
		{ID: 8, Subject: "sub-8", Email: "", Username: "u8"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestIsMember(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.user(t, 2, row{"is_verified": false})
	f.user(t, 3, row{"email": nil})
	f.user(t, 4, row{"is_banned": true})
	s := f.source(t)
	// A member needs no address to be one; the Matrix digest reaches them.
	for _, c := range []struct {
		subject string
		want    bool
	}{{"sub-1", true}, {"sub-2", true}, {"sub-3", true}, {"sub-4", false}, {"nobody", false}} {
		got, err := s.IsMember(context.Background(), c.subject)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("IsMember(%q) = %v, want %v", c.subject, got, c.want)
		}
	}
}

func TestHotExcludes(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.user(t, 2, row{"is_banned": true})
	f.user(t, 3, row{"is_deleted": true})
	f.user(t, 4, row{"ap_id": "https://remote.example.org/u/4"})
	f.magazine(t, 10, nil)
	f.magazine(t, 11, row{"is_adult": true})
	f.magazine(t, 12, row{"ap_deleted_at": mid})
	f.magazine(t, 13, row{"marked_for_deletion_at": mid})
	f.magazine(t, 14, row{"visibility": "trashed"})
	f.magazine(t, 15, row{"name": "far@remote.example.org", "ap_id": "https://remote.example.org/m/far"})
	// Only a remote account follows magazine 15, which is not a local subscriber.
	f.exec(t, `INSERT INTO magazine_subscription VALUES (4, 15)`)

	f.entry(t, 1, 1, 10, nil) // the one that stays
	f.entry(t, 2, 1, 10, row{"is_adult": true})
	f.entry(t, 3, 1, 11, nil)
	f.entry(t, 4, 1, 10, row{"visibility": "trashed"})
	f.entry(t, 5, 1, 10, row{"visibility": "soft_deleted"})
	f.entry(t, 6, 1, 12, nil)
	f.entry(t, 7, 1, 13, nil)
	f.entry(t, 8, 1, 14, nil)
	f.entry(t, 9, 4, 15, row{"ap_id": "https://remote.example.org/t/9"})
	f.entry(t, 10, 1, 10, row{"created_at": old, "last_active": from.Add(-time.Second)})
	f.entry(t, 11, 1, 10, row{"created_at": to, "last_active": to})
	f.entry(t, 12, 2, 10, nil)
	f.entry(t, 13, 3, 10, nil)
	f.post(t, 1, 1, 10, nil) // stays
	f.post(t, 2, 1, 10, row{"is_adult": true})
	f.post(t, 3, 1, 10, row{"visibility": "private"})
	f.post(t, 4, 4, 15, row{"ap_id": "https://remote.example.org/p/4"})

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	want := []digest.Key{entryKey(1), postKey(1)}
	if ks := keys(got); !slices.Equal(ks, want) {
		t.Fatalf("got %v, want %v", ks, want)
	}
}

func TestHotFederatedWithSubscriber(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.user(t, 4, row{"ap_id": "https://remote.example.org/u/4"})
	f.magazine(t, 15, row{"name": "far@remote.example.org", "ap_id": "https://remote.example.org/m/far"})
	f.exec(t, `INSERT INTO magazine_subscription VALUES (1, 15)`)
	f.entry(t, 9, 4, 15, row{"ap_id": "https://remote.example.org/t/9", "score": 5})
	f.entryComment(t, 1, 1, 9, 15, row{"up_votes": 9})

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != entryKey(9) || !got[0].Federated {
		t.Fatalf("got %+v", got)
	}
	if got[0].TopComments != nil {
		t.Errorf("federated item carries comments: %+v", got[0].TopComments)
	}
}

// A member without a verified address is a member, so their subscription
// admits federated content too.
func TestHotFederatedWithUnverifiedSubscriber(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, row{"is_verified": false})
	f.user(t, 4, row{"ap_id": "https://remote.example.org/u/4"})
	f.magazine(t, 15, row{"name": "far@remote.example.org", "ap_id": "https://remote.example.org/m/far"})
	f.exec(t, `INSERT INTO magazine_subscription VALUES (1, 15)`)
	f.entry(t, 9, 4, 15, row{"ap_id": "https://remote.example.org/t/9", "score": 5})

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != entryKey(9) {
		t.Fatalf("got %v", keys(got))
	}
}

// Only a member's subscription admits federated content: a local account
// that is not one, here banned, follows the magazine in vain.
func TestHotFederatedWithIneligibleSubscriber(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, row{"is_banned": true})
	f.user(t, 4, row{"ap_id": "https://remote.example.org/u/4"})
	f.magazine(t, 15, row{"name": "far@remote.example.org", "ap_id": "https://remote.example.org/m/far"})
	f.exec(t, `INSERT INTO magazine_subscription VALUES (1, 15)`)
	f.entry(t, 9, 4, 15, row{"ap_id": "https://remote.example.org/t/9", "score": 5})

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v", keys(got))
	}
}

// An entry and a post with the same id and figures come entry first, so the
// order never depends on the plan.
func TestHotTieEntryBeforePost(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.magazine(t, 10, nil)
	f.post(t, 5, 1, 10, row{"score": 3, "comment_count": 1})
	f.entry(t, 5, 1, 10, row{"score": 3, "comment_count": 1})

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if ks, want := keys(got), []digest.Key{entryKey(5), postKey(5)}; !slices.Equal(ks, want) {
		t.Fatalf("got %v, want %v", ks, want)
	}
}

// A title, the item's or its magazine's, is one line of text: a newline in
// it would break the plain text part of the email.
func TestHotTitleIsOneLine(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.magazine(t, 10, row{"title": "Two\nLine Magazine"})
	f.entry(t, 1, 1, 10, row{"title": "  two\r\nlines\tand  gaps "})

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "two lines and gaps" || got[0].Magazine.Title != "Two Line Magazine" {
		t.Fatalf("got %+v", got)
	}
}

func TestHotURLs(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.magazine(t, 10, row{"name": "cooking", "title": "Cooking"})
	f.magazine(t, 15, row{"name": "foo@remote.example.org", "title": "Foo", "ap_id": "https://remote.example.org/m/foo"})
	f.exec(t, `INSERT INTO magazine_subscription VALUES (1, 15)`)
	f.entry(t, 7, 1, 10, row{"slug": "a-good-loaf", "title": "A good loaf", "score": 3})
	f.post(t, 8, 1, 10, row{"slug": nil, "body": "**Bread** is [here](https://example.org) now", "score": 2})
	f.entry(t, 9, 1, 15, row{"slug": "remote-thing", "ap_id": "https://remote.example.org/t/9", "score": 1})

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		key        digest.Key
		title, url string
		mag        digest.Magazine
	}{
		{entryKey(7), "A good loaf", site + "/m/cooking/t/7/a-good-loaf", digest.Magazine{ID: 10, Name: "cooking", Title: "Cooking"}},
		{postKey(8), "Bread is here now", site + "/m/cooking/p/8/-", digest.Magazine{ID: 10, Name: "cooking", Title: "Cooking"}},
		{entryKey(9), "Entry 9", site + "/m/foo@remote.example.org/t/9/remote-thing", digest.Magazine{ID: 15, Name: "foo@remote.example.org", Title: "Foo"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items: %+v", len(got), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Key != w.key || g.Title != w.title || g.URL != w.url || g.Magazine != w.mag || g.AuthorID != 1 {
			t.Errorf("item %d: got %+v, want %+v", i, g, w)
		}
	}
}

func TestHotParticipantsAndFigures(t *testing.T) {
	f := newFixture(t)
	for id := 1; id <= 4; id++ {
		f.user(t, id, nil)
	}
	f.magazine(t, 10, nil)
	f.entry(t, 1, 1, 10, row{"score": 4, "comment_count": 6})
	f.entryComment(t, 1, 2, 1, 10, nil)
	f.entryComment(t, 2, 2, 1, 10, nil)                          // same author again
	f.entryComment(t, 3, 1, 1, 10, nil)                          // the item's own author
	f.entryComment(t, 4, 3, 1, 10, row{"created_at": old})       // before the window
	f.entryComment(t, 5, 4, 1, 10, row{"visibility": "trashed"}) // removed
	f.post(t, 1, 1, 10, nil)
	f.postComment(t, 1, 3, 1, 10, nil)

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if e := got[0]; e.Key != entryKey(1) || e.Participants != 2 || e.Comments != 6 || e.Score != 4 || e.NewComments != 3 {
		t.Errorf("entry: %+v", e)
	}
	if p := got[1]; p.Key != postKey(1) || p.Participants != 2 || p.NewComments != 1 {
		t.Errorf("post: %+v", p)
	}
}

func TestHotCap(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.magazine(t, 10, nil)
	// 201 entries scored 1..201; the weakest falls off the end.
	f.exec(t, `INSERT INTO entry (id, user_id, magazine_id, title, score, created_at, last_active)
		SELECT g, 1, 10, 'Entry ' || g, g, $1, $1 FROM generate_series(1, 201) g`, mid)

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 200 {
		t.Fatalf("got %d items, want 200", len(got))
	}
	if got[0].Key != entryKey(201) || got[199].Key != entryKey(2) {
		t.Errorf("first %v, last %v", got[0].Key, got[199].Key)
	}
}

func TestTopCommentsLocalOnly(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.user(t, 2, nil)
	f.user(t, 4, row{"ap_id": "https://remote.example.org/u/4"})
	f.magazine(t, 10, nil)
	f.entry(t, 1, 1, 10, nil)
	f.entryComment(t, 1, 2, 1, 10, row{"up_votes": 1, "body": "one"})
	f.entryComment(t, 2, 2, 1, 10, row{"up_votes": 2, "favourite_count": 3, "body": "five"})
	f.entryComment(t, 3, 4, 1, 10, row{"up_votes": 50, "body": "remote"})
	f.entryComment(t, 4, 2, 1, 10, row{"up_votes": 40, "visibility": "trashed"})
	f.entryComment(t, 5, 2, 1, 10, row{"up_votes": 30, "is_adult": true})
	f.entryComment(t, 6, 1, 1, 10, row{"favourite_count": 3, "body": "three"})
	f.entryComment(t, 7, 2, 1, 10, row{"body": "zero"})
	f.post(t, 1, 1, 10, nil)
	f.postComment(t, 1, 2, 1, 10, row{"up_votes": 2, "body": "reply"})

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	want := []digest.Comment{{AuthorID: 2, Body: "five", Votes: 5}, {AuthorID: 1, Body: "three", Votes: 3}, {AuthorID: 2, Body: "one", Votes: 1}}
	if !slices.Equal(got[0].TopComments, want) {
		t.Errorf("entry: got %+v, want %+v", got[0].TopComments, want)
	}
	if want := []digest.Comment{{AuthorID: 2, Body: "reply", Votes: 2}}; !slices.Equal(got[1].TopComments, want) {
		t.Errorf("post: got %+v, want %+v", got[1].TopComments, want)
	}
}

func TestViewJoined(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, row{"last_active": mid})
	f.user(t, 2, nil)
	f.magazine(t, 10, nil)
	f.magazine(t, 11, nil)
	f.magazine(t, 12, nil)
	f.magazine(t, 13, nil)
	for id := 1; id <= 9; id++ {
		f.entry(t, id, 2, 10, nil)
		f.post(t, id, 2, 10, nil)
	}
	f.entry(t, 20, 1, 10, nil) // 20: authored
	f.post(t, 20, 1, 10, nil)
	f.entryComment(t, 1, 1, 1, 10, nil) // 1: commented
	f.postComment(t, 1, 1, 1, 10, nil)
	f.entryComment(t, 2, 2, 7, 10, nil) // 7: a read notification about this comment
	f.postComment(t, 2, 2, 7, 10, nil)
	f.entryComment(t, 3, 2, 8, 10, nil) // 8: someone else's comment only
	f.postComment(t, 3, 2, 8, 10, nil)
	f.exec(t, `INSERT INTO entry_vote VALUES (1, 2, 1), (1, 3, -1), (1, 8, 0), (2, 9, 1)`)
	f.exec(t, `INSERT INTO post_vote VALUES (1, 2, 1), (1, 3, -1), (1, 8, 0), (2, 9, 1)`)
	f.exec(t, `INSERT INTO favourite VALUES (1, 4, NULL), (1, NULL, 4)`)
	f.exec(t, `INSERT INTO bookmark VALUES (1, 5, NULL), (1, NULL, 5)`)
	f.exec(t, `INSERT INTO notification VALUES
		(1, 'read', 6, NULL, NULL, NULL), (1, 'read', NULL, NULL, 6, NULL),
		(1, 'read', NULL, 2, NULL, NULL), (1, 'read', NULL, NULL, NULL, 2),
		(1, 'new', 9, NULL, NULL, NULL), (1, 'new', NULL, NULL, 9, NULL)`)
	f.exec(t, `INSERT INTO magazine_subscription VALUES (1, 11), (2, 12)`)
	f.exec(t, `INSERT INTO magazine_block VALUES (1, 12)`)
	f.exec(t, `INSERT INTO magazine_ban VALUES (1, 13, NULL)`)
	f.exec(t, `INSERT INTO user_block VALUES (1, 2), (2, 1)`)

	s := f.source(t)
	ctx := context.Background()
	items, err := s.Hot(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 20 {
		t.Fatalf("got %d items", len(items))
	}
	v, err := s.View(ctx, digest.Member{ID: 1, Subject: "sub-1"}, from, to, items)
	if err != nil {
		t.Fatal(err)
	}
	var joined []digest.Key
	for _, id := range []int64{20, 1, 2, 3, 4, 5, 6, 7} {
		joined = append(joined, entryKey(id), postKey(id))
	}
	for _, k := range joined {
		if !v.Joined[k] {
			t.Errorf("%v not joined", k)
		}
	}
	if len(v.Joined) != len(joined) {
		t.Errorf("joined %v, want exactly %v", v.Joined, joined)
	}
	if !v.Visited {
		t.Error("not visited")
	}
	if want := map[int64]bool{11: true}; !mapsEqual(v.Subscribed, want) {
		t.Errorf("subscribed %v", v.Subscribed)
	}
	if want := map[int64]bool{12: true}; !mapsEqual(v.BlockedMagazines, want) {
		t.Errorf("blocked magazines %v", v.BlockedMagazines)
	}
	if want := map[int64]bool{13: true}; !mapsEqual(v.BannedMagazines, want) {
		t.Errorf("banned %v", v.BannedMagazines)
	}
	if want := map[int64]bool{2: true}; !mapsEqual(v.BlockedUsers, want) {
		t.Errorf("blocked users %v", v.BlockedUsers)
	}

	// Only the items asked about are reported.
	v, err = s.View(ctx, digest.Member{ID: 1}, from, to, items[:0])
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Joined) != 0 {
		t.Errorf("joined with no items: %v", v.Joined)
	}

	// Member 2 was last here a year ago.
	v, err = s.View(ctx, digest.Member{ID: 2}, from, to, items)
	if err != nil {
		t.Fatal(err)
	}
	if v.Visited {
		t.Error("member 2 visited")
	}
}

func TestViewExpiredBanIgnored(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.exec(t, `INSERT INTO magazine_ban VALUES (1, 10, $1), (1, 11, now() + interval '1 day'), (1, 12, NULL)`,
		time.Now().Add(-time.Hour))
	v, err := f.source(t).View(context.Background(), digest.Member{ID: 1}, from, to, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int64]bool{11: true, 12: true}; !mapsEqual(v.BannedMagazines, want) {
		t.Fatalf("banned %v", v.BannedMagazines)
	}
}

func TestStatsLocalOnly(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.user(t, 2, row{"created_at": mid})
	f.user(t, 3, row{"created_at": mid, "ap_id": "https://remote.example.org/u/3"})
	f.user(t, 4, row{"created_at": mid, "is_verified": false}) // a member, though not mailable
	f.user(t, 5, row{"created_at": to})
	f.magazine(t, 10, nil)
	f.entry(t, 1, 1, 10, nil)
	f.entry(t, 2, 3, 10, row{"ap_id": "https://remote.example.org/t/2"})
	f.entry(t, 3, 1, 10, row{"created_at": old})
	f.entry(t, 4, 1, 10, row{"visibility": "trashed"})
	f.post(t, 1, 1, 10, nil)
	f.post(t, 2, 1, 10, row{"created_at": to})
	f.post(t, 3, 3, 10, row{"ap_id": "https://remote.example.org/p/3"})
	f.entryComment(t, 1, 1, 3, 10, nil) // on an old thread, still this week's comment
	f.entryComment(t, 2, 3, 1, 10, nil) // remote author
	f.entryComment(t, 3, 2, 1, 10, row{"created_at": old})
	f.postComment(t, 1, 2, 1, 10, nil)
	f.postComment(t, 2, 3, 1, 10, nil)

	got, err := f.source(t).Stats(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if want := (digest.Stats{NewThreads: 2, Comments: 2, NewMembers: 2}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func mapsEqual(a, b map[int64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}

// The week's top comments leave out older ones however well liked, and a
// federated item has none.
func TestTopNewThisWeekOnly(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.user(t, 2, nil)
	f.magazine(t, 10, nil)
	f.magazine(t, 11, row{"name": "remote@remote.example.org", "ap_id": "https://remote.example.org/m/remote"})
	f.exec(t, `INSERT INTO magazine_subscription VALUES (1, 11)`)
	f.entry(t, 1, 1, 10, nil)
	f.entryComment(t, 1, 2, 1, 10, row{"up_votes": 90, "body": "before", "created_at": old})
	f.entryComment(t, 2, 2, 1, 10, row{"up_votes": 2, "body": "this week"})
	f.entryComment(t, 3, 2, 1, 10, row{"up_votes": 9, "body": "after", "created_at": to})
	f.entry(t, 2, 2, 11, row{"ap_id": "https://remote.example.org/t/2"})
	f.entryComment(t, 4, 2, 2, 11, row{"up_votes": 5, "body": "remote thread"})

	got, err := f.source(t).Hot(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	local, remote := got[0], got[1]
	if local.Key != entryKey(1) {
		local, remote = remote, local
	}
	if want := []digest.Comment{{AuthorID: 2, Body: "this week", Votes: 2}}; !slices.Equal(local.TopNew, want) {
		t.Errorf("local: got %+v, want %+v", local.TopNew, want)
	}
	if len(local.TopComments) != 3 || local.NewComments != 1 {
		t.Errorf("local: %+v", local)
	}
	if remote.TopNew != nil || remote.NewComments != 1 {
		t.Errorf("remote: %+v", remote)
	}
}
