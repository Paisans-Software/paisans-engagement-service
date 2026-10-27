// Package matrixtest is a fake homeserver speaking the slice of the Matrix
// client-server API that the Digest Bot uses.
package matrixtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Sent is one message the server accepted.
type Sent struct {
	Room, TxnID string
	Content     map[string]any
}

// Room is one room created through the fake.
type Room struct {
	ID        string
	Request   map[string]any
	Encrypted bool
	Left      bool
	Forgotten bool
}

type refusal struct {
	status  int
	errcode string
}

type syncItem struct{ next, body string }

// Server is the fake homeserver.
type Server struct {
	*httptest.Server

	Token         string // the bearer it accepts; others get 401 M_UNKNOWN_TOKEN
	Device        string // whoami's device_id
	TokenEndpoint string // what auth_metadata returns
	Down          atomic.Bool

	serverName string
	accept     func(token string) (user, device string, ok bool)

	mu            sync.Mutex
	limitN        int
	limitMS       int
	limitHeader   string
	lastAuth      string
	sends         []Sent
	sendKeys      map[string]bool
	attempts      int
	rooms         []*Room
	encrypt       bool
	members       map[string]map[string]string // room -> user -> membership
	refuse        map[string]refusal
	syncQ         []syncItem
	syncSince     []string
	syncQuery     url.Values
	sendPaths     []string
	profiles      map[string]string
	profileWrites int
	roomCounter   int
	eventCounter  int
}

// New starts a fake homeserver named serverName and closes it with the test.
func New(t testing.TB, serverName string) *Server {
	s := &Server{
		serverName: serverName,
		Token:      "test-token",
		Device:     "DIGESTBOT",
		sendKeys:   map[string]bool{},
		members:    map[string]map[string]string{},
		refuse:     map[string]refusal{},
		profiles:   map[string]string{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Server.Close)
	return s
}

// Accept installs a function that decides every bearer check and supplies
// whoami's user and device in place of Token and Device. Passing nil restores
// the fixed Token.
func (s *Server) Accept(fn func(token string) (user, device string, ok bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accept = fn
}

// RateLimitNext makes the next n requests answer 429. retryAfterMS goes in the
// body as retry_after_ms when positive; header, when not empty, is sent as
// Retry-After.
func (s *Server) RateLimitNext(n int, retryAfterMS int, header string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limitN, s.limitMS, s.limitHeader = n, retryAfterMS, header
}

// LastAuthorization is the Authorization header of the latest request.
func (s *Server) LastAuthorization() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAuth
}

// Sends lists the accepted messages, deduplicated by (room, txn).
func (s *Server) Sends() []Sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sent(nil), s.sends...)
}

// SendAttempts counts the send PUTs received, duplicates included.
func (s *Server) SendAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

// Rooms lists the rooms created so far.
func (s *Server) Rooms() []Room {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Room, len(s.rooms))
	for i, r := range s.rooms {
		out[i] = *r
	}
	return out
}

// EncryptNewRooms makes rooms created from now on encrypted.
func (s *Server) EncryptNewRooms(b bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.encrypt = b
}

// SetMembership records a user's membership of a room.
func (s *Server) SetMembership(room, user, membership string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members[room] == nil {
		s.members[room] = map[string]string{}
	}
	s.members[room][user] = membership
}

// RefuseSend makes sends to a room answer status with errcode.
func (s *Server) RefuseSend(room string, status int, errcode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse[room] = refusal{status, errcode}
}

// QueueSync queues a /sync response: it carries next_batch next and body as
// the JSON object body. Once the queue is empty /sync blocks until the
// request ends or its timeout passes, then answers an empty response.
func (s *Server) QueueSync(next string, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncQ = append(s.syncQ, syncItem{next, body})
}

// SyncSince lists the since values /sync received.
func (s *Server) SyncSince() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.syncSince...)
}

// SendPaths lists the escaped request paths of the send PUTs received.
func (s *Server) SendPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sendPaths...)
}

// LastSyncQuery is the query string of the latest /sync request.
func (s *Server) LastSyncQuery() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncQuery
}

// Profile is the stored display name of a user, or "".
func (s *Server) Profile(user string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profiles[user]
}

// ProfileWrites counts the display name writes received.
func (s *Server) ProfileWrites() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profileWrites
}

