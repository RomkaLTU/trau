package tracker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
)

// Jira picks tickets through the Jira MCP via an agent. When JIRA_API_TOKEN (plus
// base URL and email) is configured it uses Jira's REST API directly for fast
// read/write operations, falling back to the Jira (Rovo) MCP otherwise.
type Jira struct {
	Runner          agent.Runner
	ReadyLabel      string
	QuarantineLabel string
	SplitLabel      string
	Team            string // Jira project key
	Project         string // optional project name/id for issue creation
	BaseURL         string // Jira site base URL, e.g. https://acme.atlassian.net
	Email           string // Atlassian account email (Basic-auth username)
	APIToken        string // classic Jira API token (Basic-auth password)
}

func (j *Jira) api() *jiraapi.Client {
	return jiraapi.New(j.BaseURL, j.Email, j.APIToken)
}

// jiraShouldFallback reports whether a direct-API error should cause the caller
// to retry the operation through the Rovo MCP. Unlike Linear, a Jira 401 IS
// fallback-worthy: the REST token and the Rovo MCP authenticate as independent
// Atlassian identities, so a missing or expired per-repo token can still be
// served by a working MCP session. Any other error (not-found, transient) is
// surfaced — the MCP would not do better.
func jiraShouldFallback(err error) bool {
	return errors.Is(err, jiraapi.ErrNotEnabled) || errors.Is(err, jiraapi.ErrUnauthorized)
}

// Pick returns the next eligible ticket identifier, or "" when nothing is eligible.
func (j *Jira) Pick(ctx context.Context, scope Scope) (string, error) {
	if scope.Parent != "" {
		leaves, err := j.leafSubIssues(ctx, scope.Parent)
		if err != nil {
			return "", fmt.Errorf("pick %s: list children: %w", scope.Parent, err)
		}
		if len(leaves) == 0 {
			return "", nil
		}
		res, err := j.Runner.Run(ctx, j.epicPickPrompt(scope, leaves), "pick")
		if id, matched := parsePick(res.Final, scope.prefix()); matched && leaves[id] {
			return id, nil
		}
		if err != nil {
			return "", err
		}
		return "", nil
	}

	res, err := j.Runner.Run(ctx, j.pickPrompt(scope), "pick")
	if id, matched := parsePick(res.Final, scope.prefix()); matched {
		return id, nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

func (j *Jira) leafSubIssues(ctx context.Context, parent string) (map[string]bool, error) {
	subs, err := j.SubIssues(ctx, parent)
	if err != nil {
		return nil, err
	}
	leaves := make(map[string]bool, len(subs))
	for _, s := range subs {
		if s.ID != "" && !s.HasChildren {
			leaves[s.ID] = true
		}
	}
	return leaves, nil
}

func (j *Jira) pickPrompt(scope Scope) string {
	project := j.Team
	if project == "" {
		project = scope.Team
	}
	return fmt.Sprintf("Use the Jira (Rovo) MCP. In project %q, find issues that ALL of: "+
		"(a) carry the label '%s'; "+
		"(b) are NOT started — status is 'To Do', 'Backlog', or 'Open' (exclude In Progress, Done, Closed, Canceled); "+
		"(c) have every linked blocker issue in a Done/Closed state. "+
		"Among %s, pick the best one to start next by considering, in order: priority (Highest > High > Medium > Low > Lowest), due date (sooner is better), then the lowest issue key number as a tie-breaker. "+
		"Respond with exactly one final line: 'PICK=<IDENTIFIER>' (e.g. PICK=%s-414) or 'PICK=NONE'. No other output.",
		project, j.ReadyLabel, scope.clause(), scope.prefix())
}

func (j *Jira) epicPickPrompt(scope Scope, leaves map[string]bool) string {
	ids := make([]string, 0, len(leaves))
	for id := range leaves {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Among the leaf sub-tasks of %s (%s), find one that ALL of: "+
		"(a) carries the label '%s'; "+
		"(b) is NOT started — status is 'To Do', 'Backlog', or 'Open' (exclude In Progress, Done, Closed, Canceled); "+
		"(c) has every linked blocker issue in a Done/Closed state. "+
		"Pick the best one to start next by considering, in order: priority (Highest > High > Medium > Low > Lowest), due date (sooner is better), then the lowest issue key number as a tie-breaker. "+
		"Respond with exactly one final line: 'PICK=<IDENTIFIER>' (e.g. PICK=%s-414) or 'PICK=NONE'. No other output.",
		scope.Parent, strings.Join(ids, ", "), j.ReadyLabel, scope.prefix())
}

// ListTeams enumerates the Jira projects the user can access — Jira's analogue
// of a Linear team — via the Jira (Rovo) MCP, recovered from the TEAMS=
// sentinel. A runner error with no parseable list is surfaced so onboarding can
// fall back to manual entry. Labeled "list_teams".
func (j *Jira) ListTeams(ctx context.Context) ([]Team, error) {
	res, err := j.Runner.Run(ctx, j.listTeamsPrompt(), "list_teams")
	if teams, ok := parseTeams(res.Final); ok {
		return teams, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("could not parse Jira projects")
}

func (j *Jira) listTeamsPrompt() string {
	return "Use the Atlassian/Jira (Rovo) MCP. List the Jira projects I have access to. " +
		"Respond with exactly one final line of JSON: TEAMS=[{\"key\":\"<project key>\",\"name\":\"<project name>\"}, ...] " +
		"using each project's key (e.g. PROJ) and name. If there are none, respond TEAMS=NONE. No other output."
}

// SubIssues asks the Jira MCP for the direct sub-tasks of issue id.
func (j *Jira) SubIssues(ctx context.Context, id string) ([]SubIssue, error) {
	res, err := j.Runner.Run(ctx, j.subIssuesPrompt(id), "sub_issues")
	if subs, matched := parseSubIssues(res.Final); matched {
		return subs, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("could not parse sub-issues for %s", id)
}

func (j *Jira) subIssuesPrompt(id string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. List the direct sub-tasks (children) of issue %s. "+
		"Respond with exactly one final line of JSON: SUB_ISSUES=[{\"id\":\"%s-414\",\"title\":\"...\",\"hasChildren\":false}, ...] "+
		"using each child's identifier, title, and whether it has its own sub-tasks (hasChildren boolean). "+
		"If there are none, respond SUB_ISSUES=[]. No other output.", id, prefixOf(id))
}

// Title returns the summary of issue id via the Jira REST API when a token is
// configured, otherwise (or on an auth error) via the Jira MCP.
func (j *Jira) Title(ctx context.Context, id string) (string, error) {
	if title, err := j.titleAPI(ctx, id); err == nil {
		return title, nil
	} else if !jiraShouldFallback(err) {
		return "", err
	}

	res, err := j.Runner.Run(ctx, j.titlePrompt(id), "title")
	if t, ok := parseTitle(res.Final); ok {
		return t, nil
	}
	return "", err
}

func (j *Jira) titleAPI(ctx context.Context, id string) (string, error) {
	issue, err := j.api().Issue(ctx, id)
	if err != nil {
		return "", err
	}
	return issue.Summary, nil
}

func (j *Jira) titlePrompt(id string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Get the summary (title) of issue %s. "+
		"Respond with exactly one final line: 'TITLE=<the issue summary>'. No other output.", id)
}

// IssueStatus reports whether issue id is still open or has reached a terminal
// Jira status. It is used by epic finalization and stale-checkpoint reconcile.
func (j *Jira) IssueStatus(ctx context.Context, id string) (IssueStatus, error) {
	res, err := j.Runner.Run(ctx, j.issueStatusPrompt(id), "status")
	if st, ok := parseIssueStatus(res.Final); ok {
		return st, nil
	}
	if err != nil {
		return StatusUnknown, err
	}
	return StatusUnknown, fmt.Errorf("could not parse status for %s", id)
}

func (j *Jira) issueStatusPrompt(id string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Look up issue %s and report its workflow state. "+
		"Respond with exactly one final line: 'STATUS=<done|canceled|open>' — "+
		"'done' if it is Done/Closed/completed, 'canceled' if Canceled/won't-do/duplicate, otherwise 'open'. No other output.", id)
}

// SetStatus transitions issue id to the named Jira status.
func (j *Jira) SetStatus(ctx context.Context, id, status, extra string) error {
	_, err := j.Runner.Run(ctx, j.setStatusPrompt(id, status, extra), "status")
	return err
}

func (j *Jira) setStatusPrompt(id, status, extra string) string {
	prompt := fmt.Sprintf("Use the Jira (Rovo) MCP to transition issue %s to the status %q.", id, status)
	if extra != "" {
		prompt += " " + extra
	}
	return prompt + " Reply DONE."
}

// Reset returns a ticket to a ready/unstarted state.
func (j *Jira) Reset(ctx context.Context, id string) error {
	extra := fmt.Sprintf("Remove the label '%s' if present and ensure '%s' is present so the loop can re-pick it; "+
		"transition the issue to status 'To Do' or 'Backlog'; "+
		"add a comment: \"Trau loop reset %s to start fresh.\"", j.QuarantineLabel, j.ReadyLabel, id)
	return j.SetStatus(ctx, id, "To Do", extra)
}

// Quarantine marks a ticket unrecoverable.
func (j *Jira) Quarantine(ctx context.Context, id, reason string) error {
	_, err := j.Runner.Run(ctx, j.quarantinePrompt(id, reason), "quarantine")
	return err
}

func (j *Jira) quarantinePrompt(id, reason string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP on issue %s: remove the label '%s', add the label '%s', and add a comment: \"Trau loop stopped: %s (see runs/%s/).\" Reply DONE.",
		id, j.ReadyLabel, j.QuarantineLabel, reason, id)
}

