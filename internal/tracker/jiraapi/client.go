// Package jiraapi is a small, pragmatic REST client for Jira Cloud's v3 API.
// It covers the fast read/write operations the Trau loop performs frequently so
// they do not need an expensive MCP/agent round-trip. Complex operations that
// read files or need reasoning still go through the Jira (Rovo) MCP.
//
// Auth is stateless HTTP Basic (base64(email:api_token)) against a per-site base
// URL, so a client holds exactly one account's credentials — two repos with two
// separate Jira accounts each carry their own credential set with no shared
// session, unlike the single-identity Rovo MCP.
package jiraapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Common errors the tracker uses to decide whether to fall back to the MCP.
var (
	ErrNotFound     = errors.New("jira: issue not found")
	ErrUnauthorized = errors.New("jira: unauthorized")
	ErrNotEnabled   = errors.New("jira: direct API not enabled")
)

const (
	// apiPrefix is the classic (unscoped) REST v3 base path. It works with the
	// simple https://<site>.atlassian.net base URL; scoped tokens would require
	// the api.atlassian.com/ex/jira/{cloudId} host and a cloudId lookup.
	apiPrefix = "/rest/api/3"

	// maxRetries bounds the 429 retry loop so a rate-limited site can't stall a
	// run indefinitely.
	maxRetries = 4
)

// Client talks to a single Jira Cloud site over the REST v3 API.
type Client struct {
	baseURL string
	auth    string // "Basic <base64(email:token)>", or "" when the client is disabled
	http    *http.Client
}

// New returns a client for the given site. An empty token (or a missing base URL
// or email) leaves the client disabled, so every method returns ErrNotEnabled and
// callers fall back to the MCP.
func New(baseURL, email, apiToken string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	email = strings.TrimSpace(email)
	apiToken = strings.TrimSpace(apiToken)
	c := &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	if baseURL != "" && email != "" && apiToken != "" {
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+apiToken))
	}
	return c
}

// enabled reports whether the client has the credentials to reach the API.
func (c *Client) enabled() bool { return c.auth != "" }

// Issue is the subset of a Jira issue the tracker consumes.
type Issue struct {
	Key     string
	Summary string
}

// Issue fetches a single issue by its key (e.g. "PROJ-414"). Only the summary is
// requested; later slices widen the fields as they add methods.
func (c *Client) Issue(ctx context.Context, key string) (*Issue, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrNotFound
	}
	var dst issueResponse
	path := "/issue/" + url.PathEscape(key) + "?fields=summary"
	if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
		return nil, err
	}
	return &Issue{Key: dst.Key, Summary: dst.Fields.Summary}, nil
}

type issueResponse struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
	} `json:"fields"`
}

// do performs a request against the REST v3 API and decodes the JSON response
// into dst (which may be nil for calls with no body of interest). It sets the
// Basic-auth header, honours a 429 Retry-After up to maxRetries, and maps auth
// and not-found statuses onto the typed sentinels.
func (c *Client) do(ctx context.Context, method, path string, body []byte, dst any) error {
	endpoint := c.baseURL + apiPrefix + path
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.auth)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		res, err := c.http.Do(req)
		if err != nil {
			return err
		}

		if res.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			wait := retryAfter(res.Header.Get("Retry-After"), attempt)
			_ = res.Body.Close()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		return decode(res, dst)
	}
}

// decode closes the response body and turns an HTTP status into either a typed
// error or a JSON unmarshal into dst.
func decode(res *http.Response, dst any) error {
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	}

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("jira: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(resBody)))
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(resBody, dst); err != nil {
		return fmt.Errorf("jira: invalid response (%d): %w", res.StatusCode, err)
	}
	return nil
}

// retryAfter derives how long to wait before retrying a 429. It honours a numeric
// Retry-After (seconds) header when present, otherwise backs off exponentially
// (1s, 2s, 4s, …) capped at 30s.
func retryAfter(header string, attempt int) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	backoff := time.Duration(1<<attempt) * time.Second
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	return backoff
}
