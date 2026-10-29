package welcome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"log/slog"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
	"github.com/paisans-software/paisans-engagement-service/internal/mail"
)

// alert tells the operators that the welcomes need them: by email to each
// operator, and as a Matrix message to each one whose Matrix account the bot
// can reach. At most one alert of a kind goes out a day. Failing to alert is
// logged and never stops the caller.
func (r *Runner) alert(ctx context.Context, now time.Time, kind, text string) {
	slog.Error("welcomes need an operator", "kind", kind, "problem", text)
	due, err := r.Store.AlertDue(ctx, kind, now)
	if err != nil {
		slog.Warn("could not record an operator alert", "error", err)
		return
	}
	if !due || len(r.Operators) == 0 {
		return
	}
	subject := "Welcomes on " + r.Names.Meta + " need attention"
	if r.Sender != nil {
		for _, to := range r.Operators {
			msg := mail.Message{To: to, Subject: subject, Text: text + "\n",
				HTML: "<p>" + html.EscapeString(text) + "</p>"}
			if err := r.Sender.Send(ctx, msg); err != nil {
				slog.Warn("an operator alert email failed", "error", err)
			}
		}
	}
	b := r.bot()
	if b == nil {
		return
	}
	subjects, err := r.Mbin.SubjectsByEmail(ctx, r.Operators)
	if err != nil {
		slog.Warn("could not look up the operators' Matrix accounts", "error", err)
		return
	}
	for _, sub := range subjects {
		t, ok, err := b.Prepare(ctx, sub)
		if err != nil || !ok {
			continue
		}
		h := sha256.Sum256([]byte(sub))
		txn := "alert-" + kind + "-" + now.Format("2006-01-02") + "-" + hex.EncodeToString(h[:])[:16]
		if _, err := b.Deliver(ctx, t, txn, bot.Notice{Body: subject + ": " + text}); err != nil {
			slog.Warn("an operator alert message failed", "error", err)
		}
	}
}
