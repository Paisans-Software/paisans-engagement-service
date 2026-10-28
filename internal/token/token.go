// Package token signs the unsubscribe links in the digest email and the web
// digest links in the Matrix notice. A token carries the member's subject and
// a MAC over it, so the public route can act on a link without a session or a
// lookup.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"
)

// mac binds the purpose into the MAC so a value minted for another use of the
// same key cannot be replayed as an unsubscribe link.
func mac(key []byte, subject string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("unsubscribe\x00" + subject))
	return m.Sum(nil)
}

// Sign returns the token for subject. It has no expiry: a link in a year old
// email still works.
func Sign(key []byte, subject string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(subject)) + "." +
		base64.RawURLEncoding.EncodeToString(mac(key, subject))
}

// strict rejects base64 whose spare low bits are set. It still skips CR and
// LF, which Verify refuses first, so each token has exactly one spelling.
var strict = base64.RawURLEncoding.Strict()

// Verify returns the subject tok was signed for, and false for anything that
// is malformed or was not signed with key. A token for an empty subject is
// never one the service minted for a member, so it is as invalid as a bad
// signature.
func Verify(key []byte, tok string) (subject string, ok bool) {
	payload, sig, found := strings.Cut(tok, ".")
	if !found || strings.ContainsAny(tok, "\r\n") {
		return "", false
	}
	b, err1 := strict.DecodeString(payload)
	got, err2 := strict.DecodeString(sig)
	if err1 != nil || err2 != nil || len(b) == 0 {
		return "", false
	}
	if !hmac.Equal(got, mac(key, string(b))) {
		return "", false
	}
	return string(b), true
}

// digestMAC binds the purpose, the subject and the week, so a digest token
// cannot be read as an unsubscribe token or reused for another week.
func digestMAC(key []byte, subject, date string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("digest\x00" + subject + "\x00" + date))
	return m.Sum(nil)
}

// SignDigest returns the token for subject's digest of the week that week
// falls on, dated by week's own local calendar day.
func SignDigest(key []byte, subject string, week time.Time) string {
	date := week.Format("2006-01-02")
	return base64.RawURLEncoding.EncodeToString([]byte(subject+"\x00"+date)) + "." +
		base64.RawURLEncoding.EncodeToString(digestMAC(key, subject, date))
}

// VerifyDigest returns the subject and the week's date as YYYY-MM-DD, and
// false for anything malformed, empty of a subject, or not signed with key.
func VerifyDigest(key []byte, tok string) (subject, weekDate string, ok bool) {
	payload, sig, found := strings.Cut(tok, ".")
	if !found || strings.ContainsAny(tok, "\r\n") {
		return "", "", false
	}
	b, err1 := strict.DecodeString(payload)
	got, err2 := strict.DecodeString(sig)
	if err1 != nil || err2 != nil {
		return "", "", false
	}
	i := strings.LastIndexByte(string(b), 0)
	if i <= 0 {
		return "", "", false
	}
	subject, weekDate = string(b[:i]), string(b[i+1:])
	if _, err := time.Parse("2006-01-02", weekDate); err != nil {
		return "", "", false
	}
	if !hmac.Equal(got, digestMAC(key, subject, weekDate)) {
		return "", "", false
	}
	return subject, weekDate, true
}
