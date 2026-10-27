package matrix

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/matrix/matrixtest"
	"golang.org/x/time/rate"
)

const testToken = "syt_secret_token_value"

var fast = Pacing{1000, 1000, 1000, 1000}

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) fn(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d = append(s.d, d)
	return nil
}

func (s *sleeps) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

func newClient(t *testing.T, p Pacing) (*Client, *matrixtest.Server, *sleeps) {
	t.Helper()
	srv := matrixtest.New(t, "example.org")
	srv.Token = testToken
	srv.Device = "DIGESTBOT"
	c := New(srv.URL, srv.Client(), p)
	c.SetToken(testToken)
	sl := &sleeps{}
	c.sleep = sl.fn
	return c, srv, sl
}

func TestWhoAmI(t *testing.T) {
	c, srv, _ := newClient(t, fast)
	user, dev, err := c.WhoAmI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if user != "@digestbot:example.org" || dev != "DIGESTBOT" {
		t.Fatalf("got %q %q", user, dev)
	}
	if got := srv.LastAuthorization(); got != "Bearer "+testToken {
		t.Fatalf("authorization = %q", got)
	}
}

func TestUnknownToken(t *testing.T) {
	c, _, _ := newClient(t, fast)
	c.SetToken("wrong-token")
	_, _, err := c.WhoAmI(context.Background())
	if !IsCode(err, "M_UNKNOWN_TOKEN") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatal("a refusal is not unavailability")
	}
	if strings.Contains(err.Error(), "wrong-token") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("error carries the token: %v", err)
	}
}

func TestUnavailable(t *testing.T) {
	c, srv, _ := newClient(t, fast)
	srv.Down.Store(true)
	if _, _, err := c.WhoAmI(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("down: %v", err)
	}
	srv.Down.Store(false)
	srv.Close()
	if _, _, err := c.WhoAmI(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed: %v", err)
	}
}

func TestRateLimitedRetries(t *testing.T) {
	c, srv, sl := newClient(t, fast)
	srv.RateLimitNext(2, 1500, "")
	if _, _, err := c.WhoAmI(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{1500 * time.Millisecond, 1500 * time.Millisecond}
	got := sl.got()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sleeps = %v", got)
	}
}

func TestRateLimitedWithoutBodyHint(t *testing.T) {
	c, srv, sl := newClient(t, fast)
	srv.RateLimitNext(1, 0, "2")
	if _, _, err := c.WhoAmI(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.RateLimitNext(1, 0, "")
	if _, _, err := c.WhoAmI(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := sl.got()
	if len(got) != 2 || got[0] != 2*time.Second || got[1] != 5*time.Second {
		t.Fatalf("sleeps = %v", got)
	}
}

func TestRateLimitedStopsWithContext(t *testing.T) {
	c, srv, _ := newClient(t, fast)
	c.sleep = defaultSleep
	srv.RateLimitNext(1000, 60000, "")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := c.WhoAmI(ctx); err == nil {
		t.Fatal("expected the context to end the wait")
	}
}

func TestPacingSpacesSends(t *testing.T) {
	c, _, _ := newClient(t, Pacing{MessagesPerSecond: 20, MessageBurst: 1, RoomsPerSecond: 1000, RoomBurst: 1000})
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := c.do(context.Background(), "GET", "/_matrix/client/v3/account/whoami",
			[]*rate.Limiter{c.messages}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 100*time.Millisecond {
		t.Fatalf("three paced calls took %v", el)
	}
}

func TestSetDisplayNameIsNotPaced(t *testing.T) {
	c, srv, _ := newClient(t, Pacing{MessagesPerSecond: 0.001, MessageBurst: 1, RoomsPerSecond: 0.001, RoomBurst: 1})
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := c.SetDisplayName(context.Background(), "@digestbot:example.org", "Digest Bot"); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("profile writes were paced")
	}
	name, err := c.DisplayName(context.Background(), "@digestbot:example.org")
	if err != nil || name != "Digest Bot" || srv.Profile("@digestbot:example.org") != "Digest Bot" {
		t.Fatalf("name %q err %v", name, err)
	}
	if n, err := c.DisplayName(context.Background(), "@nobody:example.org"); err != nil || n != "" {
		t.Fatalf("missing profile: %q %v", n, err)
	}
}

func TestAuthMetadataNoToken(t *testing.T) {
	c, srv, _ := newClient(t, fast)
	srv.TokenEndpoint = "https://example.org/oauth2/token"
	c.SetToken("")
	ep, err := c.AuthMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ep != "https://example.org/oauth2/token" {
		t.Fatalf("endpoint = %q", ep)
	}
	if got := srv.LastAuthorization(); got != "" {
		t.Fatalf("authorization = %q", got)
	}
}
