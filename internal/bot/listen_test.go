package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/store/storetest"
)

func TestCommandParsing(t *testing.T) {
	for in, want := range map[string]string{
		"stop": "stop", "STOP!": "stop", "  Start. ": "start", "\"resume\"": "start", "Restore": "start",
		"stop please": "", "🛑": "", "": "", "start?!stop": "",
	} {
		if got := Command(in); got != want {
			t.Errorf("Command(%q) = %q, want %q", in, got, want)
		}
	}
}

type checker map[string]bool

func (c checker) IsMember(_ context.Context, subject string) (bool, error) { return c[subject], nil }

// message is one m.room.message timeline event.
func message(id, sender, msgtype, body string) string {
	c, _ := json.Marshal(map[string]string{"msgtype": msgtype, "body": body})
	return fmt.Sprintf(`{"type":"m.room.message","event_id":%q,"sender":%q,"content":%s}`, id, sender, c)
}

func memberEvent(id, sender, key, membership string) string {
	return fmt.Sprintf(`{"type":"m.room.member","event_id":%q,"sender":%q,"state_key":%q,"content":{"membership":%q}}`,
		id, sender, key, membership)
}

func joined(room string, events ...string) string {
	return fmt.Sprintf(`{"rooms":{"join":{%q:{"timeline":{"events":[%s]}}}}}`, room, strings.Join(events, ","))
}

func invited(room, inviter string) string {
	return fmt.Sprintf(`{"rooms":{"invite":{%q:{"invite_state":{"events":[`+
		`{"type":"m.room.create","sender":%q,"state_key":"","content":{}},`+
		`{"type":"m.room.member","sender":%q,"state_key":%q,"content":{"membership":"invite"}}]}}}}}`,
		room, inviter, inviter, botUser)
}

