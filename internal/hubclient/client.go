// Package hubclient is the loop's typed HTTP client for the trau serve hub's
// internal-issue API. The hub is the only process that opens the issue database
// (ADR 0007); the loop drives internal issues entirely through this client, so no
// loop code path ever touches the SQLite file.
package hubclient

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

// apiPrefix mirrors webserver.APIPrefix. It is duplicated rather than imported to
// keep this client free of any dependency on the server package (which imports the
// tracker package that in turn uses this client).
const apiPrefix = "/api/v1"

// ErrNotFound is returned when the hub has no resource with the requested
// identifier — a 404 from a read, a transition, or a checkpoint fetch.
var ErrNotFound = errors.New("hubclient: not found")

// attachmentMaxBytes mirrors the cap the hub applies when it fetches a file, so a
// download never truncates something the hub was willing to cache.
const attachmentMaxBytes = 50 << 20

// attachmentHTTP downloads attachment bytes. Its timeout is generous because the
// first read of a tracker-hosted file makes the hub pull it on that same request,
// which the hub bounds at two minutes of its own.
var attachmentHTTP = &http.Client{Timeout: 3 * time.Minute}

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
// carries the repo's ready label; Group is its normalized status group; Blocked
// reports an unresolved blocked-by relation, so the picker skips the row.
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
	Blocked     bool     `json:"blocked"`
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

// Event is one event the loop child sends to the hub for the authoritative event
// feed (ADR 0008). The hub assigns the id and ordering; the child supplies only
// the content. Fields is the event's fields bag pre-marshalled to a JSON object
// string, or empty for none.
type Event struct {
	TS     string `json:"ts"`
	Kind   string `json:"kind"`
	Phase  string `json:"phase,omitempty"`
	Msg    string `json:"msg,omitempty"`
	Fields string `json:"fields,omitempty"`
}

type appendEventsBody struct {
	Events []Event `json:"events"`
}

