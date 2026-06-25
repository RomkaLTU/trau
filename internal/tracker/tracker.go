// Package tracker isolates project-management interactions behind a typed seam.
//
// The Linear implementation can use Linear's GraphQL API directly when a
// LINEAR_API_KEY is configured, falling back to the Linear MCP otherwise.
// Other providers reach the PM tool through the relevant MCP inside agent
// calls. Each method either uses a direct API or renders a natural-language
// prompt, runs it through an [agent.Runner], and recovers the result from
// sentinel lines. Adding a new PM provider means implementing the Tracker
// interface once; the loop and TUI stay provider-agnostic.
package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/agent"
)

// DefaultPrefix is the issue-identifier prefix assumed as a last resort when none
// can be derived from a parent id or the configured Scope.Prefix. The configured
// prefix (config.ISSUE_PREFIX, defaulting to the tracker team key) flows in via
// Scope.Prefix; sentinel/prompt builders that hold a concrete id derive the prefix
// from it directly with prefixOf.
const DefaultPrefix = "COD"

// prefixOf returns the identifier prefix of a ticket id — everything before the
// final "-<digits>" group (COD-123 → COD, TMS-9 → TMS). It falls back to
// DefaultPrefix when id carries no recognisable prefix.
func prefixOf(id string) string {
	for i := len(id) - 1; i > 0; i-- {
		if id[i] == '-' {
			if p := id[:i]; p != "" {
				return p
			}
			break
		}
	}
	return DefaultPrefix
}

// Config is the provider-agnostic configuration a Tracker implementation needs.
type Config struct {
	Team            string
	Project         string
	ReadyLabel      string
	QuarantineLabel string
	APIKey          string // Linear API key; enables direct GraphQL calls
}

// SubIssue is a lightweight identifier+title pair for an issue's children. Done
// marks a child the tracker already considers finished (completed or canceled),
// so the epic preview can flag work that will not run.
type SubIssue struct {
	ID    string
	Title string
	Done  bool
}

// ListedTicket is one eligible ticket returned by a fast list operation.
type ListedTicket struct {
	ID    string
	Title string
	State string
}

// TicketLister is the optional capability of enumerating eligible tickets
// directly through the PM tool's API (Linear GraphQL). Tracker providers that
// cannot list quickly return ErrNotImplemented or an unsupported error.
type TicketLister interface {
	ListEligible(ctx context.Context, scope Scope) ([]ListedTicket, error)
}

// Tracker is the project-management backend used by the loop. All methods run
// through an agent, so token lines bucket to runs/<ID>/.
type Tracker interface {
	// Pick returns the next eligible ticket identifier, or "" when nothing is
	// eligible (the agent answered PICK=NONE).
	Pick(ctx context.Context, scope Scope) (string, error)

	// SubIssues asks for the direct children of issue id.
	SubIssues(ctx context.Context, id string) ([]SubIssue, error)

	// Title returns the human-readable title of issue id.
	Title(ctx context.Context, id string) (string, error)

	// SetStatus moves a ticket to a workflow status (e.g. "In Review", "Done").
	SetStatus(ctx context.Context, id, status, extra string) error

	// Reset returns a ticket to an unstarted/ready state so the picker can
	// re-select it.
	Reset(ctx context.Context, id string) error

	// Quarantine marks a ticket unrecoverable.
	Quarantine(ctx context.Context, id, reason string) error

	// FileBug files a new tracker issue as a last-resort HITL blocker when the
	// loop's repair and bugfix phases could not resolve a QA failure.
	FileBug(ctx context.Context, id, verdictPath string) (string, error)

	// EnsureLabels creates the ready and quarantine labels if they do not exist.
	EnsureLabels(ctx context.Context) error
}

// IssueStatus is the normalized lifecycle bucket of a tracker issue, used by
// --status to reconcile stale local checkpoints. Each tracker maps its native
// workflow states onto these.
type IssueStatus string

const (
	// StatusOpen means the issue is still live (backlog/unstarted/started/in-review).
	StatusOpen IssueStatus = "open"
	// StatusDone means the issue reached a completed/shipped state.
	StatusDone IssueStatus = "done"
	// StatusCanceled means the issue was canceled / won't-do.
	StatusCanceled IssueStatus = "canceled"
	// StatusUnknown means the status could not be determined; reconciliation must
	// treat it as "leave intact" rather than risk clearing live work.
	StatusUnknown IssueStatus = "unknown"
)

// Terminal reports whether the status means the tracker considers the issue
// finished (Done or Canceled) — the trigger for clearing a stale local checkpoint.
func (s IssueStatus) Terminal() bool { return s == StatusDone || s == StatusCanceled }

