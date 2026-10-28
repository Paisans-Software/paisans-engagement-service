package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/config"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
	"github.com/paisans-software/paisans-engagement-service/internal/mbin"
	"github.com/paisans-software/paisans-engagement-service/internal/run"
	"github.com/paisans-software/paisans-engagement-service/internal/store"
)

type sendOpts struct {
	// member is an Mbin username; empty means every member.
	member string
	// preview mails member the digest a brand-new member would get.
	preview bool
}

const sendUsage = "usage: send [--member <username> [--preview]]"

func parseSend(args []string) (sendOpts, error) {
	var o sendOpts
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	member := fs.String("member", "", "")
	preview := fs.Bool("preview", false, "")
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
	o.member, o.preview = *member, *preview
	return o, nil
}

// sendWeek is the week a manual send covers: the latest Monday 08:00, on
// whatever day it runs.
func sendWeek(now time.Time) time.Time { return run.SendTime(now) }

// send runs one week's digest now, outside the schedule, and returns. Members
// already claimed for the week are skipped, so it never mails anyone twice.
// With --preview it mails a preview instead, which claims nobody.
func send(ctx context.Context, args []string, getenv func(string) string, now func() time.Time) error {
	opts, err := parseSend(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	if cfg.SMTP.Host == "" {
		return errors.New("ENGAGEMENT_SMTP_HOST is not set, so there is no mail host to send through")
	}
	sender, err := mail.NewSMTP(cfg.SMTP)
	if err != nil {
		return err
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
	if opts.preview {
		slog.Info("manual digest preview", "week", week.Format("2006-01-02"))
		return runner.Preview(ctx, week, opts.member)
	}
	slog.Info("manual digest send", "week", week.Format("2006-01-02"), "one_member", opts.member != "")
	return runner.Week(ctx, week)
}
