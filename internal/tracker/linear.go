package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// Linear picks tickets through Linear. When LINEAR_API_KEY is configured it
// uses Linear's GraphQL API directly for fast read/write operations; otherwise
// it falls back to the Linear MCP via an agent. Complex operations that read
// files (FileBug) always use the MCP.
type Linear struct {
	Runner          agent.Runner
	ReadyLabel      string
	QuarantineLabel string
	Team            string
	Project         string
	APIKey          string
}

func (l *Linear) api() *linearapi.Client {
	return linearapi.New(l.APIKey)
}

// shouldFallback reports whether a direct-API error should cause the caller to
// retry the operation through the MCP. Auth errors are not fallback-worthy
// because retrying through MCP won't help; "not enabled" (no API key configured)
// IS fallback-worthy because the MCP path is exactly the intended alternative.
// Transient / mapping errors are also fallback-worthy.
func shouldFallback(err error) bool {
	if err == nil {
		return false
	}
	if err == linearapi.ErrUnauthorized {
		return false
	}
	return true
}

// Pick returns the next eligible ticket identifier, or "" when nothing is
// eligible (the agent answered PICK=NONE). It surfaces a runner error only when
// the agent produced no sentinel at all, so a genuine failure is visible rather
// than silently reported as "nothing eligible".
func (l *Linear) Pick(ctx context.Context, scope Scope) (string, error) {
	// Sub-issue selection (epic scope) is not mapped to the GraphQL API, so it always
	// goes through the MCP. Only the team-queue pick uses the fast API path.
	if scope.Parent == "" {
		if id, err := l.pickAPI(ctx, scope); err == nil {
			return id, nil
		} else if !shouldFallback(err) {
			return "", err
		}
	} else {
		// Epic scope: harden the pick so a nested epic (a sub-issue that itself has
		// children) is never selected as a leaf. We first enumerate the parent's
		// children and restrict the agent to confirmed leaves, then verify the
		// returned id is actually in that set.
		leaves, err := l.leafSubIssues(ctx, scope.Parent)
		if err != nil {
			return "", fmt.Errorf("pick %s: list children: %w", scope.Parent, err)
		}
		if len(leaves) == 0 {
			return "", nil
		}
		res, err := l.Runner.Run(ctx, l.epicPickPrompt(scope, leaves), "pick")
		if id, matched := parsePick(res.Final, scope.prefix()); matched && leaves[id] {
			return id, nil
		}
		if err != nil {
			return "", err
		}
		return "", nil
	}

	res, err := l.Runner.Run(ctx, l.pickPrompt(scope), "pick")
	if id, matched := parsePick(res.Final, scope.prefix()); matched {
		return id, nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

// leafSubIssues returns the set of direct children of parent that are themselves
// leaf issues (they have no sub-issues). An empty set means the parent has no
// buildable leaves right now.
func (l *Linear) leafSubIssues(ctx context.Context, parent string) (map[string]bool, error) {
	subs, err := l.SubIssues(ctx, parent)
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

func (l *Linear) pickAPI(ctx context.Context, scope Scope) (string, error) {
	if strings.TrimSpace(l.Team) == "" {
		return "", linearapi.ErrNotEnabled
	}
	team, err := l.api().TeamByKey(ctx, l.Team)
	if err != nil {
		return "", err
	}
	candidates, err := l.api().Pick(ctx, team.ID, l.ReadyLabel)
	if err != nil {
		return "", err
	}
	prefix := scope.prefix()
	for _, c := range candidates {
		if !c.State.IsUnstarted() {
			continue
		}
		if len(c.Children) > 0 {
			// Epics are containers, not buildable leaves — never pick one directly.
			continue
		}
		if !allBlockersCompleted(c.BlockedBy) {
			continue
		}
		if !strings.HasPrefix(c.Identifier, prefix+"-") {
			continue
		}
		if !inProject(c.Project.Name, scope.Project) {
			continue
		}
		return c.Identifier, nil
	}
	return "", nil
}

// inProject reports whether a candidate's project matches the scope's owned
// project. An empty scope project means "no filter" (every candidate matches),
// preserving the team-wide pick when PROJECT is unset.
func inProject(candidate, scopeProject string) bool {
	want := strings.TrimSpace(scopeProject)
	if want == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(candidate), want)
}

func allBlockersCompleted(refs []linearapi.IssueRef) bool {
	for _, r := range refs {
		if !r.State.IsCompleted() {
			return false
		}
	}
	return true
}

// ListEligible enumerates tickets the loop could pick next. It uses the
// GraphQL API when an API key is configured, otherwise the Linear MCP.
func (l *Linear) ListEligible(ctx context.Context, scope Scope) ([]ListedTicket, error) {
	if list, err := l.listEligibleAPI(ctx, scope); err == nil {
		return list, nil
	} else if !shouldFallback(err) {
		return nil, err
	}

	res, err := l.Runner.Run(ctx, l.listEligiblePrompt(scope), "list_eligible")
	if list, matched := parseEligible(res.Final); matched {
		return list, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("could not parse eligible ticket list")
}

func (l *Linear) listEligibleAPI(ctx context.Context, scope Scope) ([]ListedTicket, error) {
	if scope.Parent != "" {
		return nil, linearapi.ErrNotEnabled
	}
	if strings.TrimSpace(l.Team) == "" {
		return nil, linearapi.ErrNotEnabled
	}
	team, err := l.api().TeamByKey(ctx, l.Team)
	if err != nil {
		return nil, err
	}
	candidates, err := l.api().Pick(ctx, team.ID, l.ReadyLabel)
	if err != nil {
		return nil, err
	}
	prefix := scope.prefix()
	out := make([]ListedTicket, 0, len(candidates))
	for _, c := range candidates {
		if !c.State.IsUnstarted() {
			continue
		}
		if !allBlockersCompleted(c.BlockedBy) {
			continue
		}
		if !strings.HasPrefix(c.Identifier, prefix+"-") {
			continue
		}
		if !inProject(c.Project.Name, scope.Project) {
			continue
		}
		out = append(out, ListedTicket{ID: c.Identifier, Title: c.Title, State: c.State.Name})
	}
	return out, nil
}

func (l *Linear) listEligiblePrompt(scope Scope) string {
	pfx := scope.prefix()
	return fmt.Sprintf("Use the Linear MCP. List eligible issues in %s that carry the label '%s', "+
		"are unstarted, have all 'blocked by' issues completed, and match prefix %s-.%s "+
		"Respond with exactly one final line of JSON: ELIGIBLE=[{\"id\":\"%s-123\",\"title\":\"...\"}, ...] "+
		"or ELIGIBLE=[]. No other output.",
		scope.clause(), l.ReadyLabel, pfx, scope.projectClause(), pfx)
}

func parseEligible(text string) ([]ListedTicket, bool) {
	if idx := strings.LastIndex(text, "ELIGIBLE="); idx >= 0 {
		text = text[idx+len("ELIGIBLE="):]
	}
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end < start {
		return nil, false
	}
	var list []ListedTicket
	if err := json.Unmarshal([]byte(text[start:end+1]), &list); err != nil {
		return nil, false
	}
	out := make([]ListedTicket, 0, len(list))
	for _, t := range list {
		if t.ID != "" {
			out = append(out, t)
		}
	}
	return out, true
}

// ListTeams enumerates the Linear teams the user can access. It uses the
// GraphQL API when an API key is configured, otherwise the Linear MCP.
func (l *Linear) ListTeams(ctx context.Context) ([]Team, error) {
	if teams, err := l.listTeamsAPI(ctx); err == nil {
		return teams, nil
	} else if !shouldFallback(err) {
		return nil, err
	}

	res, err := l.Runner.Run(ctx, l.listTeamsPrompt(), "list_teams")
	if teams, ok := parseTeams(res.Final); ok {
		return teams, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("could not parse Linear teams")
}

func (l *Linear) listTeamsAPI(ctx context.Context) ([]Team, error) {
	apiTeams, err := l.api().ListTeams(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Team, 0, len(apiTeams))
	for _, t := range apiTeams {
		out = append(out, Team{Key: t.Key, Name: t.Name})
	}
	return out, nil
}

func (l *Linear) listTeamsPrompt() string {
	return "Use the Linear MCP. List all teams I have access to. " +
		"Respond with exactly one final line of JSON: TEAMS=[{\"key\":\"<team key>\",\"name\":\"<team name>\"}, ...] " +
		"using each team's key (e.g. ENG) and display name. If there are none, respond TEAMS=NONE. No other output."
}

// SubIssues asks for the direct sub-issues of issue id.
// It returns a slice of SubIssue values or an empty slice when the agent reports
// none. A missing or malformed result is treated as an error so the caller does
// not silently assume a ticket is standalone.
func (l *Linear) SubIssues(ctx context.Context, id string) ([]SubIssue, error) {
	if subs, err := l.subIssuesAPI(ctx, id); err == nil {
		return subs, nil
	} else if !shouldFallback(err) {
		return nil, err
	}

	res, err := l.Runner.Run(ctx, l.subIssuesPrompt(id), "sub_issues")
	if subs, matched := parseSubIssues(res.Final); matched {
		return subs, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("could not parse sub-issues for %s", id)
}

func (l *Linear) subIssuesAPI(ctx context.Context, id string) ([]SubIssue, error) {
	issue, err := l.api().Issue(ctx, id)
	if err != nil {
		return nil, err
	}
	children := append([]linearapi.IssueRef(nil), issue.Children...)
	linearapi.SortChildrenForRun(children)
	out := make([]SubIssue, 0, len(children))
	for _, s := range children {
		if s.Identifier != "" {
			out = append(out, SubIssue{ID: s.Identifier, Title: s.Title, Done: s.State.IsTerminal(), HasChildren: s.HasChildren})
		}
	}
	return out, nil
}

func (l *Linear) subIssuesPrompt(id string) string {
	return fmt.Sprintf("Use the Linear MCP. List the direct sub-issues (children) of issue %s. "+
		"Respond with exactly one final line of JSON: SUB_ISSUES=[{\"id\":\"%s-494\",\"title\":\"...\",\"hasChildren\":false}, ...] "+
		"using each child's identifier, title, and whether it has its own sub-issues (hasChildren boolean). "+
		"If there are none, respond SUB_ISSUES=[]. No other output.", id, prefixOf(id))
}

func parseSubIssues(text string) ([]SubIssue, bool) {
	if subs, ok := parseSubIssuesJSON(text); ok {
		return subs, true
	}
	// Legacy sentinel: 'SUB_ISSUES=COD-494,COD-495' (titles unknown).
	re := regexp.MustCompile(`SUB_ISSUES=(.+)`)
	ms := re.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return nil, false
	}
	val := strings.TrimSpace(ms[len(ms)-1][1])
	if val == "NONE" || val == "[]" {
		return []SubIssue{}, true
	}
	var out []SubIssue
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, SubIssue{ID: part})
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func parseSubIssuesJSON(text string) ([]SubIssue, bool) {
	if idx := strings.LastIndex(text, "SUB_ISSUES="); idx >= 0 {
		text = text[idx+len("SUB_ISSUES="):]
	}
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end < start {
		return nil, false
	}
	var subs []SubIssue
	if err := json.Unmarshal([]byte(text[start:end+1]), &subs); err != nil {
		return nil, false
	}
	out := make([]SubIssue, 0, len(subs))
	for _, s := range subs {
		if s.ID != "" {
			out = append(out, s)
		}
	}
	return out, len(out) > 0 || (len(subs) == 0 && start >= 0 && end >= start)
}

func (l *Linear) pickPrompt(scope Scope) string {
	return fmt.Sprintf("Use the Linear MCP. Among %s, find issues that ALL of: "+
		"(a) carry the label '%s'; "+
		"(b) are NOT started — workflow state type is 'backlog' or 'unstarted' (exclude started, completed, canceled); "+
		"(c) have every 'blocked by' issue in a completed/Done state; "+
		"(d) are leaf issues — exclude any epic/parent that has its own sub-issues.%s "+
		"Pick the best one to start next by considering, in order: priority (Urgent > High > Medium > Low), due date (sooner is better), then the lowest issue number as a tie-breaker. "+
		"Respond with exactly one final line: 'PICK=<IDENTIFIER>' (e.g. PICK=%s-414) or 'PICK=NONE'. No other output.",
		scope.clause(), l.ReadyLabel, scope.projectClause(), scope.prefix())
}

func (l *Linear) epicPickPrompt(scope Scope, leaves map[string]bool) string {
	ids := make([]string, 0, len(leaves))
	for id := range leaves {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fmt.Sprintf("Use the Linear MCP. Among the leaf sub-issues of %s (%s), find one that ALL of: "+
		"(a) carries the label '%s'; "+
		"(b) is NOT started — workflow state type is 'backlog' or 'unstarted' (exclude started, completed, canceled); "+
		"(c) has every 'blocked by' issue in a completed/Done state. "+
		"Pick the best one to start next by considering, in order: priority (Urgent > High > Medium > Low), due date (sooner is better), then the lowest issue number as a tie-breaker. "+
		"Respond with exactly one final line: 'PICK=<IDENTIFIER>' (e.g. PICK=%s-414) or 'PICK=NONE'. No other output.",
		scope.Parent, strings.Join(ids, ", "), l.ReadyLabel, scope.prefix())
}

func parsePick(text, prefix string) (id string, matched bool) {
	if id, matched := parsePickJSON(text, prefix); matched {
		return id, true
	}
	re := regexp.MustCompile(`PICK=(` + regexp.QuoteMeta(prefix) + `-[0-9]+|NONE)`)
	ms := re.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return "", false
	}
	last := ms[len(ms)-1][1]
	if last == "NONE" {
		return "", true
	}
	return last, true
}

func parsePickJSON(text, prefix string) (id string, matched bool) {
	var result map[string]*string
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &result); err != nil {
		return "", false
	}
	for _, key := range []string{"pick", "issue"} {
		v, ok := result[key]
		if !ok {
			continue
		}
		if v == nil || *v == "" || *v == "NONE" {
			return "", true
		}
		re := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `-[0-9]+$`)
		if re.MatchString(*v) {
			return *v, true
		}
		return "", false
	}
	return "", false
}

// Title returns the title of issue id via the Linear API when possible,
// otherwise via the Linear MCP. Best-effort: a runner error or a missing
// sentinel yields "".
func (l *Linear) Title(ctx context.Context, id string) (string, error) {
	if title, err := l.titleAPI(ctx, id); err == nil {
		return title, nil
	} else if !shouldFallback(err) {
		return "", err
	}

	res, err := l.Runner.Run(ctx, l.titlePrompt(id), "title")
	if t, ok := parseTitle(res.Final); ok {
		return t, nil
	}
	return "", err
}

func (l *Linear) titleAPI(ctx context.Context, id string) (string, error) {
	issue, err := l.api().Issue(ctx, id)
	if err != nil {
		return "", err
	}
	return issue.Title, nil
}

func (l *Linear) titlePrompt(id string) string {
	return fmt.Sprintf("Use the Linear MCP. Get the title of issue %s. "+
		"Respond with exactly one final line: 'TITLE=<the issue title>'. No other output.", id)
}

func parseTitle(text string) (title string, matched bool) {
	re := regexp.MustCompile(`(?m)^.*TITLE=(.+)$`)
	ms := re.FindAllStringSubmatch(text, -1)
	if len(ms) > 0 {
		if t := strings.TrimSpace(ms[len(ms)-1][1]); t != "" {
			return t, true
		}
	}

	return parseTitleJSON(text)
}

func parseTitleJSON(text string) (title string, matched bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &obj); err != nil {
		return "", false
	}
	for _, key := range []string{"title", "TITLE"} {
		if v, ok := obj[key].(string); ok {
			if t := strings.TrimSpace(v); t != "" {
				return t, true
			}
		}
	}
	return "", false
}

// IssueStatus reports the normalized lifecycle status of issue id, used by
// --status to reconcile stale local checkpoints. The direct API maps the issue's
// workflow-state type; the MCP fallback asks the agent and recovers a STATUS=
// sentinel. An unrecoverable result yields StatusUnknown so the caller leaves the
// checkpoint intact rather than risk clearing live work.
func (l *Linear) IssueStatus(ctx context.Context, id string) (IssueStatus, error) {
	if st, err := l.issueStatusAPI(ctx, id); err == nil {
		return st, nil
	} else if !shouldFallback(err) {
		return StatusUnknown, err
	}

	res, err := l.Runner.Run(ctx, l.issueStatusPrompt(id), "status")
	if st, ok := parseIssueStatus(res.Final); ok {
		return st, nil
	}
	return StatusUnknown, err
}

func (l *Linear) issueStatusAPI(ctx context.Context, id string) (IssueStatus, error) {
	issue, err := l.api().Issue(ctx, id)
	if err != nil {
		return StatusUnknown, err
	}
	return mapLinearState(issue.State.Type), nil
}

// mapLinearState maps a Linear workflow-state type onto the normalized status.
// Linear's state types are backlog | unstarted | started | completed | canceled.
func mapLinearState(stateType string) IssueStatus {
	switch stateType {
	case "completed":
		return StatusDone
	case "canceled":
		return StatusCanceled
	default:
		return StatusOpen
	}
}

func (l *Linear) issueStatusPrompt(id string) string {
	return fmt.Sprintf("Use the Linear MCP. Look up issue %s and report its workflow state. "+
		"Respond with exactly one final line: 'STATUS=<done|canceled|open>' — "+
		"'done' if it is in a Done/completed state, 'canceled' if Canceled, otherwise 'open'. No other output.", id)
}

// parseIssueStatus recovers the normalized status from an agent response: the
// last 'STATUS=<value>' sentinel wins, accepting common synonyms. matched is
// false when no recognizable status line is present.
func parseIssueStatus(text string) (status IssueStatus, matched bool) {
	re := regexp.MustCompile(`(?mi)^.*STATUS=([A-Za-z_-]+)`)
	ms := re.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return StatusUnknown, false
	}
	switch strings.ToLower(strings.TrimSpace(ms[len(ms)-1][1])) {
	case "done", "completed", "complete", "merged", "closed", "shipped":
		return StatusDone, true
	case "canceled", "cancelled", "wontfix", "wont-do", "duplicate":
		return StatusCanceled, true
	case "open", "started", "unstarted", "backlog", "todo", "in-progress":
		return StatusOpen, true
	default:
		return StatusUnknown, false
	}
}