func (s *Server) errorJSON(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errcode": code, "error": msg})
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if s.Down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	s.mu.Lock()
	s.lastAuth = r.Header.Get("Authorization")
	limited := s.limitN > 0
	ms, hdr := s.limitMS, s.limitHeader
	if limited {
		s.limitN--
	}
	s.mu.Unlock()
	if limited {
		if hdr != "" {
			w.Header().Set("Retry-After", hdr)
		}
		body := map[string]any{"errcode": "M_LIMIT_EXCEEDED", "error": "Too many requests"}
		if ms > 0 {
			body["retry_after_ms"] = ms
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(body)
		return
	}

	path := r.URL.EscapedPath()
	if path == "/_matrix/client/v1/auth_metadata" {
		s.writeJSON(w, map[string]any{"token_endpoint": s.TokenEndpoint})
		return
	}
	s.mu.Lock()
	accept := s.accept
	s.mu.Unlock()
	whoUser, whoDevice := "@digestbot:"+s.serverName, s.Device
	if accept != nil {
		u, d, ok := accept(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if !ok {
			s.errorJSON(w, http.StatusUnauthorized, "M_UNKNOWN_TOKEN", "Unrecognised access token")
			return
		}
		whoUser, whoDevice = u, d
	} else if r.Header.Get("Authorization") != "Bearer "+s.Token {
		s.errorJSON(w, http.StatusUnauthorized, "M_UNKNOWN_TOKEN", "Unrecognised access token")
		return
	}
	seg := strings.Split(strings.TrimPrefix(path, "/_matrix/client/v3/"), "/")
	for i := range seg {
		if u, err := url.PathUnescape(seg[i]); err == nil {
			seg[i] = u
		}
	}
	switch {
	case r.Method == "GET" && path == "/_matrix/client/v3/account/whoami":
		s.writeJSON(w, map[string]any{"user_id": whoUser, "device_id": whoDevice})
	case len(seg) == 3 && seg[0] == "profile" && seg[2] == "displayname":
		s.profile(w, r, seg[1])
	case r.Method == "PUT" && len(seg) == 5 && seg[0] == "rooms" && seg[2] == "send":
		s.mu.Lock()
		s.sendPaths = append(s.sendPaths, path)
		s.mu.Unlock()
		s.send(w, r, seg[1], seg[4])
	case r.Method == "GET" && len(seg) == 5 && seg[0] == "rooms" && seg[2] == "state":
		s.state(w, seg[1], seg[3], seg[4])
	case r.Method == "POST" && path == "/_matrix/client/v3/createRoom":
		s.createRoom(w, r)
	case r.Method == "GET" && path == "/_matrix/client/v3/sync":
		s.sync(w, r)
	case r.Method == "POST" && len(seg) == 3 && seg[0] == "rooms" && (seg[2] == "leave" || seg[2] == "forget"):
		s.leaveForget(w, seg[1], seg[2])
	default:
		s.errorJSON(w, http.StatusNotFound, "M_UNRECOGNIZED", "Unrecognized request")
	}
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request, user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case "GET":
		name, ok := s.profiles[user]
		if !ok {
			s.errorJSON(w, http.StatusNotFound, "M_NOT_FOUND", "Profile was not found")
			return
		}
		s.writeJSON(w, map[string]any{"displayname": name})
	case "PUT":
		var b struct {
			DisplayName string `json:"displayname"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			s.errorJSON(w, http.StatusBadRequest, "M_BAD_JSON", "bad json")
			return
		}
		s.profiles[user] = b.DisplayName
		s.profileWrites++
		s.writeJSON(w, map[string]any{})
	default:
		s.errorJSON(w, http.StatusMethodNotAllowed, "M_UNRECOGNIZED", "method")
	}
}

func (s *Server) send(w http.ResponseWriter, r *http.Request, room, txn string) {
	var content map[string]any
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, &content); err != nil {
		s.errorJSON(w, http.StatusBadRequest, "M_NOT_JSON", "not json")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if ref, ok := s.refuse[room]; ok {
		s.errorJSON(w, ref.status, ref.errcode, "refused")
		return
	}
	key := room + "\x00" + txn
	if !s.sendKeys[key] {
		s.sendKeys[key] = true
		s.sends = append(s.sends, Sent{Room: room, TxnID: txn, Content: content})
	}
	s.eventCounter++
	s.writeJSON(w, map[string]any{"event_id": fmt.Sprintf("$event%d", s.eventCounter)})
}

func (s *Server) state(w http.ResponseWriter, room, evType, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case evType == "m.room.encryption" && key == "":
		for _, rm := range s.rooms {
			if rm.ID == room && rm.Encrypted {
				s.writeJSON(w, map[string]any{"algorithm": "m.megolm.v1.aes-sha2"})
				return
			}
		}
	case evType == "m.room.member":
		if m, ok := s.members[room][key]; ok {
			s.writeJSON(w, map[string]any{"membership": m})
			return
		}
	}
	s.errorJSON(w, http.StatusNotFound, "M_NOT_FOUND", "Event not found.")
}

func (s *Server) createRoom(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorJSON(w, http.StatusBadRequest, "M_NOT_JSON", "not json")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roomCounter++
	id := fmt.Sprintf("!room%d:%s", s.roomCounter, s.serverName)
	s.rooms = append(s.rooms, &Room{ID: id, Request: req, Encrypted: s.encrypt})
	s.writeJSON(w, map[string]any{"room_id": id})
}

func (s *Server) leaveForget(w http.ResponseWriter, room, action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rm := range s.rooms {
		if rm.ID == room {
			if action == "leave" {
				rm.Left = true
			} else {
				rm.Forgotten = true
			}
			s.writeJSON(w, map[string]any{})
			return
		}
	}
	s.errorJSON(w, http.StatusNotFound, "M_NOT_FOUND", "unknown room")
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.syncSince = append(s.syncSince, r.URL.Query().Get("since"))
	s.syncQuery = r.URL.Query()
	var item *syncItem
	if len(s.syncQ) > 0 {
		item = &s.syncQ[0]
		s.syncQ = s.syncQ[1:]
	}
	s.mu.Unlock()
	if item != nil {
		body := item.body
		if strings.TrimSpace(body) == "" {
			body = "{}"
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			s.errorJSON(w, http.StatusInternalServerError, "M_UNKNOWN", "bad queued sync body")
			return
		}
		m["next_batch"] = item.next
		s.writeJSON(w, m)
		return
	}
	wait := 30 * time.Second
	if ms := r.URL.Query().Get("timeout"); ms != "" {
		var n int
		if _, err := fmt.Sscanf(ms, "%d", &n); err == nil {
			wait = time.Duration(n) * time.Millisecond
		}
	}
	select {
	case <-r.Context().Done():
		return
	case <-time.After(wait):
	}
	s.writeJSON(w, map[string]any{"next_batch": r.URL.Query().Get("since")})
}
