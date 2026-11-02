// Package config reads the service's environment. The variable names are the
// contract with the Paisans toolkit's engagement blueprint.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
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

// Matrix configures the bot. The bot runs only when all seven variables are
// set; with fewer, the service behaves as it does without Matrix. The display
// name is optional and not one of the seven.
type Matrix struct {
	// URL is the homeserver's public address; ServerName is the part after
	// the colon in Matrix ids.
	URL, ServerName string
	// MASAdminURL is the Matrix Authentication Service's admin listener;
	// ClientID and ClientSecret are the service's client there.
	MASAdminURL, ClientID, ClientSecret string
	// UpstreamProvider is the id of the identity provider members sign in
	// with; BotLocalpart is the bot's Matrix user name.
	UpstreamProvider, BotLocalpart string
	// BotDisplayName is the bot's profile name, DefaultBotDisplayName when
	// ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME is unset or empty.
	BotDisplayName string
}

const (
	// DefaultBotDisplayName is the bot's profile name unless configured.
	DefaultBotDisplayName = "Community Bot"
	// maxDisplayName bounds the configured display name, in characters.
	maxDisplayName = 256
)

// variable is an environment variable's name and value.
type variable struct{ name, value string }

// vars pairs each variable with the field it fills, in the order Missing
// reports them.
func (m Matrix) vars() []variable {
	return []variable{
		{"ENGAGEMENT_MATRIX_URL", m.URL},
		{"ENGAGEMENT_MATRIX_SERVER_NAME", m.ServerName},
		{"ENGAGEMENT_MAS_ADMIN_URL", m.MASAdminURL},
		{"ENGAGEMENT_MAS_CLIENT_ID", m.ClientID},
		{"ENGAGEMENT_MAS_CLIENT_SECRET", m.ClientSecret},
		{"ENGAGEMENT_MAS_UPSTREAM_PROVIDER", m.UpstreamProvider},
		{"ENGAGEMENT_MATRIX_BOT_LOCALPART", m.BotLocalpart},
	}
}

// Enabled reports whether all seven variables are set.
func (m Matrix) Enabled() bool { return len(m.Missing()) == 0 }

// Missing names the unset variables.
func (m Matrix) Missing() []string { return missing(m.vars()) }

// Welcome configures the welcomes. Each part runs only when its variables are
// set: the Mbin welcomes need the three agent variables, the Matrix welcomes
// the two room variables as well as Matrix, and the daily email a mail host.
type Welcome struct {
	// MbinAPIURL is where the service reaches Mbin's API; AgentClientID and
	// AgentClientSecret are its agent OAuth client there.
	MbinAPIURL, AgentClientID, AgentClientSecret string
	// RoomName is the community room's name; RoomAlias the localpart of its
	// alias.
	RoomName, RoomAlias string
	// DailyLimit is the most members the welcome email goes to in a day.
	DailyLimit int
	// Operators are the addresses alerts go to.
	Operators []string
}

// DefaultWelcomeDailyLimit is the daily limit unless configured.
const DefaultWelcomeDailyLimit = 50

func (w Welcome) mbinVars() []variable {
	return []variable{
		{"ENGAGEMENT_MBIN_API_URL", w.MbinAPIURL},
		{"ENGAGEMENT_MBIN_AGENT_CLIENT_ID", w.AgentClientID},
		{"ENGAGEMENT_MBIN_AGENT_CLIENT_SECRET", w.AgentClientSecret},
	}
}

func (w Welcome) roomVars() []variable {
	return []variable{
		{"ENGAGEMENT_COMMUNITY_ROOM_NAME", w.RoomName},
		{"ENGAGEMENT_COMMUNITY_ROOM_ALIAS", w.RoomAlias},
	}
}

// MbinMissing names the unset variables the Mbin welcomes need.
func (w Welcome) MbinMissing() []string { return missing(w.mbinVars()) }

