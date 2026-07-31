// Package azureapi is a small, pragmatic REST client for Azure DevOps Services'
// Work Item Tracking API (api-version 7.1). It covers the read/write operations
// the Trau loop performs on every ticket, so the azure provider resolves them
// over HTTP with no agent/MCP round-trip at all.
//
// Auth is stateless HTTP Basic with an empty username and a personal access
// token as the password — the scheme Azure DevOps documents for PATs — against a
// per-organization base URL (https://dev.azure.com/<org>), so a client holds
// exactly one organization's credentials and two repos can target two separate
// Azure DevOps accounts.
package azureapi

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

// Common errors the tracker inspects to tell a configuration gap from a real
// failure.
var (
	ErrNotFound     = errors.New("azure: work item not found")
	ErrUnauthorized = errors.New("azure: unauthorized")
	ErrNotEnabled   = errors.New("azure: direct API not enabled")
	// ErrRateLimited marks a 429 the retry ladder could not outlast: the
	// organization's request budget refills on its own, so it is transient, not
	// misconfiguration.
	ErrRateLimited = errors.New("azure: rate limit exceeded")
)

// TokenHelpURL is where a user mints or regenerates a personal access token.
// PATs carry an explicit expiry (90 days by default) and cannot self-refresh, so
// a 401 most often means the token simply lapsed.
const TokenHelpURL = "https://dev.azure.com/_usersSettings/tokens"

// requiredScopes names the personal-access-token scopes this client needs: Work
// Items for the tickets themselves, Project and Team to enumerate the
// organization's team projects — which the wizard's picker lists and Ping reads.
const requiredScopes = "Work Items (read & write) and Project and Team (read)"

// AuthErrorMessageFor returns an actionable, user-facing hint for an
// ErrUnauthorized, or "" for any other error, with an echo of the organization URL
// the rejected request addressed. A PAT is scoped to one organization, so a 401
// cannot tell an expired token from one minted for a different organization or
// without the required scopes: the hint names all three causes and echoes the URL,
// never the token.
func AuthErrorMessageFor(err error, orgURL string) string {
	if !errors.Is(err, ErrUnauthorized) {
		return ""
	}
	var where string
	if orgURL = strings.TrimSpace(orgURL); orgURL != "" {
		where = " for " + orgURL
	}
	return "Azure DevOps rejected the credentials" + where + " — the personal access token is expired/revoked, " +
		"belongs to a different organization, or lacks the " + requiredScopes + " scopes. Regenerate at " + TokenHelpURL
}

const (
	// apiVersion pins requests to the 7.1 GA surface. Azure DevOps routes reject a
	// request with no api-version, so every path carries one.
	apiVersion = "7.1"

	// commentsAPIVersion pins the work-item comments read. That route is served
	// only under a preview version and rejects plain 7.1 — the same gate that keeps
	// comment writes on System.History.
	commentsAPIVersion = "7.1-preview.4"

	// maxRetries bounds the 429 retry loop so a throttled organization cannot
	// stall a run indefinitely.
	maxRetries = 4

	// maxBackoff caps a single 429 wait when the server sends no Retry-After.
	maxBackoff = 30 * time.Second

	// batchLimit is the work-item ceiling one ids= read accepts.
	batchLimit = 200
)

// Client talks to a single Azure DevOps organization over the REST 7.1 API.
type Client struct {
	baseURL string
	auth    string // "Basic <base64(:pat)>", or "" when the client is disabled
	http    *http.Client
}

// New returns a client for the given organization URL. An empty token (or base
// URL) leaves the client disabled, so every method returns ErrNotEnabled.
func New(orgURL, pat string) *Client {
	orgURL = strings.TrimRight(strings.TrimSpace(orgURL), "/")
	c := &Client{
		baseURL: orgURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	if orgURL != "" {
		c.auth = basicAuth(pat)
	}
	return c
}

// basicAuth returns the Authorization header value an Azure DevOps request
// carries, or "" when the token is missing. The username half is deliberately
// empty — Azure DevOps ignores it and reads the PAT from the password half.
func basicAuth(pat string) string {
	if pat = strings.TrimSpace(pat); pat == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+pat))
}

// enabled reports whether the client has the credentials to reach the API.
func (c *Client) enabled() bool { return c.auth != "" }

