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
	_, mac, _ := strings.Cut(Sign(key, "alice"), ".")
	forged := Sign(key, "bob")
	forgedSubject, _, _ := strings.Cut(forged, ".")
	if _, ok := Verify(key, forgedSubject+"."+mac); ok {
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
	subject, mac, _ := strings.Cut(good, ".")
	for _, tok := range []string{"", "a", "a.b.c", "!!!.!!!", subject + ".!!!", "!!!." + mac, subject + ".", "." + mac, good + "."} {
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
