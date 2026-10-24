package mail

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	gomail "github.com/wneessen/go-mail"

	"github.com/paisans-software/paisans-engagement-service/internal/config"
)

func configForTest(port int) config.SMTP {
	return config.SMTP{Host: "127.0.0.1", Port: port, From: "digest@example.org", FromName: "Talk", TLS: "none"}
}

// fakeSMTP accepts mail and keeps the DATA of each message. Each field of
// refuse, when set, is the reply that replaces success at that step; a reply
// to RCPT has the address appended, as real servers do.
type fakeSMTP struct {
	port   int
	mu     sync.Mutex
	data   []string
	refuse refusals
}

type refusals struct{ mail, rcpt, dataEnd, rset string }

func newFakeSMTP(t *testing.T, refuse refusals) *fakeSMTP {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	f := &fakeSMTP{port: l.Addr().(*net.TCPAddr).Port, refuse: refuse}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	fmt.Fprint(c, "220 test ESMTP\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			fmt.Fprint(c, "250-test\r\n250 OK\r\n")
		case strings.HasPrefix(cmd, "MAIL FROM:") && f.refuse.mail != "":
			fmt.Fprintf(c, "%s\r\n", f.refuse.mail)
		case strings.HasPrefix(cmd, "RCPT TO:") && f.refuse.rcpt != "":
			addr := strings.TrimSpace(line[len("RCPT TO:"):])
			fmt.Fprintf(c, "%s %s\r\n", f.refuse.rcpt, addr)
		case cmd == "RSET" && f.refuse.rset != "":
			fmt.Fprintf(c, "%s\r\n", f.refuse.rset)
		case cmd == "DATA":
			fmt.Fprint(c, "354 go\r\n")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			if f.refuse.dataEnd != "" {
				fmt.Fprintf(c, "%s\r\n", f.refuse.dataEnd)
				continue
			}
			f.mu.Lock()
			f.data = append(f.data, b.String())
			f.mu.Unlock()
			fmt.Fprint(c, "250 queued\r\n")
		case strings.HasPrefix(cmd, "QUIT"):
			fmt.Fprint(c, "221 bye\r\n")
			return
		default:
			fmt.Fprint(c, "250 OK\r\n")
		}
	}
}

func testMessage() Message {
	return Message{
		To: "bob@example.org", Subject: "Your week on Talk", Text: "plain body", HTML: "<p>html body</p>",
		Unsubscribe: "https://digest.example.org/unsubscribe?t=abc",
	}
}

func TestSendHeaders(t *testing.T) {
	f := newFakeSMTP(t, refusals{})
	s, err := NewSMTP(configForTest(f.port))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(t.Context(), testMessage()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.data) != 1 {
		t.Fatalf("got %d messages", len(f.data))
	}
	d := f.data[0]
	for _, want := range []string{
		"List-Unsubscribe: <https://digest.example.org/unsubscribe?t=abc>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"multipart/alternative",
		"text/plain", "text/html",
		"From: \"Talk\" <digest@example.org>",
		"To: <bob@example.org>",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("missing %q in:\n%s", want, d)
		}
	}
}

func TestSendFromWithoutName(t *testing.T) {
	f := newFakeSMTP(t, refusals{})
	c := configForTest(f.port)
	c.FromName = ""
	s, _ := NewSMTP(c)
	if err := s.Send(t.Context(), testMessage()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.data[0], "From: <digest@example.org>") {
		t.Fatalf("from:\n%s", f.data[0])
	}
}

func TestNewSMTPNeedsHost(t *testing.T) {
	if _, err := NewSMTP(config.SMTP{}); err == nil {
		t.Fatal("empty host accepted")
	}
}

func TestSanitizeDropsAddresses(t *testing.T) {
	for _, in := range []error{
		errors.New("550 <bob@example.org> no such user"),
		&gomail.SendError{Reason: gomail.ErrSMTPRcptTo},
	} {
		if got := sanitize(in).Error(); strings.Contains(got, "@") || strings.Contains(got, "example") {
			t.Fatalf("leaked: %q", got)
		}
	}
}

func TestSendErrorHasNoAddress(t *testing.T) {
	f := newFakeSMTP(t, refusals{rcpt: "550 5.1.1 no such user here:"})
	s, _ := NewSMTP(configForTest(f.port))
	err := s.Send(t.Context(), testMessage())
	if err == nil || strings.Contains(err.Error(), "@") || strings.Contains(err.Error(), "example") {
		t.Fatalf("Send leaked or succeeded: %v", err)
	}

	bad := testMessage()
	bad.To = "not an address@@"
	err = s.Send(t.Context(), bad)
	if err == nil || strings.Contains(err.Error(), "address@@") {
		t.Fatalf("%v", err)
	}
}

// A relay that cannot be reached took nothing, so the send is to be retried,
// and the error says the relay is down so the run stops trying others.
func TestRefusedConnectionIsNotSent(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	s, _ := NewSMTP(configForTest(port))
	err = s.Send(t.Context(), testMessage())
	if !errors.Is(err, ErrNotSent) || !errors.Is(err, ErrRelayUnavailable) {
		t.Fatalf("got %v, want ErrNotSent and ErrRelayUnavailable", err)
	}
}

// Every refusal before DATA leaves the message with the service; one at or
// after the end of DATA may have been delivered and is not to be retried.
func TestSendErrorClassification(t *testing.T) {
	for _, c := range []struct {
		name          string
		refuse        refusals
		notSent, down bool
	}{
		{"mail from", refusals{mail: "421 4.3.2 shutting down"}, true, true},
		{"rcpt to", refusals{rcpt: "450 4.2.1 try later:"}, true, false},
		{"rcpt to permanent", refusals{rcpt: "550 5.1.1 no such user here:"}, true, false},
		{"end of data", refusals{dataEnd: "554 5.7.1 rejected as spam"}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeSMTP(t, c.refuse)
			s, _ := NewSMTP(configForTest(f.port))
			err := s.Send(t.Context(), testMessage())
			if err == nil {
				t.Fatal("Send succeeded")
			}
			if errors.Is(err, ErrNotSent) != c.notSent || errors.Is(err, ErrRelayUnavailable) != c.down {
				t.Fatalf("%v: ErrNotSent %v, ErrRelayUnavailable %v; want %v, %v", err,
					errors.Is(err, ErrNotSent), errors.Is(err, ErrRelayUnavailable), c.notSent, c.down)
			}
			if strings.Contains(err.Error(), "@") {
				t.Fatalf("leaked: %v", err)
			}
			t.Log(err)
		})
	}
}

// A relay that accepted the message but fails the reset after it has the
// mail; a retry would send a second copy.
func TestResetAfterDeliveryIsSent(t *testing.T) {
	f := newFakeSMTP(t, refusals{rset: "421 4.3.2 going away"})
	s, _ := NewSMTP(configForTest(f.port))
	if err := s.Send(t.Context(), testMessage()); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.data) != 1 {
		t.Fatalf("relay holds %d messages", len(f.data))
	}
}

// A recipient address that cannot be parsed never reached the relay.
func TestBadRecipientIsNotSent(t *testing.T) {
	f := newFakeSMTP(t, refusals{})
	s, _ := NewSMTP(configForTest(f.port))
	m := testMessage()
	m.To = "not an address@@"
	err := s.Send(t.Context(), m)
	if !errors.Is(err, ErrNotSent) || errors.Is(err, ErrRelayUnavailable) {
		t.Fatalf("got %v", err)
	}
}
