package bot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"strings"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/digest"
)

// Notice is one m.notice message: a plain body and, when it has one, HTML.
type Notice struct{ Body, FormattedBody string }

// Replies to a member's command.
const (
	ReplyStopped = "Stopped. Reply start to get them again."
	ReplyStarted = "Started. Your next digest comes on Monday."
	ReplyHelp    = "Reply stop to stop these messages, or start to get them again."
)

// Content is the message content to send.
func (n Notice) Content() map[string]any {
	c := map[string]any{"msgtype": "m.notice", "body": n.Body}
	if n.FormattedBody != "" {
		c["format"] = "org.matrix.custom.html"
		c["formatted_body"] = n.FormattedBody
	}
	return c
}

// Reply is a plain notice.
func Reply(text string) Notice { return Notice{Body: text} }

const maxTitles = 3

type titled struct{ Title, URL string }

var weeklyHTML = template.Must(template.New("weekly").Parse(
	`<p>Your weekly digest from {{.Meta}}</p>` +
		`{{if .Items}}<ul>{{range .Items}}<li><a href="{{.URL}}">{{.Title}}</a></li>{{end}}</ul>{{end}}` +
		`<p>Read the whole digest: <a href="{{.Link}}">{{.Link}}</a></p>` +
		`<p>Reply stop to stop these messages.</p>`))

// Weekly is the Monday notice: up to three thread titles, the link to the
// whole digest on the web, and how to stop.
func Weekly(d digest.Digest, meta, link string) Notice {
	var items []titled
	add := func(it digest.Item) {
		if it.Key.Kind != digest.KindPost && len(items) < maxTitles {
			items = append(items, titled{it.Title, it.URL})
		}
	}
	if d.Featured != nil {
		add(d.Featured.Item)
	}
	for _, it := range d.More {
		add(it)
	}

	lines := []string{"Your weekly digest from " + meta, ""}
	if len(items) > 0 {
		for _, it := range items {
			lines = append(lines, "• "+it.Title)
		}
		lines = append(lines, "")
	}
	lines = append(lines, "Read the whole digest: "+link, "", "Reply stop to stop these messages.")

	var buf bytes.Buffer
	data := struct {
		Meta, Link string
		Items      []titled
	}{meta, link, items}
	if err := weeklyHTML.Execute(&buf, data); err != nil {
		buf.Reset() // the plain body still goes out
	}
	return Notice{Body: strings.Join(lines, "\n"), FormattedBody: buf.String()}
}

// TxnID is the transaction id for a member's notice in a week: the same
// member and week always give the same id, and the id does not reveal the
// subject.
func TxnID(subject string, week time.Time) string {
	h := sha256.Sum256([]byte(subject))
	return "digest-" + week.Format("2006-01-02") + "-" + hex.EncodeToString(h[:])[:16]
}
