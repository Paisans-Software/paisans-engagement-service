package bot

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"log/slog"
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
	ReplyHelp    = "Reply 'stop' to stop these messages, or start to get them again."
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

const maxTitles = 5

// titled is one line of the notice. A microblog post has no title, so Post
// has its text quoted instead, as the email does.
type titled struct {
	Title, URL string
	Post       bool
}

// stopLine ends the notice.
const stopLine = "Reply 'stop' to stop these messages."

// weeklyHTML separates the last lines with <br>, not paragraphs, so the blank
// lines show even in a client that gives paragraphs and lists no margins.
var weeklyHTML = template.Must(template.New("weekly").Parse(
	`<p>{{.Mark}}Your weekly digest from {{.Meta}}</p>` +
		`{{if .Items}}<ul>{{range .Items}}<li><a href="{{.URL}}">{{if .Post}}&ldquo;{{.Title}}&rdquo;{{else}}{{.Title}}{{end}}</a></li>{{end}}</ul>{{end}}` +
		`<br><a href="{{.Link}}">Read</a> the whole digest.<br><br>` + stopLine))

// previewMark starts a preview notice.
const previewMark = "[Preview] "

// Weekly is the Monday notice: up to five threads and posts, the link to the
// whole digest on the web, and how to stop.
func Weekly(d digest.Digest, meta, link string) Notice { return weekly(d, meta, link, "") }

// WeeklyPreview is the Monday notice marked as a preview.
func WeeklyPreview(d digest.Digest, meta, link string) Notice {
	return weekly(d, meta, link, previewMark)
}

func weekly(d digest.Digest, meta, link, mark string) Notice {
	var items []titled
	add := func(it digest.Item) {
		if len(items) < maxTitles {
			items = append(items, titled{it.Title, it.URL, it.Key.Kind == digest.KindPost})
		}
	}
	if d.Featured != nil {
		add(d.Featured.Item)
	}
	for _, it := range d.More {
		add(it)
	}

	lines := []string{mark + "Your weekly digest from " + meta, ""}
	if len(items) > 0 {
		for _, it := range items {
			if it.Post {
				lines = append(lines, "• \""+it.Title+"\"")
			} else {
				lines = append(lines, "• "+it.Title)
			}
		}
		lines = append(lines, "")
	}
	lines = append(lines, "Read the whole digest: "+link, "", stopLine)

	var buf bytes.Buffer
	data := struct {
		Mark, Meta, Link string
		Items            []titled
	}{mark, meta, link, items}
	if err := weeklyHTML.Execute(&buf, data); err != nil {
		slog.Warn("matrix bot: the notice's HTML could not be rendered; it goes out as plain text", "error", err)
		buf.Reset()
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

// PreviewTxnID is a new transaction id for a preview notice. Each preview has
// its own, so a second preview is delivered too, and none is ever a week's
// TxnID.
func PreviewTxnID() string { return "preview-" + rand.Text() }