// IssueProject reports the name of the Linear project issue id belongs to, used by
// the ownership guard to refuse cross-project runs. The direct API reads the
// issue's project; the MCP fallback asks the agent for a PROJECT= sentinel. An
// empty string means "no project / unknown" — the guard reads that as "can't
// enforce" rather than a mismatch, so uncertainty never blocks a run.
func (l *Linear) IssueProject(ctx context.Context, id string) (string, error) {
	if name, err := l.issueProjectAPI(ctx, id); err == nil {
		return name, nil
	} else if !shouldFallback(err) {
		return "", err
	}

	res, err := l.Runner.Run(ctx, l.issueProjectPrompt(id), "project")
	if name, ok := parseProject(res.Final); ok {
		return name, nil
	}
	return "", err
}

func (l *Linear) issueProjectAPI(ctx context.Context, id string) (string, error) {
	issue, err := l.api().Issue(ctx, id)
	if err != nil {
		return "", err
	}
	return issue.Project.Name, nil
}

func (l *Linear) issueProjectPrompt(id string) string {
	return fmt.Sprintf("Use the Linear MCP. Look up issue %s and report the NAME of the Linear project it belongs to. "+
		"Respond with exactly one final line: 'PROJECT=<project name>' (or 'PROJECT=NONE' if it has no project). No other output.", id)
}

