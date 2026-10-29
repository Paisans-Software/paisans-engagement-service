package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-engagement-service/internal/digest"
)

func item(kind digest.Kind, title, url string) digest.Item {
	return digest.Item{Key: digest.Key{Kind: kind, ID: 1}, Title: title, URL: url}
}

func TestWeeklyBody(t *testing.T) {
	d := digest.Digest{
		Featured: &digest.Featured{Item: item(digest.KindEntry, "Grinders", "https://t/1")},
		More: []digest.Item{
			item(digest.KindPost, "A post", "https://t/2"),
			item(digest.KindEntry, "Beans", "https://t/3"),
			item(digest.KindEntry, "Kettles", "https://t/4"),
			item(digest.KindEntry, "Filters", "https://t/5"),
			item(digest.KindEntry, "Scales", "https://t/6"),
		},
	}
	n := Weekly(d, "Talk · Example", "https://talk", "https://web/d/tok")
	want := "This week on Talk · Example\n\n• Grinders\n  https://t/1\n• \"A post\"\n  https://t/2\n• Beans\n  https://t/3\n" +
		"• Kettles\n  https://t/4\n• Filters\n  https://t/5\n\n" +
		"Read the whole digest: https://web/d/tok\n\nReply 'stop' to stop these messages."
	if n.Body != want {
		t.Fatalf("body = %q", n.Body)
	}
	wantHTML := `This week on Talk · Example<br><br><ul>` +
		`<li><a href="https://t/1">Grinders</a></li><li><a href="https://t/2">&ldquo;A post&rdquo;</a></li>` +
		`<li><a href="https://t/3">Beans</a></li><li><a href="https://t/4">Kettles</a></li>` +
		`<li><a href="https://t/5">Filters</a></li></ul>` +
		`<br><a href="https://web/d/tok">Read</a> the whole digest.<br><br>Reply 'stop' to stop these messages.`
	if n.FormattedBody != wantHTML {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
	if !strings.Contains(n.FormattedBody, `<li><a href="https://t/3">Beans</a></li>`) ||
		!strings.Contains(n.FormattedBody, `<li><a href="https://t/2">&ldquo;A post&rdquo;</a></li>`) ||
		strings.Contains(n.FormattedBody, "Scales") {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
}

// A microblog post has no title, so the notice quotes it, as the email does.
func TestWeeklyOnlyPosts(t *testing.T) {
	d := digest.Digest{More: []digest.Item{item(digest.KindPost, "P", "u")}}
	n := Weekly(d, "Talk · Example", "https://talk", "L")
	want := "This week on Talk · Example\n\n• \"P\"\n  u\n\nRead the whole digest: L\n\nReply 'stop' to stop these messages."
	if n.Body != want {
		t.Fatalf("body = %q", n.Body)
	}
	if !strings.Contains(n.FormattedBody, `<li><a href="u">&ldquo;P&rdquo;</a></li>`) {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
}

func TestWeeklyEmpty(t *testing.T) {
	n := Weekly(digest.Digest{}, "Talk · Example", "https://talk", "L")
	want := "This week on Talk · Example\n\nRead the whole digest: L\n\nReply 'stop' to stop these messages."
	wantHTML := `This week on Talk · Example<br><br>` +
		`<a href="L">Read</a> the whole digest.<br><br>Reply 'stop' to stop these messages.`
	if n.Body != want || n.FormattedBody != wantHTML {
		t.Fatalf("notice = %q / %q", n.Body, n.FormattedBody)
	}
}

func TestNoticeEscapesTitles(t *testing.T) {
	title := `<b>Tom & Jerry</b> [x](y)`
	n := Weekly(digest.Digest{Featured: &digest.Featured{Item: item(digest.KindEntry, title, "https://t/?a=1&b=2")}}, "M <i>", "https://talk", "L")
	if !strings.Contains(n.FormattedBody, "&lt;b&gt;Tom &amp; Jerry&lt;/b&gt;") || strings.Contains(n.FormattedBody, "<b>") ||
		strings.Contains(n.FormattedBody, "<i>") {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
	if !strings.Contains(n.Body, title) {
		t.Fatalf("body = %q", n.Body)
	}
}

func onMag(it digest.Item, name string) digest.Item {
	it.Magazine = digest.Magazine{ID: int64(len(name)), Name: name}
	return it
}

func discoverDigest(following []digest.Magazine) digest.Digest {
	return digest.Digest{
		Featured: &digest.Featured{Item: item(digest.KindEntry, "Grinders", "https://t/1")},
		More:     []digest.Item{item(digest.KindEntry, "Beans", "https://t/2")},
		Discover: []digest.Item{
			onMag(item(digest.KindEntry, "Bikes", "https://t/7"), "cycling"),
			onMag(item(digest.KindPost, "A post", "https://t/8"), "books"),
			onMag(item(digest.KindEntry, "Pots", "https://t/9"), "pottery"),
		},
		Following: following,
	}
}

func TestWeeklyDiscover(t *testing.T) {
	n := Weekly(discoverDigest([]digest.Magazine{{ID: 1, Name: "coffee"}}), "Talk · Example", "https://talk", "https://web/d/tok")
	want := "This week on Talk · Example\n\n• Grinders\n  https://t/1\n• Beans\n  https://t/2\n\n" +
		"Active where you don't follow:\n• Bikes · !cycling\n  https://t/7\n• \"A post\" · !books\n  https://t/8\n\n" +
		"Read the whole digest: https://web/d/tok\n\nReply 'stop' to stop these messages."
	if n.Body != want {
		t.Fatalf("body = %q", n.Body)
	}
	wantHTML := `This week on Talk · Example<br><br><ul>` +
		`<li><a href="https://t/1">Grinders</a></li><li><a href="https://t/2">Beans</a></li></ul>` +
		`<br>Active where you don&#39;t follow:<ul>` +
		`<li><a href="https://t/7">Bikes</a> · <a href="https://talk/m/cycling">!cycling</a></li>` +
		`<li><a href="https://t/8">&ldquo;A post&rdquo;</a> · <a href="https://talk/m/books">!books</a></li></ul>` +
		`<br><a href="https://web/d/tok">Read</a> the whole digest.<br><br>Reply 'stop' to stop these messages.`
	if n.FormattedBody != wantHTML {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
}

func TestWeeklyDiscoverNudge(t *testing.T) {
	n := Weekly(discoverDigest(nil), "Talk · Example", "https://talk/", "L")
	if !strings.Contains(n.FormattedBody, `<a href="https://talk/m/cycling">!cycling</a>`) {
		t.Fatalf("magazine link: %q", n.FormattedBody)
	}
	if !strings.Contains(n.Body, "\n\nActive around the site:\n• Bikes · !cycling\n  https://t/7\n") ||
		!strings.Contains(n.FormattedBody, "<br>Active around the site:<ul>") {
		t.Fatalf("notice = %q / %q", n.Body, n.FormattedBody)
	}
}

// Discover's lines come on top of the main list's five.
func TestWeeklyDiscoverOnTopOfFive(t *testing.T) {
	d := discoverDigest(nil)
	for _, title := range []string{"Kettles", "Filters", "Scales", "Cups"} {
		d.More = append(d.More, item(digest.KindEntry, title, "https://t/"+title))
	}
	n := Weekly(d, "Talk · Example", "https://talk", "L")
	for _, want := range []string{"Grinders", "Beans", "Kettles", "Filters", "Scales", "Bikes", "A post"} {
		if !strings.Contains(n.Body, want) {
			t.Fatalf("body lacks %q: %q", want, n.Body)
		}
	}
	if strings.Contains(n.Body, "Cups") || strings.Contains(n.Body, "Pots") {
		t.Fatalf("body = %q", n.Body)
	}
}

func TestNoticeEscapesDiscover(t *testing.T) {
	d := discoverDigest(nil)
	d.Discover[0].Title = "<script>x</script>"
	n := Weekly(d, "Talk · Example", "https://talk", "L")
	if strings.Contains(n.FormattedBody, "<script>") || !strings.Contains(n.FormattedBody, "&lt;script&gt;") {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
}

func TestContentIsNotice(t *testing.T) {
	c := Weekly(digest.Digest{}, "M", "https://talk", "L").Content()
	if c["msgtype"] != "m.notice" || c["format"] != "org.matrix.custom.html" || c["body"] == "" || c["formatted_body"] == "" {
		t.Fatalf("content = %v", c)
	}
	r := Reply(ReplyHelp).Content()
	if r["msgtype"] != "m.notice" || r["body"] != ReplyHelp {
		t.Fatalf("reply = %v", r)
	}
	if _, ok := r["formatted_body"]; ok {
		t.Fatalf("reply has formatted_body: %v", r)
	}
}

func TestTxnIDStable(t *testing.T) {
	w := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	a := TxnID("sub-1", w)
	if a != TxnID("sub-1", w) {
		t.Fatal("not stable")
	}
	if a == TxnID("sub-2", w) || a == TxnID("sub-1", w.AddDate(0, 0, 7)) {
		t.Fatal("collision")
	}
	if strings.Contains(a, "sub-1") || !strings.HasPrefix(a, "digest-2026-10-26-") || len(a) != len("digest-2026-10-26-")+16 {
		t.Fatalf("id = %q", a)
	}
}

func TestWeeklyPreview(t *testing.T) {
	d := digest.Digest{Featured: &digest.Featured{Item: item(digest.KindEntry, "Grinders", "https://t/1")}}
	n := WeeklyPreview(d, "Talk", "https://talk", "https://web/d/tok")
	want := "[Preview] This week on Talk\n\n• Grinders\n  https://t/1\n\n" +
		"Read the whole digest: https://web/d/tok\n\nReply 'stop' to stop these messages."
	if n.Body != want {
		t.Fatalf("body = %q", n.Body)
	}
	if !strings.HasPrefix(n.FormattedBody, "[Preview] This week on Talk<br><br>") {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
	if w := Weekly(d, "Talk", "https://talk", "https://web/d/tok"); strings.Contains(w.Body+w.FormattedBody, "Preview") {
		t.Fatalf("weekly marked as a preview: %q", w.Body)
	}
}

// A preview's transaction id is new each time, so a second preview is
// delivered, and never one a week's notice uses.
func TestPreviewTxnID(t *testing.T) {
	w := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	a, b := PreviewTxnID(), PreviewTxnID()
	if a == b {
		t.Fatal("two previews share a transaction id")
	}
	if !strings.HasPrefix(a, "preview-") || strings.HasPrefix(a, "digest-") || a == TxnID("sub-1", w) {
		t.Fatalf("id = %q", a)
	}
}