// IssueStatuser is the optional capability of reporting a single issue's
// normalized lifecycle status. --status uses it to reconcile stale local
// checkpoints against the tracker. Trackers that cannot answer skip reconcile
// (callers type-assert and fall back to leaving checkpoints intact).
type IssueStatuser interface {
	IssueStatus(ctx context.Context, id string) (IssueStatus, error)
}

// IssueProjecter is the optional capability of reporting the name of the Linear
// project an issue belongs to. The ownership guard uses it to refuse a run on a
// ticket from a different project than the repo owns. A tracker that cannot answer
// (or returns "") makes the guard a no-op — uncertainty never blocks a run, only a
// confirmed mismatch does.
type IssueProjecter interface {
	IssueProject(ctx context.Context, id string) (string, error)
}

// Team is a selectable project-management container — a Linear team or a Jira
// project — that the onboarding wizard can list and let the user pick.
type Team struct {
	Key  string `json:"key"`  // stored in config (e.g. "ENG", "PROJ")
	Name string `json:"name"` // human-readable display name
}

// TeamLister is the optional capability of enumerating selectable containers
// through the PM tool. Linear and Jira implement it; GitHub does not — its
// repository is detected locally from the git remote, not listed via an agent.
type TeamLister interface {
	ListTeams(ctx context.Context) ([]Team, error)
}

// Scope selects which issues the picker considers: sub-issues of a parent, or
// every issue in a team/project.
type Scope struct {
	Parent string
	Team   string
	// Project, when set, restricts a whole-team pick to issues in that Linear
	// project (config.PROJECT — the project this repo owns). Empty means no
	// project filter, preserving the team-wide pick.
	Project string
	// Prefix is the configured issue-identifier prefix (config.ISSUE_PREFIX). It
	// is consulted for whole-team picks, where there is no parent id to derive a
	// prefix from. Empty falls back to DefaultPrefix.
	Prefix string
}

func (s Scope) clause() string {
	if s.Parent != "" {
		return "sub-issues of " + s.Parent
	}
	return "issues in the " + s.Team + " team/project"
}

// projectClause renders an optional " They must belong to the Linear project '<X>'."
// fragment spliced into the MCP pick/list prompts, or "" when no project is scoped.
func (s Scope) projectClause() string {
	if p := strings.TrimSpace(s.Project); p != "" {
		return " They must belong to the Linear project '" + p + "'."
	}
	return ""
}

func (s Scope) prefix() string {
	if s.Parent != "" {
		return prefixOf(s.Parent)
	}
	if p := strings.ToUpper(strings.TrimSpace(s.Prefix)); p != "" {
		return p
	}
	return DefaultPrefix
}

// New creates a Tracker for the named provider.
func New(provider string, runner agent.Runner, cfg Config) (Tracker, error) {
	switch provider {
	case "linear":
		return &Linear{
			Runner:          runner,
			Team:            cfg.Team,
			Project:         cfg.Project,
			ReadyLabel:      cfg.ReadyLabel,
			QuarantineLabel: cfg.QuarantineLabel,
			APIKey:          cfg.APIKey,
		}, nil
	case "jira":
		return &Jira{Runner: runner, Team: cfg.Team, Project: cfg.Project, ReadyLabel: cfg.ReadyLabel, QuarantineLabel: cfg.QuarantineLabel}, nil
	case "github":
		return &GitHub{Runner: runner, Repo: cfg.Team, ReadyLabel: cfg.ReadyLabel, QuarantineLabel: cfg.QuarantineLabel}, nil
	default:
		return nil, fmt.Errorf("unknown tracker provider %q (expected: linear | jira | github)", provider)
	}
}

// parseTeams recovers the team/project list from an agent response. It accepts
// the 'TEAMS=[...]' sentinel (last occurrence wins, with 'TEAMS=NONE' meaning an
// empty list) or, failing that, a bare JSON array anywhere in the text — the
// same tolerance the PICK/TITLE parsers apply.
func parseTeams(text string) ([]Team, bool) {
	if idx := strings.LastIndex(text, "TEAMS="); idx >= 0 {
		rest := strings.TrimSpace(text[idx+len("TEAMS="):])
		if strings.HasPrefix(rest, "NONE") {
			return []Team{}, true
		}
		if teams, ok := parseTeamsJSON(rest); ok {
			return teams, true
		}
	}
	return parseTeamsJSON(text)
}

func parseTeamsJSON(s string) ([]Team, bool) {
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end < start {
		return nil, false
	}
	var teams []Team
	if err := json.Unmarshal([]byte(s[start:end+1]), &teams); err != nil {
		return nil, false
	}
	seen := map[string]bool{}
	out := make([]Team, 0, len(teams))
	for _, t := range teams {
		key := strings.TrimSpace(t.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = key
		}
		out = append(out, Team{Key: key, Name: name})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