// parseProject recovers a project name from an agent response: the last 'PROJECT='
// sentinel wins. 'NONE'/empty yields ("", true) — a determined "no project", which
// the guard treats the same as unknown. matched is false when no sentinel exists.
func parseProject(text string) (name string, matched bool) {
	re := regexp.MustCompile(`(?m)^.*PROJECT=(.+)$`)
	ms := re.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return "", false
	}
	v := strings.TrimSpace(ms[len(ms)-1][1])
	if v == "" || strings.EqualFold(v, "NONE") {
		return "", true
	}
	return v, true
}

// FileBug files a NEW Linear issue as a last-resort HITL blocker for a QA failure
// the slice could not self-heal, even after comprehensive bugfix passes, and
// returns the new issue identifier (or "" when none was produced). The agent
// reads the verdict at verdictPath and the new ticket is recovered from the
// BUG=<id> sentinel. This operation reads a file and reasons over it, so it
// always uses the MCP rather than the direct API.
func (l *Linear) FileBug(ctx context.Context, id, verdictPath string) (string, error) {
	res, err := l.Runner.Run(ctx, l.fileBugPrompt(id, verdictPath), "file_bug")
	if bug, ok := parseBug(res.Final, prefixOf(id)); ok {
		return bug, nil
	}
	return "", err
}

