// Command paisans-engagement-service sends the community's weekly digest
// email. See the README for its environment.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/config"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mbin"
	"github.com/paisans-software/paisans-engagement-service/internal/run"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Getenv("ENGAGEMENT_LISTEN_ADDR")))
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := serve(); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

// healthcheck probes the local /healthz and returns the process exit code.
// The image has no shell, so the binary is its own health check.
func healthcheck(listenAddr string) int {
	if listenAddr == "" {
		listenAddr = ":8083"
	}
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return 1
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/healthz", port))
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func serve() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Ping(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	// loopDone, once the digest loop is started, closes when it has
	// returned and closed the Mbin pool. It is waited on before the store's
	// deferred Close, so a member being sent at shutdown gets their claim
	// finished or released rather than left pending. stop comes first, so a return for any other reason
	// also ends the loop.
	var loopDone chan struct{}
	defer func() {
		if loopDone == nil {
			return
		}
		stop()
		select {
		case <-loopDone:
		case <-time.After(10 * time.Second):
			slog.Warn("the digest run did not stop in time; a member may stay pending this week")
		}
	}()

	// Unsubscribe links in mail already sent keep working without a mail
	// host, so the route is served either way; only the sending is skipped.
	if cfg.SMTP.Host == "" {
		slog.Warn("no mail host is configured, so no digest is sent")
	} else {
		src, err := mbin.Open(ctx, cfg.MbinDatabaseURL, cfg.MbinURL)
		if err != nil {
			return err
		}
		sender, err := mail.NewSMTP(cfg.SMTP)
		if err != nil {
			src.Close()
			return err
		}
		runner := &run.Runner{
			Store: st, Source: src, Sender: sender,
			Names:   mail.Names{Instance: cfg.InstanceTitle, Meta: cfg.MetaTitle},
			SiteURL: cfg.MbinURL, PublicURL: cfg.PublicURL, Key: cfg.SigningKey,
		}
		loopDone = make(chan struct{})
		// The loop owns the Mbin pool and closes it once it has returned, so
		// a Week still running at shutdown never reads from a closed pool.
		go func() {
			defer close(loopDone)
			defer src.Close()
			run.Loop(ctx, runner, time.Now, time.Minute)
		}()
	}

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: web.NewServer(st, cfg.SigningKey), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shut); err != nil {
			slog.Warn("shutdown", "error", err)
		}
	}()
	slog.Info("listening", "addr", cfg.ListenAddr, "mail", cfg.SMTP.Host != "")
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
}
