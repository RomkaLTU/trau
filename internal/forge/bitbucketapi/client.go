// Package bitbucketapi is a small REST client for Bitbucket Cloud's v2 API,
// covering the pull-request operations trau's closing phases perform. Atlassian
// ships no official Bitbucket CLI — `acli` covers Jira and admin only — so there
// is no `gh` equivalent to shell out to and delivery talks to the API directly.
//
// Auth is stateless HTTP Basic (base64(email:api_token)) against an Atlassian
// account API token, exactly as the Jira client authenticates. App passwords are
// gone, and scoped API tokens are the only mechanism Bitbucket Cloud now accepts.
//
// Bitbucket Data Center is a different product with a different API and is not
// covered here.
package bitbucketapi

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

	"github.com/RomkaLTU/trau/internal/forge"
)

// The errors a caller distinguishes. Everything else surfaces as an opaque HTTP
// error carrying Bitbucket's own message.
var (
	ErrNotFound     = errors.New("bitbucket: not found")
	ErrUnauthorized = errors.New("bitbucket: unauthorized")
	ErrNotEnabled   = errors.New("bitbucket: no credentials configured")
	ErrRateLimited  = errors.New("bitbucket: rate limit exceeded")
)

// TokenHelpURL is where a user mints the Atlassian API token this client
// authenticates with. App passwords were removed in July 2026; a scoped API
// token is the only credential Bitbucket Cloud accepts.
const TokenHelpURL = "https://id.atlassian.com/manage-profile/security/api-tokens"

// AuthErrorMessage returns an actionable hint for an ErrUnauthorized, or "" for
// any other error. Basic auth is an email:token pair, so a 401 cannot tell an
// expired token from an email belonging to some other Atlassian account: the
// hint names both causes and echoes the email, never the token.
func AuthErrorMessage(err error, email string) string {
	if !errors.Is(err, ErrUnauthorized) {
		return ""
	}
	var who string
	if email = strings.TrimSpace(email); email != "" {
		who = " (authenticated as " + email + ")"
	}
	return "Bitbucket rejected the credentials" + who + " — either the API token is expired, revoked or missing the " +
		"pullrequest:write scope, or the account email is not the Atlassian account that created the token. Mint one at " + TokenHelpURL
}

const (
	// DefaultBaseURL is Bitbucket Cloud's only API host. Data Center installs
	// answer a different API on their own hostname and are out of scope.
	DefaultBaseURL = "https://api.bitbucket.org/2.0"

	// WebBaseURL is where a human reads the same repository. Raw file and pull
	// request links a PR body carries are built against it, never the API host.
	WebBaseURL = "https://bitbucket.org"

	maxRetries = 4
	maxBackoff = 30 * time.Second
)

// Client talks to one Bitbucket Cloud repository as one Atlassian account.
type Client struct {
	baseURL   string
	auth      string // "Basic <base64(email:token)>", or "" when the client is disabled
	workspace string
	repoSlug  string
	http      *http.Client
}

// New returns a client for workspace/repoSlug. A missing credential or an
// unidentified repository leaves the client disabled, so every method returns
// ErrNotEnabled rather than issuing a request that can only be rejected.
func New(workspace, repoSlug, email, apiToken string) *Client {
	c := &Client{
		baseURL:   DefaultBaseURL,
		workspace: strings.TrimSpace(workspace),
		repoSlug:  strings.TrimSpace(repoSlug),
		http:      &http.Client{Timeout: 30 * time.Second},
	}
	if c.workspace != "" && c.repoSlug != "" {
		c.auth = BasicAuth(email, apiToken)
	}
	return c
}

// ClientFor returns the client repository root's Bitbucket Cloud calls go
// through, and whether that repository is delivered through Bitbucket at all: a
// repository on any other forge answers (nil, false) and its caller falls back to
// whatever serves that forge. declared is the repository's own declared FORGE,
// which overrides what its remote says — the precedence forge.Resolve applies
// everywhere.
//
// A repository whose remote names no workspace and repository slug still answers
// (client, true): the client is then disabled, and how that reads — a doctor
// warning, a refused delivery, an unreadable pull request — is the caller's to
// word, since only the caller knows what it was about to do.
func ClientFor(ctx context.Context, root, declared, remote, email, apiToken string) (*Client, bool) {
	remoteURL := forge.RemoteURL(ctx, root, remote)
	if forge.Resolve(declared, remoteURL) != forge.Bitbucket {
		return nil, false
	}
	workspace, slug, _ := forge.Slug(remoteURL)
	return New(workspace, slug, email, apiToken), true
}

