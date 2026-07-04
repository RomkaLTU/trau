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
// and any labels to apply (e.g. the ready label).
type IssueDraft struct {
	Title       string
	Description string
	Labels      []string
}

// NewIssue identifies a freshly created issue: its human identifier and a link.
type NewIssue struct {
	Identifier string
	URL        string
}

// Writer creates tracker work directly through a provider's REST/GraphQL API,
// with no agent process and no MCP. It is the seam the serve hub uses to file
// issues and comment on existing tickets from the UI, using the repo's own
// tracker credentials.
type Writer interface {
	CreateIssue(ctx context.Context, draft IssueDraft) (NewIssue, error)
	AddComment(ctx context.Context, id, body string) error
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
		return &linearWriter{client: linearapi.New(cfg.APIKey), team: cfg.Team}, nil
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
	client *linearapi.Client
	team   string
}

func (w *linearWriter) CreateIssue(ctx context.Context, draft IssueDraft) (NewIssue, error) {
	team, err := w.client.TeamByKey(ctx, w.team)
	if err != nil {
		return NewIssue{}, err
	}
	id, url, err := w.client.CreateIssue(ctx, team.ID, draft.Title, draft.Description, draft.Labels)
	if err != nil {
		return NewIssue{}, err
	}
	return NewIssue{Identifier: id, URL: url}, nil
}

func (w *linearWriter) AddComment(ctx context.Context, id, body string) error {
	return w.client.AddComment(ctx, id, body)
}

type jiraWriter struct {
	client    *jiraapi.Client
	project   string
	baseURL   string
	issueType string
}

func (w *jiraWriter) CreateIssue(ctx context.Context, draft IssueDraft) (NewIssue, error) {
	key, err := w.client.CreateIssue(ctx, w.project, w.issueType, draft.Title, draft.Description, draft.Labels)
	if err != nil {
		return NewIssue{}, err
	}
	return NewIssue{Identifier: key, URL: w.baseURL + "/browse/" + key}, nil
}

func (w *jiraWriter) AddComment(ctx context.Context, id, body string) error {
	return w.client.AddComment(ctx, id, body)
}
