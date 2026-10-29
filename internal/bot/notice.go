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

// maxTitles is the notice's lines for the main list, and maxDiscoverLines
// those for Discover, on top of them.
const (
	maxTitles        = 5
	maxDiscoverLines = 2
)

// Discover's headings, for a member who follows magazines and one who
// follows none.
const (
	discoverHeading      = "Busy where you don't follow:"
	discoverNudgeHeading = "Busy around the site:"
)

// titled is one line of the notice. A microblog post has no title, so Post
// has its text quoted instead, as the email does. Magazine is the magazine's
// name, shown on a Discover line.
type titled struct {
	Title, URL, Magazine string
	Post                 bool
}

// line is t as a plain text line.
func (t titled) line() string {
	s := "• " + t.Title
	if t.Post {
		s = "• \"" + t.Title + "\""
	}
	if t.Magazine != "" {
		s += " · !" + t.Magazine
	}
	return s
}

// stopLine ends the notice.
const stopLine = "Reply 'stop' to stop these messages."

// weeklyHTML separates the last lines with <br>, not paragraphs, so the blank
// lines show even in a client that gives paragraphs and lists no margins.
var weeklyHTML = template.Must(template.New("weekly").Parse(
	`<p>{{.Mark}}Your weekly digest from {{.Meta}}</p>` +
		`{{if .Items}}<ul>{{range .Items}}<li><a href="{{.URL}}">{{if .Post}}&ldquo;{{.Title}}&rdquo;{{else}}{{.Title}}{{end}}</a></li>{{end}}</ul>{{end}}` +
		`{{if .Discover}}<p>{{.DiscoverHeading}}</p><ul>{{range .Discover}}<li><a href="{{.URL}}">{{if .Post}}&ldquo;{{.Title}}&rdquo;{{else}}{{.Title}}{{end}}</a> · !{{.Magazine}}</li>{{end}}</ul>{{end}}` +
		`<br><a href="{{.Link}}">Read</a> the whole digest.<br><br>` + stopLine))

// previewMark starts a preview notice.
const previewMark = "[Preview] "

// Weekly is the Monday notice: up to five threads and posts, up to two from
// Discover, the link to the whole digest on the web, and how to stop.
func Weekly(d digest.Digest, meta, link string) Notice { return weekly(d, meta, link, "") }

// WeeklyPreview is the Monday notice marked as a preview.
func WeeklyPreview(d digest.Digest, meta, link string) Notice {
	return weekly(d, meta, link, previewMark)
}

func weekly(d digest.Digest, meta, link, mark string) Notice {
	var items []titled
	add := func(it digest.Item) {
		if len(items) < maxTitles {
			items = append(items, titled{Title: it.Title, URL: it.URL, Post: it.Key.Kind == digest.KindPost})
		}
	}
	if d.Featured != nil {
		add(d.Featured.Item)
	}
	for _, it := range d.More {
		add(it)
	}

	var discover []titled
	for _, it := range d.Discover[:min(len(d.Discover), maxDiscoverLines)] {
		discover = append(discover, titled{it.Title, it.URL, it.Magazine.Name, it.Key.Kind == digest.KindPost})
	}
	heading := discoverHeading
	if len(d.Following) == 0 {
		heading = discoverNudgeHeading
	}

	lines := []string{mark + "Your weekly digest from " + meta, ""}
	if len(items) > 0 {
		for _, it := range items {
			lines = append(lines, it.line())
		}
		lines = append(lines, "")
	}
	if len(discover) > 0 {
		lines = append(lines, heading)
		for _, it := range discover {
			lines = append(lines, it.line())
		}
		lines = append(lines, "")
	}
	lines = append(lines, "Read the whole digest: "+link, "", stopLine)

	var buf bytes.Buffer
	data := struct {
		Mark, Meta, Link, DiscoverHeading string
		Items, Discover                   []titled
	}{mark, meta, link, heading, items, discover}
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