// FileBug files a NEW Jira issue as a last-resort HITL blocker for a QA failure
// the slice could not self-heal, even after comprehensive bugfix passes.
func (j *Jira) FileBug(ctx context.Context, id, verdictPath string) (string, error) {
	res, err := j.Runner.Run(ctx, j.fileBugPrompt(id, verdictPath), "file_bug")
	if bug, ok := parseBug(res.Final, prefixOf(id)); ok {
		return bug, nil
	}
	return "", err
}

func (j *Jira) fileBugPrompt(id, verdictPath string) string {
	project := j.Team
	if j.Project != "" {
		project = j.Project
	}
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Read the QA verdict at %s. Create a NEW issue of type 'Bug' in project %q, labelled 'HITL', describing the failure that blocked %s's QA after automated repair and bugfix passes — a concise summary plus a description with the verdict summary and the specific failures, noting it was surfaced by the Trau loop while working on %s and needs human attention. Output exactly one final line: BUG=<IDENTIFIER> (e.g. BUG=%s-500).",
		verdictPath, project, id, id, prefixOf(id))
}

// EnsureLabels creates the ready and quarantine labels in Jira if they do not exist.
func (j *Jira) EnsureLabels(ctx context.Context) error {
	_, err := j.Runner.Run(ctx, j.ensureLabelsPrompt(), "ensure_labels")
	return err
}

func (j *Jira) ensureLabelsPrompt() string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Ensure these issue labels exist: %s. "+
		"Create them if missing. Reply DONE.", quoteLabels(managedLabelList(j.ReadyLabel, j.QuarantineLabel, j.SplitLabel)))
}
