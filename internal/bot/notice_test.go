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
		},
	}
	n := Weekly(d, "Talk · Example", "https://web/d/tok")
	want := "Your weekly digest from Talk · Example\n\n• Grinders\n• Beans\n• Kettles\n\n" +
		"Read the whole digest: https://web/d/tok\n\nReply stop to stop these messages."
	if n.Body != want {
		t.Fatalf("body = %q", n.Body)
	}
	if !strings.Contains(n.FormattedBody, `<li><a href="https://t/3">Beans</a></li>`) ||
		!strings.Contains(n.FormattedBody, `<p>Read the whole digest: <a href="https://web/d/tok">https://web/d/tok</a></p>`) ||
		strings.Contains(n.FormattedBody, "Filters") {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
}

func TestWeeklyOnlyPosts(t *testing.T) {
	d := digest.Digest{More: []digest.Item{item(digest.KindPost, "P", "u")}}
	n := Weekly(d, "Talk · Example", "L")
	want := "Your weekly digest from Talk · Example\n\nRead the whole digest: L\n\nReply stop to stop these messages."
	if n.Body != want {
		t.Fatalf("body = %q", n.Body)
	}
	if strings.Contains(n.FormattedBody, "<ul>") {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
}

func TestNoticeEscapesTitles(t *testing.T) {
	title := `<b>Tom & Jerry</b> [x](y)`
	n := Weekly(digest.Digest{Featured: &digest.Featured{Item: item(digest.KindEntry, title, "https://t/?a=1&b=2")}}, "M <i>", "L")
	if !strings.Contains(n.FormattedBody, "&lt;b&gt;Tom &amp; Jerry&lt;/b&gt;") || strings.Contains(n.FormattedBody, "<b>") ||
		strings.Contains(n.FormattedBody, "<i>") {
		t.Fatalf("formatted = %q", n.FormattedBody)
	}
	if !strings.Contains(n.Body, title) {
		t.Fatalf("body = %q", n.Body)
	}
}

func TestContentIsNotice(t *testing.T) {
	c := Weekly(digest.Digest{}, "M", "L").Content()
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
