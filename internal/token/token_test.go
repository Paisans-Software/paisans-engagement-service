package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"time"
)

var key = []byte("test-key-test-key-test-key-12345")

func TestRoundTrip(t *testing.T) {
	for _, sub := range []string{"abc-123", "user@example.org", "ünï/cødé+=?&", "x"} {
		got, ok := Verify(key, Sign(key, sub))
		if !ok || got != sub {
			t.Errorf("Verify(Sign(%q)) = %q, %v", sub, got, ok)
		}
	}
}

func TestAlteredSubjectFails(t *testing.T) {
	_, sig, _ := strings.Cut(Sign(key, "alice"), ".")
	forged := Sign(key, "bob")
	forgedSubject, _, _ := strings.Cut(forged, ".")
	if _, ok := Verify(key, forgedSubject+"."+sig); ok {
		t.Fatal("token with swapped subject verified")
	}
}

func TestOtherKeyFails(t *testing.T) {
	if _, ok := Verify([]byte("another-key-another-key-another!"), Sign(key, "alice")); ok {
		t.Fatal("token verified under a different key")
	}
}

func TestMalformedFails(t *testing.T) {
	good := Sign(key, "alice")
	subject, sig, _ := strings.Cut(good, ".")
	for _, tok := range []string{"", "a", "a.b.c", "!!!.!!!", subject + ".!!!", "!!!." + sig, subject + ".", "." + sig, good + "."} {
		if sub, ok := Verify(key, tok); ok {
			t.Errorf("Verify(%q) = %q, true", tok, sub)
		}
	}
}

