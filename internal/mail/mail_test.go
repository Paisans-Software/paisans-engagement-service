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

// fakeSMTP accepts mail, keeps the DATA of each message, and refuses
// recipients with a reply that names the address when reject is set.
type fakeSMTP struct {
	port   int
	mu     sync.Mutex
	data   []string
	reject bool
}

func newFakeSMTP(t *testing.T, reject bool) *fakeSMTP {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	f := &fakeSMTP{port: l.Addr().(*net.TCPAddr).Port, reject: reject}
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
		case strings.HasPrefix(cmd, "RCPT TO:") && f.reject:
			addr := strings.TrimSpace(line[len("RCPT TO:"):])
			fmt.Fprintf(c, "550 5.1.1 %s: no such user here\r\n", addr)
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
	f := newFakeSMTP(t, false)
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
	f := newFakeSMTP(t, false)
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
	f := newFakeSMTP(t, true)
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
