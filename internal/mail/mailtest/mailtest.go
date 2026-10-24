// Package mailtest holds a Sender for tests of the packages that send mail.
package mailtest

import (
	"context"
	"sync"

	"github.com/paisans-software/paisans-engagement-service/internal/mail"
)

// Recorder keeps every message it is asked to send.
type Recorder struct {
	mu       sync.Mutex
	Messages []mail.Message
	// Err, when set, is returned from every Send and nothing is recorded.
	Err error
}

func (r *Recorder) Send(_ context.Context, m mail.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.Messages = append(r.Messages, m)
	return nil
}

// Sent is a copy of the messages recorded so far, safe to read while another
// goroutine sends.
func (r *Recorder) Sent() []mail.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mail.Message(nil), r.Messages...)
}
