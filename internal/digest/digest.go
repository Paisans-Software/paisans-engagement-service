// Package digest holds every rule for which threads a member sees in the
// weekly email. It reads nothing and writes nothing: the caller hands it the
// candidate items and the member's view, and gets back what to send.
package digest

import (
	"cmp"
	"math/rand/v2"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Kind says which of Mbin's two content tables an item came from.
type Kind string

const (
	KindEntry Kind = "entry"
	KindPost  Kind = "post"
)

// Key identifies a thread. Entries and posts number independently, so the
// kind is part of the identity.
type Key struct {
	Kind Kind
	ID   int64
}

type Magazine struct {
	ID          int64
	Name, Title string
}

// Comment is a local author's comment; federated comments are never loaded.
type Comment struct {
	AuthorID int64
	Body     string
	Votes    int
}

type Item struct {
	Key                    Key
	Title, URL             string
	Magazine               Magazine
	AuthorID               int64
	Federated              bool
	Comments, Participants int
	Score                  int
	TopComments            []Comment // highest voted first, at most 3, empty for federated
	// NewComments counts the comments posted this week, and TopNew is the
	// best of them, chosen as TopComments is.
	NewComments int
	TopNew      []Comment
	// Again is set by Build on an item an earlier digest showed the member,
	// which is ranked and quoted by this week's activity alone.
	Again bool
}

type Member struct {
	ID      int64
	Subject string
	// Email is empty when the member has no verified address to mail.
	Email    string
	Username string
}

// View is what one member has done and chosen.
type View struct {
	Subscribed, BlockedMagazines, BannedMagazines map[int64]bool
	BlockedUsers                                  map[int64]bool
	Joined                                        map[Key]bool
	// Visited is true when the member was on the site any time since from,
	// the window's start; see mbin.Source.View.
	Visited bool
	// Shown is what earlier digests showed the member.
	Shown map[Key]bool
	// Following is the magazines the member follows and can open, by name.
	Following []Magazine
}

type Stats struct{ NewThreads, Comments, NewMembers int }

type Featured struct {
	Item    Item
	Excerpt string
}

type Digest struct {
	Stats    Stats
	Featured *Featured
	More     []Item
	// Unanswered is a few of the week's new threads that nobody has
	// answered yet, and UnansweredHeading the section's heading, both
	// picked by AddUnanswered.
	Unanswered        []Item
	UnansweredHeading string
	// Discover is a few busy threads from magazines the member does not
	// follow and the rest of the digest does not show, picked by
	// AddDiscover. Following is the first few magazines the member follows,
	// for the section to name, and FollowingMore how many more they follow.
	Discover      []Item
	Following     []Magazine
	FollowingMore int
}

// unansweredHeadings are the headings the unanswered section may have.
var unansweredHeadings = []string{"New posts", "No replies yet", "Start the conversation"}

const (
	maxMore       = 5
	maxFederated  = 2
	maxUnanswered = 3
	excerptRunes  = 200

	maxDiscover       = 3
	minDiscover       = 2
	maxFollowingNamed = 3
)

type ranked struct {
	item  Item
	score float64
}

// Build applies the member's exclusions, ranks what is left and picks the
// featured thread and the list. It returns false when nothing is left to say.
func Build(items []Item, v View, stats Stats) (Digest, bool) {
	var rs []ranked
	for _, it := range items {
		if v.Joined[it.Key] || v.BlockedMagazines[it.Magazine.ID] ||
			v.BannedMagazines[it.Magazine.ID] || v.BlockedUsers[it.AuthorID] {
			continue
		}
		// Federated content reaches a member only through a magazine they
		// follow themselves.
		if it.Federated && !v.Subscribed[it.Magazine.ID] {
			continue
		}
		// A thread the member already got comes back only for new
		// comments, since nothing else in it can be shown as new.
		if v.Shown[it.Key] {
			if it.NewComments == 0 {
				continue
			}
			it.Again = true
		}
		rs = append(rs, ranked{it, rank(it, v)})
	}
	if len(rs) == 0 {
		return Digest{}, false
	}
	sortRanked(rs)

	d := Digest{Stats: stats}
	featured := -1
	for i, r := range rs {
		if !r.item.Federated {
			featured = i
			break
		}
	}
	if featured >= 0 {
		it := rs[featured].item
		d.Featured = &Featured{Item: it, Excerpt: excerpt(it, v)}
	}
	fedSeen := 0
	for i, r := range rs {
		if i == featured || len(d.More) == maxMore {
			continue
		}
		if r.item.Federated {
			if fedSeen == maxFederated {
				continue
			}
			fedSeen++
		}
		d.More = append(d.More, r.item)
	}
	return d, true
}

// sortRanked orders rs best first, breaking ties by comments, then by id,
// then entries before posts, so the order never depends on the input's.
func sortRanked(rs []ranked) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.item.Comments != b.item.Comments {
			return a.item.Comments > b.item.Comments
		}
		if a.item.Key.ID != b.item.Key.ID {
			return a.item.Key.ID < b.item.Key.ID
		}
		return a.item.Key.Kind == KindEntry && b.item.Key.Kind != KindEntry
	})
}

