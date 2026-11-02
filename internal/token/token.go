// Package token signs the unsubscribe links in the digest and welcome emails.
// A token carries the member's subject and a MAC over it, so the public route
// can act on a link without a session or a lookup.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// Purpose is the list a token unsubscribes from.
type Purpose string

const (
	Digest  Purpose = "digest"
	Welcome Purpose = "welcome"
)

// welcomePrefix starts a welcome token. A digest token has none, so the links
// in digest emails already sent keep their meaning.
const welcomePrefix = "w."

// mac binds the purpose into the MAC so a value minted for another use of the
// same key, or for the other list, cannot be replayed as this unsubscribe link.
func mac(key []byte, p Purpose, subject string) []byte {
	label := "unsubscribe"
	if p == Welcome {
		label = "unsubscribe-welcome"
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(label + "\x00" + subject))
	return m.Sum(nil)
}

// Sign returns the digest token for subject. It has no expiry: a link in a
// year old email still works.
func Sign(key []byte, subject string) string { return SignFor(key, Digest, subject) }

// SignFor returns the token for subject on the list p.
func SignFor(key []byte, p Purpose, subject string) string {
	tok := base64.RawURLEncoding.EncodeToString([]byte(subject)) + "." +
		base64.RawURLEncoding.EncodeToString(mac(key, p, subject))
	if p == Welcome {
		return welcomePrefix + tok
	}
	return tok
}

// strict rejects base64 whose spare low bits are set. It still skips CR and
// LF, which Verify refuses first, so each token has exactly one spelling.
var strict = base64.RawURLEncoding.Strict()

// Verify returns the subject tok was signed for, and false for anything that
// is malformed or was not signed with key. A token for an empty subject is
// never one the service minted for a member, so it is as invalid as a bad
// signature.
func Verify(key []byte, tok string) (subject string, ok bool) {
	return verify(key, Digest, tok)
}

// VerifyFor returns the list and subject tok was signed for.
func VerifyFor(key []byte, tok string) (p Purpose, subject string, ok bool) {
	if rest, found := strings.CutPrefix(tok, welcomePrefix); found {
		subject, ok = verify(key, Welcome, rest)
		return Welcome, subject, ok
	}
	subject, ok = verify(key, Digest, tok)
	return Digest, subject, ok
}

func verify(key []byte, p Purpose, tok string) (subject string, ok bool) {
	payload, sig, found := strings.Cut(tok, ".")
	if !found || strings.ContainsAny(tok, "\r\n") {
		return "", false
	}
	b, err1 := strict.DecodeString(payload)
	got, err2 := strict.DecodeString(sig)
	if err1 != nil || err2 != nil || len(b) == 0 {
		return "", false
	}
	if !hmac.Equal(got, mac(key, p, string(b))) {
		return "", false
	}
	return string(b), true
}