func (l *Linear) fileBugPrompt(id, verdictPath string) string {
	target := "team " + l.Team
	if l.Project != "" {
		target += ", project '" + l.Project + "'"
	}
	return fmt.Sprintf("Use the Linear MCP. Read the QA verdict at %s. Create a NEW issue in %s, labelled 'HITL' and 'Bug', describing the failure that blocked %s's QA after automated repair and bugfix passes — a concise title plus a description with the verdict summary and the specific failures, noting it was surfaced by the Trau loop while working on %s and needs human attention. Output exactly one final line: BUG=<IDENTIFIER> (e.g. BUG=%s-500).",
		verdictPath, target, id, id, prefixOf(id))
}

// SetStatus moves a ticket to a workflow status (e.g. "In Review", "Done"). It
// uses the GraphQL API when possible, otherwise the MCP. extra is an optional
// trailing instruction spliced in before the DONE acknowledgement (e.g. attaching
// a PR link); in API mode extra is treated as a comment body.
func (l *Linear) SetStatus(ctx context.Context, id, status, extra string) error {
	if err := l.setStatusAPI(ctx, id, status, extra); err == nil {
		return nil
	} else if !shouldFallback(err) {
		return err
	}

	_, err := l.Runner.Run(ctx, l.setStatusPrompt(id, status, extra), "status")
	return err
}

