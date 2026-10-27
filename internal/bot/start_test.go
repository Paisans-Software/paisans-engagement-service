package bot

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/mas"
	"github.com/paisans-software/paisans-engagement-service/internal/mas/mastest"
	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
	"github.com/paisans-software/paisans-engagement-service/internal/matrix/matrixtest"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

const (
	serverName = "example.org"
	provider   = "01PROVIDER"
	botUser    = "@digest-bot:example.org"
)

type env struct {
	mas *mastest.Server
	syn *matrixtest.Server
	st  *store.Store
	cfg Config
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{mas: mastest.New(t), syn: matrixtest.New(t, serverName), st: storetest.New(t),
		cfg: Config{ServerName: serverName, Provider: provider, Localpart: "digest-bot"}}
	e.mas.AddProvider(provider)
	e.syn.TokenEndpoint = e.mas.TokenURL()
	e.syn.Accept(func(token string) (string, string, bool) {
		for _, s := range e.mas.Sessions() {
			if s.Token == token {
				return botUser, "DIGESTBOT", true
			}
		}
		return "", "", false
	})
	return e
}

func (e *env) start(t *testing.T) (*Bot, error) {
	t.Helper()
	m := matrix.New(e.syn.URL, nil, matrix.Pacing{MessagesPerSecond: 1000, MessageBurst: 100, RoomsPerSecond: 1000, RoomBurst: 100})
	a := mas.New(e.mas.URL, e.mas.ClientID, e.mas.Secret, nil)
	return Start(context.Background(), m, a, e.st, nil, e.cfg)
}

