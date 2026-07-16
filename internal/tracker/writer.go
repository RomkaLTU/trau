package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// ErrWriterUnavailable means the repo carries no direct tracker credentials, so
// the hub cannot create issues or comment without falling back to an agent/MCP —
// which a Writer never does.
var ErrWriterUnavailable = errors.New("tracker: no direct API credentials configured")

// jiraDefaultIssueType is the issue type a hub-created Jira issue is filed as.
// Every Jira project ships with "Task"; the general new-issue form does not pick
// a type.
const jiraDefaultIssueType = "Task"

// IssueDraft is a new issue to create: a title, an optional markdown description,
// any labels to apply (e.g. the ready label), and an optional parent to nest the
// issue under so an epic and its sub-issues can be filed from the board.
type IssueDraft struct {
	Title       string
	Description string
	Labels      []string
	Parent      string
}

// NewIssue identifies a freshly created issue: its human identifier and a link.
type NewIssue struct {
	Identifier string
	URL        string
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
}

// NewWriter builds a direct Writer for the provider from cfg, or
// ErrWriterUnavailable when cfg carries no usable direct credentials. A Writer
// only ever uses the direct API — it never falls back to an agent/MCP, so the
// hub's writes stay a single, explicit tracker identity.
func NewWriter(provider string, cfg Config) (Writer, error) {
	switch provider {
	case "linear":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, ErrWriterUnavailable
		}
		return &linearWriter{client: linearapi.New(cfg.APIKey), team: cfg.Team, project: cfg.Project}, nil
	case "jira":
		if cfg.BaseURL == "" || cfg.Email == "" || cfg.APIKey == "" {
			return nil, ErrWriterUnavailable
		}
		return &jiraWriter{
			client:    jiraapi.New(cfg.BaseURL, cfg.Email, cfg.APIKey),
			project:   cfg.Team,
			baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
			issueType: jiraDefaultIssueType,
		}, nil
	case "github":
		return nil, fmt.Errorf("tracker: github issue creation over the direct API is not supported")
	default:
		return nil, fmt.Errorf("unknown tracker provider %q (expected: linear | jira)", provider)
	}
}

type linearWriter struct {
	client  *linearapi.Client
	team    string
	project string
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
		issue, err := w.client.Issue(ctx, parent)
		if err != nil {
			return NewIssue{}, fmt.Errorf("resolve parent %s: %w", parent, err)
		}
		in.ParentID = issue.ID
	}
	id, url, err := w.client.CreateIssue(ctx, in)
	if err != nil {
		return NewIssue{}, err
	}
	return NewIssue{Identifier: id, URL: url}, nil
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
	return w.client.CreateBlockRelation(ctx, blocker, blocked)
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
}

func (w *jiraWriter) CreateIssue(ctx context.Context, draft IssueDraft) (NewIssue, error) {
	key, err := w.client.CreateIssue(ctx, w.project, w.issueType, draft.Title, draft.Description, draft.Labels, strings.TrimSpace(draft.Parent))
	if err != nil {
		return NewIssue{}, err
	}
	return NewIssue{Identifier: key, URL: w.baseURL + "/browse/" + key}, nil
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

func (w *jiraWriter) PublishDocument(ctx context.Context, draft DocumentDraft) (PublishedDocument, error) {
	key, err := w.client.CreateIssue(ctx, w.project, w.issueType, draft.Title, draft.Markdown, nil, "")
	if err != nil {
		return PublishedDocument{}, err
	}
	return PublishedDocument{URL: w.baseURL + "/browse/" + key, Identifier: key, Kind: DocumentKindIssue}, nil
}