func (l *Linear) setStatusAPI(ctx context.Context, id, status, extra string) error {
	if err := l.api().SetStatus(ctx, id, status, nil); err != nil {
		return err
	}
	if extra != "" {
		// Best-effort comment; don't fail the status move if commenting fails.
		_ = l.api().AddComment(ctx, id, extra)
	}
	return nil
}

func (l *Linear) setStatusPrompt(id, status, extra string) string {
	prompt := fmt.Sprintf("Use the Linear MCP to set issue %s to the status %q.", id, status)
	if extra != "" {
		prompt += " " + extra
	}
	return prompt + " Reply DONE."
}

// Reset returns a ticket to an unstarted/ready state so the picker re-selects it:
// it drops the quarantine label, ensures the ready label, moves the ticket to an
// unstarted workflow state, and comments. Uses the API when possible.
func (l *Linear) Reset(ctx context.Context, id string) error {
	if err := l.resetAPI(ctx, id); err == nil {
		return nil
	} else if !shouldFallback(err) {
		return err
	}

	extra := fmt.Sprintf("Remove the label '%s' if present and ensure '%s' is present so the loop can re-pick it; "+
		"set the workflow state to an unstarted one (type backlog/unstarted); "+
		"add a comment: \"Trau loop reset %s to start fresh.\"", l.QuarantineLabel, l.ReadyLabel, id)
	return l.SetStatus(ctx, id, "Todo", extra)
}