// AddDiscover picks up to three of items, the week's candidates, from
// magazines the member does not follow, for them to see that the site is
// busier than their own magazines. It takes the best ranked thread of each
// magazine that neither the featured thread nor the list is in, and leaves out
// what Build would for the member, anything federated and any thread an
// earlier digest showed, even one with new comments. Fewer than two leaves
// the section out.
func (d *Digest) AddDiscover(items []Item, v View) {
	// A magazine the rest of the digest is in is used, so none of its
	// threads, those shown among them, can be picked.
	used := map[int64]bool{}
	if d.Featured != nil {
		used[d.Featured.Item.Magazine.ID] = true
	}
	for _, it := range d.More {
		used[it.Magazine.ID] = true
	}
	var rs []ranked
	for _, it := range items {
		if used[it.Magazine.ID] || it.Federated || v.Subscribed[it.Magazine.ID] ||
			v.Joined[it.Key] || v.Shown[it.Key] || v.BlockedMagazines[it.Magazine.ID] ||
			v.BannedMagazines[it.Magazine.ID] || v.BlockedUsers[it.AuthorID] {
			continue
		}
		rs = append(rs, ranked{it, rank(it, v)})
	}
	sortRanked(rs)
	var picked []Item
	for _, r := range rs {
		if len(picked) == maxDiscover {
			break
		}
		if used[r.item.Magazine.ID] {
			continue
		}
		used[r.item.Magazine.ID] = true
		picked = append(picked, r.item)
	}
	if len(picked) < minDiscover {
		return
	}
	d.Discover = picked
	n := min(len(v.Following), maxFollowingNamed)
	if n > 0 {
		d.Following = slices.Clone(v.Following[:n])
	}
	d.FollowingMore = len(v.Following) - n
}

// AddUnanswered picks, at random from r, up to three of fresh, the week's new
// threads that nobody has answered, for the member to answer first. It leaves
// out what Build would for the member, anything federated, any thread an
// earlier digest showed and anything d already shows. The member's own
// threads are in v.Joined. It picks the section's heading from r too. A nil r
// uses the package's random source.
func (d *Digest) AddUnanswered(fresh []Item, v View, r *rand.Rand) {
	taken := map[Key]bool{}
	if d.Featured != nil {
		taken[d.Featured.Item.Key] = true
	}
	for _, it := range slices.Concat(d.More, d.Discover) {
		taken[it.Key] = true
	}
	var pool []Item
	for _, it := range fresh {
		if taken[it.Key] || v.Joined[it.Key] || v.Shown[it.Key] || it.Federated ||
			v.BlockedMagazines[it.Magazine.ID] || v.BannedMagazines[it.Magazine.ID] ||
			v.BlockedUsers[it.AuthorID] {
			continue
		}
		taken[it.Key] = true
		pool = append(pool, it)
	}
	// Sorted first, so the pick depends on r alone and not on the order the
	// items came in.
	slices.SortFunc(pool, func(a, b Item) int {
		return cmp.Or(cmp.Compare(a.Key.Kind, b.Key.Kind), cmp.Compare(a.Key.ID, b.Key.ID))
	})
	intN := rand.IntN
	if r != nil {
		intN = r.IntN
	}
	n := min(len(pool), maxUnanswered)
	for i := range n {
		j := i + intN(len(pool)-i)
		pool[i], pool[j] = pool[j], pool[i]
	}
	if n > 0 {
		d.Unanswered = pool[:n]
		d.UnansweredHeading = unansweredHeadings[intN(len(unansweredHeadings))]
	}
}

// rank weighs the engagement figures. A followed magazine counts for more,
// less so if the member was on the site this week and has likely seen it;
// federated items count for less because this server only sees the votes and
// comments that reached it. A thread shown before is weighed by this week's
// comments and people alone, so its history does not carry it.
func rank(it Item, v View) float64 {
	w := 1.0
	if v.Subscribed[it.Magazine.ID] {
		w *= 1.5
		if v.Visited {
			w *= 0.5
		}
	}
	if it.Federated {
		w *= 0.5
	}
	if it.Again {
		return w * float64(2*it.NewComments+3*it.Participants)
	}
	return w * float64(it.Score+2*it.Comments+3*it.Participants)
}

func excerpt(it Item, v View) string {
	if it.Federated {
		return ""
	}
	comments := it.TopComments
	if it.Again {
		comments = it.TopNew
	}
	for _, c := range comments {
		if v.BlockedUsers[c.AuthorID] {
			continue
		}
		if e := Plain(c.Body, excerptRunes); e != "" {
			return e
		}
	}
	return ""
}

