package tracker

import (
	"context"
	"fmt"

	"github.com/RomkaLTU/trau/internal/agent"
)

// Jira picks tickets through the Jira MCP via an agent.
type Jira struct {
	Runner          agent.Runner
	ReadyLabel      string
	QuarantineLabel string
	Team            string // Jira project key
	Project         string // optional project name/id for issue creation
}

// Pick returns the next eligible ticket identifier, or "" when nothing is eligible.
func (j *Jira) Pick(ctx context.Context, scope Scope) (string, error) {
	res, err := j.Runner.Run(ctx, j.pickPrompt(scope), "pick")
	if id, matched := parsePick(res.Final, scope.prefix()); matched {
		return id, nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
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
		"Respond with exactly one final line of JSON: SUB_ISSUES=[{\"id\":\"%s-414\",\"title\":\"...\"}, ...] "+
		"using each child's identifier and title. If there are none, respond SUB_ISSUES=[]. No other output.", id, DefaultPrefix)
}

// Title returns the summary of issue id via the Jira MCP.
func (j *Jira) Title(ctx context.Context, id string) (string, error) {
	res, err := j.Runner.Run(ctx, j.titlePrompt(id), "title")
	if t, ok := parseTitle(res.Final); ok {
		return t, nil
	}
	return "", err
}

func (j *Jira) titlePrompt(id string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Get the summary (title) of issue %s. "+
		"Respond with exactly one final line: 'TITLE=<the issue summary>'. No other output.", id)
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
	if bug, ok := parseBug(res.Final); ok {
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
		verdictPath, project, id, id, DefaultPrefix)
}

// EnsureLabels creates the ready and quarantine labels in Jira if they do not exist.
func (j *Jira) EnsureLabels(ctx context.Context) error {
	_, err := j.Runner.Run(ctx, j.ensureLabelsPrompt(), "ensure_labels")
	return err
}

func (j *Jira) ensureLabelsPrompt() string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Ensure two issue labels exist: '%s' and '%s'. "+
		"Create them if missing. Reply DONE.", j.ReadyLabel, j.QuarantineLabel)
}
