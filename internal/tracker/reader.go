package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// ErrReaderUnavailable means the repo carries no direct tracker credentials, so
// the hub cannot browse the backlog without falling back to an agent/MCP — which
// a Reader never does. It is also returned for providers with no direct read API
// (GitHub), so the hub shows a backlog-unavailable state instead of erroring.
var ErrReaderUnavailable = errors.New("tracker: no direct API credentials configured")

// ErrIssueNotFound means the tracker has no issue with the requested identifier,
// so a caller can tell a mistyped or absent ticket apart from a transport error.
var ErrIssueNotFound = errors.New("tracker: issue not found")

// StatusGroup buckets a tracker issue's workflow state into a small,
// provider-neutral set the backlog board groups its columns on.
type StatusGroup string

const (
	StatusGroupBacklog   StatusGroup = "backlog"
	StatusGroupUnstarted StatusGroup = "unstarted"
	StatusGroupStarted   StatusGroup = "started"
	StatusGroupDone      StatusGroup = "done"
	StatusGroupCanceled  StatusGroup = "canceled"
	StatusGroupUnknown   StatusGroup = "unknown"
)

// BacklogItem is one issue in a Project's full backlog: its identifier, title,
// display status and normalized group, label names, and epic relationship.
// Parent carries the epic a sub-issue belongs to ("" for a top-level issue);
// HasChildren marks an issue that is itself an epic/parent. Ready reports whether
// the issue carries the repo's ready label, so the board can distinguish it.
type BacklogItem struct {
	ID          string
	Title       string
	Status      string
	Group       StatusGroup
	Labels      []string
	Parent      string
	HasChildren bool
	Ready       bool
}

// IssueSummary is one issue read by identifier: the BacklogItem fields plus the
// issue's own project and whether it belongs to the repo's configured project. A
// cross-project ticket (InProject false) exists but is out of this repo's scope,
// so the hub can refuse to run it here instead of trusting the raw id. When the
// repo configures no project, InProject is always true — there is no guard.
type IssueSummary struct {
	BacklogItem
	Project   string
	InProject bool
}

// Reader lists a Project's full tracker backlog directly through a provider's
// REST/GraphQL API, with no agent process and no MCP. It is the read counterpart
// of Writer: the seam the serve hub uses to browse every ticket in the Active
// repo's Project — not just the eligible queue — using the repo's own credentials.
type Reader interface {
	Backlog(ctx context.Context) ([]BacklogItem, error)
	// Issue fetches one issue by its human identifier (e.g. "COD-712"), so the
	// hub can confirm a specific ticket before running it. It returns
	// ErrIssueNotFound when the tracker has no such issue. A ticket in another
	// project is returned with InProject false rather than hidden, so the caller
	// can explain why it cannot be run here.
	Issue(ctx context.Context, id string) (IssueSummary, error)
}

// NewReader builds a direct Reader for the provider from cfg, or
// ErrReaderUnavailable when cfg carries no usable direct credentials. A Reader
// only ever uses the direct API — it never falls back to an agent/MCP, so the
// hub's reads stay a single, explicit tracker identity.
func NewReader(provider string, cfg Config) (Reader, error) {
	switch provider {
	case "linear":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, ErrReaderUnavailable
		}
		return &linearReader{client: linearapi.New(cfg.APIKey), team: cfg.Team, project: cfg.Project, readyLabel: cfg.ReadyLabel}, nil
	case "jira":
		if cfg.BaseURL == "" || cfg.Email == "" || cfg.APIKey == "" {
			return nil, ErrReaderUnavailable
		}
		return &jiraReader{client: jiraapi.New(cfg.BaseURL, cfg.Email, cfg.APIKey), project: cfg.Team, readyLabel: cfg.ReadyLabel}, nil
	case "github":
		return nil, ErrReaderUnavailable
	default:
		return nil, fmt.Errorf("unknown tracker provider %q (expected: linear | jira)", provider)
	}
}

type linearReader struct {
	client     *linearapi.Client
	team       string
	project    string
	readyLabel string
}

