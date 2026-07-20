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
)

// TokenHelpURL is where a user regenerates a classic Jira API token. Classic
// tokens cannot self-refresh and expire roughly annually, so a 401/403 usually
// means the token lapsed rather than that the account lost access.
const TokenHelpURL = "https://id.atlassian.com/manage-profile/security/api-tokens"

// AuthErrorMessage returns an actionable, user-facing hint for an ErrUnauthorized
// (an expired or invalid token), or "" for any other error. It deliberately does
// not reword ErrUnauthorized itself: the sentinel's identity arms the tracker's
// MCP fallback, so the human-facing string lives at the boundary that has already
// exhausted fallback (doctor) rather than in the error value.
func AuthErrorMessage(err error) string {
	if errors.Is(err, ErrUnauthorized) {
		return "Jira token expired or invalid — regenerate it at " + TokenHelpURL
	}
	return ""
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
// ADF body flattened to text with its embedded images kept as markdown image
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
	Labels      []string
	Attachments []Attachment
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
	path := "/issue/" + url.PathEscape(key) + "?fields=summary,description,status,resolution,project,parent,labels,attachment"
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
	iss.Labels = r.Fields.Labels
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

// adfNode is one node of an Atlassian Document Format tree. Text carries inline
// content (marks are ignored); Content holds child nodes. Attrs stays raw because
// its shape varies by node type — decoding it eagerly would let one unfamiliar
// node blank an entire description.
type adfNode struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Attrs   json.RawMessage `json:"attrs"`
	Content []adfNode       `json:"content"`
}

// adfMediaAttrs are a media node's attributes: id addresses the file in the
// issue's attachment list, url carries an externally hosted image directly, and
// alt is the caption Jira echoes for it.
type adfMediaAttrs struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	Alt string `json:"alt"`
}

// adfMedia resolves a document's media nodes against the issue's attachment list.
type adfMedia []Attachment

func (m adfMedia) byID(id string) (Attachment, bool) {
	for _, att := range m {
		if id != "" && att.ID == id {
			return att, true
		}
	}
	return Attachment{}, false
}

func (m adfMedia) byFilename(name string) (Attachment, bool) {
	for _, att := range m {
		if name != "" && strings.EqualFold(att.Filename, name) {
			return att, true
		}
	}
	return Attachment{}, false
}

// adfToText flattens a v3 ADF document with no attachment list to resolve its
// embedded images against, so each one leaves a placeholder rather than a URL.
func adfToText(raw json.RawMessage) string {
	return adfToMarkdown(raw, nil)
}

// adfToMarkdown flattens a v3 ADF document to plain, readable text, rendering its
// embedded media as markdown image references resolved against the issue's
// attachments. A missing or null body, or any decode failure, yields "" so callers
// treat an unreadable body as "no description" rather than an error.
func adfToMarkdown(raw json.RawMessage, media adfMedia) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	var doc adfNode
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return ""
	}
	var b strings.Builder
	writeADF(&b, doc, media)
	return strings.TrimSpace(collapseBlankLines(b.String()))
}

// writeADF walks an ADF node depth-first, emitting text and a newline after every
// block-level node so paragraphs and list items stay on their own lines.
func writeADF(b *strings.Builder, n adfNode, media adfMedia) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
	case "hardBreak":
		b.WriteByte('\n')
	case "media", "mediaInline":
		b.WriteString(mediaRef(n, media))
	}
	for _, c := range n.Content {
		writeADF(b, c, media)
	}
	if isADFBlock(n.Type) {
		b.WriteByte('\n')
	}
}

// mediaRef renders a media node — the leaf inside a mediaSingle or mediaGroup —
// as a markdown image, so an embedded screenshot survives the flattening instead
// of vanishing from the stored body. An external node carries its URL directly; a
// file node resolves through the issue's attachments, by media id and then by the
// filename Jira echoes into alt. A node nothing resolves still leaves a trace, so
// a reader knows an image was there.
func mediaRef(n adfNode, media adfMedia) string {
	var attrs adfMediaAttrs
	if err := json.Unmarshal(n.Attrs, &attrs); err != nil {
		return "[image]"
	}
	if attrs.URL != "" {
		return "![" + attrs.Alt + "](" + attrs.URL + ")"
	}
	att, found := media.byID(attrs.ID)
	if !found {
		att, found = media.byFilename(attrs.Alt)
	}
	if found && att.Content != "" {
		return "![" + attrs.Alt + "](" + att.Content + ")"
	}
	if name := firstNonEmpty(attrs.Alt, attrs.ID); name != "" {
		return "[image: " + name + "]"
	}
	return "[image]"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

// adfDoc is a minimal ADF document assembled for v3 write bodies (transition
// comments, descriptions) — the counterpart of adfToText.
type adfDoc struct {
	Type    string     `json:"type"`
	Version int        `json:"version"`
	Content []adfBlock `json:"content"`
}

type adfBlock struct {
	Type    string      `json:"type"`
	Content []adfInline `json:"content,omitempty"`
}

type adfInline struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// buildADF wraps plain, possibly multi-line text into an ADF document — one
// paragraph per line, each holding a single text node — as Jira v3 requires for
// comment and description bodies. It round-trips back through adfToText.
func buildADF(text string) adfDoc {
	lines := strings.Split(text, "\n")
	content := make([]adfBlock, 0, len(lines))
	for _, line := range lines {
		block := adfBlock{Type: "paragraph"}
		if line != "" {
			block.Content = []adfInline{{Type: "text", Text: line}}
		}
		content = append(content, block)
	}
	return adfDoc{Type: "doc", Version: 1, Content: content}
}