// TranscriptChunk is one ordered slice of an agent session's PTY output the child
// posts to the hub (ADR 0008 §4). Stem is the transcript-session id; Seq orders
// the chunk within the session; Data is the raw terminal bytes base64-encoded
// (they carry control characters); Cols/Rows carry the session's dimensions.
type TranscriptChunk struct {
	Stem string `json:"stem"`
	Seq  int64  `json:"seq"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Data string `json:"data"`
}

type appendTranscriptBody struct {
	Chunks []TranscriptChunk `json:"chunks"`
}

type transcriptChunkData struct {
	Seq  int64  `json:"seq"`
	Data string `json:"data"`
}

type transcriptChunksResponse struct {
	ID     string                `json:"id"`
	Cols   int                   `json:"cols"`
	Rows   int                   `json:"rows"`
	Chunks []transcriptChunkData `json:"chunks"`
}

// TranscriptPoll is one poll of a repo's live transcript as the Go pollers (the
// TUI live view and `trau watch`) consume it: the resolved session id, its
// dimensions, the decoded bytes appended since the caller's cursor, and the seq
// to resume from. A changed ID means the follow target advanced to a new phase.
type TranscriptPoll struct {
	ID   string
	Cols int
	Rows int
	Data []byte
	Seq  int64
}

// artifactBody carries a run artifact's content on the wire, both directions.
type artifactBody struct {
	Content string `json:"content"`
}

// Lesson is one distilled repair-experiment record in a repo's durable ledger, as
// the child posts it and reads it back (COD-529, ADR 0008). The takeaway plus the
// context it came from — the ticket and phase, the failure type, the evidence, how
// the repair ended, and when it was recorded.
type Lesson struct {
	Ticket       string   `json:"ticket,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	FailureType  string   `json:"failure_type,omitempty"`
	AttemptedFix string   `json:"attempted_fix,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	Result       string   `json:"result,omitempty"`
	Lesson       string   `json:"lesson"`
	Tags         []string `json:"tags,omitempty"`
	RecordedAt   string   `json:"recorded_at,omitempty"`
}

// lessonsBody carries a repo's ledger on a read; it decodes the lessons the hub
// returns and ignores the response's other fields.
type lessonsBody struct {
	Lessons []Lesson `json:"lessons"`
}

// drainOutcomeBody carries a queued child's exit outcome on the wire: the failure
// class and reason, both empty for a clean finish.
type drainOutcomeBody struct {
	Class  string `json:"class,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// phaseLogBody carries a phase log's content on a write.
type phaseLogBody struct {
	Content string `json:"content"`
}

// PhaseLog is one phase's stored agent output as the hub returns it in a list.
type PhaseLog struct {
	Phase   string    `json:"phase"`
	Content string    `json:"content"`
	Updated time.Time `json:"updated"`
}

type phaseLogsBody struct {
	Logs []PhaseLog `json:"logs"`
}

// TokenCall is one normalized provider call the loop child sends to the hub for the
// authoritative token ledger (ADR 0008). Ticket buckets the call (a ticket id, or
// _loop / _plans); CostUSD is nil for a call a provider reported no per-call cost
// for. Skills is the call's skill list pre-marshalled to a JSON array string.
type TokenCall struct {
	Ticket        string   `json:"ticket"`
	TS            string   `json:"ts"`
	Phase         string   `json:"phase"`
	Input         int      `json:"input"`
	Output        int      `json:"output"`
	CacheRead     int      `json:"cache_read"`
	CacheCreation int      `json:"cache_creation"`
	Reasoning     int      `json:"reasoning"`
	Total         int      `json:"total"`
	CostUSD       *float64 `json:"cost_usd"`
	Turns         int      `json:"turns"`
	IsError       bool     `json:"is_error"`
	Provider      string   `json:"provider,omitempty"`
	Model         string   `json:"model,omitempty"`
	Context       int      `json:"context,omitempty"`
	Skills        string   `json:"skills,omitempty"`
}

// InstanceHeartbeat is a loop's presence as it reports it to the hub (ADR 0005,
// ADR 0008 §7): its repo, where its runs land, when it started, and the session
// state it is reporting. The hub keys presence by PID, stamps its own last-seen
// heartbeat, and reaps a dead PID via signal 0.
type InstanceHeartbeat struct {
	RepoRoot     string    `json:"repo_root"`
	RunsDir      string    `json:"runs_dir"`
	StartedAt    time.Time `json:"started_at"`
	SessionState string    `json:"session_state"`
	Ticket       string    `json:"ticket,omitempty"`
	Phase        string    `json:"phase,omitempty"`
	Activity     string    `json:"activity,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	StateSince   time.Time `json:"state_since,omitzero"`
}

// Anomaly is one flagged cost anomaly the child records for a run: the phase that
// cleared a soft threshold, its output/turns/cost, and the human reasons.
type Anomaly struct {
	TS      string   `json:"ts"`
	Phase   string   `json:"phase"`
	Output  int      `json:"output"`
	Turns   int      `json:"turns"`
	Cost    float64  `json:"cost_usd"`
	Reasons []string `json:"reasons"`
}

// Spend is an accumulated (tokens, cost) figure the hub returns for a ticket total
// or a day total. Metered is false when some call in the sum recorded no per-call
// cost, so Cost is then a lower bound.
type Spend struct {
	Tokens  int     `json:"tokens"`
	Cost    float64 `json:"cost_usd"`
	Metered bool    `json:"metered"`
}

type appendTokensBody struct {
	Calls []TokenCall `json:"calls"`
}

type recordAnomaliesBody struct {
	Anomalies []Anomaly `json:"anomalies"`
}

// AppendEvents posts a batch of events for repo to the hub, which appends them to
// the authoritative events table in order and fans them out to live streams. The
// batch is sent whole so the hub preserves its order; a hub-connection failure
// surfaces as an IsUnreachable error so the caller can retry.
func (c *Client) AppendEvents(ctx context.Context, repo string, evs []Event) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "events"), appendEventsBody{Events: evs}, nil)
}

// AppendTranscript posts a batch of transcript chunks for repo to the hub, which
// appends them to the chunk store and fans them out to live subscribers. The batch
// is sent whole so the hub preserves per-session order; a hub-connection failure
// surfaces as an IsUnreachable error so the caller can retry.
func (c *Client) AppendTranscript(ctx context.Context, repo string, chunks []TranscriptChunk) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "transcripts"), appendTranscriptBody{Chunks: chunks}, nil)
}

// TranscriptChunks polls repo's live transcript for the chunks appended since
// after. A non-empty id pins one session (replay); follow advances to the newest
// session at or after since (the TUI/watch live tail). It returns the resolved
// session id, its dimensions, and the decoded bytes, so the caller feeds a
// terminal emulator without ever reading a file.
func (c *Client) TranscriptChunks(ctx context.Context, repo, id string, after int64, follow bool, since int64) (TranscriptPoll, error) {
	values := url.Values{}
	if id != "" {
		values.Set("id", id)
	}
	// Always sent, including 0 and -1: seqs start at 0, so a cursor of 0 (seq 0
	// seen) must page past it rather than replay it.
	values.Set("after", strconv.FormatInt(after, 10))
	if follow {
		values.Set("follow", "1")
	}
	if since > 0 {
		values.Set("since", strconv.FormatInt(since, 10))
	}
	path := c.repoPath(repo, "transcript/chunks")
	if enc := values.Encode(); enc != "" {
		path += "?" + enc
	}
	var body transcriptChunksResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return TranscriptPoll{}, err
	}
	out := TranscriptPoll{ID: body.ID, Cols: body.Cols, Rows: body.Rows, Seq: after}
	for _, ch := range body.Chunks {
		data, err := base64.StdEncoding.DecodeString(ch.Data)
		if err != nil {
			continue
		}
		out.Data = append(out.Data, data...)
		out.Seq = ch.Seq
	}
	return out, nil
}

// AppendTokenCalls posts a batch of token calls for repo to the hub, which appends
// them to the authoritative token_calls table. Each call carries its own ticket, so
// one batch may span buckets; a hub-connection failure surfaces as an IsUnreachable
// error so the caller can retry.
func (c *Client) AppendTokenCalls(ctx context.Context, repo string, calls []TokenCall) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "tokens"), appendTokensBody{Calls: calls}, nil)
}

// TokenTotal reads a ticket's summed token + cost spend from the hub — the status
// and budget ticket-cap read.
func (c *Client) TokenTotal(ctx context.Context, repo, ticket string) (Spend, error) {
	var sp Spend
	err := c.do(ctx, http.MethodGet, c.repoPath(repo, "runs/"+url.PathEscape(ticket)+"/tokens"), nil, &sp)
	return sp, err
}

// TokenDayTotal reads repo's summed spend for a local date (YYYY-MM-DD) from the
// hub — the budget day-cap read.
func (c *Client) TokenDayTotal(ctx context.Context, repo, date string) (Spend, error) {
	var sp Spend
	path := c.repoPath(repo, "tokens/day") + "?date=" + url.QueryEscape(date)
	err := c.do(ctx, http.MethodGet, path, nil, &sp)
	return sp, err
}

// RecordAnomalies records a ticket's flagged cost anomalies on the hub, replacing
// any it already holds for the ticket. A hub-connection failure surfaces as an
// IsUnreachable error so the caller can retry.
func (c *Client) RecordAnomalies(ctx context.Context, repo, ticket string, anomalies []Anomaly) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "runs/"+url.PathEscape(ticket)+"/anomalies"), recordAnomaliesBody{Anomalies: anomalies}, nil)
}

// PutInstance registers or refreshes this loop's presence with the hub, keyed by
// pid — sent on start, on every session-state change, and on the heartbeat timer.
// Presence is best-effort, so the caller ignores the returned error.
func (c *Client) PutInstance(ctx context.Context, pid int, hb InstanceHeartbeat) error {
	return c.do(ctx, http.MethodPut, c.instancePath(pid), hb, nil)
}

// DeleteInstance drops this loop's presence from the hub — the deregister on clean
// exit. A missing entry is not an error.
func (c *Client) DeleteInstance(ctx context.Context, pid int) error {
	err := c.do(ctx, http.MethodDelete, c.instancePath(pid), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Instance is one live loop as the hub lists it: the identifying PID and repo
// plus the session state it last reported. The start-path guards read the list
// to spot a takeover terminal or an in-flight run before touching a repo.
type Instance struct {
	PID          int    `json:"pid"`
	Repo         string `json:"repo"`
	RepoRoot     string `json:"repo_root"`
	SessionState string `json:"session_state"`
	Ticket       string `json:"ticket,omitempty"`
	Phase        string `json:"phase,omitempty"`
}

// Instances lists the loops the hub currently holds presence for — live entries
// only, dead PIDs already reaped.
func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	var out struct {
		Instances []Instance `json:"instances"`
	}
	if err := c.do(ctx, http.MethodGet, apiPrefix+"/instances", nil, &out); err != nil {
		return nil, err
	}
	return out.Instances, nil
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

// RunSummary is one ticket's run as the hub's run board reports it: its
// checkpoint phase and the failure class/reason that flags a paused, faulted, or
// quarantined run.
type RunSummary struct {
	Ticket        string `json:"ticket"`
	Title         string `json:"title,omitempty"`
	Phase         string `json:"phase"`
	Terminal      bool   `json:"terminal"`
	Branch        string `json:"branch,omitempty"`
	PR            string `json:"pr,omitempty"`
	PRURL         string `json:"pr_url,omitempty"`
	FailureClass  string `json:"failure_class,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// Runs lists every run the hub holds for repo, in the board's phase order — the
// forensics runs read.
func (c *Client) Runs(ctx context.Context, repo string) ([]RunSummary, error) {
	var out struct {
		Runs []RunSummary `json:"runs"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath(repo, "runs"), nil, &out); err != nil {
		return nil, err
	}
	return out.Runs, nil
}

// EventRecord is one persisted event as the forensics query returns it: the
// ordering id, the envelope, and the decoded fields bag.
type EventRecord struct {
	ID     string         `json:"id"`
	TS     string         `json:"ts"`
	Kind   string         `json:"kind"`
	Phase  string         `json:"phase,omitempty"`
	Msg    string         `json:"msg,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// EventQuery narrows a forensics event read. After pages forward past an id for a
// follow tail; Since is an RFC3339 lower bound, empty for none.
type EventQuery struct {
	Kind   string
	Ticket string
	Grep   string
	Since  string
	After  int64
	Limit  int
}

// QueryEvents reads repo's events matching q from the hub, in chronological order.
func (c *Client) QueryEvents(ctx context.Context, repo string, q EventQuery) ([]EventRecord, error) {
	values := url.Values{}
	if q.Kind != "" {
		values.Set("kind", q.Kind)
	}
	if q.Ticket != "" {
		values.Set("ticket", q.Ticket)
	}
	if q.Grep != "" {
		values.Set("grep", q.Grep)
	}
	if q.Since != "" {
		values.Set("since", q.Since)
	}
	if q.After > 0 {
		values.Set("after", strconv.FormatInt(q.After, 10))
	}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	path := c.repoPath(repo, "events/query")
	if enc := values.Encode(); enc != "" {
		path += "?" + enc
	}
	var out struct {
		Events []EventRecord `json:"events"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Events, nil
}

// PhaseSpend is one phase's slice of a ticket's spend in a summary.
type PhaseSpend struct {
	Phase   string  `json:"phase"`
	Tokens  int     `json:"tokens"`
	Cost    float64 `json:"cost_usd"`
	Turns   int     `json:"turns"`
	Calls   int     `json:"calls"`
	Metered bool    `json:"metered"`
}

// SpendSummary is a ticket's spend broken down by phase, with the same grand total
// the status view reports.
type SpendSummary struct {
	Ticket string       `json:"ticket"`
	Total  Spend        `json:"total"`
	Phases []PhaseSpend `json:"phases"`
}

// TicketSpend reads a ticket's per-phase spend summary from the hub. The total
// carries the same figures as TokenTotal, so a summary never drifts from status.
func (c *Client) TicketSpend(ctx context.Context, repo, ticket string) (SpendSummary, error) {
	var out SpendSummary
	err := c.do(ctx, http.MethodGet, c.repoPath(repo, "runs/"+url.PathEscape(ticket)+"/spend"), nil, &out)
	return out, err
}

// PutArtifact writes a ticket's phase artifact of the given kind to the hub, which
// persists it in the authoritative artifacts table. A hub-connection failure
// surfaces as an IsUnreachable error so the caller can retry; the request is
// idempotent.
func (c *Client) PutArtifact(ctx context.Context, repo, ticket, kind, content string) error {
	return c.do(ctx, http.MethodPut, c.artifactPath(repo, ticket, kind), artifactBody{Content: content}, nil)
}

// GetArtifact reads a ticket's phase artifact of the given kind from the hub,
// returning ok=false when the hub holds none.
func (c *Client) GetArtifact(ctx context.Context, repo, ticket, kind string) (content string, ok bool, err error) {
	var out artifactBody
	err = c.do(ctx, http.MethodGet, c.artifactPath(repo, ticket, kind), nil, &out)
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return out.Content, true, nil
}

// DeleteArtifacts drops every artifact the hub holds for a ticket — the
// reset/clear/fresh-build sweep. A ticket with none is not an error.
func (c *Client) DeleteArtifacts(ctx context.Context, repo, ticket string) error {
	err := c.do(ctx, http.MethodDelete, c.artifactsPath(repo, ticket), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// AppendLesson records one distilled lesson for repo with the hub, which appends it
// to the authoritative per-repo lessons ledger. A hub-connection failure surfaces as
// an IsUnreachable error so the caller can retry.
func (c *Client) AppendLesson(ctx context.Context, repo string, l Lesson) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "lessons"), l, nil)
}

// Lessons returns every lesson the hub holds for repo, most recent first — the ledger
// the child recalls prompt-injection lessons from. A repo with none yields an empty
// slice, not an error.
func (c *Client) Lessons(ctx context.Context, repo string) ([]Lesson, error) {
	var out lessonsBody
	err := c.do(ctx, http.MethodGet, c.repoPath(repo, "lessons"), nil, &out)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return out.Lessons, nil
}

// PutDrainOutcome records a queued child's exit outcome — the failure class and
// reason it hit, both empty for a clean finish — with the hub, keyed by ticket.
// The drainer reads it to settle the item. A hub-connection failure surfaces as
// an IsUnreachable error so the caller can retry; the request is idempotent.
func (c *Client) PutDrainOutcome(ctx context.Context, repo, ticket, class, reason string) error {
	return c.do(ctx, http.MethodPut, c.drainOutcomePath(repo, ticket), drainOutcomeBody{Class: class, Reason: reason}, nil)
}

// PutPhaseLog stores a ticket's log for a phase with the hub, replacing any prior
// content for that phase. The inspector reads it back with PhaseLogs. A
// hub-connection failure surfaces as an IsUnreachable error so the caller can
// retry; the request is idempotent.
func (c *Client) PutPhaseLog(ctx context.Context, repo, ticket, phase, content string) error {
	return c.do(ctx, http.MethodPut, c.phaseLogPath(repo, ticket, phase), phaseLogBody{Content: content}, nil)
}

// PhaseLogs returns a ticket's stored phase logs, most-recently-written first. A
// ticket with none yields an empty slice, not an error.
func (c *Client) PhaseLogs(ctx context.Context, repo, ticket string) ([]PhaseLog, error) {
	var out phaseLogsBody
	err := c.do(ctx, http.MethodGet, c.phaseLogsPath(repo, ticket), nil, &out)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return out.Logs, nil
}

// DeletePhaseLogs drops every phase log the hub holds for a ticket — the
// reset/clear/fresh-build sweep. A ticket with none is not an error.
func (c *Client) DeletePhaseLogs(ctx context.Context, repo, ticket string) error {
	err := c.do(ctx, http.MethodDelete, c.phaseLogsPath(repo, ticket), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// resolvedPromptsBody decodes the repo prompt catalog down to what the
// pipeline needs: each prompt's effective scope and body.
type resolvedPromptsBody struct {
	Prompts []struct {
		Name          string `json:"name"`
		Effective     string `json:"effective"`
		EffectiveBody string `json:"effective_body"`
	} `json:"prompts"`
}

// ResolvedPrompts reads repo's effective prompt set from the hub and returns
// the overridden entries as a name → body map (repo > global precedence
// resolved hub-side). Prompts still on their built-in default are omitted, so
// the caller renders those locally.
func (c *Client) ResolvedPrompts(ctx context.Context, repo string) (map[string]string, error) {
	var body resolvedPromptsBody
	if err := c.do(ctx, http.MethodGet, c.repoPath(repo, "prompts"), nil, &body); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range body.Prompts {
		if p.Effective != "default" {
			out[p.Name] = p.EffectiveBody
		}
	}
	return out, nil
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

// Attachment is one of an issue's files as the hub reports it: the metadata a
// caller needs to describe the file, plus the source URL an issue body embeds.
type Attachment struct {
	ID        int64  `json:"id"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	IsImage   bool   `json:"is_image"`
	SourceURL string `json:"source_url"`
}

// IssueAttachments lists the files attached to an issue — what sync discovered in
// its description and comments, plus anything uploaded against it. Metadata only;
// bytes come from AttachmentBytes.
func (c *Client) IssueAttachments(ctx context.Context, repo, id string) ([]Attachment, error) {
	var out []Attachment
	err := c.do(ctx, http.MethodGet, c.repoPath(repo, "issues/"+url.PathEscape(id)+"/attachments"), nil, &out)
	return out, err
}

// AttachmentBytes downloads an attachment's bytes. The hub fetches them from the
// tracker and caches them on the first read, so a child asking for a screenshot
// never writes the attachment store itself (ADR 0008).
func (c *Client) AttachmentBytes(ctx context.Context, repo string, id int64) ([]byte, error) {
	path := c.repoPath(repo, "attachments/"+strconv.FormatInt(id, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := attachmentHTTP.Do(req)
	if err != nil {
		return nil, &transportError{op: http.MethodGet + " " + path, err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: %s", path, hubError(resp.Body, resp.Status))
	}
	return io.ReadAll(io.LimitReader(resp.Body, attachmentMaxBytes))
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

func (c *Client) instancePath(pid int) string {
	return apiPrefix + "/instances/" + strconv.Itoa(pid)
}

func (c *Client) checkpointPath(repo, ticket string) string {
	return c.repoPath(repo, "runs/"+url.PathEscape(ticket)+"/checkpoint")
}

func (c *Client) artifactsPath(repo, ticket string) string {
	return c.repoPath(repo, "runs/"+url.PathEscape(ticket)+"/artifacts")
}

func (c *Client) artifactPath(repo, ticket, kind string) string {
	return c.artifactsPath(repo, ticket) + "/" + url.PathEscape(kind)
}

func (c *Client) drainOutcomePath(repo, ticket string) string {
	return c.repoPath(repo, "runs/"+url.PathEscape(ticket)+"/drain-outcome")
}

func (c *Client) phaseLogsPath(repo, ticket string) string {
	return c.repoPath(repo, "runs/"+url.PathEscape(ticket)+"/logs")
}

func (c *Client) phaseLogPath(repo, ticket, phase string) string {
	return c.phaseLogsPath(repo, ticket) + "/" + url.PathEscape(phase)
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