func (e *env) state(t *testing.T, key string) string {
	t.Helper()
	v, _, err := e.st.BotState(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestStartCreatesBot(t *testing.T) {
	e := newEnv(t)
	b, err := e.start(t)
	if err != nil {
		t.Fatal(err)
	}
	if b.UserID() != botUser {
		t.Fatalf("UserID = %q", b.UserID())
	}
	users := e.mas.Users()
	if len(users) != 1 || users[0].Username != "digest-bot" {
		t.Fatalf("users = %+v", users)
	}
	ss := e.mas.Sessions()
	if len(ss) != 1 || ss[0].Scope != Scope || ss[0].HasExpiresIn || ss[0].UserID != users[0].ID {
		t.Fatalf("sessions = %+v", ss)
	}
	if got := e.syn.Profile(botUser); got != DisplayName {
		t.Fatalf("profile = %q", got)
	}
	for k, want := range map[string]string{"mas_user_id": users[0].ID, "localpart": "digest-bot",
		"session_id": ss[0].ID, "access_token": ss[0].Token} {
		if got := e.state(t, k); got != want {
			t.Errorf("state %s = %q, want %q", k, got, want)
		}
	}
	if _, ok, _ := e.st.BotState(context.Background(), "create_intent"); ok {
		t.Error("create_intent kept")
	}
}

// MAS serves its token endpoint on the public listener named in the
// homeserver's auth metadata, not on the admin listener, so the bot must
// learn the endpoint before its first admin call.
func TestStartTokenEndpointFromMetadata(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Post(e.mas.URL+"/oauth2/token", "application/x-www-form-urlencoded", strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin listener answered the token route with %d, want 404", resp.StatusCode)
	}
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	if e.mas.TokenRequests() == 0 {
		t.Fatal("no token was requested from the metadata's endpoint")
	}
}

func TestStartReusesEverything(t *testing.T) {
	e := newEnv(t)
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	if n := len(e.mas.Users()); n != 1 {
		t.Fatalf("users = %d", n)
	}
	if ss := e.mas.Sessions(); len(ss) != 1 || ss[0].Token != e.state(t, "access_token") {
		t.Fatalf("sessions = %+v", ss)
	}
	if n := e.syn.ProfileWrites(); n != 1 {
		t.Fatalf("profile writes = %d, want 1", n)
	}
}

func TestStartTakenLocalpart(t *testing.T) {
	e := newEnv(t)
	id := e.mas.AddUser("digest-bot", time.Now().Add(-time.Hour))
	e.mas.Link(provider, "sub-x", id)
	_, err := e.start(t)
	if !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
	for _, w := range []string{"digest-bot", "ENGAGEMENT_MATRIX_BOT_LOCALPART", "bot_localpart"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("message lacks %q: %v", w, err)
		}
	}
	if strings.Contains(err.Error(), "sub-x") {
		t.Errorf("message names a subject: %v", err)
	}
	if len(e.mas.Sessions()) != 0 {
		t.Fatal("session created")
	}
}

func TestStartTakenWithoutLinksNoIntent(t *testing.T) {
	e := newEnv(t)
	e.mas.AddUser("digest-bot", time.Now())
	if _, err := e.start(t); !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartRecoversLostCreate(t *testing.T) {
	e := newEnv(t)
	at := time.Now().UTC().Truncate(time.Second)
	if err := e.st.SetBotState(context.Background(), "create_intent", "digest-bot "+at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	id := e.mas.AddUser("digest-bot", at.Add(time.Second))
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	if got := e.state(t, "mas_user_id"); got != id {
		t.Fatalf("mas_user_id = %q, want %q", got, id)
	}
	if len(e.mas.Users()) != 1 {
		t.Fatal("created a second user")
	}
}

func TestStartIntentForOtherLocalpartDoesNotAdopt(t *testing.T) {
	e := newEnv(t)
	at := time.Now().UTC().Truncate(time.Second)
	_ = e.st.SetBotState(context.Background(), "create_intent", "other "+at.Format(time.RFC3339))
	e.mas.AddUser("digest-bot", at.Add(time.Second))
	if _, err := e.start(t); !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartUserOlderThanIntentNotAdopted(t *testing.T) {
	e := newEnv(t)
	at := time.Now().UTC().Truncate(time.Second)
	_ = e.st.SetBotState(context.Background(), "create_intent", "digest-bot "+at.Format(time.RFC3339))
	e.mas.AddUser("digest-bot", at.Add(-10*time.Minute))
	if _, err := e.start(t); !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartOldMAS(t *testing.T) {
	for _, v := range []string{"v1.4.1", "main"} {
		e := newEnv(t)
		e.mas.VersionString = v
		_, err := e.start(t)
		if !errors.Is(err, ErrOff) {
			t.Fatalf("%s: err = %v", v, err)
		}
		if !strings.Contains(err.Error(), v) || !strings.Contains(err.Error(), "1.5.0") {
			t.Errorf("%s: message = %v", v, err)
		}
		if len(e.mas.Users()) != 0 {
			t.Errorf("%s: user created", v)
		}
	}
}

func TestStartRenewsRefusedToken(t *testing.T) {
	e := newEnv(t)
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	sess := e.mas.Sessions()[0]
	if err := e.st.SetBotState(context.Background(), "access_token", "stale-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	ss := e.mas.Sessions()
	if len(ss) != 1 || ss[0].Token == sess.Token {
		t.Fatalf("sessions = %+v (token not regenerated)", ss)
	}
	if got := e.state(t, "access_token"); got != ss[0].Token {
		t.Fatalf("stored token = %q", got)
	}
}

func TestStartCreatesSessionWhenGone(t *testing.T) {
	e := newEnv(t)
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	old := e.mas.Sessions()[0]
	e.mas.RevokeSession(old.ID)
	_ = e.st.SetBotState(context.Background(), "access_token", "stale-token")
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	ss := e.mas.Sessions()
	if len(ss) != 2 || e.state(t, "session_id") != ss[1].ID || e.state(t, "access_token") != ss[1].Token {
		t.Fatalf("sessions = %+v", ss)
	}
}

func TestStartUnavailable(t *testing.T) {
	e := newEnv(t)
	e.mas.Down.Store(true)
	_, err := e.start(t)
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartHomeserverUnavailable(t *testing.T) {
	e := newEnv(t)
	e.syn.Down.Store(true)
	_, err := e.start(t)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestCallRenewsOnUnknownToken(t *testing.T) {
	e := newEnv(t)
	b, err := e.start(t)
	if err != nil {
		t.Fatal(err)
	}
	b.m.SetToken("stale")
	calls := 0
	err = b.call(context.Background(), func() error {
		calls++
		_, _, err := b.m.WhoAmI(context.Background())
		return err
	})
	if err != nil || calls != 2 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
	if e.state(t, "access_token") != e.mas.Sessions()[0].Token {
		t.Fatal("renewed token not stored")
	}
}
