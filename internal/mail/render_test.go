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
	if m.Subject != "This week on Talk · Example Community" {
		t.Fatalf("subject %q", m.Subject)
	}
	for _, body := range []string{m.HTML, m.Text} {
		if strings.Count(body, "This week on Talk · Example Community") < 1 ||
			!strings.Contains(body, "member of Talk · Example Community.") {
			t.Fatalf("meta missing in %s", body)
		}
		// The HTML part also carries it in its title.
		want := 2
		if body == m.HTML {
			want = 3
		}
		if strings.Count(body, "Talk · Example Community") != want {
			t.Fatalf("meta should appear in heading and footer (and the HTML title) only: %s", body)
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
	if strings.Contains(m.HTML+m.Text, "grinders") {
		t.Fatal("featured thread rendered")
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

// A microblog post has no title, so the email quotes its text and links
// the magazine line to it, featured, listed or unanswered.
func TestRenderPostQuoted(t *testing.T) {
	d := sample()
	d.Featured.Item.Key.Kind = digest.KindPost
	d.More[0].Key.Kind = digest.KindPost
	d.Unanswered = []digest.Item{item(4, "kettles")}
	d.Unanswered[0].Key.Kind = digest.KindPost
	d.UnansweredHeading = "New posts"
	m := render(t, d)
	if strings.Contains(m.Text, "microblog") || strings.Contains(m.HTML, "microblog") {
		t.Fatalf("post labelled:\n%s", m.Text)
	}
	for _, it := range []digest.Item{d.Featured.Item, d.More[0], d.Unanswered[0]} {
		if !strings.Contains(m.Text, "\n\""+it.Title+"\"\n!coffee\n"+it.URL+"\n") {
			t.Fatalf("text: %s not quoted:\n%s", it.Title, m.Text)
		}
		if !strings.Contains(m.HTML, "&ldquo;"+it.Title+"&rdquo;") ||
			!strings.Contains(m.HTML, `<a href="`+it.URL+`" style="color:#22aa66;text-decoration:none;font-weight:600;">!coffee</a>`) {
			t.Fatalf("html: %s not quoted:\n%s", it.Title, m.HTML)
		}
	}
	// The featured post still quotes its best comment after its own text.
	if !strings.Contains(m.Text, d.Featured.Item.URL+"\n\"hand grinders win\"\n") {
		t.Fatalf("featured comment excerpt:\n%s", m.Text)
	}
}

// Every item, featured, listed or unanswered, a returning thread too, shows
// its title and its magazine, and nothing
// it could be counted by. The featured thread quotes its excerpt after them.
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
		for _, bad := range []string{"comment", "vote", "joined in", "big one", "this week"} {
			if strings.Contains(body, bad) {
				t.Fatalf("%s: %q in:\n%s", name, bad, body)
			}
		}
		if strings.Count(body, "!coffee") != 4 {
			t.Fatalf("%s: magazines:\n%s", name, body)
		}
	}
	if !strings.Contains(m.Text, "new members\n\ngrinders\nhttps://talk.example.org/m/coffee/t/grinders\n!coffee\n\"hand grinders win\"\n\ngarden\n") {
		t.Fatalf("text layout:\n%s", m.Text)
	}
	// In the HTML too, the featured thread's magazine comes after its title.
	card := m.HTML[strings.Index(m.HTML, "#f4f7fb"):]
	if strings.Index(card, "!coffee") < strings.Index(card, ">grinders</a>") {
		t.Fatalf("magazine above the featured title:\n%s", card)
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

func renderPage(t *testing.T, d digest.Digest) string {
	t.Helper()
	h, err := RenderPage(d, nm, "https://talk.example.org/", from, to)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRenderPageHasNoUnsubscribe(t *testing.T) {
	h := renderPage(t, sample())
	for _, bad := range []string{"Unsubscribe", "/u/", "unsubscribe"} {
		if strings.Contains(h, bad) {
			t.Fatalf("page contains %q: %s", bad, h)
		}
	}
	for _, want := range []string{"grinders", "See everything on Talk", "<title>This week on Talk · Example Community</title>", `name="viewport"`} {
		if !strings.Contains(h, want) {
			t.Fatalf("page lacks %q: %s", want, h)
		}
	}
}

func TestRenderPageMatchesEmail(t *testing.T) {
	email := render(t, sample()).HTML
	const link = `<br><a href="https://digest.example.org/unsubscribe?t=abc" style="color:#999999;">Unsubscribe from the weekly digest</a>`
	if !strings.Contains(email, link) {
		t.Fatalf("email lacks the unsubscribe link: %s", email)
	}
	if got, want := renderPage(t, sample()), strings.Replace(email, link, "", 1); got != want {
		t.Fatalf("page differs from the email:\n%s\nwant\n%s", got, want)
	}
}

func withDiscover(following []digest.Magazine, more int) digest.Digest {
	d := sample()
	d.Discover = []digest.Item{item(6, "bikes"), item(7, "novels")}
	d.Discover[0].Magazine = digest.Magazine{ID: 2, Name: "cycling", Title: "Cycling"}
	d.Discover[1].Magazine = digest.Magazine{ID: 3, Name: "books", Title: "Books"}
	d.Following, d.FollowingMore = following, more
	return d
}

func TestRenderDiscover(t *testing.T) {
	d := withDiscover([]digest.Magazine{{Name: "baking", Title: "Baking"}, {Name: "gardening", Title: "Gardening"}}, 0)
	d.Unanswered = []digest.Item{item(4, "kettles")}
	d.UnansweredHeading = "No replies yet"
	m := render(t, d)
	for name, part := range map[string]string{"text": m.Text, "html": m.HTML} {
		head := strings.Index(part, "Around Talk this week")
		if head < 0 || strings.Count(part, "Around Talk this week") != 1 {
			t.Fatalf("%s: heading missing:\n%s", name, part)
		}
		if !strings.Contains(part, "You follow Baking and Gardening. Other magazines are busy too.") {
			t.Fatalf("%s: explainer missing:\n%s", name, part)
		}
		if strings.Index(part, d.More[1].URL) > head || strings.Index(part, "No replies yet") < head ||
			strings.Index(part, "See everything on") < head {
			t.Fatalf("%s: section out of place:\n%s", name, part)
		}
		for _, it := range d.Discover {
			if i := strings.Index(part, it.URL); i < head || i > strings.Index(part, "No replies yet") {
				t.Fatalf("%s: %s missing from the section:\n%s", name, it.URL, part)
			}
		}
	}
	if !strings.Contains(m.Text, "\nbikes\nhttps://talk.example.org/m/coffee/t/bikes\n!cycling\n") {
		t.Fatalf("text layout:\n%s", m.Text)
	}
}

func TestRenderDiscoverManyFollowed(t *testing.T) {
	d := withDiscover([]digest.Magazine{{Title: "A"}, {Title: "B"}, {Title: "C"}}, 2)
	m := render(t, d)
	if !strings.Contains(m.Text, "You follow A, B, C and 2 more. Other magazines are busy too.") {
		t.Fatalf("text:\n%s", m.Text)
	}
	d = withDiscover([]digest.Magazine{{Title: "A"}, {Title: "B"}, {Title: "C"}}, 0)
	if m := render(t, d); !strings.Contains(m.Text, "You follow A, B and C. Other") {
		t.Fatalf("text:\n%s", m.Text)
	}
	d = withDiscover([]digest.Magazine{{Name: "solo"}}, 0)
	if m := render(t, d); !strings.Contains(m.Text, "You follow solo. Other") {
		t.Fatalf("an untitled magazine is named by its name:\n%s", m.Text)
	}
}

func TestRenderDiscoverNudge(t *testing.T) {
	m := render(t, withDiscover(nil, 0))
	for name, part := range map[string]string{"text": m.Text, "html": m.HTML} {
		if !strings.Contains(part, "follow any magazines yet. Here") || strings.Contains(part, "You follow ") {
			t.Fatalf("%s: no nudge:\n%s", name, part)
		}
	}
	if !strings.Contains(m.Text, "You don't follow any magazines yet. Here's what's busy.") {
		t.Fatalf("text:\n%s", m.Text)
	}
}

// A digest without Discover, including one stored before it existed, has no
// trace of the section.
func TestRenderNoDiscover(t *testing.T) {
	m := render(t, sample())
	for _, bad := range []string{"Around Talk", "You follow", "follow any magazines"} {
		if strings.Contains(m.HTML+m.Text, bad) {
			t.Fatalf("empty section rendered %q", bad)
		}
	}
}

func TestRenderDiscoverEscapes(t *testing.T) {
	m := render(t, withDiscover([]digest.Magazine{{Title: "<b>x</b>"}}, 0))
	if strings.Contains(m.HTML, "<b>x</b>") || !strings.Contains(m.HTML, "&lt;b&gt;x&lt;/b&gt;") {
		t.Fatalf("html:\n%s", m.HTML)
	}
}
