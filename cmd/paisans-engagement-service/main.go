// Command paisans-engagement-service sends the community's weekly digest and
// welcomes its new members. See the README for its environment.
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
	"sync"
	"syscall"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
	"github.com/paisans-software/paisans-engagement-service/internal/config"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mas"
	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
	"github.com/paisans-software/paisans-engagement-service/internal/mbin"
	"github.com/paisans-software/paisans-engagement-service/internal/mbinapi"
	"github.com/paisans-software/paisans-engagement-service/internal/run"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
	"github.com/paisans-software/paisans-engagement-service/internal/web"
	"github.com/paisans-software/paisans-engagement-service/internal/welcome"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Getenv("ENGAGEMENT_LISTEN_ADDR")))
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) > 1 && os.Args[1] == "send" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := send(ctx, os.Args[2:], os.Getenv, time.Now)
		stop()
		if err != nil {
			slog.Error("digest send failed", "error", err)
			os.Exit(1)
		}
		return
	}
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

// dmFrom adapts the holder to the runner's accessor. It returns an explicit
// nil while no bot is running, never a nil *bot.Bot inside the interface.
func dmFrom(h *bot.Holder) func() run.DM {
	return func() run.DM {
		if b := h.Get(); b != nil {
			return b
		}
		return nil
	}
}

// welcomeBotFrom is dmFrom for the welcomes.
func welcomeBotFrom(h *bot.Holder) func() welcome.MatrixBot {
	return func() welcome.MatrixBot {
		if b := h.Get(); b != nil {
			return b
		}
		return nil
	}
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

	// stopped, once the digest loop, the welcomes or the bot supervisor are
	// started, closes when all have returned and the Mbin pool is closed. It
	// is waited on before the store's deferred Close, so a member being sent
	// at shutdown gets their claim finished or released rather than left
	// pending. stop comes first, so a return for any other reason also ends
	// them.
	var stopped chan struct{}
	defer func() {
		if stopped == nil {
			return
		}
		stop()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			slog.Warn("the digest run or the Matrix bot did not stop in time; a member may stay pending this week")
		}
	}()

	matrixOn := cfg.Matrix.Enabled()
	if !matrixOn {
		slog.Info("Matrix is off", "missing", cfg.Matrix.Missing())
	}
	// Unsubscribe links in mail already sent keep working without a mail
	// host, so the route is served either way; only the sending is skipped.
	if cfg.SMTP.Host == "" {
		slog.Warn("no mail host is configured, so no digest or welcome email is sent")
	}
	mbinWelcomes := len(cfg.Welcome.MbinMissing()) == 0
	if !mbinWelcomes {
		slog.Info("Mbin welcomes are off", "missing", cfg.Welcome.MbinMissing())
	}
	matrixWelcomes := matrixOn && len(cfg.Welcome.RoomMissing()) == 0
	if matrixOn && !matrixWelcomes {
		slog.Info("Matrix welcomes are off", "missing", cfg.Welcome.RoomMissing())
	}
	if cfg.SMTP.Host != "" || matrixOn || mbinWelcomes {
		src, err := mbin.Open(ctx, cfg.MbinDatabaseURL, cfg.MbinURL)
		if err != nil {
			return err
		}
		var sender mail.Sender
		if cfg.SMTP.Host != "" {
			s, err := mail.NewSMTP(cfg.SMTP)
			if err != nil {
				src.Close()
				return err
			}
			sender = s
		}
		runner := &run.Runner{
			Store: st, Source: src, Sender: sender,
			Names:   mail.Names{Meta: cfg.MetaTitle},
			SiteURL: cfg.MbinURL, PublicURL: cfg.PublicURL, Key: cfg.SigningKey,
		}
		var wg sync.WaitGroup
		wr := &welcome.Runner{
			Store: st, Mbin: src, Sender: sender, Names: runner.Names,
			SiteURL: cfg.MbinURL, PublicURL: cfg.PublicURL, Key: cfg.SigningKey,
			DailyLimit: cfg.Welcome.DailyLimit, Operators: cfg.Welcome.Operators,
		}
		if mbinWelcomes {
			wr.API = mbinapi.New(cfg.Welcome.MbinAPIURL, cfg.Welcome.AgentClientID, cfg.Welcome.AgentClientSecret,
				&http.Client{Timeout: 30 * time.Second})
		}
		if matrixOn {
			h := bot.NewHolder()
			runner.Matrix = dmFrom(h)
			runner.MatrixConfigured = true
			runner.MatrixPending = h.Pending
			deps := bot.Deps{
				Matrix:  matrix.New(cfg.Matrix.URL, &http.Client{Timeout: 60 * time.Second}, matrix.DefaultPacing),
				MAS:     mas.New(cfg.Matrix.MASAdminURL, cfg.Matrix.ClientID, cfg.Matrix.ClientSecret, &http.Client{Timeout: 30 * time.Second}),
				Store:   st,
				Members: src,
				Config: bot.Config{
					ServerName:  cfg.Matrix.ServerName,
					Provider:    cfg.Matrix.UpstreamProvider,
					Localpart:   cfg.Matrix.BotLocalpart,
					DisplayName: cfg.Matrix.BotDisplayName,
				},
				Retry: time.Minute,
			}
			wr.Bot = welcomeBotFrom(h)
			if matrixWelcomes {
				wr.RoomName, wr.RoomAlias = cfg.Welcome.RoomName, cfg.Welcome.RoomAlias
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				bot.Supervise(ctx, deps, h)
			}()
		}
		if cfg.SMTP.Host != "" || matrixOn {
			wg.Add(1)
			go func() {
				defer wg.Done()
				run.Loop(ctx, runner, time.Now, time.Minute)
			}()
		}
		var lock welcome.LockHolder
		wg.Add(2)
		go func() {
			defer wg.Done()
			lock.Hold(ctx, st, time.Minute)
		}()
		go func() {
			defer wg.Done()
			welcome.Loop(ctx, wr, time.Now, time.Minute, lock.Held)
		}()
		stopped = make(chan struct{})
		// The pool closes only once both have returned, so a Week or a bot
		// invite check still running at shutdown never reads a closed pool.
		go func() {
			defer close(stopped)
			wg.Wait()
			src.Close()
		}()
	}

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: web.NewServer(web.Options{
		Store: st, Key: cfg.SigningKey, Matrix: cfg.Matrix.Enabled(),
		Names: mail.Names{Meta: cfg.MetaTitle}, SiteURL: cfg.MbinURL,
	}), ReadHeaderTimeout: 10 * time.Second}
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
	slog.Info("listening", "addr", cfg.ListenAddr, "mail", cfg.SMTP.Host != "", "matrix", matrixOn,
		"mbin_welcomes", mbinWelcomes, "matrix_welcomes", matrixWelcomes)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
}
