package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/tracker/azureapi"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// ErrWriterUnavailable means the repo carries no direct tracker credentials, so
// the hub cannot create issues or comment without falling back to an agent/MCP —
// which a Writer never does.
var ErrWriterUnavailable = errors.New("tracker: no direct API credentials configured")

// ErrUnsupported means the provider has no direct-API path for the requested
// operation. It is a capability gap, not a failure: a surface that reads it hides
// the affordance rather than reporting an error.
var ErrUnsupported = errors.New("tracker: operation not supported by this provider")

// jiraDefaultIssueType is the issue type a hub-created Jira issue is filed as.
// Every Jira project ships with "Task"; the general new-issue form does not pick
// a type.
const jiraDefaultIssueType = "Task"

// IssueDraft is a new issue to create: a title, an optional markdown description,
// any labels to apply (e.g. the ready label), and an optional parent to nest the
// issue under so an epic and its sub-issues can be filed from the board. Epic
// marks a draft that will carry sub-issues of its own, so a tracker with a typed
// hierarchy files it one level up — Jira rejects a child whose parent sits at the
// child's own level. Slice is the mirror image: a draft filed as one piece of the
// issue named by Parent, which Azure DevOps files a level down as a Task where
// Parent alone would read as the Feature a story hangs off. Type pins the
// tracker's own work-item type, for a provider whose hierarchy is typed and offers
// more than one type at the draft's level; empty files that level's default.
type IssueDraft struct {
	Title       string
	Description string
	Labels      []string
	Parent      string
	Epic        bool
	Slice       bool
	Type        string
}

// NewIssue identifies a freshly created issue: its human identifier and a link.
type NewIssue struct {
	Identifier string
	URL        string
}

// AssignableUser is one person the tracker will accept as an issue's assignee,
// keyed by the provider's own user id (Linear user id, Jira accountId). The
// lookup is live — trau persists no users of its own (ADR 0020).
type AssignableUser struct {
	ID   string
	Name string
}

// DocumentDraft is a PRD to publish: a title and its markdown body.
type DocumentDraft struct {
	Title    string
	Markdown string
}

// The kinds a PublishedDocument can take, by where the PRD landed.
const (
	DocumentKindDocument = "document" // a Linear project document
	DocumentKindIssue    = "issue"    // the Jira issue-description fallback
)

// PublishedDocument identifies published PRD output. URL links to it; Identifier
// carries the issue key for the Jira fallback and is empty for a Linear document;
// Kind is one of the DocumentKind constants.
type PublishedDocument struct {
	URL        string
	Identifier string
	Kind       string
}

// Writer creates tracker work directly through a provider's REST/GraphQL API,
// with no agent process and no MCP. It is the seam the serve hub uses to file
// issues, comment on existing tickets and publish PRDs from the UI, using the
// repo's own tracker credentials.
type Writer interface {
	CreateIssue(ctx context.Context, draft IssueDraft) (NewIssue, error)
	AddComment(ctx context.Context, id, body string) error
	UpdateDescription(ctx context.Context, id, body string) error
	UpdateLabels(ctx context.Context, id string, add, remove []string) error
	// LinkBlocks records that blocker blocks blocked (blocked is blocked by
	// blocker), the direction the readers interpret. Both are human identifiers.
	LinkBlocks(ctx context.Context, blocker, blocked string) error
	PublishDocument(ctx context.Context, draft DocumentDraft) (PublishedDocument, error)
	// AssignIssue writes the issue's assignee on the tracker; an empty assigneeID
	// clears it (Unassigned). Always driven by an explicit user gesture (ADR 0020).
	AssignIssue(ctx context.Context, id, assigneeID string) error
	// AssignableUsers lists who an issue can be assigned to, optionally narrowed by
	// a display-name query.
	AssignableUsers(ctx context.Context, query string) ([]AssignableUser, error)
}

// IssueTyper is the optional capability of naming the work-item types a create
// may file as, for a provider whose hierarchy is typed and offers more than one at
// the level trau files on. A surface that reads it offers the choice; one whose
// Writer does not implement it hides the affordance.
type IssueTyper interface {
	CreatableTypes(ctx context.Context) ([]string, error)
}

