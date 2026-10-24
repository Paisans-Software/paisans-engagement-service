package config

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

func env(over map[string]string) func(string) string {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	base := map[string]string{
		"ENGAGEMENT_PUBLIC_URL":        "https://digest.example.org",
		"ENGAGEMENT_DATABASE_URL":      "postgres://own",
		"ENGAGEMENT_MBIN_DATABASE_URL": "postgres://mbin",
		"ENGAGEMENT_MBIN_URL":          "https://forum.example.org",
		"ENGAGEMENT_INSTANCE_TITLE":    "Example Forum",
		"ENGAGEMENT_META_TITLE":        "Example",
		"ENGAGEMENT_SIGNING_KEY":       key,
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestLoadValid(t *testing.T) {
	c, err := Load(env(map[string]string{
		"ENGAGEMENT_LISTEN_ADDR":    "127.0.0.1:9000",
		"ENGAGEMENT_SMTP_HOST":      "mail.example.org",
		"ENGAGEMENT_SMTP_PORT":      "465",
		"ENGAGEMENT_SMTP_USER":      "u",
		"ENGAGEMENT_SMTP_PASSWORD":  "p",
		"ENGAGEMENT_SMTP_FROM":      "digest@example.org",
		"ENGAGEMENT_SMTP_FROM_NAME": "Example",
		"ENGAGEMENT_SMTP_TLS":       "tls",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		ListenAddr: "127.0.0.1:9000", PublicURL: "https://digest.example.org",
		DatabaseURL: "postgres://own", MbinDatabaseURL: "postgres://mbin", MbinURL: "https://forum.example.org",
		InstanceTitle: "Example Forum", MetaTitle: "Example", SigningKey: make([]byte, 32),
		SMTP: SMTP{Host: "mail.example.org", Port: 465, User: "u", Password: "p", From: "digest@example.org", FromName: "Example", TLS: "tls"},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8083" || c.SMTP.Port != 587 || c.SMTP.TLS != "starttls" || c.SMTP.Host != "" {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil {
		t.Fatal("empty environment accepted")
	}
	for _, k := range []string{
		"ENGAGEMENT_PUBLIC_URL", "ENGAGEMENT_DATABASE_URL", "ENGAGEMENT_MBIN_DATABASE_URL", "ENGAGEMENT_MBIN_URL",
		"ENGAGEMENT_INSTANCE_TITLE", "ENGAGEMENT_META_TITLE", "ENGAGEMENT_SIGNING_KEY",
	} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error does not name %s: %v", k, err)
		}
	}
	// No mail host means the service sends nothing, so nothing else about
	// mail is required.
	if strings.Contains(err.Error(), "SMTP") {
		t.Errorf("empty environment complains about SMTP: %v", err)
	}
}

func TestLoadRequiresFromWithHost(t *testing.T) {
	_, err := Load(env(map[string]string{"ENGAGEMENT_SMTP_HOST": "mail.example.org"}))
	if err == nil || !strings.Contains(err.Error(), "ENGAGEMENT_SMTP_FROM is required") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadRejectsShortKey(t *testing.T) {
	_, err := Load(env(map[string]string{"ENGAGEMENT_SIGNING_KEY": base64.RawURLEncoding.EncodeToString(make([]byte, 16))}))
	if err == nil || !strings.Contains(err.Error(), "ENGAGEMENT_SIGNING_KEY must be 32 bytes as unpadded base64url") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadRejectsTrailingSlash(t *testing.T) {
	for _, k := range []string{"ENGAGEMENT_PUBLIC_URL", "ENGAGEMENT_MBIN_URL"} {
		_, err := Load(env(map[string]string{k: "https://x.example.org/"}))
		if err == nil || !strings.Contains(err.Error(), k+" must not end in /") {
			t.Errorf("%s: got %v", k, err)
		}
	}
}

func TestLoadRejectsBadScheme(t *testing.T) {
	_, err := Load(env(map[string]string{"ENGAGEMENT_PUBLIC_URL": "ftp://x.example.org"}))
	if err == nil || !strings.Contains(err.Error(), "ENGAGEMENT_PUBLIC_URL must start with https:// or http://") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadRejectsUnknownTLS(t *testing.T) {
	_, err := Load(env(map[string]string{"ENGAGEMENT_SMTP_TLS": "ssl"}))
	if err == nil || !strings.Contains(err.Error(), "ENGAGEMENT_SMTP_TLS must be none, starttls or tls") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadRejectsBadPort(t *testing.T) {
	for _, p := range []string{"0", "65536", "x"} {
		if _, err := Load(env(map[string]string{"ENGAGEMENT_SMTP_PORT": p})); err == nil {
			t.Errorf("port %q accepted", p)
		}
	}
}

// A missing key is one problem, reported once.
func TestLoadMissingKeyOneError(t *testing.T) {
	_, err := Load(env(map[string]string{"ENGAGEMENT_SIGNING_KEY": ""}))
	if err == nil {
		t.Fatal("missing key accepted")
	}
	if n := strings.Count(err.Error(), "ENGAGEMENT_SIGNING_KEY"); n != 1 {
		t.Fatalf("named %d times: %v", n, err)
	}
}

// These values reach mail headers or links, where a line break would let
// the value add a header of its own.
func TestLoadRejectsLineBreaks(t *testing.T) {
	for _, k := range []string{"ENGAGEMENT_META_TITLE", "ENGAGEMENT_INSTANCE_TITLE", "ENGAGEMENT_PUBLIC_URL", "ENGAGEMENT_MBIN_URL"} {
		for _, br := range []string{"\r", "\n"} {
			v := "https://x.example.org" + br + "Bcc: someone@example.org"
			_, err := Load(env(map[string]string{k: v}))
			if err == nil || !strings.Contains(err.Error(), k+" must be one line") {
				t.Errorf("%s with %q: got %v", k, br, err)
			}
		}
	}
}
