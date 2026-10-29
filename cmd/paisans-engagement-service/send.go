package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
	"github.com/paisans-software/paisans-engagement-service/internal/config"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mas"
	"github.com/paisans-software/paisans-engagement-service/internal/matrix"
	"github.com/paisans-software/paisans-engagement-service/internal/mbin"
	"github.com/paisans-software/paisans-engagement-service/internal/run"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
)

type sendOpts struct {
	// member is an Mbin username; empty means every member.
	member string
	// preview sends member the digest a brand-new member would get.
	preview bool
	// matrix sends member the Matrix notice instead of the email.
	matrix bool
}

const sendUsage = "usage: send [--member <username> [--matrix] [--preview]]"

func parseSend(args []string) (sendOpts, error) {
	var o sendOpts
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	member := fs.String("member", "", "")
	preview := fs.Bool("preview", false, "")
	toMatrix := fs.Bool("matrix", false, "")
	if err := fs.Parse(args); err != nil {
		return o, fmt.Errorf("%w; %s", err, sendUsage)
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q; %s", fs.Arg(0), sendUsage)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["member"] && (*member == "" || strings.TrimSpace(*member) != *member) {
		return o, errors.New("--member needs an Mbin username")
	}
	if *preview && !set["member"] {
		return o, errors.New("--preview needs --member, the member the preview is mailed to")
	}
	if *toMatrix && !set["member"] {
		return o, errors.New("--matrix needs --member, the member the Matrix message is sent to")
	}
	o.member, o.preview, o.matrix = *member, *preview, *toMatrix
	return o, nil
}

// sendWeek is the week a manual send covers: the latest Monday 08:00, on
// whatever day it runs.
func sendWeek(now time.Time) time.Time { return run.SendTime(now) }

// send runs one week's digest now, outside the schedule, and returns. Members
// already claimed for the week are skipped, so it never mails anyone twice.
// With --preview it mails a preview instead, which claims nobody. With
// --matrix it sends the member's Matrix notice, or its preview, instead of
// the email.
func send(ctx context.Context, args []string, getenv func(string) string, now func() time.Time) error {
	opts, err := parseSend(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	if opts.matrix {
		if missing := cfg.Matrix.Missing(); len(missing) > 0 {
			return fmt.Errorf("the Matrix bot is not configured: %s not set", strings.Join(missing, ", "))
		}
	} else if cfg.SMTP.Host == "" {
		return errors.New("ENGAGEMENT_SMTP_HOST is not set, so there is no mail host to send through")
	}
	var sender mail.Sender
	if !opts.matrix {
		if sender, err = mail.NewSMTP(cfg.SMTP); err != nil {
			return err
		}
	}
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
	src, err := mbin.Open(ctx, cfg.MbinDatabaseURL, cfg.MbinURL)
	if err != nil {
		return err
	}
	defer src.Close()
	runner := &run.Runner{
		Store: st, Source: src, Sender: sender,
		Names:   mail.Names{Instance: cfg.InstanceTitle, Meta: cfg.MetaTitle},
		SiteURL: cfg.MbinURL, PublicURL: cfg.PublicURL, Key: cfg.SigningKey,
		Only: opts.member,
	}
	week := sendWeek(now())
	if opts.matrix {
		return sendMatrix(ctx, cfg, st, src, runner, week, opts)
	}
	if opts.preview {
		slog.Info("manual digest preview", "week", week.Format("2006-01-02"))
		return runner.Preview(ctx, week, opts.member)
	}
	slog.Info("manual digest send", "week", week.Format("2006-01-02"), "one_member", opts.member != "")
	return runner.Week(ctx, week)
}

// sendMatrix sends the member's Matrix notice for the week, or its preview,
// as the Matrix bot. bot.Once decides how this process acts as the bot: with
// the bot lock, held only for this send, when no copy of the service runs the
// bot, and otherwise with the running bot's stored token, leaving the
// service's lock and listener alone.
func sendMatrix(ctx context.Context, cfg config.Config, st *store.Store, src *mbin.Source, runner *run.Runner, week time.Time, opts sendOpts) error {
	b, done, err := bot.Once(ctx, bot.Deps{
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
	})
	if err != nil {
		return fmt.Errorf("the Matrix bot cannot start: %w", err)
	}
	defer done()
	runner.Matrix = func() run.DM { return b }
	runner.MatrixConfigured = true
	if opts.preview {
		slog.Info("manual matrix preview", "week", week.Format("2006-01-02"))
		return runner.PreviewMatrix(ctx, week, opts.member)
	}
	slog.Info("manual matrix send", "week", week.Format("2006-01-02"))
	return runner.MatrixWeek(ctx, week, opts.member)
}
