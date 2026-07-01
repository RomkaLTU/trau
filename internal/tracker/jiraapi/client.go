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

// Issue is the subset of a Jira issue the tracker consumes. Description is the v3
// ADF body flattened to plain text; Status/Resolution/Project/Parent back the
// tracker's status, ownership-guard and epic-parent reads.
type Issue struct {
	Key         string
	Summary     string
	Description string
	Status      Status
	Resolution  string // resolution.name, "" while unresolved
	Project     Project
	Parent      string // parent issue key, "" when top-level
}

// Status is an issue's workflow status. Category is the stable statusCategory.key
// (new | indeterminate | done); Name is the display label.
type Status struct {
	Name     string
	Category string
}

// Project is the Jira project an issue belongs to. Key is the canonical
// identifier (the "PROJ" of PROJ-414).
type Project struct {
	Key  string
	Name string
	ID   string
}

// Issue fetches a single issue by its key (e.g. "PROJ-414"), reading the summary,
// description, status, resolution, project and parent fields the tracker consumes.
func (c *Client) Issue(ctx context.Context, key string) (*Issue, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrNotFound
	}
	var dst issueResponse
	path := "/issue/" + url.PathEscape(key) + "?fields=summary,description,status,resolution,project,parent"
	if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
		return nil, err
	}
	return dst.toIssue(), nil
}

type issueResponse struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
		Status      *struct {
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		} `json:"status"`
		Resolution *struct {
			Name string `json:"name"`
		} `json:"resolution"`
		Project *struct {
			Key  string `json:"key"`
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"project"`
		Parent *struct {
			Key string `json:"key"`
		} `json:"parent"`
	} `json:"fields"`
}

// toIssue maps the raw REST payload onto the Issue the tracker consumes,
// tolerating absent optional objects (null status/resolution/project/parent).
func (r *issueResponse) toIssue() *Issue {
	iss := &Issue{
		Key:         r.Key,
		Summary:     r.Fields.Summary,
		Description: adfToText(r.Fields.Description),
	}
	if s := r.Fields.Status; s != nil {
		iss.Status = Status{Name: s.Name, Category: s.StatusCategory.Key}
	}
	if res := r.Fields.Resolution; res != nil {
		iss.Resolution = res.Name
	}
	if p := r.Fields.Project; p != nil {
		iss.Project = Project{Key: p.Key, Name: p.Name, ID: p.ID}
	}
	if p := r.Fields.Parent; p != nil {
		iss.Parent = p.Key
	}
	return iss
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

// adfNode is one node of an Atlassian Document Format tree. Text carries inline
// content (marks are ignored); Content holds child nodes.
type adfNode struct {
	Type    string    `json:"type"`
	Text    string    `json:"text"`
	Content []adfNode `json:"content"`
}

// adfToText flattens a v3 ADF description document to plain, readable text. A
// missing or null description, or any decode failure, yields "" so callers treat
// an unreadable body as "no description" rather than an error.
func adfToText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	var doc adfNode
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return ""
	}
	var b strings.Builder
	writeADF(&b, doc)
	return strings.TrimSpace(collapseBlankLines(b.String()))
}

// writeADF walks an ADF node depth-first, emitting text and a newline after every
// block-level node so paragraphs and list items stay on their own lines.
func writeADF(b *strings.Builder, n adfNode) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
	case "hardBreak":
		b.WriteByte('\n')
	}
	for _, c := range n.Content {
		writeADF(b, c)
	}
	if isADFBlock(n.Type) {
		b.WriteByte('\n')
	}
}

// isADFBlock reports whether an ADF node type is block-level and should be
// followed by a line break in the flattened text.
func isADFBlock(t string) bool {
	switch t {
	case "paragraph", "heading", "blockquote", "codeBlock", "rule", "panel",
		"listItem", "bulletList", "orderedList", "taskItem", "taskList",
		"decisionItem", "decisionList", "mediaSingle", "mediaGroup", "tableRow":
		return true
	default:
		return false
	}
}

// collapseBlankLines squeezes runs of three or more newlines down to a single
// blank line so nested block nodes don't stack extra spacing.
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
