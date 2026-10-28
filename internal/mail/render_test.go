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
		Featured: &digest.Featured{Item: item(1, "grinders")},
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
	if m.Subject != "This week on Talk · Example Community" {
		t.Fatalf("subject %q", m.Subject)
	}
	for _, body := range []string{m.HTML, m.Text} {
		if strings.Count(body, "This week on Talk · Example Community") < 1 ||
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
	m := render(t, d)
	if strings.Contains(m.HTML, "<script>") ||
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
	if strings.Contains(m.HTML+m.Text, "grinders") {
		t.Fatal("featured thread rendered")
	}
	if !strings.Contains(m.Text, "garden") || !strings.Contains(m.HTML, "148") {
		t.Fatal("list or stats missing")
	}
}

func TestRenderNoMore(t *testing.T) {
	d := sample()
	d.More = nil
	m := render(t, d)
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

func TestRenderPostLabelled(t *testing.T) {
	d := sample()
	d.More[0].Key.Kind = digest.KindPost
	m := render(t, d)
	if strings.Count(m.Text, "microblog") != 1 || strings.Count(m.HTML, "microblog") != 1 {
		t.Fatalf("label count wrong:\n%s", m.Text)
	}
}

// Every item, featured, listed or unanswered, a returning thread too, shows
// its title, its magazine and, for a post, the microblog label, and nothing
// it could be counted by.
func TestRenderNoFigures(t *testing.T) {
	d := sample()
	d.Featured.Item.Again, d.Featured.Item.NewComments = true, 2
	d.More[0].Again, d.More[0].NewComments = true, 1
	d.More[1].Comments = 0
	d.Unanswered = []digest.Item{item(4, "kettles")}
	d.UnansweredHeading = "New posts"
	m := render(t, d)
	for name, part := range map[string]string{"text": m.Text, "html": m.HTML} {
		body := part[strings.Index(part, "new members")+len("new members") : strings.Index(part, "See everything on")]
		for _, bad := range []string{"comment", "vote", "joined in", "big one", "this week", "&ldquo;"} {
			if strings.Contains(body, bad) {
				t.Fatalf("%s: %q in:\n%s", name, bad, body)
			}
		}
		if strings.Count(body, "!coffee") != 4 {
			t.Fatalf("%s: magazines:\n%s", name, body)
		}
	}
	if strings.Contains(m.Text, `"`) {
		t.Fatalf("text part quotes:\n%s", m.Text)
	}
	if !strings.Contains(m.Text, "\n!coffee\ngrinders\nhttps://talk.example.org/m/coffee/t/grinders\n\ngarden\n") {
		t.Fatalf("text layout:\n%s", m.Text)
	}
}

// The week's unanswered threads have a section of their own, after the list,
// under the heading the digest picked.
func TestRenderUnanswered(t *testing.T) {
	d := sample()
	d.Unanswered = []digest.Item{item(4, "kettles"), item(5, "beans")}
	d.Unanswered[1].Key.Kind = digest.KindPost
	d.UnansweredHeading = "Start the conversation"
	m := render(t, d)
	for name, part := range map[string]string{"text": m.Text, "html": m.HTML} {
		head := strings.Index(part, "Start the conversation")
		if head < 0 || strings.Count(part, "Start the conversation") != 1 {
			t.Fatalf("%s: heading missing:\n%s", name, part)
		}
		if strings.Index(part, d.More[1].URL) > head || strings.Index(part, "See everything on") < head {
			t.Fatalf("%s: section out of place:\n%s", name, part)
		}
		for _, it := range d.Unanswered {
			if i := strings.Index(part, it.URL); i < head {
				t.Fatalf("%s: %s missing from the section:\n%s", name, it.URL, part)
			}
		}
		section := part[head:strings.Index(part, "See everything on")]
		if strings.Count(section, "microblog") != 1 {
			t.Fatalf("%s: post not labelled:\n%s", name, section)
		}
	}
	if !strings.Contains(m.Text, "\nkettles\nhttps://talk.example.org/m/coffee/t/kettles\n!coffee\n") {
		t.Fatalf("text layout:\n%s", m.Text)
	}
}

func TestRenderNoUnanswered(t *testing.T) {
	m := render(t, sample())
	if strings.Contains(m.HTML+m.Text, "New posts") || strings.Contains(m.HTML+m.Text, "No replies") ||
		strings.Contains(m.HTML+m.Text, "Start the conversation") {
		t.Fatal("empty section rendered")
	}
}
