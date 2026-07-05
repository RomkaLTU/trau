package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// jiraShouldFallback reports whether a direct-API error is the kind the Rovo MCP
// could still serve: a disabled (no-token) or unauthorized client. Any other
// error (not-found, transient) is surfaced — the MCP would not do better.
func jiraShouldFallback(err error) bool {
	return errors.Is(err, jiraapi.ErrNotEnabled) || errors.Is(err, jiraapi.ErrUnauthorized)
}

// canFallback reports whether a direct-API error should be retried through the
// Rovo MCP. It gates jiraShouldFallback on having a runner at all: when the
// tracker is built rest-only — a full set of per-repo REST credentials, so no
// runner — the API is the sole Atlassian identity and its errors are surfaced,
// never masked by the shared Rovo MCP, which authenticates as a different
// account. This is the same rule onboarding detection applies, extended to every
// tracker operation so a rest-only loop never silently switches identity.
func (j *Jira) canFallback(err error) bool {
	return j.Runner != nil && jiraShouldFallback(err)
}

// Pick returns the next eligible ticket identifier, or "" when nothing is
// eligible. A whole-project pick uses the REST /search/jql path when a token is
// configured, falling back to the Rovo MCP on an auth/not-enabled error. Epic
// scope (a parent id) is restricted to the epic's confirmed leaves and likewise
// prefers REST, falling back to the MCP only when a runner is available.
func (j *Jira) Pick(ctx context.Context, scope Scope) (string, error) {
	if scope.Parent == "" {
		if id, err := j.pickAPI(ctx, scope); err == nil {
			return id, nil
		} else if !j.canFallback(err) {
			return "", err
		}
	} else {
		leaves, err := j.leafSubIssues(ctx, scope.Parent)
		if err != nil {
			return "", fmt.Errorf("pick %s: list children: %w", scope.Parent, err)
		}
		if len(leaves) == 0 {
			return "", nil
		}
		if id, err := j.pickEpicAPI(ctx, scope, leaves); err == nil {
			return id, nil
		} else if !j.canFallback(err) {
			return "", err
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

// pickAPI selects the highest-ranked eligible ticket via /search/jql. The query
// already filters by project, ready label, unstarted status and unresolved state
// and orders by the loop's rules; this applies the remaining policy the JQL can't
// express: skip epics (containers), skip tickets with an unresolved blocker, and
// keep only the configured key prefix.
func (j *Jira) pickAPI(ctx context.Context, scope Scope) (string, error) {
	project := j.pickProject(scope)
	if project == "" {
		return "", jiraapi.ErrNotEnabled
	}
	candidates, err := j.api().Eligible(ctx, project, j.ReadyLabel)
	if err != nil {
		return "", err
	}
	prefix := scope.prefix()
	for _, c := range candidates {
		if c.IsEpic {
			continue
		}
		if !allBlockersResolved(c.BlockedBy) {
			continue
		}
		if !strings.HasPrefix(c.Key, prefix+"-") {
			continue
		}
		return c.Key, nil
	}
	return "", nil
}

// pickEpicAPI selects the highest-ranked eligible leaf sub-issue of an epic via
// /search/jql. It runs the same project-wide eligibility query as pickAPI —
// ready label, unstarted, unresolved, ranked — and keeps only candidates in the
// epic's confirmed leaf set, skipping epics and unresolved-blocker tickets. This
// is the REST equivalent of the epic-scoped MCP pick, so a rest-only Jira
// identity can work an epic queue without falling back to Rovo.
func (j *Jira) pickEpicAPI(ctx context.Context, scope Scope, leaves map[string]bool) (string, error) {
	project := j.pickProject(scope)
	if project == "" {
		return "", jiraapi.ErrNotEnabled
	}
	candidates, err := j.api().Eligible(ctx, project, j.ReadyLabel)
	if err != nil {
		return "", err
	}
	for _, c := range candidates {
		if c.IsEpic || !leaves[c.Key] {
			continue
		}
		if !allBlockersResolved(c.BlockedBy) {
			continue
		}
		return c.Key, nil
	}
	return "", nil
}

// pickProject returns the Jira project key to search: the configured project key,
// falling back to the scope's team when the field is unset.
func (j *Jira) pickProject(scope Scope) string {
	if p := strings.TrimSpace(j.Team); p != "" {
		return p
	}
	return strings.TrimSpace(scope.Team)
}

// allBlockersResolved reports whether every "is blocked by" link on a candidate is
// resolved. Jira JQL has no native way to test this, so it is enforced client-side
// over the candidate's issuelinks.
func allBlockersResolved(blockers []jiraapi.Blocker) bool {
	for _, b := range blockers {
		if !b.Resolved {
			return false
		}
	}
	return true
}

// ListEligible enumerates the tickets the loop could pick next. It uses the REST
// /search/jql path when a token is configured, otherwise the Rovo MCP. Unlike
// Pick it keeps epics in the list — the caller decides what to do with them.
func (j *Jira) ListEligible(ctx context.Context, scope Scope) ([]ListedTicket, error) {
	if list, err := j.listEligibleAPI(ctx, scope); err == nil {
		return list, nil
	} else if !j.canFallback(err) {
		return nil, err
	}

	res, err := j.Runner.Run(ctx, j.listEligiblePrompt(scope), "list_eligible")
	if list, matched := parseEligible(res.Final); matched {
		return list, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("could not parse eligible ticket list")
}

func (j *Jira) listEligibleAPI(ctx context.Context, scope Scope) ([]ListedTicket, error) {
	if scope.Parent != "" {
		return nil, jiraapi.ErrNotEnabled
	}
	project := j.pickProject(scope)
	if project == "" {
		return nil, jiraapi.ErrNotEnabled
	}
	candidates, err := j.api().Eligible(ctx, project, j.ReadyLabel)
	if err != nil {
		return nil, err
	}
	prefix := scope.prefix()
	out := make([]ListedTicket, 0, len(candidates))
	for _, c := range candidates {
		if !allBlockersResolved(c.BlockedBy) {
			continue
		}
		if !strings.HasPrefix(c.Key, prefix+"-") {
			continue
		}
		out = append(out, ListedTicket{ID: c.Key, Title: c.Summary, State: c.StatusName, Labels: c.Labels})
	}
	return out, nil
}

func (j *Jira) listEligiblePrompt(scope Scope) string {
	pfx := scope.prefix()
	return fmt.Sprintf("Use the Jira (Rovo) MCP. List eligible issues in project %q that carry the label '%s', "+
		"are unstarted (status category To Do — not In Progress, Done or Closed), have every 'is blocked by' issue resolved (Done/Closed), and match key prefix %s-. "+
		"Respond with exactly one final line of JSON: ELIGIBLE=[{\"id\":\"%s-123\",\"title\":\"...\",\"labels\":[\"label-a\",\"label-b\"]}, ...] "+
		"or ELIGIBLE=[]. No other output.",
		j.pickProject(scope), j.ReadyLabel, pfx, pfx)
}

func (j *Jira) leafSubIssues(ctx context.Context, parent string) (map[string]bool, error) {
	subs, err := j.SubIssues(ctx, parent)
	if err != nil {
		return nil, err
	}
	leaves := make(map[string]bool, len(subs))
	for _, s := range subs {
		if s.ID != "" && !s.HasChildren && !s.Done {
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
// of a Linear team. It uses the REST /project/search path when a token is
// configured, falling back to the Rovo MCP (TEAMS= sentinel) on an
// auth/not-enabled error. A runner error with no parseable list is surfaced so
// onboarding can fall back to manual entry.
func (j *Jira) ListTeams(ctx context.Context) ([]Team, error) {
	teams, apiErr := j.listTeamsAPI(ctx)
	if apiErr == nil {
		return teams, nil
	}
	if !j.canFallback(apiErr) {
		return nil, apiErr
	}

	res, err := j.Runner.Run(ctx, j.listTeamsPrompt(), "list_teams")
	if teams, ok := parseTeams(res.Final); ok {
		return teams, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("could not parse Jira projects")
}

func (j *Jira) listTeamsAPI(ctx context.Context) ([]Team, error) {
	projects, err := j.api().ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Team, 0, len(projects))
	for _, p := range projects {
		if p.Key == "" {
			continue
		}
		out = append(out, Team{Key: p.Key, Name: p.Name})
	}
	return out, nil
}

func (j *Jira) listTeamsPrompt() string {
	return "Use the Atlassian/Jira (Rovo) MCP. List the Jira projects I have access to. " +
		"Respond with exactly one final line of JSON: TEAMS=[{\"key\":\"<project key>\",\"name\":\"<project name>\"}, ...] " +
		"using each project's key (e.g. PROJ) and name. If there are none, respond TEAMS=NONE. No other output."
}

// SubIssues returns the direct children of issue id — sub-tasks and epic-children
// alike, via the unified parent field. It uses the REST /search/jql path when a
// token is configured, otherwise the Jira (Rovo) MCP.
func (j *Jira) SubIssues(ctx context.Context, id string) ([]SubIssue, error) {
	if subs, err := j.subIssuesAPI(ctx, id); err == nil {
		return subs, nil
	} else if !j.canFallback(err) {
		return nil, err
	}

	res, err := j.Runner.Run(ctx, j.subIssuesPrompt(id), "sub_issues")
	if subs, matched := parseSubIssues(res.Final); matched {
		return subs, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("could not parse sub-issues for %s", id)
}

func (j *Jira) subIssuesAPI(ctx context.Context, id string) ([]SubIssue, error) {
	children, err := j.api().SubIssues(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]SubIssue, 0, len(children))
	for _, ch := range children {
		if ch.Key == "" {
			continue
		}
		out = append(out, SubIssue{ID: ch.Key, Title: ch.Summary, Done: ch.Done, HasChildren: ch.HasChildren})
	}
	return out, nil
}

func (j *Jira) subIssuesPrompt(id string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. List the direct sub-tasks (children) of issue %s. "+
		"Respond with exactly one final line of JSON: SUB_ISSUES=[{\"id\":\"%s-414\",\"title\":\"...\",\"hasChildren\":false,\"done\":false}, ...] "+
		"using each child's identifier, title, whether it has its own sub-tasks (hasChildren boolean), "+
		"and whether its status is Done/Closed/Canceled (done boolean). "+
		"If there are none, respond SUB_ISSUES=[]. No other output.", id, prefixOf(id))
}

// Title returns the summary of issue id via the Jira REST API when a token is
// configured, otherwise (or on an auth error) via the Jira MCP.
func (j *Jira) Title(ctx context.Context, id string) (string, error) {
	if title, err := j.titleAPI(ctx, id); err == nil {
		return title, nil
	} else if !j.canFallback(err) {
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
// Jira status, used by epic finalization and stale-checkpoint reconcile. It maps
// the issue's statusCategory (plus resolution) via the REST API, falling back to
// the Rovo MCP on an auth/not-enabled error.
func (j *Jira) IssueStatus(ctx context.Context, id string) (IssueStatus, error) {
	if st, err := j.issueStatusAPI(ctx, id); err == nil {
		return st, nil
	} else if !j.canFallback(err) {
		return StatusUnknown, err
	}

	res, err := j.Runner.Run(ctx, j.issueStatusPrompt(id), "status")
	if st, ok := parseIssueStatus(res.Final); ok {
		return st, nil
	}
	if err != nil {
		return StatusUnknown, err
	}
	return StatusUnknown, fmt.Errorf("could not parse status for %s", id)
}

func (j *Jira) issueStatusAPI(ctx context.Context, id string) (IssueStatus, error) {
	issue, err := j.api().Issue(ctx, id)
	if err != nil {
		return StatusUnknown, err
	}
	return mapJiraStatus(issue.Status.Category, issue.Resolution), nil
}

// mapJiraStatus maps a Jira statusCategory key onto the normalized status. Jira
// has no "canceled" category, so a done-category issue closed with a won't-do or
// duplicate resolution reports as canceled; an unrecognized category is unknown
// so reconcile leaves the checkpoint intact.
func mapJiraStatus(category, resolution string) IssueStatus {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "done":
		if isCanceledResolution(resolution) {
			return StatusCanceled
		}
		return StatusDone
	case "new", "indeterminate":
		return StatusOpen
	default:
		return StatusUnknown
	}
}

// isCanceledResolution reports whether a Jira resolution name denotes a
// won't-do/duplicate outcome (case-insensitive) rather than a completion.
func isCanceledResolution(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "won't do", "wont do", "won't fix", "wontfix",
		"cancelled", "canceled", "duplicate", "declined", "abandoned", "rejected":
		return true
	default:
		return false
	}
}

func (j *Jira) issueStatusPrompt(id string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Look up issue %s and report its workflow state. "+
		"Respond with exactly one final line: 'STATUS=<done|canceled|open>' — "+
		"'done' if it is Done/Closed/completed, 'canceled' if Canceled/won't-do/duplicate, otherwise 'open'. No other output.", id)
}

// IssueProject reports the key of the Jira project issue id belongs to, used by
// the ownership guard to refuse cross-project runs. It reads project.key — the
// canonical identifier that doubles as the configured project key — via the REST
// API, falling back to the Rovo MCP on an auth/not-enabled error. An empty result
// means "unknown", which the guard treats as "cannot enforce".
func (j *Jira) IssueProject(ctx context.Context, id string) (string, error) {
	if key, err := j.issueProjectAPI(ctx, id); err == nil {
		return key, nil
	} else if !j.canFallback(err) {
		return "", err
	}

	res, err := j.Runner.Run(ctx, j.issueProjectPrompt(id), "project")
	if key, ok := parseProject(res.Final); ok {
		return key, nil
	}
	return "", err
}

func (j *Jira) issueProjectAPI(ctx context.Context, id string) (string, error) {
	issue, err := j.api().Issue(ctx, id)
	if err != nil {
		return "", err
	}
	return issue.Project.Key, nil
}

func (j *Jira) issueProjectPrompt(id string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Look up issue %s and report the KEY of the Jira project it belongs to. "+
		"Respond with exactly one final line: 'PROJECT=<project key>' (or 'PROJECT=NONE' if it has none). No other output.", id)
}

// ParentIssue reports the key of id's immediate parent (the epic it belongs to),
// or "" when id is top-level. It reads the unified parent field — not the
// deprecated Epic Link custom field — via the REST API, falling back to the Rovo
// MCP on an auth/not-enabled error.
func (j *Jira) ParentIssue(ctx context.Context, id string) (string, error) {
	if parent, err := j.parentIssueAPI(ctx, id); err == nil {
		return parent, nil
	} else if !j.canFallback(err) {
		return "", err
	}

	res, err := j.Runner.Run(ctx, j.parentIssuePrompt(id), "parent")
	if parent, ok := parseParent(res.Final); ok {
		return parent, nil
	}
	return "", err
}

func (j *Jira) parentIssueAPI(ctx context.Context, id string) (string, error) {
	issue, err := j.api().Issue(ctx, id)
	if err != nil {
		return "", err
	}
	return issue.Parent, nil
}

func (j *Jira) parentIssuePrompt(id string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Look up issue %s and report the KEY of its parent issue (the epic it belongs to). "+
		"Respond with exactly one final line: 'PARENT=<key>' (or 'PARENT=NONE' if it has no parent). No other output.", id)
}

// CreateIssue creates a new issue through the hierarchical-create capability: an
// Epic when the spec has no parent, otherwise a Task nested under the parent via
// the unified parent field set at creation time. The configured team key doubles
// as the Jira project key, so the spec's project is ignored — a Jira issue's
// project is fixed by that key. Like IssueDetail it is API-only: a multi-line PRD
// body does not survive a single-line MCP sentinel.
func (j *Jira) CreateIssue(ctx context.Context, spec IssueSpec) (string, error) {
	issueType := "Epic"
	parent := strings.TrimSpace(spec.Parent)
	if parent != "" {
		issueType = "Task"
	}
	return j.api().CreateIssue(ctx, strings.TrimSpace(j.Team), issueType, spec.Title, spec.Description, spec.Labels, parent)
}

// IssueDetail returns the title and full description of issue id for build-prompt
// context. Like Linear it is API-only: a multi-line ADF description cannot survive
// a single-line MCP sentinel, so an unconfigured or failing API leaves the pipeline
// to build without the injected context (a best-effort enrichment).
func (j *Jira) IssueDetail(ctx context.Context, id string) (IssueDetail, error) {
	issue, err := j.api().Issue(ctx, id)
	if err != nil {
		return IssueDetail{}, err
	}
	return IssueDetail{Title: issue.Summary, Description: issue.Description}, nil
}

// SetStatus transitions issue id to the named Jira status via the two-step REST
// transition flow when a token is configured — matching the target status name
// to a workflow transition and optionally attaching a comment — falling back to
// the Rovo MCP on an auth/not-enabled error. An unknown target status is
// surfaced, not sent to the MCP: the workflow simply has no transition to it.
func (j *Jira) SetStatus(ctx context.Context, id, status, extra string) error {
	if err := j.setStatusAPI(ctx, id, status, extra); err == nil {
		return nil
	} else if !j.canFallback(err) {
		return err
	}
	return j.setStatusMCP(ctx, id, status, extra)
}

func (j *Jira) setStatusAPI(ctx context.Context, id, status, extra string) error {
	return j.api().SetStatus(ctx, id, status, "", extra)
}

func (j *Jira) setStatusMCP(ctx context.Context, id, status, extra string) error {
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

// AddLabel adds one label to an issue without disturbing its other labels, via
// the incremental PUT /issue label add op when a token is configured, otherwise
// the Rovo MCP.
func (j *Jira) AddLabel(ctx context.Context, id, label string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil
	}
	if err := j.addLabelAPI(ctx, id, label); err == nil {
		return nil
	} else if !j.canFallback(err) {
		return err
	}

	_, err := j.Runner.Run(ctx, j.addLabelPrompt(id, label), "label")
	return err
}

func (j *Jira) addLabelAPI(ctx context.Context, id, label string) error {
	return j.api().UpdateLabels(ctx, id, []string{label}, nil)
}

func (j *Jira) addLabelPrompt(id, label string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP on issue %s: add the label '%s' (keep every other label). Reply DONE.", id, label)
}

// Reset returns a ticket to a ready/unstarted state so the picker re-selects it:
// it drops the quarantine label, ensures the ready label, transitions back to an
// unstarted status and comments. It uses the REST API when a token is configured,
// falling back to the Rovo MCP on an auth/not-enabled error.
func (j *Jira) Reset(ctx context.Context, id string) error {
	if err := j.resetAPI(ctx, id); err == nil {
		return nil
	} else if !j.canFallback(err) {
		return err
	}

	extra := fmt.Sprintf("Remove the label '%s' if present and ensure '%s' is present so the loop can re-pick it; "+
		"transition the issue to status 'To Do' or 'Backlog'; "+
		"add a comment: \"Trau loop reset %s to start fresh.\"", j.QuarantineLabel, j.ReadyLabel, id)
	return j.setStatusMCP(ctx, id, "To Do", extra)
}

func (j *Jira) resetAPI(ctx context.Context, id string) error {
	if err := j.api().UpdateLabels(ctx, id, []string{j.ReadyLabel}, []string{j.QuarantineLabel}); err != nil {
		return err
	}
	return j.api().SetStatus(ctx, id, "To Do", "", fmt.Sprintf("Trau loop reset %s to start fresh.", id))
}

// Quarantine marks a ticket unrecoverable: it drops the ready label, adds the
// quarantine label and comments with the reason. It uses the REST API when a
// token is configured, falling back to the Rovo MCP on an auth/not-enabled error.
func (j *Jira) Quarantine(ctx context.Context, id, reason string) error {
	if err := j.quarantineAPI(ctx, id, reason); err == nil {
		return nil
	} else if !j.canFallback(err) {
		return err
	}

	_, err := j.Runner.Run(ctx, j.quarantinePrompt(id, reason), "quarantine")
	return err
}

func (j *Jira) quarantineAPI(ctx context.Context, id, reason string) error {
	if err := j.api().UpdateLabels(ctx, id, []string{j.QuarantineLabel}, []string{j.ReadyLabel}); err != nil {
		return err
	}
	return j.api().AddComment(ctx, id, fmt.Sprintf("Trau loop stopped: %s (see runs/%s/).", reason, id))
}

func (j *Jira) quarantinePrompt(id, reason string) string {
	return fmt.Sprintf("Use the Jira (Rovo) MCP on issue %s: remove the label '%s', add the label '%s', and add a comment: \"Trau loop stopped: %s (see runs/%s/).\" Reply DONE.",
		id, j.ReadyLabel, j.QuarantineLabel, reason, id)
}

// FileBug files a NEW Jira issue as a last-resort HITL blocker for a QA failure
// the slice could not self-heal, even after comprehensive bugfix passes. It uses
// POST /issue when a token is configured — reading the verdict file to build an
// ADF description — falling back to the Rovo MCP on an auth/not-enabled error.
func (j *Jira) FileBug(ctx context.Context, id, verdictPath string) (string, error) {
	if bug, err := j.fileBugAPI(ctx, id, verdictPath); err == nil {
		return bug, nil
	} else if !j.canFallback(err) {
		return "", err
	}

	res, err := j.Runner.Run(ctx, j.fileBugPrompt(id, verdictPath), "file_bug")
	if bug, ok := parseBug(res.Final, prefixOf(id)); ok {
		return bug, nil
	}
	return "", err
}

func (j *Jira) fileBugAPI(ctx context.Context, id, verdictPath string) (string, error) {
	summary, description := bugContent(id, verdictPath)
	return j.api().CreateIssue(ctx, strings.TrimSpace(j.Team), "Bug", summary, description, []string{"HITL"}, "")
}

// qaVerdict is the subset of the QA verdict JSON FileBug reports on.
type qaVerdict struct {
	Summary  string   `json:"summary"`
	Failures []string `json:"failures"`
}

// bugContent renders the Jira Bug summary and description from the QA verdict at
// verdictPath. A missing or unparseable verdict still yields a filable bug that
// points at the run artifacts.
func bugContent(id, verdictPath string) (summary, description string) {
	summary = fmt.Sprintf("Trau QA blocked %s — human attention needed", id)
	var b strings.Builder
	fmt.Fprintf(&b, "Trau loop QA verification of %s failed after automated repair and bugfix passes and needs human attention.\n\n", id)
	if data, err := os.ReadFile(verdictPath); err == nil {
		var v qaVerdict
		if json.Unmarshal(data, &v) == nil {
			if s := strings.TrimSpace(v.Summary); s != "" {
				summary = truncateSummary(fmt.Sprintf("Trau QA blocked %s: %s", id, s))
				fmt.Fprintf(&b, "Summary: %s\n\n", s)
			}
			if len(v.Failures) > 0 {
				b.WriteString("Failures:\n")
				for _, f := range v.Failures {
					fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(f))
				}
				b.WriteString("\n")
			}
		}
	}
	fmt.Fprintf(&b, "See runs/%s/ for the full run artifacts.", id)
	return summary, b.String()
}

// truncateSummary keeps a Jira issue summary within the 255-character field limit.
func truncateSummary(s string) string {
	const max = 250
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func (j *Jira) fileBugPrompt(id, verdictPath string) string {
	project := j.Team
	if j.Project != "" {
		project = j.Project
	}
	return fmt.Sprintf("Use the Jira (Rovo) MCP. Read the QA verdict at %s. Create a NEW issue of type 'Bug' in project %q, labelled 'HITL', describing the failure that blocked %s's QA after automated repair and bugfix passes — a concise summary plus a description with the verdict summary and the specific failures, noting it was surfaced by the Trau loop while working on %s and needs human attention. Output exactly one final line: BUG=<IDENTIFIER> (e.g. BUG=%s-500).",
		verdictPath, project, id, id, prefixOf(id))
}

// EnsureLabels is a no-op on Jira: labels are freeform strings created implicitly
// on first use, so there is nothing to pre-create.
func (j *Jira) EnsureLabels(ctx context.Context) error {
	return nil
}