// NewWriter builds a direct Writer for the provider from cfg, or
// ErrWriterUnavailable when cfg carries no usable direct credentials. A Writer
// only ever uses the direct API — it never falls back to an agent/MCP, so the
// hub's writes stay a single, explicit tracker identity. The internal provider
// is refused here: internal issues live in the hub's own store, which the hub
// writes in-process rather than through any API client.
func NewWriter(provider string, cfg Config) (Writer, error) {
	switch provider {
	case "linear":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, ErrWriterUnavailable
		}
		return newLinearWriter(linearapi.New(cfg.APIKey), cfg.Team, cfg.Project), nil
	case "jira":
		if cfg.BaseURL == "" || cfg.Email == "" || cfg.APIKey == "" {
			return nil, ErrWriterUnavailable
		}
		return &jiraWriter{
			client:    jiraapi.New(cfg.BaseURL, cfg.Email, cfg.APIKey),
			project:   cfg.Team,
			baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
			issueType: jiraDefaultIssueType,
			epicType:  cfg.EpicType,
		}, nil
	case "azure":
		if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
			return nil, ErrWriterUnavailable
		}
		return &azureWriter{
			client:  azureapi.New(cfg.BaseURL, cfg.APIKey),
			project: cfg.Team,
			team:    levelTeam(cfg.BoardTeams),
		}, nil
	case "github":
		return nil, fmt.Errorf("tracker: %s issue creation over the direct API is not supported: %w", provider, ErrUnsupported)
	case "internal":
		return nil, errors.New("tracker: internal issues are hub-owned; the serve hub writes them straight to its issue store, not through a direct-API writer")
	default:
		return nil, fmt.Errorf("unknown tracker provider %q (expected: linear | jira | azure | github | internal)", provider)
	}
}

// Linear's identifier search lags a create by a moment, so a not-found lookup is
// retried before it is reported.
const (
	linearResolveAttempts = 3
	linearResolveBackoff  = time.Second
)

type linearWriter struct {
	client  *linearapi.Client
	team    string
	project string
	// created maps the identifier of every issue this writer created to its node
	// id, so a follow-up write addresses it without a lookup Linear's search index
	// may not answer yet. The hub builds a writer per request, so the cache lives
	// exactly as long as one apply.
	created map[string]string
	// backoff is the first retry delay, doubling per attempt — a field so tests
	// need not sleep out the real one.
	backoff time.Duration
}

func newLinearWriter(client *linearapi.Client, team, project string) *linearWriter {
	return &linearWriter{
		client:  client,
		team:    team,
		project: project,
		created: map[string]string{},
		backoff: linearResolveBackoff,
	}
}

// resolveID returns the node id behind a human identifier: from the created cache
// when this writer filed the issue, otherwise from a retried lookup.
func (w *linearWriter) resolveID(ctx context.Context, identifier string) (string, error) {
	if id, ok := w.created[identifier]; ok {
		return id, nil
	}
	var err error
	for attempt := range linearResolveAttempts {
		var issue *linearapi.Issue
		if issue, err = w.client.Issue(ctx, identifier); err == nil {
			return issue.ID, nil
		}
		if !errors.Is(err, linearapi.ErrNotFound) || attempt == linearResolveAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(w.backoff << attempt):
		}
	}
	return "", err
}

func (w *linearWriter) CreateIssue(ctx context.Context, draft IssueDraft) (NewIssue, error) {
	team, err := w.client.TeamByKey(ctx, w.team)
	if err != nil {
		return NewIssue{}, err
	}
	in := linearapi.CreateIssueInput{TeamID: team.ID, Title: draft.Title, Description: draft.Description, Labels: draft.Labels}
	if project := strings.TrimSpace(w.project); project != "" {
		p, err := w.client.ProjectByName(ctx, project)
		if err != nil {
			return NewIssue{}, fmt.Errorf("resolve project %q: %w", project, err)
		}
		in.ProjectID = p.ID
	}
	if parent := strings.TrimSpace(draft.Parent); parent != "" {
		parentID, err := w.resolveID(ctx, parent)
		if err != nil {
			return NewIssue{}, fmt.Errorf("resolve parent %s: %w", parent, err)
		}
		in.ParentID = parentID
	}
	id, identifier, url, err := w.client.CreateIssue(ctx, in)
	if err != nil {
		return NewIssue{}, err
	}
	w.created[identifier] = id
	return NewIssue{Identifier: identifier, URL: url}, nil
}

func (w *linearWriter) AddComment(ctx context.Context, id, body string) error {
	return w.client.AddComment(ctx, id, body)
}

func (w *linearWriter) UpdateDescription(ctx context.Context, id, body string) error {
	return w.client.UpdateDescription(ctx, id, body)
}