func (l *Linear) resetAPI(ctx context.Context, id string) error {
	issue, err := l.api().Issue(ctx, id)
	if err != nil {
		return err
	}
	labelNames := make([]string, 0, len(issue.Labels)+1)
	seenReady := false
	for _, label := range issue.Labels {
		if label.Name == l.QuarantineLabel {
			continue
		}
		if label.Name == l.ReadyLabel {
			seenReady = true
		}
		labelNames = append(labelNames, label.Name)
	}
	if !seenReady {
		labelNames = append(labelNames, l.ReadyLabel)
	}
	if err := l.api().SetStatus(ctx, id, "Todo", labelNames); err != nil {
		return err
	}
	return l.api().AddComment(ctx, id, fmt.Sprintf("Trau loop reset %s to start fresh.", id))
}

// Quarantine marks a ticket unrecoverable: it drops the ready label, adds the
// quarantine label, and leaves a comment pointing at the run artifacts. Uses the
// API when possible.
func (l *Linear) Quarantine(ctx context.Context, id, reason string) error {
	if err := l.quarantineAPI(ctx, id, reason); err == nil {
		return nil
	} else if !shouldFallback(err) {
		return err
	}

	_, err := l.Runner.Run(ctx, l.quarantinePrompt(id, reason), "quarantine")
	return err
}

