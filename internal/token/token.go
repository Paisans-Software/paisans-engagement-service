// Package token signs the unsubscribe links in the digest email. A token
// carries the member's subject and a MAC over it, so the public route can act
// on a link without a session or a lookup.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
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

// Verify returns the subject tok was signed for, and false for anything that
// is malformed or was not signed with key.
func Verify(key []byte, tok string) (subject string, ok bool) {
	payload, sig, found := strings.Cut(tok, ".")
	if !found {
		return "", false
	}
	b, err1 := base64.RawURLEncoding.DecodeString(payload)
	got, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil {
		return "", false
	}
	if !hmac.Equal(got, mac(key, string(b))) {
		return "", false
	}
	return string(b), true
}