// A link's target may hold one level of parentheses, as Wikipedia's do.
const target = `\((?:[^()]|\([^()]*\))*\)`

// A bare URL runs to the next space or bracket and may hold one level of
// parentheses, as Wikipedia's do. Any space before it goes with it, so the
// text around it closes up.
const bareURL = `\s*https?://(?:[^\s<>()\[\]]|\([^\s<>()]*\))+`

var (
	reURL = regexp.MustCompile(bareURL)
	// reGone is where a URL was, marked by Plain with a NUL, which no Mbin
	// body holds since Postgres text cannot, with the wrapper it may have
	// had: !(url), (url) or <url>.
	reGone  = regexp.MustCompile(`\s*(?:!?\(\s*\x00\s*\)|<\s*\x00\s*>|\x00)`)
	reImage = regexp.MustCompile(`!\[([^\]]*)\]` + target)
	reLink  = regexp.MustCompile(`\[([^\]]*)\]` + target)
	reLead  = regexp.MustCompile(`(?m)^\s*(#{1,6}\s+|>+\s?)`)
	// Emphasis is a pair of marks whose inside neither starts nor ends with
	// a space, so a lone "5 * 3" is left as written. The inside is matched
	// lazily, so "*a* *b*" is two pairs rather than one around "a* *b". Underscores also need a
	// non word character outside them, so snake_case stays whole.
	reMarks = []*regexp.Regexp{
		regexp.MustCompile("`([^`\n]+)`"),
		regexp.MustCompile(`\*\*(\S(?:.*?\S)??)\*\*`),
		regexp.MustCompile(`~~(\S(?:.*?\S)??)~~`),
		regexp.MustCompile(`\*(\S(?:.*?\S)??)\*`),
	}
	reUnderscores = []*regexp.Regexp{
		regexp.MustCompile(`(^|[^\pL\pN_])__(\S(?:.*?\S)??)__($|[^\pL\pN_])`),
		regexp.MustCompile(`(^|[^\pL\pN_])_(\S(?:.*?\S)??)_($|[^\pL\pN_])`),
	}
)

// Plain reduces markdown to one line of plain text of at most max runes. It
// keeps link text, drops URLs and paired emphasis marks, collapses whitespace
// and, when it must cut, cuts at a word boundary. Text that was cut or lost a
// URL ends with an ellipsis that counts toward max, so a reader can tell
// there is more. A body that is nothing but URLs comes out empty.
func Plain(body string, max int) string {
	dropped := false
	keep := func(sub []string) string {
		if strings.TrimSpace(sub[1]) == "" {
			dropped = true
		}
		return sub[1]
	}
	s := reLead.ReplaceAllString(body, "")
	s = replaceSub(reImage, s, keep)
	s = replaceSub(reLink, s, keep)
	if t := reURL.ReplaceAllStringFunc(s, func(m string) string { return "\x00" + trailing(m) }); t != s {
		dropped = true
		s = reGone.ReplaceAllString(t, "")
	}
	for _, re := range reMarks {
		s = re.ReplaceAllString(s, "$1")
	}
	// Each underscore match consumes the character on either side, so the
	// pair right after it is only found on another pass.
	for _, re := range reUnderscores {
		for prev := ""; prev != s; {
			prev, s = s, re.ReplaceAllString(s, "$1$2$3")
		}
	}
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")

	r := []rune(s)
	if len(r) == 0 || max < 1 {
		return ""
	}
	if len(r) <= max && !dropped {
		return s
	}
	if len(r) < max {
		return ellipsis(s)
	}
	cut := r[:max-1]
	// Back up to the last space unless the cut already ends on a word.
	if !unicode.IsSpace(r[max-1]) {
		if i := lastSpace(cut); i > 0 {
			cut = cut[:i]
		}
	}
	return ellipsis(string(cut))
}

// ellipsis ends s with one, in place of any space or stop it ended with.
func ellipsis(s string) string {
	return strings.TrimRightFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(".,;:", r)
	}) + "…"
}

// replaceSub is ReplaceAllStringFunc given each match's submatches.
func replaceSub(re *regexp.Regexp, s string, f func([]string) string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		return f(re.FindStringSubmatch(m))
	})
}

// trailing is the punctuation a URL match ended with, which belongs to the
// sentence rather than the URL.
func trailing(m string) string {
	return m[len(strings.TrimRight(m, `.,;:!?'"`)):]
}

// Host is the host of the first URL in body, without a leading "www." or a
// port, or empty when body has none.
func Host(body string) string {
	m := strings.TrimSpace(reURL.FindString(body))
	m = m[:len(m)-len(trailing(m))]
	if m == "" {
		return ""
	}
	u, err := url.Parse(m)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

func lastSpace(r []rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if unicode.IsSpace(r[i]) {
			return i
		}
	}
	return -1
}
