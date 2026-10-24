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

type itemView struct {
	Title, URL, Magazine, Label, Figure string
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
}

// Render builds the message for one member. from and to are the window; the
// heading shows from through the last day the window covers.
func Render(d digest.Digest, n Names, siteURL, unsubscribeURL string, from, to time.Time) (Message, error) {
	v := view{
		Meta: n.Meta, Instance: n.Instance, SiteURL: siteURL, Unsubscribe: unsubscribeURL,
		Dates: from.Format("Jan 2") + " – " + to.AddDate(0, 0, -1).Format("Jan 2"),
		Stats: d.Stats,
	}
	if f := d.Featured; f != nil {
		fv := featuredView{itemView: toView(f.Item), Excerpt: f.Excerpt}
		fv.Figure = fmt.Sprintf("%s · %s", plural(f.Item.Comments, "comment", "comments"), plural(f.Item.Participants, "person joined in", "people joined in"))
		v.Featured = &fv
	}
	for _, it := range d.More {
		v.More = append(v.More, toView(it))
	}

	var html, text bytes.Buffer
	if err := htmlTmpl.Execute(&html, v); err != nil {
		return Message{}, fmt.Errorf("render html: %w", err)
	}
	if err := textTmpl.Execute(&text, v); err != nil {
		return Message{}, fmt.Errorf("render text: %w", err)
	}
	return Message{
		Subject:     "Your week on " + n.Meta,
		Text:        text.String(),
		HTML:        html.String(),
		Unsubscribe: unsubscribeURL,
	}, nil
}

// toView gives a list item its one figure: the comment count when there are
// comments, else the score.
func toView(it digest.Item) itemView {
	iv := itemView{Title: it.Title, URL: it.URL, Magazine: "!" + it.Magazine.Name}
	if it.Key.Kind == digest.KindPost {
		iv.Label = "microblog"
	}
	if it.Comments > 0 {
		iv.Figure = plural(it.Comments, "comment", "comments")
	} else {
		iv.Figure = plural(it.Score, "vote", "votes")
	}
	return iv
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
