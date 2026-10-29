package mail

import (
	"bytes"
	"cmp"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/digest"
)

//go:embed templates/digest.html templates/digest.txt
var files embed.FS

var (
	htmlTmpl = htmltemplate.Must(htmltemplate.ParseFS(files, "templates/digest.html"))
	textTmpl = texttemplate.Must(texttemplate.ParseFS(files, "templates/digest.txt"))
)

// Names are the community's two titles. Meta names the community in the
// subject, heading and footer; Instance the button and the Discover heading.
type Names struct{ Instance, Meta string }

// itemView is what the email shows of a thread: its title, link and
// magazine. A microblog post has no title, so Post has the email quote its
// text and link the magazine line to it instead.
type itemView struct {
	Title, URL, Magazine string
	Post                 bool
}

type featuredView struct {
	itemView
	Excerpt string
}

type view struct {
	Meta, Instance, SiteURL, Unsubscribe, Dates string
	Stats                                       digest.Stats
	Featured                                    *featuredView
	More                                        []itemView
	Unanswered                                  []itemView
	UnansweredHeading                           string
	Discover                                    []itemView
	DiscoverHeading, DiscoverLine               string
}

// discoverNudge is the Discover line for a member who follows nothing.
const discoverNudge = "You don't follow any magazines yet. Here's what's busy."

func discoverHeading(n Names) string { return "Around " + n.Instance + " this week" }

// discoverLine names the magazines the member follows, the first few and
// how many more, or nudges them to follow one.
func discoverLine(following []digest.Magazine, more int) string {
	if len(following) == 0 {
		return discoverNudge
	}
	var names []string
	for _, m := range following {
		names = append(names, cmp.Or(m.Title, m.Name))
	}
	if more > 0 {
		names = append(names, fmt.Sprintf("%d more", more))
	}
	list := names[0]
	if n := len(names); n > 1 {
		list = strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	}
	return "You follow " + list + ". Other magazines are busy too."
}

// Render builds the message for one member. from and to are the window; the
// heading shows from through the last day the window covers.
func Render(d digest.Digest, n Names, siteURL, unsubscribeURL string, from, to time.Time) (Message, error) {
	v := newView(d, n, siteURL, unsubscribeURL, from, to)
	var html, text bytes.Buffer
	if err := htmlTmpl.Execute(&html, v); err != nil {
		return Message{}, fmt.Errorf("render html: %w", err)
	}
	if err := textTmpl.Execute(&text, v); err != nil {
		return Message{}, fmt.Errorf("render text: %w", err)
	}
	return Message{
		Subject:     "This week on " + n.Meta,
		Text:        text.String(),
		HTML:        html.String(),
		Unsubscribe: unsubscribeURL,
	}, nil
}

// RenderPage builds the email's HTML without the unsubscribe link, for the
// digest's page on the web.
func RenderPage(d digest.Digest, n Names, siteURL string, from, to time.Time) (string, error) {
	var html bytes.Buffer
	if err := htmlTmpl.Execute(&html, newView(d, n, siteURL, "", from, to)); err != nil {
		return "", fmt.Errorf("render html: %w", err)
	}
	return html.String(), nil
}

func newView(d digest.Digest, n Names, siteURL, unsubscribeURL string, from, to time.Time) view {
	v := view{
		Meta: n.Meta, Instance: n.Instance, SiteURL: siteURL, Unsubscribe: unsubscribeURL,
		Dates: from.Format("Jan 2") + " – " + to.AddDate(0, 0, -1).Format("Jan 2"),
		Stats: d.Stats, UnansweredHeading: d.UnansweredHeading,
	}
	if f := d.Featured; f != nil {
		v.Featured = &featuredView{itemView: toView(f.Item), Excerpt: f.Excerpt}
	}
	for _, it := range d.More {
		v.More = append(v.More, toView(it))
	}
	for _, it := range d.Unanswered {
		v.Unanswered = append(v.Unanswered, toView(it))
	}
	if len(d.Discover) > 0 {
		v.DiscoverHeading = discoverHeading(n)
		v.DiscoverLine = discoverLine(d.Following, d.FollowingMore)
		for _, it := range d.Discover {
			v.Discover = append(v.Discover, toView(it))
		}
	}
	return v
}

func toView(it digest.Item) itemView {
	return itemView{Title: it.Title, URL: it.URL, Magazine: "!" + it.Magazine.Name, Post: it.Key.Kind == digest.KindPost}
}