func (r *linearReader) Backlog(ctx context.Context) ([]BacklogItem, error) {
	team, err := r.client.TeamByKey(ctx, r.team)
	if err != nil {
		return nil, err
	}
	issues, err := r.client.TeamBacklog(ctx, team.ID)
	if err != nil {
		return nil, err
	}
	out := make([]BacklogItem, 0, len(issues))
	for _, iss := range issues {
		if !inProject(iss.ProjectName, r.project) {
			continue
		}
		labels := labelNames(iss.Labels)
		out = append(out, BacklogItem{
			ID:          iss.Identifier,
			Title:       iss.Title,
			Status:      iss.State.Name,
			Group:       mapLinearGroup(iss.State.Type),
			Labels:      labels,
			Parent:      iss.ParentID,
			HasChildren: iss.HasChildren,
			Ready:       containsLabel(labels, r.readyLabel),
		})
	}
	return out, nil
}

func (r *linearReader) Issue(ctx context.Context, id string) (IssueSummary, error) {
	iss, err := r.client.Issue(ctx, id)
	if err != nil {
		if errors.Is(err, linearapi.ErrNotFound) {
			return IssueSummary{}, ErrIssueNotFound
		}
		return IssueSummary{}, err
	}
	labels := labelNames(iss.Labels)
	return IssueSummary{
		BacklogItem: BacklogItem{
			ID:          iss.Identifier,
			Title:       iss.Title,
			Status:      iss.State.Name,
			Group:       mapLinearGroup(iss.State.Type),
			Labels:      labels,
			Parent:      iss.Parent.Identifier,
			HasChildren: len(iss.Children) > 0,
			Ready:       containsLabel(labels, r.readyLabel),
		},
		Project:   iss.Project.Name,
		InProject: inProject(iss.Project.Name, r.project),
	}, nil
}

type jiraReader struct {
	client     *jiraapi.Client
	project    string
	readyLabel string
}

func (r *jiraReader) Backlog(ctx context.Context) ([]BacklogItem, error) {
	issues, err := r.client.Backlog(ctx, r.project)
	if err != nil {
		return nil, err
	}
	out := make([]BacklogItem, 0, len(issues))
	for _, iss := range issues {
		out = append(out, BacklogItem{
			ID:          iss.Key,
			Title:       iss.Summary,
			Status:      iss.StatusName,
			Group:       mapJiraGroup(iss.StatusCategory, iss.Resolution),
			Labels:      iss.Labels,
			Parent:      iss.ParentKey,
			HasChildren: iss.IsEpic,
			Ready:       containsLabel(iss.Labels, r.readyLabel),
		})
	}
	return out, nil
}

func (r *jiraReader) Issue(ctx context.Context, id string) (IssueSummary, error) {
	iss, err := r.client.Issue(ctx, id)
	if err != nil {
		if errors.Is(err, jiraapi.ErrNotFound) {
			return IssueSummary{}, ErrIssueNotFound
		}
		return IssueSummary{}, err
	}
	return IssueSummary{
		BacklogItem: BacklogItem{
			ID:     iss.Key,
			Title:  iss.Summary,
			Status: iss.Status.Name,
			Group:  mapJiraGroup(iss.Status.Category, iss.Resolution),
			Labels: iss.Labels,
			Parent: iss.Parent,
			Ready:  containsLabel(iss.Labels, r.readyLabel),
		},
		Project:   iss.Project.Key,
		InProject: inProject(iss.Project.Key, r.project),
	}, nil
}

// mapLinearGroup maps a Linear workflow-state type onto a normalized status
// group. Linear's state types are triage | backlog | unstarted | started |
// completed | canceled.
func mapLinearGroup(stateType string) StatusGroup {
	switch strings.ToLower(strings.TrimSpace(stateType)) {
	case "triage", "backlog":
		return StatusGroupBacklog
	case "unstarted":
		return StatusGroupUnstarted
	case "started":
		return StatusGroupStarted
	case "completed":
		return StatusGroupDone
	case "canceled":
		return StatusGroupCanceled
	default:
		return StatusGroupUnknown
	}
}

// mapJiraGroup maps a Jira statusCategory key onto a normalized status group.
// Jira has no backlog or canceled category, so a To-Do issue groups as unstarted
// and a done-category issue closed with a won't-do/duplicate resolution groups as
// canceled rather than done.
func mapJiraGroup(category, resolution string) StatusGroup {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "new":
		return StatusGroupUnstarted
	case "indeterminate":
		return StatusGroupStarted
	case "done":
		if isCanceledResolution(resolution) {
			return StatusGroupCanceled
		}
		return StatusGroupDone
	default:
		return StatusGroupUnknown
	}
}

// containsLabel reports whether labels carries want (case-insensitively), so the
// board can flag ready-labelled tickets. An empty want is never a match.
func containsLabel(labels []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), want) {
			return true
		}
	}
	return false
}
