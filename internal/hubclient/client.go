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

// ErrNotFound is returned when the hub has no resource with the requested
// identifier — a 404 from a read, a transition, or a checkpoint fetch.
var ErrNotFound = errors.New("hubclient: not found")

// transportError wraps a failure to reach the hub at all — a dial or transport
// error where the request never got an HTTP response, distinct from an error
// status the hub returned. Its message and unwrap match the plain wrapping the
// client used before, so string and errors.Is checks are unchanged.
type transportError struct {
	op  string
	err error
}

func (e *transportError) Error() string { return e.op + ": " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// IsUnreachable reports whether err is a hub-connection failure — the request
// never reached the hub — as opposed to an error status the hub returned. Run
// data writers retry on this before pausing the run (ADR 0008 §3).
func IsUnreachable(err error) bool {
	var te *transportError
	return errors.As(err, &te)
}

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

// Issue is an issue as the hub returns it — internal or synced. State is the
// normalized status group; Status is its display label. Group, Comments, Project,
// and InProject are populated by the store-backed by-id read the pipeline uses for
// synced tickets; Deleted flags a synced ticket tombstoned after removal from the
// tracker.
type Issue struct {
	Repo        string    `json:"repo"`
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"`
	Status      string    `json:"status"`
	Group       string    `json:"group"`
	Labels      []string  `json:"labels"`
	Parent      string    `json:"parent"`
	Source      string    `json:"source"`
	HasChildren bool      `json:"has_children"`
	Comments    []Comment `json:"comments"`
	Project     string    `json:"project"`
	InProject   bool      `json:"in_project"`
	Deleted     bool      `json:"deleted"`
}

// Comment is one comment on an issue as the hub returns it.
type Comment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
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

// SyncedMirror mirrors a tracker write onto a synced issue's store row: an optional
// new display status and status group, plus label deltas. The tracker owns the
// write; this only keeps the store row in step so the board never lags a transition
// (ADR 0007).
type SyncedMirror struct {
	Status       string   `json:"status"`
	StatusGroup  string   `json:"status_group"`
	AddLabels    []string `json:"add_labels"`
	RemoveLabels []string `json:"remove_labels"`
}

// Checkpoint is a ticket's checkpoint as the hub stores it (ADR 0008). Data is
// the full checkpoint key set (PHASE, BRANCH, PR, …); the hub derives the
// projected columns from it, so a write only need populate Ticket and Data.
type Checkpoint struct {
	Ticket        string            `json:"ticket"`
	Phase         string            `json:"phase"`
	Title         string            `json:"title"`
	Branch        string            `json:"branch"`
	PR            string            `json:"pr"`
	PRURL         string            `json:"pr_url"`
	FailureReason string            `json:"failure_reason"`
	UpdatedAt     string            `json:"updated_at"`
	Data          map[string]string `json:"data"`
}

// PutCheckpoint writes a ticket's checkpoint to the hub, which persists it in the
// authoritative checkpoints table. A hub-connection failure surfaces as an
// IsUnreachable error so the caller can retry; the request is idempotent.
func (c *Client) PutCheckpoint(ctx context.Context, repo, ticket string, cp Checkpoint) error {
	return c.do(ctx, http.MethodPut, c.checkpointPath(repo, ticket), cp, nil)
}

// GetCheckpoint reads a ticket's checkpoint from the hub, returning ok=false when
// the hub has no checkpoint for it.
func (c *Client) GetCheckpoint(ctx context.Context, repo, ticket string) (cp Checkpoint, ok bool, err error) {
	err = c.do(ctx, http.MethodGet, c.checkpointPath(repo, ticket), nil, &cp)
	if errors.Is(err, ErrNotFound) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	return cp, true, nil
}

// DeleteCheckpoint drops a ticket's checkpoint from the hub. A missing checkpoint
// is not an error — removal is idempotent.
func (c *Client) DeleteCheckpoint(ctx context.Context, repo, ticket string) error {
	err := c.do(ctx, http.MethodDelete, c.checkpointPath(repo, ticket), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Checkpoints lists every checkpoint the hub holds for repo — the resume scan's
// whole-repo read.
func (c *Client) Checkpoints(ctx context.Context, repo string) ([]Checkpoint, error) {
	var out struct {
		Checkpoints []Checkpoint `json:"checkpoints"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath(repo, "checkpoints"), nil, &out); err != nil {
		return nil, err
	}
	return out.Checkpoints, nil
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

// Issue reads a single issue by identifier from the hub's store — internal or
// synced, with its comments — for the store-backed pipeline reads (ADR 0007). The
// hub answers from the store, falling back to a one-off tracker fetch (and syncing
// it in) only when the id is not yet in the store. A cross-project ticket comes
// back with InProject false rather than an error. ErrNotFound means the tracker has
// no such issue in this repo.
func (c *Client) Issue(ctx context.Context, repo, id string) (Issue, error) {
	var out Issue
	err := c.do(ctx, http.MethodGet, c.repoPath(repo, "issues/"+url.PathEscape(id)), nil, &out)
	return out, err
}

// MirrorSynced applies a tracker write's status/label change to a synced issue's
// store row so the board reflects it immediately. ErrNotFound means the id is not a
// synced issue in this repo (a missing or internal identifier).
func (c *Client) MirrorSynced(ctx context.Context, repo, id string, m SyncedMirror) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "issues/"+url.PathEscape(id)), m, nil)
}

// Sync nudges the hub to pull the repo's Project into the store before a read, so a
// ticket finished, reopened, or removed out-of-band is caught before the next pick
// (ADR 0007). It is a one-way inbound sync; the hub owns the pull.
func (c *Client) Sync(ctx context.Context, repo string) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "sync"), nil, nil)
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

func (c *Client) checkpointPath(repo, ticket string) string {
	return c.repoPath(repo, "runs/"+url.PathEscape(ticket)+"/checkpoint")
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
		return &transportError{op: method + " " + path, err: err}
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
