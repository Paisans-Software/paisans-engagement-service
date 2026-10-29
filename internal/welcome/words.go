// Package welcome announces new members: a thread in Mbin's welcome magazine
// and a message in the Matrix community room every few minutes, and once a
// day an email asking existing members to greet the day's Mbin newcomers.
package welcome

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-engagement-service/internal/bot"
)

// titleNames is how many newcomers a thread title names before it counts the
// rest.
const titleNames = 3

// list joins names as "a", "a and b" or "a, b and c".
func list(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func mentions(usernames []string) []string {
	out := make([]string, len(usernames))
	for i, u := range usernames {
		out[i] = "@" + u
	}
	return out
}

// ThreadTitle is the welcome thread's title.
func ThreadTitle(usernames []string) string {
	m := mentions(usernames)
	if len(m) > titleNames {
		m = append(m[:titleNames-1], fmt.Sprintf("%d others", len(usernames)-(titleNames-1)))
	}
	return "Welcome " + list(m)
}

// ThreadBody is the welcome thread's text. It mentions every newcomer, which
// Mbin turns into a notification for each.
func ThreadBody(usernames []string, community string) string {
	return list(mentions(usernames)) + " just joined " + community + ". " +
		"Say hello in the comments, and tell them what you enjoy here."
}

// Announcement is the community room message: each newcomer as a mention
// pill, and listed in m.mentions so their client notifies them.
func Announcement(users []bot.Newcomer, community string) map[string]any {
	plain := make([]string, len(users))
	pills := make([]string, len(users))
	ids := make([]string, len(users))
	for i, u := range users {
		plain[i] = u.Username
		pills[i] = `<a href="https://matrix.to/#/` + html.EscapeString(u.UserID) + `">` + html.EscapeString(u.Username) + `</a>`
		ids[i] = u.UserID
	}
	return map[string]any{
		"msgtype": "m.text",
		"body":    "Please welcome " + list(plain) + " to " + community + "! Say hello.",
		"format":  "org.matrix.custom.html",
		"formatted_body": "Please welcome " + list(pills) + " to " + html.EscapeString(community) +
			"! Say hello.",
		"m.mentions": map[string]any{"user_ids": ids},
	}
}

// BatchTxnID is the transaction id of the announcement naming accounts, the
// same whatever their order, so a retried batch is delivered once.
func BatchTxnID(accounts []string) string {
	s := slices.Clone(accounts)
	slices.Sort(s)
	h := sha256.Sum256([]byte(strings.Join(s, "\x00")))
	return "welcome-" + hex.EncodeToString(h[:])[:24]
}
