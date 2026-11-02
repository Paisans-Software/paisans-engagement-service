package token

import (
	"regexp"
	"strings"
	"testing"
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

func TestWelcomeTokenIsNotADigestToken(t *testing.T) {
	w := SignFor(key, Welcome, "s")
	if _, ok := Verify(key, w); ok {
		t.Fatal("a welcome token verified as a digest token")
	}
	if p, sub, ok := VerifyFor(key, w); !ok || p != Welcome || sub != "s" {
		t.Fatalf("VerifyFor(welcome) = %q, %q, %v", p, sub, ok)
	}
	if p, sub, ok := VerifyFor(key, Sign(key, "s")); !ok || p != Digest || sub != "s" {
		t.Fatalf("VerifyFor(digest) = %q, %q, %v", p, sub, ok)
	}
	if SignFor(key, Digest, "s") != Sign(key, "s") {
		t.Fatal("a digest token changed shape")
	}
	// A digest signature under the welcome prefix is not a welcome token.
	if _, _, ok := VerifyFor(key, "w."+Sign(key, "s")); ok {
		t.Fatal("a digest signature passed as a welcome token")
	}
}
