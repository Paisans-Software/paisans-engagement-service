// Package web is the service's only HTTP surface: the unsubscribe links in
// the digest and welcome emails, each sent digest's page and each preview's for the
// signed-in member, and a health check for the container.
package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/digest"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/token"
)

// Options configure the server.
type Options struct {
	Store   *store.Store
	Key     []byte
	Names   mail.Names
	SiteURL string
	// Matrix is true when the deployment has a homeserver. Unsubscribing
	// stops the email only; the Matrix messages have a setting of their own.
	// Matrix decides whether unsubscribing forgets the threads the member was
	// shown: with a homeserver, only when their Matrix messages are off too.
	Matrix bool
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type server struct {
	store   *store.Store
	key     []byte
	names   mail.Names
	siteURL string
	matrix  bool
	now     func() time.Time
}

// NewServer answers /healthz, the unsubscribe route and the digest pages;
// every other path is a 404, because no other page exists.
func NewServer(o Options) http.Handler {
	s := &server{store: o.Store, key: o.Key, names: o.Names, siteURL: o.SiteURL, matrix: o.Matrix, now: o.Now}
	if s.now == nil {
		s.now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	// GET is the link in the email; POST is RFC 8058's one click, whose body
	// (List-Unsubscribe=One-Click) carries nothing the route needs.
	mux.HandleFunc("GET /u/{token}", s.unsubscribe)
	mux.HandleFunc("POST /u/{token}", s.unsubscribe)
	// A GET pattern also matches HEAD, which link checkers and mail
	// scanners send without a member clicking anything, so HEAD has its own
	// handler that changes nothing.
	mux.HandleFunc("HEAD /u/{token}", s.head)
	// Without these the mux answers another method on a known path with
	// 405, which says the path exists.
	mux.HandleFunc("/u/{token}", s.invalid)
	// The digest pages sit behind the community's sign-in gate, which names
	// the member; the path names only the week, so every member's link for
	// the week is the same.
	mux.HandleFunc("GET /d/{week}", digestHeaders(s.page(s.store.WebDigest)))
	mux.HandleFunc("HEAD /d/{week}", digestHeaders(s.page(s.store.WebDigest)))
	mux.HandleFunc("/d/{week}", digestHeaders(s.invalid))
	mux.HandleFunc("GET /d/preview/{week}", digestHeaders(s.page(s.store.PreviewDigest)))
	mux.HandleFunc("HEAD /d/preview/{week}", digestHeaders(s.page(s.store.PreviewDigest)))
	mux.HandleFunc("/d/preview/{week}", digestHeaders(s.invalid))
	mux.HandleFunc("/healthz", http.NotFound)
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

func (s *server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	purpose, subject, ok := token.VerifyFor(s.key, r.PathValue("token"))
	if !ok {
		s.invalid(w, r)
		return
	}
	if purpose == token.Welcome {
		if err := s.store.UnsubscribeWelcome(r.Context(), subject); err != nil {
			slog.Error("welcome unsubscribe failed", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Something went wrong. Please try the link again."))
			return
		}
		w.Write([]byte("You're unsubscribed from welcome emails."))
		return
	}
	if err := s.store.Unsubscribe(r.Context(), subject, s.matrix); err != nil {
		// The subject is a member's identity and stays out of the log.
		slog.Error("unsubscribe failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Something went wrong. Please try the link again."))
		return
	}
	w.Write([]byte("You're unsubscribed from the weekly digest."))
}

func (s *server) head(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, _, ok := token.VerifyFor(s.key, r.PathValue("token")); !ok {
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *server) invalid(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		w.Write([]byte("This link is not valid."))
	}
}

// digestHeaders keeps a digest page, and every answer on its path, out of
// search indexes and shared caches.
func digestHeaders(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("Cache-Control", "private, no-store")
		h(w, r)
	}
}

// fail answers 500 with a plain sentence and logs what failed. The log
// line names no member: the subject is their identity, and an error may
// quote it.
func (s *server) fail(w http.ResponseWriter, what string) {
	slog.Error(what)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte("Something went wrong. Please try the link again."))
}

// memberHeader names the signed-in member. The gate (oauth2-proxy behind
// Caddy's forward_auth) sets it to the member's Pocket ID subject on every
// request it admits, and Caddy strips any copy a client sends, so it is
// trusted when present.
const memberHeader = "X-Auth-Request-User"

// page renders the signed-in member's stored digest for the path's week,
// read by read: the sent digest or the preview. It reads and records
// nothing else, so a link checker's GET or HEAD changes nothing. HEAD gets
// the same status and headers without the body.
func (s *server) page(read func(ctx context.Context, subject, weekDate string, now time.Time) ([]byte, bool, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject := r.Header.Get(memberHeader)
		if subject == "" {
			// No header means the request did not pass the gate, so the
			// deployment is not gated. The answer is the same 404 as any bad
			// link rather than a 401: it says nothing about whether a digest
			// exists, and it does not hint that a header would open the page.
			// A member never sees it, because the gate signs them in first.
			slog.Warn("a web digest was requested without " + memberHeader + "; the engagement hostname is not behind the sign-in gate")
			s.invalid(w, r)
			return
		}
		// The date goes to Postgres, so it must be a calendar day spelled
		// exactly as YYYY-MM-DD.
		date := r.PathValue("week")
		day, err := time.ParseInLocation("2006-01-02", date, time.Local)
		if err != nil || day.Format("2006-01-02") != date {
			s.invalid(w, r)
			return
		}
		content, found, err := read(r.Context(), subject, date, s.now())
		if err != nil {
			s.fail(w, "reading a web digest failed")
			return
		}
		if !found {
			s.invalid(w, r)
			return
		}
		var d digest.Digest
		if err := json.Unmarshal(content, &d); err != nil {
			s.fail(w, "a stored web digest is unreadable")
			return
		}
		html, err := mail.RenderPage(d, s.names, s.siteURL, day.AddDate(0, 0, -7), day)
		if err != nil {
			s.fail(w, "rendering a web digest failed")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodHead {
			w.Write([]byte(html))
		}
	}
}
