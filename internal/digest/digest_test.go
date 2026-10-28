package digest

import (
	"fmt"
	"math/rand/v2"
	"slices"
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

// A top comment that is nothing once its markdown is gone, an image alone
// say, gives way to the next one.
func TestExcerptSkipsEmptyComment(t *testing.T) {
	it := entry(1, 1, 1, 1, 1)
	it.TopComments = []Comment{
		{AuthorID: 50, Body: "![](https://example.org/i.png)", Votes: 9},
		{AuthorID: 51, Body: "worth reading", Votes: 3},
	}
	d, _ := Build([]Item{it}, View{}, Stats{})
	if d.Featured.Excerpt != "worth reading" {
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
		{"nested parens in a link", "read [Go (lang)](https://example.org/wiki/Go_(language)) first"},
		{"nested parens in an image", "![a (b) c](https://example.org/i_(1).png) after"},
		{"lone asterisks", "5 * 3 = 15 and 2 ** 8"},
		{"spaced pair", "a * b * c"},
		{"intraword underscores", "snake_case_name and __init__ stay"},
		{"underscore emphasis", "this is _it_ and __so__"},
		{"lone backtick and tilde", "a ` b and ~~ c"},
		{"nested emphasis", "**bold *and em***"},
		{"adjacent underscore pairs", "_a_ _b_ and __c__ __d__"},
		{"adjacent asterisk pairs", "*a* *b* and **c** **d** and ~~e~~ ~~f~~"},
	}
	want := []string{
		"see the docs now",
		"this is bold and also and it and gone",
		"one two three four",
		"",
		"alt text after",
		"Title quoted",
		"use go test here",
		"read Go (lang) first",
		"a (b) c after",
		"5 * 3 = 15 and 2 ** 8",
		"a * b * c",
		"snake_case_name and init stay",
		"this is it and so",
		"a ` b and ~~ c",
		"bold and em",
		"a b and c d",
		"a b and c d and e f",
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

// A thread an earlier digest showed comes back only with new comments, and
// then marked as back.
func TestShownNeedsNewComments(t *testing.T) {
	quiet, busy := entry(1, 1, 50, 50, 5), entry(2, 1, 1, 9, 3)
	busy.NewComments = 4
	v := View{Shown: map[Key]bool{{KindEntry, 1}: true, {KindEntry, 2}: true}}
	d, ok := Build([]Item{quiet, busy}, v, Stats{})
	if !ok || d.Featured == nil || d.Featured.Item.Key.ID != 2 || len(d.More) != 0 {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
	if !d.Featured.Item.Again {
		t.Fatal("a returning thread is not marked as back")
	}
	if _, ok := Build([]Item{quiet}, v, Stats{}); ok {
		t.Fatal("a digest of only quiet returning threads was sent")
	}
	if d, _ := Build([]Item{quiet}, View{}, Stats{}); d.Featured == nil || d.Featured.Item.Again {
		t.Fatalf("a thread never shown: %+v", d)
	}
}

// A returning thread is ranked by this week's comments and people, not by
// its score or its comments before.
func TestShownRanksByThisWeek(t *testing.T) {
	old := entry(1, 1, 100, 100, 2)
	old.NewComments = 1
	fresh := entry(2, 1, 1, 3, 2)
	d, _ := Build([]Item{old, fresh}, View{Shown: map[Key]bool{{KindEntry, 1}: true}}, Stats{})
	// old scores 2*1+3*2 = 8 again; fresh scores 1+2*3+3*2 = 13.
	if d.Featured.Item.Key.ID != 2 || !eq(keys(d.More), []int64{1}) {
		t.Fatalf("got featured %d, more %v", d.Featured.Item.Key.ID, keys(d.More))
	}
}

// A returning thread quotes the best of this week's comments, never an
// older one.
func TestShownExcerptIsThisWeek(t *testing.T) {
	it := entry(1, 1, 1, 9, 1)
	it.NewComments = 1
	it.TopComments = []Comment{{AuthorID: 50, Body: "from before", Votes: 40}}
	v := View{Shown: map[Key]bool{{KindEntry, 1}: true}}
	d, _ := Build([]Item{it}, v, Stats{})
	if d.Featured.Excerpt != "" {
		t.Fatalf("quoted an older comment: %q", d.Featured.Excerpt)
	}
	it.TopNew = []Comment{{AuthorID: 51, Body: "this week", Votes: 2}}
	d, _ = Build([]Item{it}, v, Stats{})
	if d.Featured.Excerpt != "this week" {
		t.Fatalf("excerpt %q", d.Featured.Excerpt)
	}
}

func seeded(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed)) }

// fresh is a new thread nobody has answered.
func fresh(id int64, mag int64) Item { return entry(id, mag, 0, 0, 0) }

// Three of the week's unanswered threads are picked, at random but the same
// for the same seed whatever order they came in.
func TestUnansweredPicksThree(t *testing.T) {
	var in []Item
	for i := int64(1); i <= 6; i++ {
		in = append(in, fresh(i, 1))
	}
	d := Digest{}
	d.AddUnanswered(in, View{}, seeded(1))
	got := keys(d.Unanswered)
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	seen := map[int64]bool{}
	for _, id := range got {
		if id < 1 || id > 6 || seen[id] {
			t.Fatalf("got %v", got)
		}
		seen[id] = true
	}
	rev := slices.Clone(in)
	slices.Reverse(rev)
	again := Digest{}
	again.AddUnanswered(rev, View{}, seeded(1))
	if !eq(keys(again.Unanswered), got) {
		t.Fatalf("same seed gave %v then %v", got, keys(again.Unanswered))
	}
}

// Over enough seeds every candidate gets its turn, so the pick is not just
// the first three.
func TestUnansweredIsRandom(t *testing.T) {
	var in []Item
	for i := int64(1); i <= 6; i++ {
		in = append(in, fresh(i, 1))
	}
	picked := map[int64]bool{}
	for seed := uint64(0); seed < 50; seed++ {
		d := Digest{}
		d.AddUnanswered(in, View{}, seeded(seed))
		for _, id := range keys(d.Unanswered) {
			picked[id] = true
		}
	}
	if len(picked) != 6 {
		t.Fatalf("picked only %v", picked)
	}
}

func TestUnansweredFewerThanThree(t *testing.T) {
	d := Digest{}
	d.AddUnanswered([]Item{fresh(1, 1), fresh(2, 1)}, View{}, seeded(1))
	if got := keys(d.Unanswered); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	d = Digest{}
	d.AddUnanswered(nil, View{}, seeded(1))
	if d.Unanswered != nil {
		t.Fatalf("got %v", keys(d.Unanswered))
	}
}

// The member's own and joined threads, what they blocked or are banned
// from, anything federated or shown before, and anything the digest already
// shows are never picked.
func TestUnansweredExcludes(t *testing.T) {
	own := fresh(1, 1)
	blockedAuthor := fresh(4, 1)
	far := fresh(5, 7)
	far.Federated = true
	post := fresh(8, 1)
	post.Key.Kind = KindPost
	in := []Item{own, fresh(2, 2), fresh(3, 3), blockedAuthor, far, fresh(6, 1), fresh(7, 1), fresh(8, 1), post, fresh(9, 1)}
	v := View{
		Joined:           map[Key]bool{{KindEntry, 1}: true},
		BlockedMagazines: map[int64]bool{2: true},
		BannedMagazines:  map[int64]bool{3: true},
		BlockedUsers:     map[int64]bool{1004: true},
		Subscribed:       map[int64]bool{7: true},
		Shown:            map[Key]bool{{KindEntry, 6}: true},
	}
	d := Digest{Featured: &Featured{Item: fresh(7, 1)}, More: []Item{fresh(8, 1)}}
	for seed := uint64(0); seed < 20; seed++ {
		d.Unanswered = nil
		d.AddUnanswered(in, v, seeded(seed))
		got := d.Unanswered
		if len(got) != 2 {
			t.Fatalf("seed %d: got %v", seed, keys(got))
		}
		for _, it := range got {
			if it.Key != post.Key && it.Key != (Key{KindEntry, 9}) {
				t.Fatalf("seed %d: picked %+v", seed, it.Key)
			}
		}
	}
}

// The section's heading is one of three, picked by the same source as the
// threads, and none when there is no section.
func TestUnansweredHeading(t *testing.T) {
	headings := map[string]bool{}
	for seed := uint64(0); seed < 50; seed++ {
		d := Digest{}
		d.AddUnanswered([]Item{fresh(1, 1)}, View{}, seeded(seed))
		again := Digest{}
		again.AddUnanswered([]Item{fresh(1, 1)}, View{}, seeded(seed))
		if d.UnansweredHeading != again.UnansweredHeading {
			t.Fatalf("seed %d gave %q then %q", seed, d.UnansweredHeading, again.UnansweredHeading)
		}
		headings[d.UnansweredHeading] = true
	}
	want := map[string]bool{"New posts": true, "No replies yet": true, "Start the conversation": true}
	if len(headings) != len(want) {
		t.Fatalf("headings %v", headings)
	}
	for h := range headings {
		if !want[h] {
			t.Fatalf("heading %q", h)
		}
	}
	d := Digest{}
	d.AddUnanswered(nil, View{}, seeded(1))
	if d.UnansweredHeading != "" {
		t.Fatalf("heading %q with no section", d.UnansweredHeading)
	}
}
