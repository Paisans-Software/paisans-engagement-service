// Package config reads the service's environment. The variable names are the
// contract with the Paisans toolkit's engagement blueprint.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type SMTP struct {
	// Host is empty when the deployment has no mail host; the service then
	// sends nothing.
	Host     string
	Port     int
	User     string
	Password string
	From     string
	FromName string
	TLS      string
}

type Config struct {
	ListenAddr string
	// PublicURL is where members reach the unsubscribe page.
	PublicURL string
	// DatabaseURL is the service's own database; MbinDatabaseURL is Mbin's,
	// reached through a role that can only read.
	DatabaseURL, MbinDatabaseURL string
	// MbinURL is the community's Mbin, which the digest links to.
	MbinURL string
	// InstanceTitle is Mbin's site title, used on the button; MetaTitle names
	// the community everywhere else in the email.
	InstanceTitle, MetaTitle string
	SigningKey               []byte
	SMTP                     SMTP
}

func Load(getenv func(string) string) (Config, error) {
	var errs []error
	req := func(k string) string {
		v := getenv(k)
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", k))
		}
		return v
	}
	opt := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	key := func(k string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(req(k))
		if err != nil || len(b) != 32 {
			errs = append(errs, fmt.Errorf("%s must be 32 bytes as unpadded base64url", k))
			return nil
		}
		return b
	}
	intIn := func(k, def string, lo, hi int) int {
		n, err := strconv.Atoi(opt(k, def))
		if err != nil || n < lo || n > hi {
			errs = append(errs, fmt.Errorf("%s must be a whole number from %d to %d", k, lo, hi))
		}
		return n
	}

	c := Config{
		ListenAddr:      opt("ENGAGEMENT_LISTEN_ADDR", ":8083"),
		PublicURL:       req("ENGAGEMENT_PUBLIC_URL"),
		DatabaseURL:     req("ENGAGEMENT_DATABASE_URL"),
		MbinDatabaseURL: req("ENGAGEMENT_MBIN_DATABASE_URL"),
		MbinURL:         req("ENGAGEMENT_MBIN_URL"),
		InstanceTitle:   req("ENGAGEMENT_INSTANCE_TITLE"),
		MetaTitle:       req("ENGAGEMENT_META_TITLE"),
		SigningKey:      key("ENGAGEMENT_SIGNING_KEY"),
		SMTP: SMTP{
			Host: getenv("ENGAGEMENT_SMTP_HOST"), Port: intIn("ENGAGEMENT_SMTP_PORT", "587", 1, 65535),
			User: getenv("ENGAGEMENT_SMTP_USER"), Password: getenv("ENGAGEMENT_SMTP_PASSWORD"),
			FromName: getenv("ENGAGEMENT_SMTP_FROM_NAME"), TLS: opt("ENGAGEMENT_SMTP_TLS", "starttls"),
		},
	}
	// Without a mail host nothing is sent, so a sender address is only
	// needed once there is one.
	if c.SMTP.Host != "" {
		c.SMTP.From = req("ENGAGEMENT_SMTP_FROM")
	} else {
		c.SMTP.From = getenv("ENGAGEMENT_SMTP_FROM")
	}
	switch c.SMTP.TLS {
	case "none", "starttls", "tls":
	default:
		errs = append(errs, errors.New("ENGAGEMENT_SMTP_TLS must be none, starttls or tls"))
	}
	for _, u := range []struct{ name, value string }{
		{"ENGAGEMENT_PUBLIC_URL", c.PublicURL},
		{"ENGAGEMENT_MBIN_URL", c.MbinURL},
	} {
		if u.value == "" {
			continue
		}
		if !strings.HasPrefix(u.value, "https://") && !strings.HasPrefix(u.value, "http://") {
			errs = append(errs, fmt.Errorf("%s must start with https:// or http://", u.name))
		}
		if strings.HasSuffix(u.value, "/") {
			errs = append(errs, fmt.Errorf("%s must not end in /", u.name))
		}
	}
	return c, errors.Join(errs...)
}
