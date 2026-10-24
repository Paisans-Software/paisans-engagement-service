package digest

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func entry(id int64, mag int64, score, comments, people int) Item {
	return Item{
		Key:          Key{Kind: KindEntry, ID: id},
		Title:        fmt.Sprintf("thread %d", id),
		Magazine:     Magazine{ID: mag, Name: fmt.Sprintf("mag%d", mag)},
		AuthorID:     1000 + id,
		Score:        score,
		Comments:     comments,
		Participants: people,
	}
}

func fed(id int64, mag int64, score, comments, people int) Item {
	it := entry(id, mag, score, comments, people)
	it.Federated = true
	return it
}

func keys(items []Item) []int64 {
	var out []int64
	for _, it := range items {
		out = append(out, it.Key.ID)
	}
	return out
}

func eq(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDropsJoined(t *testing.T) {
	items := []Item{entry(1, 1, 5, 5, 5), entry(2, 1, 1, 1, 1)}
	v := View{Joined: map[Key]bool{{KindEntry, 1}: true}}
	d, ok := Build(items, v, Stats{})
	if !ok || d.Featured == nil || d.Featured.Item.Key.ID != 2 {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
	// a post with the same id is a different thread
	post := entry(1, 1, 9, 9, 9)
	post.Key.Kind = KindPost
	d, _ = Build([]Item{entry(1, 1, 5, 5, 5), post}, v, Stats{})
	if d.Featured == nil || d.Featured.Item.Key.Kind != KindPost {
		t.Fatalf("post wrongly dropped: %+v", d)
	}
}

func TestDropsBlockedMagazineBannedMagazineBlockedAuthor(t *testing.T) {
	items := []Item{entry(1, 1, 5, 5, 5), entry(2, 2, 5, 5, 5), entry(3, 3, 5, 5, 5), entry(4, 4, 1, 1, 1)}
	v := View{
		BlockedMagazines: map[int64]bool{1: true},
		BannedMagazines:  map[int64]bool{2: true},
		BlockedUsers:     map[int64]bool{1003: true},
	}
	d, ok := Build(items, v, Stats{})
	if !ok || d.Featured.Item.Key.ID != 4 || len(d.More) != 0 {
		t.Fatalf("got %+v", d)
	}
}

func TestFederatedNeedsOwnSubscription(t *testing.T) {
	items := []Item{fed(1, 7, 5, 5, 5), fed(2, 8, 5, 5, 5)}
	d, ok := Build(items, View{Subscribed: map[int64]bool{8: true}}, Stats{})
	if !ok || !eq(keys(d.More), []int64{2}) {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
}

func TestFederatedNeverFeatured(t *testing.T) {
	items := []Item{fed(1, 7, 500, 500, 500), entry(2, 1, 1, 0, 0)}
	d, _ := Build(items, View{Subscribed: map[int64]bool{7: true}}, Stats{})
	if d.Featured == nil || d.Featured.Item.Key.ID != 2 {
		t.Fatalf("featured %+v", d.Featured)
	}
	if !eq(keys(d.More), []int64{1}) {
		t.Fatalf("more %v", keys(d.More))
	}
}

func TestFederatedCappedAtTwo(t *testing.T) {
	sub := map[int64]bool{7: true}
	items := []Item{entry(1, 1, 1, 0, 0), fed(2, 7, 90, 9, 9), fed(3, 7, 80, 9, 9), fed(4, 7, 70, 9, 9), entry(5, 1, 0, 0, 0)}
	d, _ := Build(items, View{Subscribed: sub}, Stats{})
	n := 0
	for _, it := range d.More {
		if it.Federated {
			n++
		}
	}
	if n != 2 || len(d.More) != 3 {
		t.Fatalf("more %v federated=%d", keys(d.More), n)
	}
	if !eq(keys(d.More), []int64{2, 3, 5}) {
		t.Fatalf("order %v", keys(d.More))
	}
}

func TestSubscribedRanksHigher(t *testing.T) {
	// 10 vs 8 * 1.5 = 12
	items := []Item{entry(1, 1, 10, 0, 0), entry(2, 2, 8, 0, 0)}
	d, _ := Build(items, View{Subscribed: map[int64]bool{2: true}}, Stats{})
	if d.Featured.Item.Key.ID != 2 {
		t.Fatalf("featured %d", d.Featured.Item.Key.ID)
	}
}

func TestVisitedSubscribedRanksLower(t *testing.T) {
	// subscribed and visited: 1.5*0.5 = 0.75, so 10 beats 12 * 0.75 = 9
	items := []Item{entry(1, 1, 10, 0, 0), entry(2, 2, 12, 0, 0)}
	v := View{Subscribed: map[int64]bool{2: true}, Visited: true}
	d, _ := Build(items, v, Stats{})
	if d.Featured.Item.Key.ID != 1 {
		t.Fatalf("featured %d", d.Featured.Item.Key.ID)
	}
	// visiting does not touch unsubscribed magazines
	v = View{Visited: true}
	d, _ = Build(items, v, Stats{})
	if d.Featured.Item.Key.ID != 2 {
		t.Fatalf("featured %d", d.Featured.Item.Key.ID)
	}
}

func TestFederatedHalfWeight(t *testing.T) {
	// Subscribed federated 30 weighs 30*1.5*0.5 = 22.5 halved, 45 unhalved;
	// the local 25 sits between the two.
	items := []Item{entry(1, 1, 100, 0, 0), entry(2, 1, 25, 0, 0), fed(3, 7, 30, 0, 0)}
	d, _ := Build(items, View{Subscribed: map[int64]bool{7: true}}, Stats{})
	if d.Featured.Item.Key.ID != 1 || !eq(keys(d.More), []int64{2, 3}) {
		t.Fatalf("got %d %v", d.Featured.Item.Key.ID, keys(d.More))
	}
}

func TestTies(t *testing.T) {
	// equal weight: score 4 + 2*0 = 4 vs score 0 + 2*2 = 4; more comments first
	a := entry(5, 1, 4, 0, 0)
	b := entry(9, 1, 0, 2, 0)
	d, _ := Build([]Item{a, b}, View{}, Stats{})
	if d.Featured.Item.Key.ID != 9 {
		t.Fatalf("featured %d", d.Featured.Item.Key.ID)
	}
	// equal comments: lower id first
	d, _ = Build([]Item{entry(9, 1, 3, 1, 0), entry(5, 1, 3, 1, 0)}, View{}, Stats{})
	if d.Featured.Item.Key.ID != 5 {
		t.Fatalf("featured %d", d.Featured.Item.Key.ID)
	}
	// equal ids: entry before post, whatever the input order
	e, p := entry(5, 1, 3, 1, 0), entry(5, 1, 3, 1, 0)
	p.Key.Kind = KindPost
	d, _ = Build([]Item{p, e}, View{}, Stats{})
	if d.Featured.Item.Key.Kind != KindEntry || d.More[0].Key.Kind != KindPost {
		t.Fatalf("got %+v", d)
	}
}

func TestMoreAtMostFive(t *testing.T) {
	var items []Item
	for i := int64(1); i <= 9; i++ {
		items = append(items, entry(i, 1, int(100-i), 0, 0))
	}
	d, _ := Build(items, View{}, Stats{})
	if d.Featured.Item.Key.ID != 1 || !eq(keys(d.More), []int64{2, 3, 4, 5, 6}) {
		t.Fatalf("got %d %v", d.Featured.Item.Key.ID, keys(d.More))
	}
}

func TestExcerptSkipsBlockedAuthor(t *testing.T) {
	it := entry(1, 1, 1, 1, 1)
	it.TopComments = []Comment{
		{AuthorID: 50, Body: "blocked words", Votes: 9},
		{AuthorID: 51, Body: "a **good** [point](https://example.org/x)\n\nhere", Votes: 3},
	}
	d, _ := Build([]Item{it}, View{BlockedUsers: map[int64]bool{50: true}}, Stats{})
	if d.Featured.Excerpt != "a good point here" {
		t.Fatalf("excerpt %q", d.Featured.Excerpt)
	}
}

func TestExcerptOmittedWhenNone(t *testing.T) {
	it := entry(1, 1, 1, 1, 1)
	d, _ := Build([]Item{it}, View{}, Stats{})
	if d.Featured.Excerpt != "" {
		t.Fatalf("excerpt %q", d.Featured.Excerpt)
	}
	it.TopComments = []Comment{{AuthorID: 50, Body: "x"}}
	d, _ = Build([]Item{it}, View{BlockedUsers: map[int64]bool{50: true}}, Stats{})
	if d.Featured.Excerpt != "" {
		t.Fatalf("excerpt %q", d.Featured.Excerpt)
	}
}

func TestEmptyWhenEverythingDropped(t *testing.T) {
	if _, ok := Build(nil, View{}, Stats{}); ok {
		t.Fatal("empty input sent")
	}
	items := []Item{entry(1, 1, 1, 1, 1), fed(2, 7, 1, 1, 1)}
	v := View{BlockedMagazines: map[int64]bool{1: true}}
	if _, ok := Build(items, v, Stats{}); ok {
		t.Fatal("all dropped but sent")
	}
}

func TestNoLocalItemsMeansNoFeatured(t *testing.T) {
	d, ok := Build([]Item{fed(1, 7, 5, 5, 5)}, View{Subscribed: map[int64]bool{7: true}}, Stats{NewThreads: 3})
	if !ok || d.Featured != nil || len(d.More) != 1 || d.Stats.NewThreads != 3 {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
}

func TestPlain(t *testing.T) {
	long := strings.Repeat("word ", 50) // 250 runes
	cases := []struct{ name, in string }{
		{"link", "see [the docs](https://example.org/a_b) now"},
		{"bold", "this is **bold** and __also__ and *it* and ~~gone~~"},
		{"newlines", "one\n\ntwo\t three\r\nfour"},
		{"empty", "   "},
		{"image", "![alt text](https://example.org/i.png) after"},
		{"heading quote", "# Title\n> quoted"},
		{"code", "use `go test` here"},
	}
	want := []string{
		"see the docs now",
		"this is bold and also and it and gone",
		"one two three four",
		"",
		"alt text after",
		"Title quoted",
		"use go test here",
	}
	for i, c := range cases {
		if got := Plain(c.in, 200); got != want[i] {
			t.Errorf("%s: got %q want %q", c.name, got, want[i])
		}
	}
	got := Plain(long, 200)
	if n := utf8.RuneCountInString(got); n > 200 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long: %d runes %q", n, got)
	}
	if strings.Contains(got, " …") || strings.HasSuffix(strings.TrimSuffix(got, "…"), "wor") {
		t.Fatalf("not on a word boundary: %q", got)
	}
	if got := Plain("héllo wörld again", 12); got != "héllo wörld…" {
		t.Fatalf("got %q", got)
	}
	if got := Plain("supercalifragilistic", 8); got != "superca…" {
		t.Fatalf("got %q", got)
	}
	if got := Plain("exactly ten", 11); got != "exactly ten" {
		t.Fatalf("got %q", got)
	}
}