// WithBaseURL points the client at another API host. It exists for the test
// server; production always answers on DefaultBaseURL.
func (c *Client) WithBaseURL(baseURL string) *Client {
	if baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/"); baseURL != "" {
		c.baseURL = baseURL
	}
	return c
}

// BasicAuth is the Authorization header value a Bitbucket Cloud request carries,
// or "" when either credential is missing.
func BasicAuth(email, apiToken string) string {
	email = strings.TrimSpace(email)
	apiToken = strings.TrimSpace(apiToken)
	if email == "" || apiToken == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+apiToken))
}

// Enabled reports whether the client holds the credentials and the repository
// identity it needs to reach the API.
func (c *Client) Enabled() bool { return c.auth != "" }

// Workspace and RepoSlug name the repository this client addresses. The PR body's
// raw-file links are built from them, so they are read back rather than re-parsed
// from the remote at every use.
func (c *Client) Workspace() string { return c.workspace }
func (c *Client) RepoSlug() string  { return c.repoSlug }

// Repository is the repository metadata trau reads: whether it is private, which
// decides how a proof screenshot is referenced in a PR body, and the branch
// Bitbucket calls its main one.
type Repository struct {
	Private    bool
	MainBranch string
}

// Repo reads the repository's own record. It is the client's cheapest
// authenticated call and doubles as the doctor's live credential probe.
func (c *Client) Repo(ctx context.Context) (Repository, error) {
	if !c.Enabled() {
		return Repository{}, ErrNotEnabled
	}
	var dst struct {
		IsPrivate  bool `json:"is_private"`
		MainBranch *struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath(), nil, &dst); err != nil {
		return Repository{}, err
	}
	repo := Repository{Private: dst.IsPrivate}
	if dst.MainBranch != nil {
		repo.MainBranch = dst.MainBranch.Name
	}
	return repo, nil
}

// Ping verifies the client's credentials against the repository it addresses.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Repo(ctx)
	return err
}

func (c *Client) repoPath() string {
	return "/repositories/" + url.PathEscape(c.workspace) + "/" + url.PathEscape(c.repoSlug)
}

// do performs a JSON request and decodes the response into dst, which may be nil
// for calls whose body carries nothing of interest.
func (c *Client) do(ctx context.Context, method, path string, body []byte, dst any) error {
	endpoint := c.baseURL + path
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
		return fmt.Errorf("bitbucket: HTTP %d: %s", res.StatusCode, errorMessage(resBody))
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(resBody, dst); err != nil {
		return fmt.Errorf("bitbucket: invalid response (%d): %w", res.StatusCode, err)
	}
	return nil
}

// errorMessage lifts the human-readable line out of Bitbucket's error envelope
// ({"error":{"message":"…"}}), falling back to the raw payload when the response
// is not that shape.
func errorMessage(body []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		switch {
		case env.Error.Message != "" && env.Error.Detail != "":
			return env.Error.Message + ": " + env.Error.Detail
		case env.Error.Message != "":
			return env.Error.Message
		}
	}
	return strings.TrimSpace(string(body))
}

// RateLimitError is a 429 the retry ladder could not outlast, carrying when the
// budget refills — zero when the response named no time.
type RateLimitError struct {
	ResetAt time.Time
}

func (e *RateLimitError) Error() string {
	if e.ResetAt.IsZero() {
		return ErrRateLimited.Error()
	}
	return ErrRateLimited.Error() + " (budget resets " + e.ResetAt.UTC().Format(time.RFC3339) + ")"
}

func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// retryAfter derives how long to wait before retrying a 429: a numeric
// Retry-After (seconds) verbatim, otherwise exponential backoff capped at
// maxBackoff plus up to 25% jitter. jitter is caller-supplied (from
// rand.Float64) so the function stays pure and deterministically testable.
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

// retryAfterReset reads when a Retry-After header says the budget refills, in
// either form the header takes: delta-seconds from now, or an HTTP-date.
func retryAfterReset(header string, now time.Time) time.Time {
	header = strings.TrimSpace(header)
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		return now.Add(time.Duration(secs) * time.Second)
	}
	if at, err := http.ParseTime(header); err == nil {
		return at
	}
	return time.Time{}
}