// memberRoom starts a bot for a linked member with a room the fake knows.
func memberRoom(t *testing.T) (*env, *Bot, string) {
	t.Helper()
	e, b := started(t)
	b.members = checker{"sub-1": true}
	ctx := context.Background()
	id := link(e, "sub-1", "one")
	room := "!dm:example.org"
	e.syn.AddRoom(room)
	for _, err := range []error{
		e.st.SetMatrixAccount(ctx, "sub-1", id, "@one:example.org"),
		e.st.SetMatrixRoom(ctx, "sub-1", room),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return e, b, room
}

// run feeds the queued batches to a fresh Listen and returns once they are handled.
func run(t *testing.T, e *env, b *Bot, batches int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := len(e.syn.SyncSince())
	done := make(chan error, 1)
	go func() { done <- b.Listen(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if len(e.syn.SyncSince()) > base+batches {
			break // the call after the last batch means the last one was handled
		}
		if time.Now().After(deadline) {
			t.Fatal("sync queue never drained")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Listen returned nil")
	}
}

func syncOf(e *env, _ *Bot, batches ...[2]string) {
	for _, q := range batches {
		e.syn.QueueSync(q[0], q[1])
	}
}

func prime(t *testing.T, e *env) {
	t.Helper()
	if err := e.st.SetBotState(context.Background(), keySyncToken, "start"); err != nil {
		t.Fatal(err)
	}
}

func enabled(t *testing.T, e *env) bool {
	t.Helper()
	m, err := e.st.MatrixMember(context.Background(), "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	return m.Enabled
}

func TestStopAndStart(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	syncOf(e, b,
		[2]string{"b1", joined(room, message("$1", "@one:example.org", "m.text", "STOP"))},
		[2]string{"b2", joined(room, message("$2", "@one:example.org", "m.text", "stop"))})
	run(t, e, b, 2)
	if enabled(t, e) {
		t.Fatal("on after two stops")
	}
	syncOf(e, b, [2]string{"b3", joined(room, message("$3", "@one:example.org", "m.text", "start"))})
	run(t, e, b, 1)
	sends := e.syn.Sends()
	if len(sends) != 3 || !enabled(t, e) {
		t.Fatalf("sends=%d enabled=%v", len(sends), enabled(t, e))
	}
	for i, want := range []string{ReplyStopped, ReplyStopped, ReplyStarted} {
		if sends[i].Room != room || sends[i].Content["body"] != want || sends[i].Content["msgtype"] != "m.notice" {
			t.Errorf("send %d = %+v", i, sends[i])
		}
	}
	if !strings.HasPrefix(sends[0].TxnID, "reply-") || len(sends[0].TxnID) != len("reply-")+16 {
		t.Errorf("txn = %q", sends[0].TxnID)
	}
}

func TestStopSticks(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	syncOf(e, b, [2]string{"b1", joined(room, message("$1", "@one:example.org", "m.text", "STOP"))})
	run(t, e, b, 1)
	if enabled(t, e) {
		t.Fatal("still on")
	}
}

func TestHelpReply(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	syncOf(e, b, [2]string{"b1", joined(room, message("$1", "@one:example.org", "m.text", "hello"))})
	run(t, e, b, 1)
	s := e.syn.Sends()
	if len(s) != 1 || s[0].Content["body"] != ReplyHelp || !enabled(t, e) {
		t.Fatalf("sends = %+v", s)
	}
}

func TestIgnoresNoticesSelfAndStrangers(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	syncOf(e, b,
		[2]string{"b1", joined(room,
			message("$1", "@one:example.org", "m.notice", "stop"),
			message("$2", botUser, "m.notice", "stop"),
			message("$3", botUser, "m.text", "stop"),
			message("$4", "@two:example.org", "m.text", "stop"))},
		[2]string{"b2", joined("!other:example.org", message("$5", "@one:example.org", "m.text", "stop"))})
	run(t, e, b, 2)
	if n := len(e.syn.Sends()); n != 0 || !enabled(t, e) {
		t.Fatalf("sends=%d enabled=%v", n, enabled(t, e))
	}
}

func TestLeaveCountsAsStop(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	syncOf(e, b, [2]string{"b1", joined(room, memberEvent("$1", "@one:example.org", "@one:example.org", "leave"))},
		[2]string{"b2", `{}`})
	run(t, e, b, 2)
	ctx := context.Background()
	m, _ := e.st.MatrixMember(ctx, "sub-1")
	if m.Enabled || m.RoomID != "" {
		t.Fatalf("member = %+v", m)
	}
	var left bool
	for _, r := range e.syn.Rooms() {
		if r.ID == room {
			left = r.Left && r.Forgotten
		}
	}
	if !left || len(e.syn.Sends()) != 0 {
		t.Fatalf("left=%v sends=%d", left, len(e.syn.Sends()))
	}
	if _, ok, err := b.Prepare(ctx, "sub-1"); ok || err != nil {
		t.Fatal(ok, err)
	}
	if len(e.syn.Rooms()) != 1 {
		t.Fatalf("rooms = %d", len(e.syn.Rooms()))
	}
}

func TestBotLeavingForgetsRoom(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	syncOf(e, b, [2]string{"b1", fmt.Sprintf(`{"rooms":{"leave":{%q:{"timeline":{"events":[]}}}}}`, room)})
	run(t, e, b, 1)
	m, _ := e.st.MatrixMember(context.Background(), "sub-1")
	if m.RoomID != "" || !m.Enabled {
		t.Fatalf("member = %+v", m)
	}
}

func TestInviteFromMember(t *testing.T) {
	e, b, old := memberRoom(t)
	prime(t, e)
	e.syn.AddRoom("!invite:example.org")
	syncOf(e, b, [2]string{"b1", invited("!invite:example.org", "@one:example.org")})
	if err := e.st.SetMatrixEnabled(context.Background(), "sub-1", false); err != nil {
		t.Fatal(err)
	}
	run(t, e, b, 1)
	var fresh []string
	for _, r := range e.syn.Rooms() {
		switch r.ID {
		case "!invite:example.org":
			if !r.Left {
				t.Error("invite room not left")
			}
		case old:
			if !r.Left || !r.Forgotten {
				t.Error("old room kept")
			}
		default:
			fresh = append(fresh, r.ID)
			if r.Request["is_direct"] != true || r.Request["preset"] != "private_chat" {
				t.Errorf("request = %v", r.Request)
			}
		}
	}
	m, _ := e.st.MatrixMember(context.Background(), "sub-1")
	if len(fresh) != 1 || m.RoomID != fresh[0] || !m.Enabled || m.UserID != "@one:example.org" {
		t.Fatalf("fresh=%v member=%+v", fresh, m)
	}
	s := e.syn.Sends()
	if len(s) != 1 || s[0].Room != fresh[0] || s[0].Content["body"] != ReplyStarted {
		t.Fatalf("sends = %+v", s)
	}
}

func TestForeignInvite(t *testing.T) {
	e, b, _ := memberRoom(t)
	prime(t, e)
	link(e, "sub-2", "nolink")
	e.mas.AddUser("unlinked", e.mas.Users()[0].CreatedAt)
	for _, r := range []string{"!a:remote.org", "!b:example.org", "!c:example.org", "!d:example.org"} {
		e.syn.AddRoom(r)
	}
	syncOf(e, b,
		[2]string{"b1", invited("!a:remote.org", "@x:remote.org")},
		[2]string{"b2", invited("!b:example.org", "@unlinked:example.org")},
		[2]string{"b3", invited("!c:example.org", "@nobody:example.org")},
		[2]string{"b4", invited("!d:example.org", "@nolink:example.org")}) // linked, but not a member
	run(t, e, b, 4)
	for _, r := range e.syn.Rooms() {
		if r.ID != "!dm:example.org" && !r.Left {
			t.Errorf("%s not left", r.ID)
		}
	}
	if len(e.syn.Rooms()) != 5 || len(e.syn.Sends()) != 0 {
		t.Fatalf("rooms=%d sends=%d", len(e.syn.Rooms()), len(e.syn.Sends()))
	}
}

func TestSyncTokenStored(t *testing.T) {
	e, b := started(t)
	syncOf(e, b, [2]string{"b1", `{}`}, [2]string{"b2", `{}`})
	run(t, e, b, 2)
	if got := e.syn.SyncSince(); len(got) < 3 || got[0] != "" || got[1] != "b1" || got[2] != "b2" {
		t.Fatalf("since = %q", got)
	}
	if e.state(t, keySyncToken) != "b2" {
		t.Fatalf("token = %q", e.state(t, keySyncToken))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	n := len(e.syn.SyncSince())
	_ = b.Listen(ctx)
	if got := e.syn.SyncSince(); got[n] != "b2" {
		t.Fatalf("fresh Listen started from %q", got[n])
	}
}

func TestInitialSyncIgnoresTimeline(t *testing.T) {
	e, b, room := memberRoom(t)
	syncOf(e, b, [2]string{"b1", joined(room, message("$1", "@one:example.org", "m.text", "stop"))})
	run(t, e, b, 1)
	if len(e.syn.Sends()) != 0 || !enabled(t, e) {
		t.Fatal("initial timeline was handled")
	}
	if e.state(t, keySyncToken) != "b1" {
		t.Fatal("token not stored")
	}
}

func TestInviteReplyFailureDoesNotInviteAgain(t *testing.T) {
	e, b, _ := memberRoom(t)
	prime(t, e)
	e.syn.AddRoom("!invite:example.org")
	e.syn.FailNextSends(1, 503)
	syncOf(e, b, [2]string{"b1", invited("!invite:example.org", "@one:example.org")}, [2]string{"b2", `{}`})
	run(t, e, b, 2)
	fresh := 0
	for _, r := range e.syn.Rooms() {
		if r.ID != "!invite:example.org" && r.ID != "!dm:example.org" {
			fresh++
		}
		if r.ID == "!invite:example.org" && !r.Left {
			t.Error("invite not rejected")
		}
	}
	m, _ := e.st.MatrixMember(context.Background(), "sub-1")
	if fresh != 1 || m.RoomID == "" || m.RoomID == "!dm:example.org" || !m.Enabled {
		t.Fatalf("fresh=%d member=%+v", fresh, m)
	}
}

func TestPoisonInviteDoesNotStopListener(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	e.syn.AddRoom("!invite:example.org")
	e.mas.FailUsernames.Store(400)
	syncOf(e, b, [2]string{"b1", poisonBatch(room)})
	run(t, e, b, 1)
	if enabled(t, e) || e.state(t, keySyncToken) != "b1" {
		t.Fatalf("enabled=%v token=%q", enabled(t, e), e.state(t, keySyncToken))
	}
	for _, r := range e.syn.Rooms() {
		if r.ID == "!invite:example.org" && !r.Left {
			t.Error("invite not rejected")
		}
	}
}

func poisonBatch(room string) string {
	inv := invited("!invite:example.org", "@one:example.org")
	inv = strings.TrimSuffix(strings.TrimPrefix(inv, `{"rooms":{`), `}}`)
	return `{"rooms":{` + inv + `,"join":{"` + room + `":{"timeline":{"events":[` +
		message("$1", "@one:example.org", "m.text", "stop") + `]}}}}}`
}

// failingStore renames the members table away, so every member lookup fails
// with a store error, and returns the function that puts it back.
func failingStore(t *testing.T, e *env) func() {
	t.Helper()
	c := storetest.Conn(t, e.st)
	if _, err := c.Exec(context.Background(), `ALTER TABLE matrix_members RENAME TO matrix_members_away`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := c.Exec(context.Background(), `ALTER TABLE matrix_members_away RENAME TO matrix_members`); err != nil {
			t.Error(err)
		}
	}
}

// listenUntil runs Listen until the sync token is next or 10s pass.
func listenUntil(t *testing.T, e *env, b *Bot, next string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Listen(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, _, err := e.st.BotState(context.Background(), keySyncToken)
		if err == nil && v == next {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync token never became %q", next)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A leave that meets a passing store error is applied on the next attempt.
func TestStoreErrorRetriesBatch(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	restore := failingStore(t, e)
	var waits atomic.Int32
	b.wait = func(ctx context.Context, d time.Duration) error {
		if waits.Add(1) == 1 {
			restore()
		}
		return nil
	}
	syncOf(e, b, [2]string{"b1", joined(room, memberEvent("$1", "@one:example.org", "@one:example.org", "leave"))})
	listenUntil(t, e, b, "b1")
	if enabled(t, e) {
		t.Fatal("the leave was dropped")
	}
	if n := waits.Load(); n != 1 {
		t.Fatalf("%d waits", n)
	}
}

// An event whose store error does not pass is skipped after three attempts,
// so it cannot stop the listener.
func TestStoreErrorSkippedAfterThreeAttempts(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	restore := failingStore(t, e)
	b.wait = func(ctx context.Context, d time.Duration) error { return nil }
	syncOf(e, b, [2]string{"b1", joined(room, memberEvent("$1", "@one:example.org", "@one:example.org", "leave"))})
	listenUntil(t, e, b, "b1")
	restore()
	attempts := 0
	for _, s := range e.syn.SyncSince() {
		if s == "start" {
			attempts++
		}
	}
	if attempts != 3 {
		t.Fatalf("%d attempts", attempts)
	}
	if !enabled(t, e) {
		t.Fatal("the skipped leave was applied")
	}
}

func TestCommandOfReply(t *testing.T) {
	fallback := "> <@digest-bot:example.org> Your weekly digest from Talk\n> \n> Reply stop to stop these messages.\n\nstop"
	if got := commandOf(fallback, true); got != "stop" {
		t.Errorf("reply: %q", got)
	}
	if got := commandOf(fallback, false); got != "" {
		t.Errorf("not a reply: %q", got)
	}
	if got := commandOf("> quoted\nstart", true); got != "start" {
		t.Errorf("no blank line: %q", got)
	}
}

func TestStopAsReply(t *testing.T) {
	e, b, room := memberRoom(t)
	prime(t, e)
	c, _ := json.Marshal(map[string]any{"msgtype": "m.text",
		"body":         "> <@digest-bot:example.org> Your weekly digest\n\nstop",
		"m.relates_to": map[string]any{"m.in_reply_to": map[string]string{"event_id": "$notice"}}})
	ev := fmt.Sprintf(`{"type":"m.room.message","event_id":"$1","sender":"@one:example.org","content":%s}`, c)
	syncOf(e, b, [2]string{"b1", joined(room, ev)})
	listenUntil(t, e, b, "b1")
	if enabled(t, e) {
		t.Fatal("a stop sent as a reply was not obeyed")
	}
}

// syncAttempts is how many times the fake homeserver was asked for the batch
// that follows since.
func syncAttempts(e *env, since string) int {
	n := 0
	for _, s := range e.syn.SyncSince() {
		if s == since {
			n++
		}
	}
	return n
}

func roomLeft(e *env, id string) bool {
	for _, r := range e.syn.Rooms() {
		if r.ID == id {
			return r.Left
		}
	}
	return false
}

// A member's invitation that cannot be prepared for a reason other than an
// outage is tried for three batches, then rejected, not left pending.
func TestInvitePrepareFailureRejectsAfterThreeAttempts(t *testing.T) {
	e, b, _ := memberRoom(t)
	prime(t, e)
	e.syn.AddRoom("!invite:example.org")
	restore := failingStore(t, e)
	defer restore()
	b.wait = func(context.Context, time.Duration) error { return nil }
	syncOf(e, b, [2]string{"b1", invited("!invite:example.org", "@one:example.org")})
	listenUntil(t, e, b, "b1")
	if n := syncAttempts(e, "start"); n != 3 {
		t.Fatalf("%d attempts", n)
	}
	if !roomLeft(e, "!invite:example.org") {
		t.Error("invite not rejected")
	}
}

// A store error that clears within the attempts still gets the invitation
// answered.
func TestInvitePrepareTransientFailureAnswers(t *testing.T) {
	e, b, _ := memberRoom(t)
	prime(t, e)
	e.syn.AddRoom("!invite:example.org")
	restore := failingStore(t, e)
	var waits atomic.Int32
	b.wait = func(context.Context, time.Duration) error {
		if waits.Add(1) == 1 {
			restore()
		}
		return nil
	}
	syncOf(e, b, [2]string{"b1", invited("!invite:example.org", "@one:example.org")})
	listenUntil(t, e, b, "b1")
	if !roomLeft(e, "!invite:example.org") {
		t.Error("invite room not left")
	}
	if s := e.syn.Sends(); len(s) != 1 || s[0].Content["body"] != ReplyStarted {
		t.Fatalf("sends = %+v", s)
	}
}

// flakyChecker fails its first fails calls, then says the member is one.
type flakyChecker struct {
	fails int
	calls atomic.Int32
}

func (c *flakyChecker) IsMember(context.Context, string) (bool, error) {
	if int(c.calls.Add(1)) <= c.fails {
		return false, errors.New("mbin is down")
	}
	return true, nil
}

// An invitation whose membership check keeps failing is retried for three
// batches, so a persistent Mbin error cannot stall the listener, and then
// rejected.
func TestInviteMembershipCheckFailureRejectsAfterThreeAttempts(t *testing.T) {
	e, b, _ := memberRoom(t)
	prime(t, e)
	c := &flakyChecker{fails: 1 << 30}
	b.members = c
	e.syn.AddRoom("!invite:example.org")
	b.wait = func(context.Context, time.Duration) error { return nil }
	syncOf(e, b, [2]string{"b1", invited("!invite:example.org", "@one:example.org")})
	listenUntil(t, e, b, "b1")
	if n := c.calls.Load(); n != 3 {
		t.Fatalf("%d membership checks", n)
	}
	if !roomLeft(e, "!invite:example.org") {
		t.Error("invite not rejected")
	}
	if len(e.syn.Sends()) != 0 {
		t.Error("a rejected invite was greeted")
	}
}

// A membership check that recovers within the attempts still answers the
// invitation.
func TestInviteMembershipCheckTransientFailureAnswers(t *testing.T) {
	e, b, _ := memberRoom(t)
	prime(t, e)
	c := &flakyChecker{fails: 2}
	b.members = c
	e.syn.AddRoom("!invite:example.org")
	b.wait = func(context.Context, time.Duration) error { return nil }
	syncOf(e, b, [2]string{"b1", invited("!invite:example.org", "@one:example.org")})
	listenUntil(t, e, b, "b1")
	if s := e.syn.Sends(); len(s) != 1 || s[0].Content["body"] != ReplyStarted {
		t.Fatalf("sends = %+v", s)
	}
	if n := c.calls.Load(); n != 3 {
		t.Fatalf("%d membership checks", n)
	}
}