func TestTokenIsURLSafe(t *testing.T) {
	tok := Sign(key, "ünï/cødé+=?&#% ")
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`).MatchString(tok) {
		t.Fatalf("token %q is not URL safe", tok)
	}
}

// A token for an empty subject is never one minted for a member.
func TestEmptySubjectFails(t *testing.T) {
	if sub, ok := Verify(key, Sign(key, "")); ok {
		t.Fatalf("Verify(Sign(\"\")) = %q, true", sub)
	}
}

// Base64 with stray low bits decodes to the same bytes as the canonical
// form, so without strict decoding one link would have many spellings.
func TestNonCanonicalEncodingFails(t *testing.T) {
	good := Sign(key, "ab") // "ab" encodes to "YWI", whose last character carries two spare bits
	subject, sig, _ := strings.Cut(good, ".")
	if subject != "YWI" {
		t.Fatalf("subject encodes to %q", subject)
	}
	if _, ok := Verify(key, "YWJ."+sig); ok {
		t.Fatal("a subject with nonzero padding bits verified")
	}
	// The MAC is 32 bytes: 43 characters, the last with two spare bits.
	// The canonical last character has them clear; setting one leaves the
	// decoded bytes the same.
	alt := b64[strings.IndexByte(b64, sig[len(sig)-1])|1]
	if _, ok := Verify(key, subject+"."+sig[:len(sig)-1]+string(alt)); ok {
		t.Fatal("a MAC with nonzero padding bits verified")
	}
}

const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// The base64 decoder skips CR and LF even in strict mode, so a token with
// one inside would be a second spelling of the same link.
func TestLineBreakFails(t *testing.T) {
	good := Sign(key, "ab")
	for _, tok := range []string{"YW\nI" + good[3:], "YW\rI" + good[3:], good[:len(good)-1] + "\n" + good[len(good)-1:]} {
		if _, ok := Verify(key, tok); ok {
			t.Errorf("Verify(%q) succeeded", tok)
		}
	}
}

var digestWeek = time.Date(2026, 10, 26, 8, 0, 0, 0, time.FixedZone("x", -5*3600))

func TestDigestRoundTrip(t *testing.T) {
	sub, date, ok := VerifyDigest(key, SignDigest(key, "sub-1", digestWeek))
	if !ok || sub != "sub-1" || date != "2026-10-26" {
		t.Fatal(sub, date, ok)
	}
}

func TestDigestAndUnsubscribeDoNotCross(t *testing.T) {
	if _, ok := Verify(key, SignDigest(key, "sub-1", digestWeek)); ok {
		t.Fatal("digest token read as unsubscribe")
	}
	if _, _, ok := VerifyDigest(key, Sign(key, "sub-1")); ok {
		t.Fatal("unsubscribe token read as digest")
	}
}

func TestDigestAltered(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	good := SignDigest(key, "sub-1", digestWeek)
	payload, sig, _ := strings.Cut(good, ".")
	other := func(sub string, wk time.Time) string {
		p, _, _ := strings.Cut(SignDigest(key, sub, wk), ".")
		return p
	}
	cases := map[string]string{
		"other subject":   other("sub-2", digestWeek) + "." + sig,
		"other date":      other("sub-1", digestWeek.AddDate(0, 0, 7)) + "." + sig,
		"wrong key":       SignDigest([]byte("another-key-another-key-another!"), "sub-1", digestWeek),
		"empty subject":   SignDigest(key, "", digestWeek),
		"bad date":        enc([]byte("sub-1\x002026-13-40")) + "." + sig,
		"missing NUL":     enc([]byte("sub-12026-10-26")) + "." + sig,
		"empty":           "",
		"no dot":          payload,
		"CR in payload":   payload[:4] + "\r" + payload[4:] + "." + sig,
		"LF in signature": payload + "." + sig[:4] + "\n" + sig[4:],
		"trailing LF":     good + "\n",
	}
	for name, tok := range cases {
		if sub, date, ok := VerifyDigest(key, tok); ok {
			t.Errorf("%s: VerifyDigest = %q, %q, true", name, sub, date)
		}
	}
	// A correctly signed token with a bad date or missing NUL still fails.
	for name, raw := range map[string]string{"bad date": "sub-1\x002026-13-40", "missing NUL": "sub-12026-10-26"} {
		m := hmac.New(sha256.New, key)
		m.Write([]byte("digest\x00" + raw))
		tok := enc([]byte(raw)) + "." + enc(m.Sum(nil))
		if _, _, ok := VerifyDigest(key, tok); ok {
			t.Errorf("signed %s verified", name)
		}
	}
}

func TestPreviewRoundTrip(t *testing.T) {
	sub, date, ok := VerifyPreview(key, SignPreview(key, "sub-1", digestWeek))
	if !ok || sub != "sub-1" || date != "2026-10-26" {
		t.Fatal(sub, date, ok)
	}
}

// A preview token is never read as the week's digest token, or the other way
// round, so a preview link cannot open the member's real digest.
func TestPreviewAndDigestDoNotCross(t *testing.T) {
	if _, _, ok := VerifyDigest(key, SignPreview(key, "sub-1", digestWeek)); ok {
		t.Fatal("preview token read as digest")
	}
	if _, _, ok := VerifyPreview(key, SignDigest(key, "sub-1", digestWeek)); ok {
		t.Fatal("digest token read as preview")
	}
	if _, ok := Verify(key, SignPreview(key, "sub-1", digestWeek)); ok {
		t.Fatal("preview token read as unsubscribe")
	}
	if _, _, ok := VerifyPreview(key, Sign(key, "sub-1")); ok {
		t.Fatal("unsubscribe token read as preview")
	}
}

func TestPreviewAltered(t *testing.T) {
	good := SignPreview(key, "sub-1", digestWeek)
	_, sig, _ := strings.Cut(good, ".")
	p, _, _ := strings.Cut(SignPreview(key, "sub-2", digestWeek), ".")
	for name, tok := range map[string]string{
		"other subject": p + "." + sig,
		"wrong key":     SignPreview([]byte("another-key-another-key-another!"), "sub-1", digestWeek),
		"empty subject": SignPreview(key, "", digestWeek),
		"trailing LF":   good + "\n",
	} {
		if _, _, ok := VerifyPreview(key, tok); ok {
			t.Errorf("%s verified", name)
		}
	}
}
