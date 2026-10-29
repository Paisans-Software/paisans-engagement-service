package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
)

// healthcheck probes 127.0.0.1 on the listen address's port, whatever host
// the address names, since that is where the container's own server is.
func TestHealthcheck(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if code := healthcheck(":" + port); code != 0 {
		t.Fatalf("healthy server: exit %d", code)
	}
	if code := healthcheck("0.0.0.0:" + port); code != 0 {
		t.Fatalf("healthy server on 0.0.0.0: exit %d", code)
	}
	healthy.Store(false)
	if code := healthcheck(":" + port); code != 1 {
		t.Fatalf("unhealthy server: exit %d", code)
	}
}

func TestHealthcheckBadAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, closed, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	for _, addr := range []string{"no-port", "127.0.0.1:", ":" + closed} {
		if code := healthcheck(addr); code != 1 {
			t.Errorf("healthcheck(%q) = %d, want 1", addr, code)
		}
	}
}

// A Holder with no bot must give the runner an untyped nil, not a nil
// *bot.Bot inside the interface, which would pass the runner's nil check.
func TestRunnerMatrixNilIsUntyped(t *testing.T) {
	if dm := dmFrom(&bot.Holder{})(); dm != nil {
		t.Fatalf("dmFrom(empty holder)() = %#v, want untyped nil", dm)
	}
}