func (l *Linear) quarantineAPI(ctx context.Context, id, reason string) error {
	issue, err := l.api().Issue(ctx, id)
	if err != nil {
		return err
	}
	labelNames := make([]string, 0, len(issue.Labels)+1)
	seenQuarantine := false
	for _, label := range issue.Labels {
		if label.Name == l.ReadyLabel {
			continue
		}
		if label.Name == l.QuarantineLabel {
			seenQuarantine = true
		}
		labelNames = append(labelNames, label.Name)
	}
	if !seenQuarantine {
		labelNames = append(labelNames, l.QuarantineLabel)
	}
	if err := l.api().SetStatus(ctx, id, "", labelNames); err != nil {
		return err
	}
	return l.api().AddComment(ctx, id, fmt.Sprintf("Trau loop stopped: %s (see runs/%s/).", reason, id))
}

// EnsureLabels creates the ready and quarantine labels in Linear if they do not
// already exist. Uses the API when possible.
func (l *Linear) EnsureLabels(ctx context.Context) error {
	if err := l.ensureLabelsAPI(ctx); err == nil {
		return nil
	} else if !shouldFallback(err) {
		return err
	}

	_, err := l.Runner.Run(ctx, l.ensureLabelsPrompt(), "ensure_labels")
	return err
}

func (l *Linear) ensureLabelsAPI(ctx context.Context) error {
	if strings.TrimSpace(l.Team) == "" {
		return linearapi.ErrNotEnabled
	}
	team, err := l.api().TeamByKey(ctx, l.Team)
	if err != nil {
		return err
	}
	if err := l.api().EnsureLabel(ctx, team.ID, l.ReadyLabel); err != nil {
		return err
	}
	return l.api().EnsureLabel(ctx, team.ID, l.QuarantineLabel)
}

func (l *Linear) ensureLabelsPrompt() string {
	return fmt.Sprintf("Use the Linear MCP. Ensure two issue labels exist: '%s' and '%s'. "+
		"Create them if missing. Reply DONE.", l.ReadyLabel, l.QuarantineLabel)
}

func (l *Linear) quarantinePrompt(id, reason string) string {
	return fmt.Sprintf("Use the Linear MCP on issue %s: remove the label '%s', add the label '%s', and add a comment: \"Trau loop stopped: %s (see runs/%s/).\" Reply DONE.",
		id, l.ReadyLabel, l.QuarantineLabel, reason, id)
}

func parseBug(text, prefix string) (id string, matched bool) {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if id, matched := parseBugJSON(text, prefix); matched {
		return id, true
	}
	re := regexp.MustCompile(`BUG=(` + regexp.QuoteMeta(prefix) + `-[0-9]+)`)
	ms := re.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return "", false
	}
	return ms[len(ms)-1][1], true
}

func parseBugJSON(text, prefix string) (id string, matched bool) {
	var result map[string]*string
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &result); err != nil {
		return "", false
	}
	for _, key := range []string{"bug", "issue"} {
		v, ok := result[key]
		if !ok {
			continue
		}
		if v == nil || *v == "" {
			return "", true
		}
		re := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `-[0-9]+$`)
		if re.MatchString(*v) {
			return *v, true
		}
		return "", false
	}
	return "", false
}