// RoomMissing names the unset variables the Matrix welcomes need on top of
// Matrix's own.
func (w Welcome) RoomMissing() []string { return missing(w.roomVars()) }

func missing(vs []variable) []string {
	var out []string
	for _, v := range vs {
		if v.value == "" {
			out = append(out, v.name)
		}
	}
	return out
}

var (
	aliasRE     = regexp.MustCompile(`^[a-z0-9-]+$`)
	localpartRE = regexp.MustCompile(`^[a-z0-9._=/-]+$`)
	ulidRE      = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
)

type Config struct {
	ListenAddr string
	// PublicURL is where members reach the unsubscribe page.
	PublicURL string
	// DatabaseURL is the service's own database; MbinDatabaseURL is Mbin's,
	// reached through a role that can only read.
	DatabaseURL, MbinDatabaseURL string
	// MbinURL is the community's Mbin, which the digest links to.
	MbinURL string
	// MetaTitle names the community in the digest.
	MetaTitle  string
	SigningKey []byte
	SMTP       SMTP
	Matrix     Matrix
	Welcome    Welcome
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
		v := req(k)
		if v == "" {
			return nil
		}
		b, err := base64.RawURLEncoding.DecodeString(v)
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
		MetaTitle:       req("ENGAGEMENT_META_TITLE"),
		SigningKey:      key("ENGAGEMENT_SIGNING_KEY"),
		SMTP: SMTP{
			Host: getenv("ENGAGEMENT_SMTP_HOST"), Port: intIn("ENGAGEMENT_SMTP_PORT", "587", 1, 65535),
			User: getenv("ENGAGEMENT_SMTP_USER"), Password: getenv("ENGAGEMENT_SMTP_PASSWORD"),
			FromName: getenv("ENGAGEMENT_SMTP_FROM_NAME"), TLS: opt("ENGAGEMENT_SMTP_TLS", "starttls"),
		},
	}
	c.Matrix = Matrix{
		URL: getenv("ENGAGEMENT_MATRIX_URL"), ServerName: getenv("ENGAGEMENT_MATRIX_SERVER_NAME"),
		MASAdminURL: getenv("ENGAGEMENT_MAS_ADMIN_URL"), ClientID: getenv("ENGAGEMENT_MAS_CLIENT_ID"),
		ClientSecret:     getenv("ENGAGEMENT_MAS_CLIENT_SECRET"),
		UpstreamProvider: getenv("ENGAGEMENT_MAS_UPSTREAM_PROVIDER"),
		BotLocalpart:     getenv("ENGAGEMENT_MATRIX_BOT_LOCALPART"),
		BotDisplayName:   DefaultBotDisplayName,
	}
	c.Welcome = Welcome{
		MbinAPIURL: getenv("ENGAGEMENT_MBIN_API_URL"), AgentClientID: getenv("ENGAGEMENT_MBIN_AGENT_CLIENT_ID"),
		AgentClientSecret: getenv("ENGAGEMENT_MBIN_AGENT_CLIENT_SECRET"),
		RoomName:          strings.TrimSpace(getenv("ENGAGEMENT_COMMUNITY_ROOM_NAME")),
		RoomAlias:         getenv("ENGAGEMENT_COMMUNITY_ROOM_ALIAS"),
		DailyLimit:        intIn("ENGAGEMENT_WELCOME_DAILY_LIMIT", strconv.Itoa(DefaultWelcomeDailyLimit), 1, 1_000_000),
	}
	for _, a := range strings.Split(getenv("ENGAGEMENT_OPERATOR_EMAILS"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			c.Welcome.Operators = append(c.Welcome.Operators, a)
		}
	}
	errs = append(errs, c.Welcome.validate()...)
	// A display name of only spaces is set, not empty, and refused below.
	displayName := getenv("ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME")
	if displayName != "" {
		c.Matrix.BotDisplayName = strings.TrimSpace(displayName)
	}
	// Without a mail host nothing is sent, so a sender address is only
	// needed once there is one.
	if c.SMTP.Host != "" {
		c.SMTP.From = req("ENGAGEMENT_SMTP_FROM")
	} else {
		c.SMTP.From = getenv("ENGAGEMENT_SMTP_FROM")
	}
	// These reach mail headers and links, where a line break would let the
	// value start a header of its own.
	errs = append(errs, oneLine([]variable{
		{"ENGAGEMENT_META_TITLE", c.MetaTitle},
		{"ENGAGEMENT_PUBLIC_URL", c.PublicURL},
		{"ENGAGEMENT_MBIN_URL", c.MbinURL},
	})...)
	switch c.SMTP.TLS {
	case "none", "starttls", "tls":
	default:
		errs = append(errs, errors.New("ENGAGEMENT_SMTP_TLS must be none, starttls or tls"))
	}
	errs = append(errs, baseURLs([]variable{
		{"ENGAGEMENT_PUBLIC_URL", c.PublicURL},
		{"ENGAGEMENT_MBIN_URL", c.MbinURL},
	})...)
	if c.Matrix.Enabled() {
		errs = append(errs, c.Matrix.validate()...)
	}
	return c, errors.Join(errs...)
}