// Ping verifies the client's credentials with a single cheap authenticated read
// (the first accessible project). It returns ErrNotEnabled when no credentials
// are set, ErrUnauthorized when the token is rejected, and nil when it is
// accepted — the live auth check the doctor runs for the azure provider.
func (c *Client) Ping(ctx context.Context) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	return c.do(ctx, http.MethodGet, "/_apis/projects?$top=1", nil, nil)
}

// Project is an Azure DevOps team project — the analogue of a Jira project. Name
// is the canonical identifier: it is what appears in API paths and what the repo
// stores as its tracker key.
type Project struct {
	ID   string
	Name string
}

// ListProjects enumerates the team projects the credentials can see.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	var dst struct {
		Value []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"value"`
	}
	if err := c.do(ctx, http.MethodGet, "/_apis/projects?$top="+strconv.Itoa(batchLimit), nil, &dst); err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(dst.Value))
	for _, p := range dst.Value {
		if p.Name == "" {
			continue
		}
		out = append(out, Project{ID: p.ID, Name: p.Name})
	}
	return out, nil
}

// do performs a JSON request against the REST API and decodes the response into
// dst (nil for calls with no body of interest).
func (c *Client) do(ctx context.Context, method, path string, body []byte, dst any) error {
	return c.send(ctx, method, path, body, "application/json", dst)
}

// patch performs a JSON-Patch document request — the only shape Azure DevOps
// accepts for work-item creates and updates, and it insists on the
// application/json-patch+json media type.
func (c *Client) patch(ctx context.Context, path string, ops []patchOp, dst any) error {
	body, err := json.Marshal(ops)
	if err != nil {
		return err
	}
	return c.send(ctx, http.MethodPatch, path, body, "application/json-patch+json", dst)
}

// send issues one request, honouring a 429 Retry-After up to maxRetries, and
// maps auth and not-found statuses onto the typed sentinels.
func (c *Client) send(ctx context.Context, method, path string, body []byte, contentType string, dst any) error {
	endpoint := c.baseURL + withAPIVersion(path)
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
			req.Header.Set("Content-Type", contentType)
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

// withAPIVersion appends the pinned api-version to a path that may already carry
// its own query string, leaving a route that pins a version of its own alone.
func withAPIVersion(path string) string {
	if strings.Contains(path, "api-version=") {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "api-version=" + apiVersion
}

// decode closes the response body and turns an HTTP status into either a typed
// error or a JSON unmarshal into dst.
//
// A rejected PAT does not always arrive as a 401: Azure DevOps answers some
// routes with 203 Non-Authoritative Information and an HTML sign-in page, which
// would otherwise surface as a JSON parse error rather than an auth failure.
func decode(res *http.Response, dst any) error {
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNonAuthoritativeInfo:
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
		return fmt.Errorf("azure: HTTP %d: %s", res.StatusCode, errorMessage(resBody))
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(resBody, dst); err != nil {
		return fmt.Errorf("azure: invalid response (%d): %w", res.StatusCode, err)
	}
	return nil
}

// errorMessage pulls the human-readable message out of an Azure DevOps error
// envelope ({"message":"TF401232: ..."}), falling back to the raw body.
func errorMessage(body []byte) string {
	var env struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &env) == nil {
		if msg := strings.TrimSpace(env.Message); msg != "" {
			return msg
		}
	}
	return strings.TrimSpace(string(body))
}

// retryAfter derives how long to wait before retrying a 429. It honours a
// numeric Retry-After (seconds) header verbatim when present, otherwise backs
// off exponentially (1s, 2s, 4s, …) capped at maxBackoff, plus up to 25% jitter
// to decorrelate retries. jitter is a caller-supplied fraction in [0,1) (from
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

// projectPath renders the "/{project}/_apis/wit/..." prefix every work-item
// route hangs off, escaping a project name that may contain spaces. An empty
// project addresses the organization-scoped route, which reads a work item
// whatever team project it belongs to.
func projectPath(project, suffix string) string {
	if project = strings.TrimSpace(project); project == "" {
		return "/_apis/wit" + suffix
	}
	return "/" + url.PathEscape(project) + "/_apis/wit" + suffix
}
