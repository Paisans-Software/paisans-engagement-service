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
	// A token for an empty subject is never one the service minted for a
	// member, so it is as invalid as a bad signature.
	subject, ok := token.Verify(s.key, r.PathValue("token"))
	if !ok || subject == "" {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("This unsubscribe link is not valid."))
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
