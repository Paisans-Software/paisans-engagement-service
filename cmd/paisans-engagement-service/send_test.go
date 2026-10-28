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
		matrix  bool
		wantErr bool
	}{
		{nil, "", false, false, false},
		{[]string{"--member", "alice"}, "alice", false, false, false},
		{[]string{"-member=alice"}, "alice", false, false, false},
		{[]string{"--member"}, "", false, false, true},
		{[]string{"--member", ""}, "", false, false, true},
		{[]string{"--member", " alice"}, "", false, false, true},
		{[]string{"extra"}, "", false, false, true},
		{[]string{"--everyone"}, "", false, false, true},
		{[]string{"--member", "alice", "--preview"}, "alice", true, false, false},
		{[]string{"--preview", "--member=alice"}, "alice", true, false, false},
		{[]string{"--preview"}, "", false, false, true},
		{[]string{"--member", "alice", "--preview", "bob"}, "", false, false, true},
		{[]string{"--member", "alice", "--matrix"}, "alice", false, true, false},
		{[]string{"--matrix", "--member=alice", "--preview"}, "alice", true, true, false},
		{[]string{"--matrix"}, "", false, false, true},
		{[]string{"--matrix", "--preview"}, "", false, false, true},
	}
	for _, c := range cases {
		got, err := parseSend(c.args)
		if (err != nil) != c.wantErr {
			t.Errorf("parseSend(%q): error %v, want error %v", c.args, err, c.wantErr)
			continue
		}
		if err == nil && (got.member != c.member || got.preview != c.preview || got.matrix != c.matrix) {
			t.Errorf("parseSend(%q): member %q preview %v matrix %v, want %q preview %v matrix %v",
				c.args, got.member, got.preview, got.matrix, c.member, c.preview, c.matrix)
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

// Without every Matrix variable, send --matrix refuses before it opens any
// database, and names what is missing; it needs no mail host.
func TestSendMatrixRefusesWithoutMatrix(t *testing.T) {
	env := map[string]string{
		"ENGAGEMENT_PUBLIC_URL":         "https://digest.example.org",
		"ENGAGEMENT_DATABASE_URL":       "postgres://unreachable.invalid/engagement",
		"ENGAGEMENT_MBIN_DATABASE_URL":  "postgres://unreachable.invalid/mbin",
		"ENGAGEMENT_MBIN_URL":           "https://talk.example.org",
		"ENGAGEMENT_INSTANCE_TITLE":     "Talk",
		"ENGAGEMENT_META_TITLE":         "Talk",
		"ENGAGEMENT_SIGNING_KEY":        "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"ENGAGEMENT_MATRIX_URL":         "https://matrix.example.org",
		"ENGAGEMENT_MATRIX_SERVER_NAME": "example.org",
	}
	for _, args := range [][]string{{"--member", "alice", "--matrix"}, {"--member", "alice", "--matrix", "--preview"}} {
		err := send(context.Background(), args, func(k string) string { return env[k] }, time.Now)
		if err == nil || !strings.Contains(err.Error(), "ENGAGEMENT_MAS_ADMIN_URL") || strings.Contains(err.Error(), "SMTP") {
			t.Fatalf("send %q without Matrix: %v", args, err)
		}
	}
}
