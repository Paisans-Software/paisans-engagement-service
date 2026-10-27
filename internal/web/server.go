// Package web is the service's only public surface: the unsubscribe link in
// the digest email, each sent digest's page, and a health check for the
// container.
package web

import (
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
	// Matrix is true when the deployment has a homeserver; unsubscribing
	// then stops the Matrix digest as well.
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

// NewServer answers /healthz, the unsubscribe route and the digest page;
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
	mux.HandleFunc("GET /d/{token}", s.page)
	mux.HandleFunc("HEAD /d/{token}", s.page)
	mux.HandleFunc("/d/{token}", s.invalid)
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
	subject, ok := token.Verify(s.key, r.PathValue("token"))
	if !ok {
		s.invalid(w, r)
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
	if _, ok := token.Verify(s.key, r.PathValue("token")); !ok {
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *server) invalid(w http.ResponseWriter, r *http.Request) {
	digestHeaders(w, r)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		w.Write([]byte("This link is not valid."))
	}
}

// digestHeaders keeps a digest page, and every answer on its path, out of
// search indexes and shared caches.
func digestHeaders(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Path) >= 3 && r.URL.Path[:3] == "/d/" {
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("Cache-Control", "private, no-store")
	}
}

// page renders a sent digest. It reads and records nothing else, so a link
// checker's GET or HEAD changes nothing. HEAD gets the same status and headers without the body.
func (s *server) page(w http.ResponseWriter, r *http.Request) {
	subject, date, ok := token.VerifyDigest(s.key, r.PathValue("token"))
	if !ok {
		s.invalid(w, r)
		return
	}
	// The date goes to Postgres, so it is checked here as well as signed.
	day, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil || day.Format("2006-01-02") != date {
		s.invalid(w, r)
		return
	}
	digestHeaders(w, r)
	content, found, err := s.store.WebDigest(r.Context(), subject, date, s.now())
	if err != nil {
		// The subject is a member's identity and the error may quote it, so
		// neither is logged.
		slog.Error("reading a web digest failed")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Something went wrong. Please try the link again."))
		return
	}
	if !found {
		s.invalid(w, r)
		return
	}
	var d digest.Digest
	if err := json.Unmarshal(content, &d); err != nil {
		slog.Error("a stored web digest is unreadable")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Something went wrong. Please try the link again."))
		return
	}
	html, err := mail.RenderPage(d, s.names, s.siteURL, day.AddDate(0, 0, -7), day)
	if err != nil {
		slog.Error("rendering a web digest failed")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Something went wrong. Please try the link again."))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodHead {
		w.Write([]byte(html))
	}
}
