package mas_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/mas"
	"github.com/paisans-software/paisans-engagement-service/internal/mas/mastest"
)

func newClient(t *testing.T) (*mas.Client, *mastest.Server) {
	t.Helper()
	s := mastest.New(t)
	c := mas.New(s.URL, s.ClientID, s.Secret, nil)
	c.SetTokenEndpoint(s.TokenURL())
	return c, s
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want mas.Version
		ok   bool
	}{
		{"v1.28.0", mas.Version{1, 28, 0}, true},
		{"1.28.0", mas.Version{1, 28, 0}, true},
		{"v1.28.0-3-gabc", mas.Version{1, 28, 0}, true},
		{"v0.0.0-test", mas.Version{0, 0, 0}, true},
		{"", mas.Version{}, false},
		{"main", mas.Version{}, false},
	}
	for _, tc := range cases {
		got, ok := mas.ParseVersion(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("ParseVersion(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if v, _ := mas.ParseVersion("v0.0.0-test"); !v.Less(mas.MinVersion) {
		t.Error("v0.0.0-test must be older than the floor")
	}
}

func TestVersionFloor(t *testing.T) {
	for in, less := range map[string]bool{"v1.4.1": true, "v1.5.0": false, "v1.28.0": false} {
		v, ok := mas.ParseVersion(in)
		if !ok || v.Less(mas.MinVersion) != less {
			t.Errorf("%s: Less(MinVersion) = %v, want %v", in, v.Less(mas.MinVersion), less)
		}
	}
}

func TestVersionCall(t *testing.T) {
	c, s := newClient(t)
	s.VersionString = "v1.28.0-3-gabc"
	got, err := c.Version(context.Background())
	if err != nil || got != "v1.28.0-3-gabc" {
		t.Fatalf("Version = %q, %v", got, err)
	}
}

func TestClientCredentials(t *testing.T) {
	c, s := newClient(t)
	ctx := context.Background()
	if _, err := c.Version(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Version(ctx); err != nil {
		t.Fatal(err)
	}
	if n := s.TokenRequests(); n != 1 {
		t.Fatalf("TokenRequests = %d, want 1", n)
	}
	s.ExpireTokens()
	if _, err := c.Version(ctx); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if n := s.TokenRequests(); n != 2 {
		t.Fatalf("TokenRequests = %d, want 2", n)
	}
}

func TestLinkedUserID(t *testing.T) {
	c, s := newClient(t)
	ctx := context.Background()
	uid := s.AddUser("alice", time.Now())
	s.Link("prov1", "sub-linked", uid)
	s.Link("prov1", "sub-null", "")

	got, ok, err := c.LinkedUserID(ctx, "prov1", "sub-linked")
	if err != nil || !ok || got != uid {
		t.Fatalf("linked = %q %v %v", got, ok, err)
	}
	q := s.LastQuery()
	want := map[string]bool{"filter[provider]": true, "filter[subject]": true, "page[first]": true}
	vals, _ := url.ParseQuery(q)
	if len(vals) != len(want) {
		t.Fatalf("query keys = %v", vals)
	}
	for k := range vals {
		if !want[k] {
			t.Errorf("unexpected key %q", k)
		}
	}
	if _, ok, err := c.LinkedUserID(ctx, "prov1", "nobody"); err != nil || ok {
		t.Fatalf("unlinked = %v %v", ok, err)
	}
	if _, ok, err := c.LinkedUserID(ctx, "prov1", "sub-null"); err != nil || ok {
		t.Fatalf("null user = %v %v", ok, err)
	}
}

func TestLinkedSubjectAndHasLinks(t *testing.T) {
	c, s := newClient(t)
	ctx := context.Background()
	uid := s.AddUser("bob", time.Now())
	other := s.AddUser("carol", time.Now())
	s.Link("prov1", "sub-bob", uid)

	sub, ok, err := c.LinkedSubject(ctx, "prov1", uid)
	if err != nil || !ok || sub != "sub-bob" {
		t.Fatalf("LinkedSubject = %q %v %v", sub, ok, err)
	}
	if _, ok, _ := c.LinkedSubject(ctx, "prov1", other); ok {
		t.Fatal("carol has no link")
	}
	if has, err := c.HasLinks(ctx, uid); err != nil || !has {
		t.Fatalf("HasLinks bob = %v %v", has, err)
	}
	if has, err := c.HasLinks(ctx, other); err != nil || has {
		t.Fatalf("HasLinks carol = %v %v", has, err)
	}
}

func TestUsers(t *testing.T) {
	c, s := newClient(t)
	ctx := context.Background()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	id := s.AddUser("dave", created)
	u, ok, err := c.User(ctx, id)
	if err != nil || !ok || u.Username != "dave" || u.ID != id || !u.CreatedAt.Equal(created) || u.Deactivated || u.Locked {
		t.Fatalf("User = %+v %v %v", u, ok, err)
	}
	s.Deactivate(id)
	u, _, _ = c.UserByUsername(ctx, "dave")
	if !u.Deactivated {
		t.Fatal("expected deactivated")
	}
	if _, ok, err := c.User(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing = %v %v", ok, err)
	}
	if _, ok, err := c.UserByUsername(ctx, "nobody"); err != nil || ok {
		t.Fatalf("missing by name = %v %v", ok, err)
	}
}

func TestCreateUserTaken(t *testing.T) {
	c, s := newClient(t)
	ctx := context.Background()
	s.AddUser("digestbot", time.Now())
	if _, err := c.CreateUser(ctx, "digestbot"); !errors.Is(err, mas.ErrTaken) {
		t.Fatalf("err = %v, want ErrTaken", err)
	}
	u, err := c.CreateUser(ctx, "freshbot")
	if err != nil || u.Username != "freshbot" || u.ID == "" {
		t.Fatalf("CreateUser = %+v %v", u, err)
	}
}

func TestCreateSessionScope(t *testing.T) {
	c, s := newClient(t)
	ctx := context.Background()
	id := s.AddUser("digestbot", time.Now())
	scope := "urn:matrix:client:api:* urn:matrix:client:device:DIGESTBOT"
	sess, err := c.CreateSession(ctx, id, "Digest Bot", scope)
	if err != nil || sess.Token == "" || sess.ID == "" {
		t.Fatalf("CreateSession = %+v %v", sess, err)
	}
	recs := s.Sessions()
	if len(recs) != 1 || recs[0].Scope != scope || recs[0].UserID != id || recs[0].HumanName != "Digest Bot" || recs[0].HasExpiresIn {
		t.Fatalf("recorded = %+v", recs)
	}
	tok, err := c.RegenerateSession(ctx, sess.ID)
	if err != nil || tok == "" || tok == sess.Token {
		t.Fatalf("Regenerate = %q %v", tok, err)
	}
}

func TestRegenerateGone(t *testing.T) {
	c, s := newClient(t)
	ctx := context.Background()
	id := s.AddUser("digestbot", time.Now())
	sess, _ := c.CreateSession(ctx, id, "Digest Bot", "urn:matrix:client:api:*")
	s.RevokeSession(sess.ID)
	if _, err := c.RegenerateSession(ctx, sess.ID); !errors.Is(err, mas.ErrSessionGone) {
		t.Fatalf("err = %v, want ErrSessionGone", err)
	}
	if _, err := c.RegenerateSession(ctx, "unknown"); !errors.Is(err, mas.ErrSessionGone) {
		t.Fatalf("unknown: err = %v", err)
	}
}

func TestUnavailableHidesSecret(t *testing.T) {
	c, s := newClient(t)
	ctx := context.Background()
	if _, err := c.Version(ctx); err != nil {
		t.Fatal(err)
	}
	s.Down.Store(true)
	_, _, err := c.LinkedUserID(ctx, "prov1", "secret-subject")
	if !errors.Is(err, mas.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	s.ExpireTokens()
	_, err2 := c.Version(ctx) // token endpoint down too
	if !errors.Is(err2, mas.ErrUnavailable) {
		t.Fatalf("err2 = %v", err2)
	}
	for _, e := range []error{err, err2} {
		for _, bad := range []string{s.Secret, "secret-subject", "mas_tok_"} {
			if strings.Contains(e.Error(), bad) {
				t.Errorf("error %q leaks %q", e, bad)
			}
		}
	}
}

func TestNetworkErrorIsUnavailable(t *testing.T) {
	s := mastest.New(t)
	c := mas.New(s.URL, s.ClientID, s.Secret, nil)
	c.SetTokenEndpoint("http://127.0.0.1:1/oauth2/token")
	if _, err := c.Version(context.Background()); !errors.Is(err, mas.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
