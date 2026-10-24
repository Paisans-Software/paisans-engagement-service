package mail

import (
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/digest"
)

var (
	from = time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	to   = time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	nm   = Names{Instance: "Talk", Meta: "Talk · Example Community"}
)

func item(id int64, title string) digest.Item {
	return digest.Item{
		Key: digest.Key{Kind: digest.KindEntry, ID: id}, Title: title,
		URL:      "https://talk.example.org/m/coffee/t/" + title,
		Magazine: digest.Magazine{ID: 1, Name: "coffee"}, Comments: 4, Participants: 3, Score: 9,
	}
}

func sample() digest.Digest {
	return digest.Digest{
		Stats:    digest.Stats{NewThreads: 12, Comments: 148, NewMembers: 3},
		Featured: &digest.Featured{Item: item(1, "grinders"), Excerpt: "hand grinders win"},
		More:     []digest.Item{item(2, "garden"), item(3, "meetup")},
	}
}

func render(t *testing.T, d digest.Digest) Message {
	t.Helper()
	m, err := Render(d, nm, "https://talk.example.org/", "https://digest.example.org/unsubscribe?t=abc", from, to)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRenderNames(t *testing.T) {
	m := render(t, sample())
	if m.Subject != "Your week on Talk · Example Community" {
		t.Fatalf("subject %q", m.Subject)
	}
	for _, body := range []string{m.HTML, m.Text} {
		if strings.Count(body, "Your week on Talk · Example Community") < 1 ||
			!strings.Contains(body, "member of Talk · Example Community.") {
			t.Fatalf("meta missing in %s", body)
		}
		if strings.Count(body, "Talk · Example Community") != 2 {
			t.Fatalf("meta should appear in heading and footer only: %s", body)
		}
		if strings.Count(body, "See everything on Talk") != 1 {
			t.Fatalf("button missing: %s", body)
		}
		// Instance appears only in the button.
		if strings.Count(strings.ReplaceAll(body, "Talk · Example Community", ""), "Talk") != 1 {
			t.Fatalf("instance outside the button: %s", body)
		}
	}
}

func TestRenderDates(t *testing.T) {
	m := render(t, sample())
	if !strings.Contains(m.HTML, "Oct 12 – Oct 18") || !strings.Contains(m.Text, "Oct 12 – Oct 18") {
		t.Fatalf("dates: %s", m.Text)
	}
}

func TestRenderEscapes(t *testing.T) {
	d := sample()
	d.Featured.Item.Title = "<script>alert(1)</script>"
	d.Featured.Excerpt = "<b>x</b>"
	m := render(t, d)
	if strings.Contains(m.HTML, "<script>") || strings.Contains(m.HTML, "<b>x</b>") ||
		!strings.Contains(m.HTML, "&lt;script&gt;") {
		t.Fatalf("not escaped: %s", m.HTML)
	}
}

// Every field that comes from Mbin is escaped, in the list as in the featured
// thread, and a URL that is not a web address never becomes a link.
func TestRenderEscapesListAndURLs(t *testing.T) {
	d := sample()
	d.Featured.Item.Magazine.Name = "<b>mag</b>"
	d.More[0].Title = "<b>listed</b>"
	d.More[1].URL = "javascript:alert(1)"
	m := render(t, d)
	for _, bad := range []string{"<b>", "javascript:"} {
		if strings.Contains(m.HTML, bad) {
			t.Fatalf("%q in:\n%s", bad, m.HTML)
		}
	}
	for _, want := range []string{"!&lt;b&gt;mag&lt;/b&gt;", "&lt;b&gt;listed&lt;/b&gt;", `href="#ZgotmplZ"`} {
		if !strings.Contains(m.HTML, want) {
			t.Fatalf("missing %q in:\n%s", want, m.HTML)
		}
	}
}

func TestRenderNoFeatured(t *testing.T) {
	d := sample()
	d.Featured = nil
	m := render(t, d)
	if strings.Contains(m.HTML+m.Text, "the big one") {
		t.Fatal("featured label rendered")
	}
	if !strings.Contains(m.Text, "garden") || !strings.Contains(m.HTML, "148") {
		t.Fatal("list or stats missing")
	}
}

func TestRenderNoExcerptNoMore(t *testing.T) {
	d := sample()
	d.Featured.Excerpt = ""
	d.More = nil
	m := render(t, d)
	if strings.Contains(m.HTML, "&ldquo;") || strings.Contains(m.Text, `"`) {
		t.Fatal("empty excerpt rendered")
	}
	if !strings.Contains(m.HTML, "new members") || !strings.Contains(m.Text, "3 new members") {
		t.Fatal("stats row missing")
	}
}

func TestRenderTextPartHasEveryLink(t *testing.T) {
	d := sample()
	m := render(t, d)
	for _, u := range []string{
		d.Featured.Item.URL, d.More[0].URL, d.More[1].URL,
		"https://talk.example.org/", "https://digest.example.org/unsubscribe?t=abc",
	} {
		if !strings.Contains(m.Text, u) {
			t.Fatalf("text part lacks %s:\n%s", u, m.Text)
		}
		if !strings.Contains(m.HTML, u) {
			t.Fatalf("html part lacks %s", u)
		}
	}
}

func TestRenderFigure(t *testing.T) {
	d := sample()
	d.More[0].Comments = 0
	d.More[0].Score = 31
	d.More[1].Comments = 1
	m := render(t, d)
	for _, w := range []string{"31 votes", "1 comment", "4 comments", "3 people joined in"} {
		if !strings.Contains(m.Text, w) {
			t.Fatalf("missing %q:\n%s", w, m.Text)
		}
	}
	if strings.Contains(m.Text, "1 comments") {
		t.Fatal("bad plural")
	}
}

func TestRenderPostLabelled(t *testing.T) {
	d := sample()
	d.More[0].Key.Kind = digest.KindPost
	m := render(t, d)
	if strings.Count(m.Text, "microblog") != 1 || strings.Count(m.HTML, "microblog") != 1 {
		t.Fatalf("label count wrong:\n%s", m.Text)
	}
}

// A thread shown before is counted by this week's comments, in the featured
// slot and in the list.
func TestRenderReturningFigure(t *testing.T) {
	d := sample()
	d.Featured.Item.Again, d.Featured.Item.NewComments = true, 2
	d.More[0].Again, d.More[0].NewComments = true, 1
	m := render(t, d)
	for _, part := range []string{m.Text, m.HTML} {
		for _, w := range []string{"2 new comments this week · 3 people joined in", "1 new comment this week", "4 comments"} {
			if !strings.Contains(part, w) {
				t.Fatalf("missing %q:\n%s", w, part)
			}
		}
	}
}