func (w *linearWriter) UpdateLabels(ctx context.Context, id string, add, remove []string) error {
	return w.client.UpdateLabels(ctx, id, add, remove)
}

func (w *linearWriter) LinkBlocks(ctx context.Context, blocker, blocked string) error {
	blockerID, err := w.resolveID(ctx, blocker)
	if err != nil {
		return fmt.Errorf("resolve blocker %s: %w", blocker, err)
	}
	blockedID, err := w.resolveID(ctx, blocked)
	if err != nil {
		return fmt.Errorf("resolve blocked %s: %w", blocked, err)
	}
	return w.client.CreateBlockRelationByID(ctx, blockerID, blockedID)
}

func (w *linearWriter) AssignIssue(ctx context.Context, id, assigneeID string) error {
	issueID, err := w.resolveID(ctx, id)
	if err != nil {
		return err
	}
	return w.client.AssignIssueByID(ctx, issueID, assigneeID)
}

func (w *linearWriter) AssignableUsers(ctx context.Context, query string) ([]AssignableUser, error) {
	users, err := w.client.AssignableUsers(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]AssignableUser, 0, len(users))
	for _, u := range users {
		out = append(out, AssignableUser{ID: u.ID, Name: u.Name})
	}
	return out, nil
}

func (w *linearWriter) PublishDocument(ctx context.Context, draft DocumentDraft) (PublishedDocument, error) {
	if strings.TrimSpace(w.project) == "" {
		return PublishedDocument{}, errors.New("tracker: no Linear project configured for this repo (set PROJECT) — a PRD document needs a project to live under")
	}
	project, err := w.client.ProjectByName(ctx, w.project)
	if err != nil {
		return PublishedDocument{}, err
	}
	url, err := w.client.CreateDocument(ctx, project.ID, draft.Title, draft.Markdown)
	if err != nil {
		return PublishedDocument{}, err
	}
	return PublishedDocument{URL: url, Kind: DocumentKindDocument}, nil
}

type jiraWriter struct {
	client    *jiraapi.Client
	project   string
	baseURL   string
	issueType string
	epicType  string
}

func (w *jiraWriter) CreateIssue(ctx context.Context, draft IssueDraft) (NewIssue, error) {
	key, err := w.createKey(ctx, draft)
	if err != nil {
		return NewIssue{}, err
	}
	return NewIssue{Identifier: key, URL: w.baseURL + "/browse/" + key}, nil
}

func (w *jiraWriter) createKey(ctx context.Context, draft IssueDraft) (string, error) {
	if draft.Epic {
		return w.client.CreateEpic(ctx, w.project, w.epicType, draft.Title, draft.Description, draft.Labels)
	}
	return w.client.CreateIssue(ctx, w.project, w.issueType, draft.Title, draft.Description, draft.Labels, strings.TrimSpace(draft.Parent))
}

func (w *jiraWriter) AddComment(ctx context.Context, id, body string) error {
	return w.client.AddComment(ctx, id, body)
}

func (w *jiraWriter) UpdateDescription(ctx context.Context, id, body string) error {
	return w.client.UpdateDescription(ctx, id, body)
}

func (w *jiraWriter) UpdateLabels(ctx context.Context, id string, add, remove []string) error {
	return w.client.UpdateLabels(ctx, id, add, remove)
}

func (w *jiraWriter) LinkBlocks(ctx context.Context, blocker, blocked string) error {
	return w.client.LinkBlocks(ctx, blocker, blocked)
}

func (w *jiraWriter) AssignIssue(ctx context.Context, id, assigneeID string) error {
	return w.client.AssignIssue(ctx, id, assigneeID)
}

func (w *jiraWriter) AssignableUsers(ctx context.Context, query string) ([]AssignableUser, error) {
	users, err := w.client.AssignableUsers(ctx, w.project, query)
	if err != nil {
		return nil, err
	}
	out := make([]AssignableUser, 0, len(users))
	for _, u := range users {
		out = append(out, AssignableUser{ID: u.ID, Name: u.Name})
	}
	return out, nil
}

func (w *jiraWriter) PublishDocument(ctx context.Context, draft DocumentDraft) (PublishedDocument, error) {
	key, err := w.client.CreateIssue(ctx, w.project, w.issueType, draft.Title, draft.Markdown, nil, "")
	if err != nil {
		return PublishedDocument{}, err
	}
	return PublishedDocument{URL: w.baseURL + "/browse/" + key, Identifier: key, Kind: DocumentKindIssue}, nil
}
