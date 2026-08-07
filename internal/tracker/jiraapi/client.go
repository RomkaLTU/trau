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
	"math/rand/v2"
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
	// ErrRateLimited marks a 429 the retry ladder could not outlast: the site's
	// request budget refills on its own, so it is transient, not misconfiguration.
	ErrRateLimited = errors.New("jira: rate limit exceeded")
)

// TokenHelpURL is where a user regenerates a classic Jira API token. Classic
// tokens cannot self-refresh and expire roughly annually, so a 401/403 often
// means the token lapsed — though the same status covers an email that is not
// the account the token was minted for.
const TokenHelpURL = "https://id.atlassian.com/manage-profile/security/api-tokens"

// AuthErrorMessage returns an actionable, user-facing hint for an ErrUnauthorized,
// or "" for any other error. It deliberately does not reword ErrUnauthorized itself:
// the sentinel's identity arms the tracker's MCP fallback, so the human-facing
// string lives at the boundary that has already exhausted fallback (doctor) rather
// than in the error value.
func AuthErrorMessage(err error) string {
	return AuthErrorMessageFor(err, "")
}

// AuthErrorMessageFor is AuthErrorMessage with an echo of the account email the
// rejected request authenticated as. Basic auth is an email:token pair, so a 401
// cannot tell an expired token from an email belonging to some other Atlassian
// account: the hint names both causes and echoes the email, never the token.
func AuthErrorMessageFor(err error, email string) string {
	if !errors.Is(err, ErrUnauthorized) {
		return ""
	}
	var who string
	if email = strings.TrimSpace(email); email != "" {
		who = " (authenticated as " + email + ")"
	}
	return "Jira rejected the credentials" + who + " — either the API token is expired/invalid, " +
		"or the account email is not the Atlassian account that created the token. Regenerate at " + TokenHelpURL
}

const (
	// apiPrefix is the classic (unscoped) REST v3 base path. It works with the
	// simple https://<site>.atlassian.net base URL; scoped tokens would require
	// the api.atlassian.com/ex/jira/{cloudId} host and a cloudId lookup.
	apiPrefix = "/rest/api/3"

	// maxRetries bounds the 429 retry loop so a rate-limited site can't stall a
	// run indefinitely.
	maxRetries = 4

	// maxBackoff caps a single 429 wait when the server sends no Retry-After.
	maxBackoff = 30 * time.Second
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
	if baseURL != "" {
		c.auth = BasicAuth(email, apiToken)
	}
	return c
}

// BasicAuth returns the Authorization header value a Jira Cloud request carries,
// or "" when either credential is missing. Attachment downloads hit the site's
// content URLs rather than the REST API, so they authenticate with this directly
// instead of going through a Client.
func BasicAuth(email, apiToken string) string {
	email = strings.TrimSpace(email)
	apiToken = strings.TrimSpace(apiToken)
	if email == "" || apiToken == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+apiToken))
}

// enabled reports whether the client has the credentials to reach the API.
func (c *Client) enabled() bool { return c.auth != "" }

// Ping verifies the client's credentials with a single cheap authenticated
// request (GET /myself). It returns ErrNotEnabled when no credentials are set,
// ErrUnauthorized when the token is missing/expired/rejected, and nil when the
// token is accepted — the live auth check the doctor runs for the jira provider.
func (c *Client) Ping(ctx context.Context) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	return c.do(ctx, http.MethodGet, "/myself", nil, nil)
}

// Myself returns the identity behind the client's credentials — the accountId and
// display name of the Jira user the token authenticates as: the repo binding's Me
// the hub resolves each sync cycle. accountId is canonical; users are never matched
// by email, which Atlassian privacy settings can hide.
func (c *Client) Myself(ctx context.Context) (id, name string, err error) {
	if !c.enabled() {
		return "", "", ErrNotEnabled
	}
	var dst struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
	}
	if err := c.do(ctx, http.MethodGet, "/myself", nil, &dst); err != nil {
		return "", "", err
	}
	return dst.AccountID, dst.DisplayName, nil
}

// Issue is the subset of a Jira issue the tracker consumes. Description is the v3
// ADF body rendered back to markdown with its embedded images kept as image
// references; Status/Resolution/Project/Parent back the tracker's status,
// ownership-guard and epic-parent reads.
type Issue struct {
	Key         string
	Summary     string
	Description string
	Status      Status
	Resolution  string // resolution.name, "" while unresolved
	Project     Project
	Parent      string // parent issue key, "" when top-level
	HasChildren bool   // a container (epic or an issue with sub-issues), never a buildable leaf
	Labels      []string
	Attachments []Attachment
}

// Status is an issue's workflow status. Category is the stable statusCategory.key
// (undefined | new | indeterminate | done); Name is the display label.
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
	path := "/issue/" + url.PathEscape(key) + "?fields=summary,description,status,resolution,project,parent,issuetype,subtasks,labels,attachment"
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
		IssueType  *issueTypeField   `json:"issuetype"`
		Subtasks   []json.RawMessage `json:"subtasks"`
		Labels     []string          `json:"labels"`
		Attachment []attachmentField `json:"attachment"`
	} `json:"fields"`
}

// toIssue maps the raw REST payload onto the Issue the tracker consumes,
// tolerating absent optional objects (null status/resolution/project/parent).
func (r *issueResponse) toIssue() *Issue {
	files := toAttachments(r.Fields.Attachment)
	iss := &Issue{
		Key:         r.Key,
		Summary:     r.Fields.Summary,
		Description: adfToMarkdown(r.Fields.Description, files),
		Attachments: files,
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
	iss.HasChildren = hasChildren(r.Fields.IssueType, r.Fields.Subtasks)
	iss.Labels = r.Fields.Labels
	return iss
}

// do performs a JSON request against the REST v3 API and decodes the JSON
// response into dst (which may be nil for calls with no body of interest).
func (c *Client) do(ctx context.Context, method, path string, body []byte, dst any) error {
	var header http.Header
	if body != nil {
		header = http.Header{"Content-Type": {"application/json"}}
	}
	return c.send(ctx, method, path, body, header, dst)
}

// send issues one request carrying header on top of the Basic-auth and Accept
// headers every call sets, honours a 429 Retry-After up to maxRetries, and maps
// auth and not-found statuses onto the typed sentinels.
func (c *Client) send(ctx context.Context, method, path string, body []byte, header http.Header, dst any) error {
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
		for name, values := range header {
			for _, v := range values {
				req.Header.Add(name, v)
			}
		}

		res, err := c.http.Do(req)
		if err != nil {
			return err
		}

		if res.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			wait := retryAfter(res.Header.Get("Retry-After"), attempt, rand.Float64())
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
	case http.StatusTooManyRequests:
		return &RateLimitError{ResetAt: retryAfterReset(res.Header.Get("Retry-After"), time.Now())}
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
// Retry-After (seconds) header verbatim when present, otherwise backs off
// exponentially (1s, 2s, 4s, …) capped at maxBackoff, plus up to 25% jitter to
// decorrelate retries. jitter is a caller-supplied fraction in [0,1) (from
// rand.Float64), keeping the function pure and deterministically testable.
func retryAfter(header string, attempt int, jitter float64) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	backoff := time.Duration(1<<attempt) * time.Second
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff + time.Duration(jitter*float64(backoff)/4)
}
