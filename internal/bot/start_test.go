package bot

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
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
	botUser    = "@community-bot:example.org"
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
		cfg: Config{ServerName: serverName, Provider: provider, Localpart: "community-bot", DisplayName: "Community Bot"}}
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
	if len(users) != 1 || users[0].Username != "community-bot" {
		t.Fatalf("users = %+v", users)
	}
	ss := e.mas.Sessions()
	if len(ss) != 1 || ss[0].Scope != Scope || ss[0].HasExpiresIn || ss[0].UserID != users[0].ID {
		t.Fatalf("sessions = %+v", ss)
	}
	if got := e.syn.Profile(botUser); got != "Community Bot" {
		t.Fatalf("profile = %q", got)
	}
	if ss[0].HumanName != "Community Bot" {
		t.Fatalf("session name = %q", ss[0].HumanName)
	}
	for k, want := range map[string]string{"mas_user_id": users[0].ID, "localpart": "community-bot",
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

// A bot started under another name, such as its old default, is renamed to
// the configured one at the next start.
func TestStartRenamesBot(t *testing.T) {
	e := newEnv(t)
	b, err := e.start(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.m.SetDisplayName(context.Background(), botUser, "Digest Bot"); err != nil {
		t.Fatal(err)
	}
	e.cfg.DisplayName = "Paisans Bot"
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	if got := e.syn.Profile(botUser); got != "Paisans Bot" {
		t.Fatalf("profile = %q", got)
	}
	if n := e.syn.ProfileWrites(); n != 3 {
		t.Fatalf("profile writes = %d, want 3", n)
	}
}

// A new localpart makes a new bot: its own account and token, no sync
// position and no member rooms, which belonged to the old bot. Members'
// settings and the week's sends are kept.
func TestStartNewLocalpart(t *testing.T) {
	e := newEnv(t)
	e.syn.Accept(func(token string) (string, string, bool) {
		for _, s := range e.mas.Sessions() {
			for _, u := range e.mas.Users() {
				if s.Token == token && u.ID == s.UserID {
					return "@" + u.Username + ":" + serverName, "DIGESTBOT", true
				}
			}
		}
		return "", "", false
	})
	ctx := context.Background()
	e.cfg.Localpart = "digest-bot"
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	old := map[string]string{"mas_user_id": e.state(t, "mas_user_id"), "session_id": e.state(t, "session_id"),
		"access_token": e.state(t, "access_token")}
	week := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, err := range []error{
		e.st.SetBotState(ctx, keySyncToken, "s1"),
		e.st.SetMatrixAccount(ctx, "sub-1", "01ONE", "@one:example.org"),
		e.st.SetMatrixRoom(ctx, "sub-1", "!dm1:example.org"),
		e.st.SetMatrixRoom(ctx, "sub-2", "!dm2:example.org"),
		e.st.SetMatrixEnabled(ctx, "sub-2", false),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := e.st.ClaimDM(ctx, "sub-1", week); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := e.st.FinishDM(ctx, "sub-1", week, store.StatusSent); err != nil {
		t.Fatal(err)
	}

	e.cfg.Localpart = "community-bot"
	b, err := e.start(t)
	if err != nil {
		t.Fatal(err)
	}
	if b.UserID() != "@community-bot:example.org" {
		t.Fatalf("UserID = %q", b.UserID())
	}
	if e.state(t, "localpart") != "community-bot" {
		t.Fatalf("localpart = %q", e.state(t, "localpart"))
	}
	for k, v := range old {
		if e.state(t, k) == v {
			t.Errorf("state %s kept the old bot's %q", k, v)
		}
	}
	if user, _, err := b.m.WhoAmI(ctx); err != nil || user != "@community-bot:example.org" {
		t.Fatalf("whoami = %q, %v", user, err)
	}
	if _, ok, _ := e.st.BotState(ctx, keySyncToken); ok {
		t.Error("the old bot's sync token was kept")
	}
	m1, _ := e.st.MatrixMember(ctx, "sub-1")
	m2, _ := e.st.MatrixMember(ctx, "sub-2")
	if m1.RoomID != "" || m2.RoomID != "" {
		t.Errorf("rooms kept: %q %q", m1.RoomID, m2.RoomID)
	}
	if !m1.Enabled || m2.Enabled || m1.UserID != "@one:example.org" {
		t.Errorf("members changed: %+v %+v", m1, m2)
	}
	if ok, err := e.st.ClaimDM(ctx, "sub-1", week); ok || err != nil {
		t.Errorf("the week's send was forgotten: %v %v", ok, err)
	}
}

// A first start, or a restart under the same localpart, keeps the rooms.
func TestStartSameLocalpartKeepsRooms(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.st.SetMatrixRoom(ctx, "sub-1", "!dm1:example.org"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := e.start(t); err != nil {
			t.Fatal(err)
		}
	}
	if m, _ := e.st.MatrixMember(ctx, "sub-1"); m.RoomID != "!dm1:example.org" {
		t.Fatalf("room = %q", m.RoomID)
	}
}

func TestStartTakenLocalpart(t *testing.T) {
	e := newEnv(t)
	id := e.mas.AddUser("community-bot", time.Now().Add(-time.Hour))
	e.mas.Link(provider, "sub-x", id)
	_, err := e.start(t)
	if !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
	for _, w := range []string{"community-bot", "ENGAGEMENT_MATRIX_BOT_LOCALPART", "bot_localpart"} {
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
	e.mas.AddUser("community-bot", time.Now())
	if _, err := e.start(t); !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartRecoversLostCreate(t *testing.T) {
	e := newEnv(t)
	at := time.Now().UTC().Truncate(time.Second)
	if err := e.st.SetBotState(context.Background(), "create_intent", "community-bot "+at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	id := e.mas.AddUser("community-bot", at.Add(time.Second))
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
	e.mas.AddUser("community-bot", at.Add(time.Second))
	if _, err := e.start(t); !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartUserOlderThanIntentNotAdopted(t *testing.T) {
	e := newEnv(t)
	at := time.Now().UTC().Truncate(time.Second)
	_ = e.st.SetBotState(context.Background(), "create_intent", "community-bot "+at.Format(time.RFC3339))
	e.mas.AddUser("community-bot", at.Add(-10*time.Minute))
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

// A name taken between the lookup and the create is refused like any taken
// name, and leaves no intent behind that could adopt the other account later.
func TestStartTakenRace(t *testing.T) {
	e := newEnv(t)
	e.mas.AddUser("community-bot", time.Now())
	e.mas.FailUsernames.Store(404)
	if _, err := e.start(t); !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
	if _, ok, _ := e.st.BotState(context.Background(), "create_intent"); ok {
		t.Fatal("create_intent kept after a taken name")
	}
}

// A stored account MAS no longer has is replaced by a new one under the
// same name.
func TestStartStoredAccountMissing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for k, v := range map[string]string{"mas_user_id": "01MISSING", "localpart": "community-bot",
		"session_id": "01OLDSESSION", "access_token": "old-token"} {
		if err := e.st.SetBotState(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	users := e.mas.Users()
	if len(users) != 1 || e.state(t, "mas_user_id") != users[0].ID {
		t.Fatalf("users=%+v state=%q", users, e.state(t, "mas_user_id"))
	}
	if ss := e.mas.Sessions(); len(ss) != 1 || e.state(t, "session_id") != ss[0].ID {
		t.Fatalf("sessions=%+v", ss)
	}
}

// A stored account that MAS deactivated is not ours to use, and the name
// stays taken by it.
func TestStartStoredAccountDeactivated(t *testing.T) {
	e := newEnv(t)
	if _, err := e.start(t); err != nil {
		t.Fatal(err)
	}
	e.mas.Deactivate(e.mas.Users()[0].ID)
	if _, err := e.start(t); !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v", err)
	}
	if n := len(e.mas.Users()); n != 1 {
		t.Fatalf("users = %d", n)
	}
}

// A stored token that works but belongs to another user or device is
// replaced.
func TestStartTokenForWrongDevice(t *testing.T) {
	for name, who := range map[string][2]string{
		"device": {botUser, "OTHER"},
		"user":   {"@someone:example.org", "DIGESTBOT"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if _, err := e.start(t); err != nil {
				t.Fatal(err)
			}
			old := e.state(t, "access_token")
			e.syn.Accept(func(token string) (string, string, bool) {
				if token == old {
					return who[0], who[1], true
				}
				for _, s := range e.mas.Sessions() {
					if s.Token == token {
						return botUser, "DIGESTBOT", true
					}
				}
				return "", "", false
			})
			if _, err := e.start(t); err != nil {
				t.Fatal(err)
			}
			if got := e.state(t, "access_token"); got == old || got != e.mas.Sessions()[0].Token {
				t.Fatalf("token %q, old %q", got, old)
			}
		})
	}
}

// Callers refused for the same stale token renew it once between them.
func TestConcurrentRefusalsRenewOnce(t *testing.T) {
	e, b := started(t)
	ctx := context.Background()
	before := e.mas.Sessions()[0].Token
	e.syn.Accept(func(token string) (string, string, bool) {
		for _, s := range e.mas.Sessions() {
			if s.Token == token && token != before {
				return botUser, "DIGESTBOT", true
			}
		}
		return "", "", false
	})
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = b.call(ctx, func() error { _, _, err := b.m.WhoAmI(ctx); return err })
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if got := e.mas.Sessions()[0].Token; got != before+"r" {
		t.Fatalf("token %q after renewal from %q", got, before)
	}
}
