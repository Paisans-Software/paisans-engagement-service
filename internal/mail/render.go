package mail

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
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
// subject, heading and footer; Instance only in the button.
type Names struct{ Instance, Meta string }

// itemView is what the email shows of a thread: its title, link and
// magazine.
type itemView struct {
	Title, URL, Magazine string
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
}

// Render builds the message for one member. from and to are the window; the
// heading shows from through the last day the window covers.
func Render(d digest.Digest, n Names, siteURL, unsubscribeURL string, from, to time.Time) (Message, error) {
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

func toView(it digest.Item) itemView {
	return itemView{Title: it.Title, URL: it.URL, Magazine: "!" + it.Magazine.Name}
}
