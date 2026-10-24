// Package digest holds every rule for which threads a member sees in the
// weekly email. It reads nothing and writes nothing: the caller hands it the
// candidate items and the member's view, and gets back what to send.
package digest

import (
	"regexp"
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
}

type Member struct {
	ID                       int64
	Subject, Email, Username string
}

// View is what one member has done and chosen.
type View struct {
	Subscribed, BlockedMagazines, BannedMagazines map[int64]bool
	BlockedUsers                                  map[int64]bool
	Joined                                        map[Key]bool
	// Visited is true when the member was on the site any time since from,
	// the window's start; see mbin.Source.View.
	Visited bool
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
}

const (
	maxMore      = 5
	maxFederated = 2
	excerptRunes = 200
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
		rs = append(rs, ranked{it, rank(it, v)})
	}
	if len(rs) == 0 {
		return Digest{}, false
	}
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

// rank weighs the engagement figures. A followed magazine counts for more,
// less so if the member was on the site this week and has likely seen it;
// federated items count for less because this server only sees the votes and
// comments that reached it.
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
	return w * float64(it.Score+2*it.Comments+3*it.Participants)
}

func excerpt(it Item, v View) string {
	if it.Federated {
		return ""
	}
	for _, c := range it.TopComments {
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

var (
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
// keeps link text, drops paired emphasis marks, collapses whitespace and,
// when it must cut, cuts at a word boundary and ends with an ellipsis that
// counts toward max.
func Plain(body string, max int) string {
	s := reLead.ReplaceAllString(body, "")
	s = reImage.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1")
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
	if len(r) <= max {
		return s
	}
	if max < 1 {
		return ""
	}
	cut := r[:max-1]
	// Back up to the last space unless the cut already ends on a word.
	if !unicode.IsSpace(r[max-1]) {
		if i := lastSpace(cut); i > 0 {
			cut = cut[:i]
		}
	}
	return strings.TrimRightFunc(string(cut), unicode.IsSpace) + "…"
}

func lastSpace(r []rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if unicode.IsSpace(r[i]) {
			return i
		}
	}
	return -1
}
