package welcome

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
)

func TestThreadTitle(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"a"}, "Welcome @a"},
		{[]string{"a", "b"}, "Welcome @a and @b"},
		{[]string{"a", "b", "c"}, "Welcome @a, @b and @c"},
		{[]string{"a", "b", "c", "d", "e", "f"}, "Welcome @a, @b and 4 others"},
	} {
		if got := ThreadTitle(tc.in); got != tc.want {
			t.Errorf("ThreadTitle(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestThreadBodyMentionsEveryone(t *testing.T) {
	body := ThreadBody([]string{"a", "b", "c", "d"}, "Riverside")
	for _, u := range []string{"@a", "@b", "@c", "@d", "Riverside"} {
		if !strings.Contains(body, u) {
			t.Errorf("body %q lacks %s", body, u)
		}
	}
}

func TestAnnouncementEscapes(t *testing.T) {
	users := []bot.Newcomer{{UserID: "@x:example.org", Username: "<b>x"}, {UserID: "@y:example.org", Username: "y"}}
	c := Announcement(users, "River & Co")
	if c["msgtype"] != "m.text" || c["format"] != "org.matrix.custom.html" {
		t.Fatalf("content = %v", c)
	}
	html := c["formatted_body"].(string)
	if strings.Contains(html, "<b>x") || !strings.Contains(html, "&lt;b&gt;x") || !strings.Contains(html, "River &amp; Co") {
		t.Fatalf("formatted_body not escaped: %s", html)
	}
	if !strings.Contains(html, `href="https://matrix.to/#/@x:example.org"`) {
		t.Fatalf("no pill: %s", html)
	}
	ids := c["m.mentions"].(map[string]any)["user_ids"].([]string)
	if !slices.Equal(ids, []string{"@x:example.org", "@y:example.org"}) {
		t.Fatalf("mentions = %v", ids)
	}
	if body := c["body"].(string); !strings.Contains(body, "<b>x") || !strings.Contains(body, "y") {
		t.Fatalf("body = %q", body)
	}
}

func TestBatchTxnIDIgnoresOrder(t *testing.T) {
	a, b := BatchTxnID([]string{"1", "2"}), BatchTxnID([]string{"2", "1"})
	if a != b || a == BatchTxnID([]string{"1"}) || !strings.HasPrefix(a, "welcome-") {
		t.Fatalf("txn ids %q %q", a, b)
	}
}
