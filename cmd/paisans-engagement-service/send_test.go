package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseSend(t *testing.T) {
	cases := []struct {
		args    []string
		member  string
		preview bool
		wantErr bool
	}{
		{nil, "", false, false},
		{[]string{"--member", "alice"}, "alice", false, false},
		{[]string{"-member=alice"}, "alice", false, false},
		{[]string{"--member"}, "", false, true},
		{[]string{"--member", ""}, "", false, true},
		{[]string{"--member", " alice"}, "", false, true},
		{[]string{"extra"}, "", false, true},
		{[]string{"--everyone"}, "", false, true},
		{[]string{"--member", "alice", "--preview"}, "alice", true, false},
		{[]string{"--preview", "--member=alice"}, "alice", true, false},
		{[]string{"--preview"}, "", false, true},
		{[]string{"--member", "alice", "--preview", "bob"}, "", false, true},
	}
	for _, c := range cases {
		got, err := parseSend(c.args)
		if (err != nil) != c.wantErr {
			t.Errorf("parseSend(%q): error %v, want error %v", c.args, err, c.wantErr)
			continue
		}
		if err == nil && (got.member != c.member || got.preview != c.preview) {
			t.Errorf("parseSend(%q): member %q preview %v, want %q preview %v", c.args, got.member, got.preview, c.member, c.preview)
		}
	}
}

// send sends the latest Monday's week whatever the day, not only on the
// Monday the schedule allows.
func TestSendWeek(t *testing.T) {
	utc := time.UTC
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 10, 28, 15, 0, 0, 0, utc), time.Date(2026, 10, 26, 8, 0, 0, 0, utc)},
		{time.Date(2026, 11, 1, 23, 0, 0, 0, utc), time.Date(2026, 10, 26, 8, 0, 0, 0, utc)},
		{time.Date(2026, 10, 26, 7, 0, 0, 0, utc), time.Date(2026, 10, 19, 8, 0, 0, 0, utc)},
	}
	for _, c := range cases {
		if got := sendWeek(c.now); !got.Equal(c.want) {
			t.Errorf("sendWeek(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

// Without a mail host, send refuses before it opens any database.
func TestSendRefusesWithoutMailHost(t *testing.T) {
	env := map[string]string{
		"ENGAGEMENT_PUBLIC_URL":        "https://digest.example.org",
		"ENGAGEMENT_DATABASE_URL":      "postgres://unreachable.invalid/engagement",
		"ENGAGEMENT_MBIN_DATABASE_URL": "postgres://unreachable.invalid/mbin",
		"ENGAGEMENT_MBIN_URL":          "https://talk.example.org",
		"ENGAGEMENT_INSTANCE_TITLE":    "Talk",
		"ENGAGEMENT_META_TITLE":        "Talk",
		"ENGAGEMENT_SIGNING_KEY":       "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	err := send(context.Background(), nil, func(k string) string { return env[k] }, time.Now)
	if err == nil || !strings.Contains(err.Error(), "ENGAGEMENT_SMTP_HOST") {
		t.Fatalf("send without a mail host: %v", err)
	}
}
