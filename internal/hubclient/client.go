// Package hubclient is the loop's typed HTTP client for the trau serve hub's
// internal-issue API. The hub is the only process that opens the issue database
// (ADR 0007); the loop drives internal issues entirely through this client, so no
// loop code path ever touches the SQLite file.
package hubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiPrefix mirrors webserver.APIPrefix. It is duplicated rather than imported to
// keep this client free of any dependency on the server package (which imports the
// tracker package that in turn uses this client).
const apiPrefix = "/api/v1"

// ErrNotFound is returned when the hub has no internal issue with the requested
// identifier — a 404 from a read or a transition.
var ErrNotFound = errors.New("hubclient: internal issue not found")

// Client talks to a running serve hub over HTTP. base is the hub origin
// (e.g. http://127.0.0.1:8728); token authenticates against an exposed hub and is
// omitted on a loopback bind.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New builds a Client for the hub at base, sending token as a bearer credential
// when it is non-empty.
func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Issue is an internal issue as the hub returns it. State is the normalized status
// group; Status is its display label.
type Issue struct {
	Repo        string   `json:"repo"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Status      string   `json:"status"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent"`
	Source      string   `json:"source"`
	HasChildren bool     `json:"has_children"`
}

// BacklogItem is one issue on the hub backlog board. Ready reports whether it
// carries the repo's ready label; Group is its normalized status group.
type BacklogItem struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Group       string   `json:"group"`
	Labels      []string `json:"labels"`
	Source      string   `json:"source"`
	Parent      string   `json:"parent"`
	HasChildren bool     `json:"has_children"`
	Ready       bool     `json:"ready"`
}

// BacklogQuery narrows a backlog listing to the rows the internal picker needs.
type BacklogQuery struct {
	Source string
	Label  string
	State  string
}

// InternalDraft is a new internal issue to create.
type InternalDraft struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent"`
}

// Transition is a loop-driven write to an internal issue: an optional new state,
// label deltas, and an optional comment.
type Transition struct {
	State        string   `json:"state"`
	AddLabels    []string `json:"add_labels"`
	RemoveLabels []string `json:"remove_labels"`
	Comment      string   `json:"comment"`
}

// InternalIssue fetches a single internal issue, returning ErrNotFound when the
// hub has no internal issue with that identifier.
func (c *Client) InternalIssue(ctx context.Context, repo, id string) (Issue, error) {
	var out Issue
	err := c.do(ctx, http.MethodGet, c.issuePath(repo, id, ""), nil, &out)
	return out, err
}

// Backlog lists the repo's backlog rows matching q.
func (c *Client) Backlog(ctx context.Context, repo string, q BacklogQuery) ([]BacklogItem, error) {
	values := url.Values{}
	if q.Source != "" {
		values.Set("source", q.Source)
	}
	if q.Label != "" {
		values.Set("label", q.Label)
	}
	if q.State != "" {
		values.Set("state", q.State)
	}
	path := c.repoPath(repo, "backlog")
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []BacklogItem `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// CreateInternalIssue files a new internal issue and returns it with its allocated
// identifier.
func (c *Client) CreateInternalIssue(ctx context.Context, repo string, d InternalDraft) (Issue, error) {
	var out Issue
	err := c.do(ctx, http.MethodPost, c.repoPath(repo, "issues/internal"), d, &out)
	return out, err
}

// TransitionInternalIssue applies t to an internal issue and returns the updated
// row, returning ErrNotFound for a missing or synced identifier.
func (c *Client) TransitionInternalIssue(ctx context.Context, repo, id string, t Transition) (Issue, error) {
	var out Issue
	err := c.do(ctx, http.MethodPost, c.issuePath(repo, id, "transition"), t, &out)
	return out, err
}

func (c *Client) repoPath(repo, tail string) string {
	return apiPrefix + "/repos/" + url.PathEscape(repo) + "/" + tail
}

func (c *Client) issuePath(repo, id, verb string) string {
	path := c.repoPath(repo, "issues/internal/"+url.PathEscape(id))
	if verb != "" {
		path += "/" + verb
	}
	return path
}

// do issues one request against the hub, encoding body as JSON when non-nil and
// decoding a JSON response into out. A 404 becomes ErrNotFound; any other non-2xx
// carries the hub's error message.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, path, hubError(resp.Body, resp.Status))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// hubError recovers the hub's {"error": "..."} message, falling back to the HTTP
// status when the body is not the expected shape.
func hubError(body io.Reader, status string) string {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<16)).Decode(&payload); err == nil && payload.Error != "" {
		return payload.Error
	}
	return status
}
