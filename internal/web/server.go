// Package web is the service's only public surface: the unsubscribe link in
// the digest email, and a health check for the container.
package web

import (
	"log/slog"
	"net/http"

	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/token"
)

type server struct {
	store *store.Store
	key   []byte
}

// NewServer answers /healthz and the unsubscribe route; every other path is a
// 404, because no other page exists.
func NewServer(st *store.Store, key []byte) http.Handler {
	s := &server{store: st, key: key}
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
	if err := s.store.Unsubscribe(r.Context(), subject); err != nil {
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
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte("This unsubscribe link is not valid."))
}