// oneLine refuses a value with a line break.
func oneLine(vs []variable) []error {
	var errs []error
	for _, v := range vs {
		if strings.ContainsAny(v.value, "\r\n") {
			errs = append(errs, fmt.Errorf("%s must be one line", v.name))
		}
	}
	return errs
}

// baseURLs refuses a value that is not an http or https URL without a
// trailing slash. An empty value is left to the check that requires it.
func baseURLs(vs []variable) []error {
	var errs []error
	for _, u := range vs {
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
	return errs
}

func (w Welcome) validate() []error {
	errs := oneLine(append(w.mbinVars(), w.roomVars()...))
	errs = append(errs, baseURLs([]variable{{"ENGAGEMENT_MBIN_API_URL", w.MbinAPIURL}})...)
	if w.RoomAlias != "" && !aliasRE.MatchString(w.RoomAlias) {
		errs = append(errs, errors.New("ENGAGEMENT_COMMUNITY_ROOM_ALIAS must be lower case letters, digits and -"))
	}
	for _, a := range w.Operators {
		// An address reaches a mail header, where a line break would start
		// a header of its own.
		if strings.ContainsAny(a, "\r\n") {
			errs = append(errs, errors.New("ENGAGEMENT_OPERATOR_EMAILS must be one line"))
			break
		}
	}
	return errs
}

func (m Matrix) validate() []error {
	errs := oneLine(m.vars())
	errs = append(errs, baseURLs([]variable{
		{"ENGAGEMENT_MATRIX_URL", m.URL},
		{"ENGAGEMENT_MAS_ADMIN_URL", m.MASAdminURL},
	})...)
	const name = "ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME"
	switch n := utf8.RuneCountInString(m.BotDisplayName); {
	case n == 0:
		errs = append(errs, errors.New(name+" must not be blank"))
	case n > maxDisplayName:
		errs = append(errs, fmt.Errorf("%s must be at most %d characters", name, maxDisplayName))
	case strings.ContainsAny(m.BotDisplayName, "\r\n"):
		errs = append(errs, errors.New(name+" must be one line"))
	}
	if !localpartRE.MatchString(m.BotLocalpart) {
		errs = append(errs, errors.New("ENGAGEMENT_MATRIX_BOT_LOCALPART must be lower case letters, digits and . _ = / -"))
	}
	for _, id := range []variable{
		{"ENGAGEMENT_MAS_CLIENT_ID", m.ClientID},
		{"ENGAGEMENT_MAS_UPSTREAM_PROVIDER", m.UpstreamProvider},
	} {
		if !ulidRE.MatchString(id.value) {
			errs = append(errs, fmt.Errorf("%s must be a 26 character upper case ULID", id.name))
		}
	}
	return errs
}
