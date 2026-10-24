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

// Message is one rendered digest for one member. Unsubscribe is the absolute
// URL behind both the footer link and the List-Unsubscribe headers.
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

// Send errors are fixed text: go-mail's own errors carry recipient addresses
// and the server's reply, which must not reach a log.
func (s *smtpSender) Send(ctx context.Context, msg Message) error {
	client, err := gomail.NewClient(s.c.Host, s.opts...)
	if err != nil {
		return errors.New("mail client could not be set up")
	}
	m := gomail.NewMsg()
	if s.c.FromName != "" {
		err = m.FromFormat(s.c.FromName, s.c.From)
	} else {
		err = m.From(s.c.From)
	}
	if err != nil {
		return errors.New("sender address rejected")
	}
	if err := m.To(msg.To); err != nil {
		return errors.New("recipient address rejected")
	}
	m.Subject(msg.Subject)
	m.SetBodyString(gomail.TypeTextPlain, msg.Text)
	m.AddAlternativeString(gomail.TypeTextHTML, msg.HTML)
	// One click unsubscribe, so mail clients can offer their own button.
	m.SetGenHeader("List-Unsubscribe", "<"+msg.Unsubscribe+">")
	m.SetGenHeader("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return sanitize(err)
	}
	slog.Info("mail sent", "subject", msg.Subject)
	return nil
}

// sanitize reduces a send error to a reason and a numeric code.
func sanitize(err error) error {
	var se *gomail.SendError
	if errors.As(err, &se) {
		return fmt.Errorf("send mail: %s (code %d)", se.Reason, se.ErrorCode())
	}
	return errors.New("send mail: delivery failed")
}
