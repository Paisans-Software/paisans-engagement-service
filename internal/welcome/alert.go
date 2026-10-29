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
// can reach. At most one alert of a kind goes out a day, and only one that
// reached someone uses up the day. Failing to alert is logged and never
// stops the caller.
func (r *Runner) alert(ctx context.Context, now time.Time, kind, text string) {
	slog.Error("welcomes need an operator", "kind", kind, "problem", text)
	if len(r.Operators) == 0 {
		return
	}
	sent, err := r.Store.AlertSentToday(ctx, kind, now)
	if err != nil {
		slog.Warn("could not read the operator alerts", "error", err)
		return
	}
	if sent {
		return
	}
	subject := "Welcomes on " + r.Names.Meta + " need attention"
	delivered := false
	if r.Sender != nil {
		for _, to := range r.Operators {
			msg := mail.Message{To: to, Subject: subject, Text: text + "\n",
				HTML: "<p>" + html.EscapeString(text) + "</p>"}
			if err := r.Sender.Send(ctx, msg); err != nil {
				slog.Warn("an operator alert email failed", "error", err)
				continue
			}
			delivered = true
		}
	}
	if b := r.bot(); b != nil {
		delivered = r.alertMatrix(ctx, b, now, kind, subject+": "+text) || delivered
	}
	if !delivered {
		return
	}
	if err := r.Store.RecordAlert(ctx, kind, now); err != nil {
		slog.Warn("could not record an operator alert", "error", err)
	}
}

// alertMatrix messages each operator the bot can reach and reports whether
// any message went out.
func (r *Runner) alertMatrix(ctx context.Context, b MatrixBot, now time.Time, kind, text string) bool {
	subjects, err := r.Mbin.SubjectsByEmail(ctx, r.Operators)
	if err != nil {
		slog.Warn("could not look up the operators' Matrix accounts", "error", err)
		return false
	}
	delivered := false
	for _, sub := range subjects {
		t, ok, err := b.Prepare(ctx, sub)
		if err != nil || !ok {
			continue
		}
		h := sha256.Sum256([]byte(sub))
		txn := "alert-" + kind + "-" + now.Format("2006-01-02") + "-" + hex.EncodeToString(h[:])[:16]
		out, err := b.Deliver(ctx, t, txn, bot.Notice{Body: text})
		if err != nil {
			slog.Warn("an operator alert message failed", "error", err)
			continue
		}
		delivered = delivered || out == bot.Sent
	}
	return delivered
}
