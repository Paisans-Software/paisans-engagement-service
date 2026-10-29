// Package mbinapi is a client for Mbin's HTTP API as the engagement agent: a
// bot account whose OAuth client uses the client credentials grant and holds
// Mbin's agent role. It creates the welcome magazine, posts the welcome
// threads and subscribes newcomers to the magazine. Error values never carry
// the client secret or a token.
package mbinapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scopes are the scopes the client asks for.
const Scopes = "agent:magazine:subscribe agent:magazine:create read write moderate:magazine_admin:update"

var (
	// ErrForbidden means Mbin refuses the agent: its client, its role or its
	// standing in the magazine. It lasts until someone changes Mbin.
	ErrForbidden = errors.New("mbin refuses the agent")
	// ErrRefused means Mbin refuses the request itself, such as a target the
	// agent may not act on.
	ErrRefused = errors.New("mbin refuses the request")
	// ErrUnavailable is a network error, a timeout or a 5xx answer.
	ErrUnavailable = errors.New("mbin unavailable")
)

// Magazine is the part of a magazine the welcomes use.
type Magazine struct {
	ID                int64
	Name              string
	PostingRestricted bool
}

// Entry is a thread the agent posted.
type Entry struct {
	ID   int64
	Slug string
}

// Client talks to one Mbin.
type Client struct {
	base, host, id, secret string
	hc                     *http.Client

	mu    sync.Mutex
	token string
	until time.Time
}

// New returns a client for the Mbin at baseURL. host, when not empty, is
// sent as every request's Host: Mbin answers only for its own hostname, and
// baseURL is its mesh address. A nil hc gets a client with a 30 second
// timeout.
func New(baseURL, host, clientID, secret string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), host: host, id: clientID, secret: secret, hc: hc}
}

// send performs a request. Transport errors are reduced to fixed text,
// because net/http's own names the URL.
func (c *Client) send(ctx context.Context, req *http.Request, what string) (int, []byte, error) {
	if c.host != "" {
		req.Host = c.host
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, fmt.Errorf("%w: %s request failed", ErrUnavailable, what)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %s answer unreadable", ErrUnavailable, what)
	}
	return resp.StatusCode, b, nil
}

// bearer returns a token, asking Mbin for one when none is cached or the
// cached one is about to expire.
func (c *Client) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.until) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.id},
		"client_secret": {c.secret}, "scope": {Scopes}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("mbin API URL invalid")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	status, body, err := c.send(ctx, req, "token")
	if err != nil {
		return "", err
	}
	switch {
	case status >= 500:
		return "", fmt.Errorf("%w: token endpoint answered %d", ErrUnavailable, status)
	case status != http.StatusOK:
		return "", fmt.Errorf("%w: the token endpoint refused the client credentials (status %d)", ErrForbidden, status)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if json.Unmarshal(body, &out) != nil || out.AccessToken == "" {
		return "", errors.New("mbin token endpoint answered without an access token")
	}
	life := max(time.Duration(out.ExpiresIn)*time.Second-time.Minute, 30*time.Second)
	c.token, c.until = out.AccessToken, time.Now().Add(life)
	return c.token, nil
}

func (c *Client) drop(stale string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == stale {
		c.token = ""
	}
}

// errNotFound is a 404, which only MagazineByName turns into an answer.
var errNotFound = errors.New("mbin: not found")

// do calls the API. A 401 drops the token and tries once more. what names
// the call in errors, in place of a path that may hold ids.
func (c *Client) do(ctx context.Context, method, path, what string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		tok, err := c.bearer(ctx)
		if err != nil {
			return err
		}
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
		if err != nil {
			return errors.New("mbin API URL invalid")
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		status, b, err := c.send(ctx, req, what)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusUnauthorized && attempt == 0:
			c.drop(tok)
			continue
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return fmt.Errorf("%w: %s answered %d", ErrForbidden, what, status)
		case status == http.StatusNotFound:
			return fmt.Errorf("%w: %s", errNotFound, what)
		case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: %s answered %d", ErrRefused, what, status)
		case status >= 500:
			return fmt.Errorf("%w: %s answered %d", ErrUnavailable, what, status)
		case status >= 300:
			return fmt.Errorf("mbin %s: unexpected status %d", what, status)
		}
		if out != nil && json.Unmarshal(b, out) != nil {
			return fmt.Errorf("mbin %s: unreadable answer", what)
		}
		return nil
	}
}

type magazineJSON struct {
	ID         int64  `json:"magazineId"`
	Name       string `json:"name"`
	Restricted bool   `json:"isPostingRestrictedToMods"`
}

func (m magazineJSON) magazine() Magazine {
	return Magazine{ID: m.ID, Name: m.Name, PostingRestricted: m.Restricted}
}

// MagazineByName returns the magazine; false when Mbin has none by that name.
func (c *Client) MagazineByName(ctx context.Context, name string) (Magazine, bool, error) {
	var m magazineJSON
	err := c.do(ctx, http.MethodGet, "/api/magazine/name/"+url.PathEscape(name), "magazine by name", nil, &m)
	if errors.Is(err, errNotFound) {
		return Magazine{}, false, nil
	}
	if err != nil {
		return Magazine{}, false, err
	}
	return m.magazine(), true, nil
}

// CreateMagazine creates a public magazine, posting restricted to its
// moderators, with the agent as its owner.
func (c *Client) CreateMagazine(ctx context.Context, name, title, description string) (Magazine, error) {
	var m magazineJSON
	err := c.do(ctx, http.MethodPost, "/api/agent/magazines", "create magazine", map[string]any{
		"name": name, "title": title, "description": description,
		"isAdult": false, "isPostingRestrictedToMods": true,
	}, &m)
	if err != nil {
		return Magazine{}, err
	}
	return m.magazine(), nil
}

// RestrictPosting restricts the magazine's new threads to its moderators.
// isAdult is sent as false because Mbin's update reads a missing isAdult as
// null.
func (c *Client) RestrictPosting(ctx context.Context, magazineID int64) error {
	return c.do(ctx, http.MethodPut, "/api/moderate/magazine/"+strconv.FormatInt(magazineID, 10), "update magazine",
		map[string]any{"isPostingRestrictedToMods": true, "isAdult": false}, nil)
}

// CreateThread posts a text thread in the magazine as the agent.
func (c *Client) CreateThread(ctx context.Context, magazineID int64, title, body string) (Entry, error) {
	var out struct {
		ID   int64  `json:"entryId"`
		Slug string `json:"slug"`
	}
	err := c.do(ctx, http.MethodPost, "/api/magazine/"+strconv.FormatInt(magazineID, 10)+"/article", "create thread",
		map[string]any{"title": title, "body": body, "lang": "en", "isAdult": false, "isOc": false}, &out)
	if err != nil {
		return Entry{}, err
	}
	return Entry{ID: out.ID, Slug: out.Slug}, nil
}

// Subscribe subscribes the member to the magazine. Subscribing someone
// already subscribed succeeds.
func (c *Client) Subscribe(ctx context.Context, userID, magazineID int64) error {
	return c.do(ctx, http.MethodPut,
		"/api/agent/users/"+strconv.FormatInt(userID, 10)+"/magazines/"+strconv.FormatInt(magazineID, 10)+"/subscription",
		"subscribe", nil, nil)
}
