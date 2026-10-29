// Package mail renders the weekly digest and sends it.
package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	gomail "github.com/wneessen/go-mail"

	"github.com/paisans-software/paisans-engagement-service/internal/config"
)

// Message is one rendered email for one member. Unsubscribe is the absolute
// URL behind both the footer link and the List-Unsubscribe headers; an
// operator alert has none, and then no such headers.
type Message struct {
	To, Subject, Text, HTML, Unsubscribe string
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

type smtpSender struct {
	c    config.SMTP
	opts []gomail.Option
}

// NewSMTP builds a sender. The service does not call it without a mail host,
// so an empty one is an error.
func NewSMTP(c config.SMTP) (Sender, error) {
	if c.Host == "" {
		return nil, errors.New("no mail host configured")
	}
	opts := []gomail.Option{gomail.WithPort(c.Port)}
	switch c.TLS {
	case "none":
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	case "starttls":
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	case "tls":
		opts = append(opts, gomail.WithSSL())
	}
	if c.User != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthPlain), gomail.WithUsername(c.User), gomail.WithPassword(c.Password))
	}
	return &smtpSender{c: c, opts: opts}, nil
}

// ErrNotSent marks a send whose message never reached the relay, so trying
// again cannot mail the member twice. ErrRelayUnavailable, which is also an
// ErrNotSent, says the relay or the service's own mail setup is at fault
// rather than this one message, so the next member would fare no better.
var (
	ErrNotSent          = errors.New("mail not handed over")
	ErrRelayUnavailable = fmt.Errorf("mail relay unavailable: %w", ErrNotSent)
)

// Send errors are fixed text: go-mail's own errors carry recipient addresses
// and the server's reply, which must not reach a log.
func (s *smtpSender) Send(ctx context.Context, msg Message) error {
	client, err := gomail.NewClient(s.c.Host, s.opts...)
	if err != nil {
		return fmt.Errorf("%w: the mail client could not be set up", ErrRelayUnavailable)
	}
	m := gomail.NewMsg()
	if s.c.FromName != "" {
		err = m.FromFormat(s.c.FromName, s.c.From)
	} else {
		err = m.From(s.c.From)
	}
	if err != nil {
		return fmt.Errorf("%w: sender address rejected", ErrRelayUnavailable)
	}
	if err := m.To(msg.To); err != nil {
		return fmt.Errorf("%w: recipient address rejected", ErrNotSent)
	}
	m.Subject(msg.Subject)
	m.SetBodyString(gomail.TypeTextPlain, msg.Text)
	m.AddAlternativeString(gomail.TypeTextHTML, msg.HTML)
	// One click unsubscribe, so mail clients can offer their own button.
	if msg.Unsubscribe != "" {
		m.SetGenHeader("List-Unsubscribe", "<"+msg.Unsubscribe+">")
		m.SetGenHeader("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	}

	// Dial, send and close are separate steps, so a failure to connect is
	// told apart from one after the message was handed over, and a failed
	// QUIT does not turn a delivered message into an error.
	conn, err := client.DialToSMTPClientWithContext(ctx)
	if err != nil {
		return fmt.Errorf("%w: could not connect, start TLS or log in", ErrRelayUnavailable)
	}
	defer func() {
		if err := client.CloseWithSMTPClient(conn); err != nil {
			slog.Warn("mail relay connection not closed cleanly")
		}
	}()
	if err := client.SendWithSMTPClient(conn, m); err != nil {
		var se *gomail.SendError
		if errors.As(err, &se) && se.Reason == gomail.ErrSMTPReset {
			// go-mail resets only after the relay accepted the message.
			slog.Warn("mail sent; the relay failed the reset after it", "subject", msg.Subject)
			return nil
		}
		return sanitize(err)
	}
	slog.Info("mail sent", "subject", msg.Subject)
	return nil
}

// sanitize reduces a send error to a reason and a numeric code, and says
// whether the message was handed over. Anything at DATA or after, or an error
// it cannot place, may have been delivered and is not marked ErrNotSent.
func sanitize(err error) error {
	var se *gomail.SendError
	if !errors.As(err, &se) {
		return errors.New("send mail: delivery failed")
	}
	switch se.Reason {
	case gomail.ErrConnCheck, gomail.ErrSMTPMailFrom, gomail.ErrGetSender, gomail.ErrNoUnencoded:
		return fmt.Errorf("%w: %s (code %d)", ErrRelayUnavailable, se.Reason, se.ErrorCode())
	case gomail.ErrSMTPRcptTo, gomail.ErrGetRcpts:
		return fmt.Errorf("%w: %s (code %d)", ErrNotSent, se.Reason, se.ErrorCode())
	}
	return fmt.Errorf("send mail: %s (code %d)", se.Reason, se.ErrorCode())
}
