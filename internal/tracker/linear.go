package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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
// retry the operation through the MCP. Auth and "not enabled" errors are not
// fallback-worthy because retrying through MCP won't help; transient / mapping
// errors are.
func shouldFallback(err error) bool {
	if err == nil {
		return false
	}
	if err == linearapi.ErrUnauthorized || err == linearapi.ErrNotEnabled {
		return false
	}
	return true
}

// Pick returns the next eligible ticket identifier, or "" when nothing is
// eligible (the agent answered PICK=NONE). It surfaces a runner error only when
// the agent produced no sentinel at all, so a genuine failure is visible rather
// than silently reported as "nothing eligible".
func (l *Linear) Pick(ctx context.Context, scope Scope) (string, error) {
	if id, err := l.pickAPI(ctx, scope); err == nil {
		return id, nil
	} else if !shouldFallback(err) {
		return "", err
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

func (l *Linear) pickAPI(ctx context.Context, scope Scope) (string, error) {
	if scope.Parent != "" {
		// Sub-issue picking is not yet mapped to the GraphQL API; fall through to MCP.
		return "", linearapi.ErrNotEnabled
	}
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
		if !allBlockersCompleted(c.BlockedBy) {
			continue
		}
		if !strings.HasPrefix(c.Identifier, prefix+"-") {
			continue
		}
		return c.Identifier, nil
	}
	return "", nil
}

func allBlockersCompleted(refs []linearapi.IssueRef) bool {
	for _, r := range refs {
		if !r.State.IsCompleted() {
			return false
		}
	}
	return true
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
	out := make([]SubIssue, 0, len(issue.Children))
	for _, s := range issue.Children {
		if s.Identifier != "" {
			out = append(out, SubIssue{ID: s.Identifier, Title: s.Title})
		}
	}
	return out, nil
}

func (l *Linear) subIssuesPrompt(id string) string {
	return fmt.Sprintf("Use the Linear MCP. List the direct sub-issues (children) of issue %s. "+
		"Respond with exactly one final line of JSON: SUB_ISSUES=[{\"id\":\"COD-494\",\"title\":\"...\"}, ...] "+
		"using each child's identifier and title. If there are none, respond SUB_ISSUES=[]. No other output.", id)
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
		"(c) have every 'blocked by' issue in a completed/Done state. "+
		"Pick the best one to start next by considering, in order: priority (Urgent > High > Medium > Low), due date (sooner is better), then the lowest issue number as a tie-breaker. "+
		"Respond with exactly one final line: 'PICK=<IDENTIFIER>' (e.g. PICK=%s-414) or 'PICK=NONE'. No other output.",
		scope.clause(), l.ReadyLabel, scope.prefix())
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

// FileBug files a NEW Linear issue as a last-resort HITL blocker for a QA failure
// the slice could not self-heal, even after comprehensive bugfix passes, and
// returns the new issue identifier (or "" when none was produced). The agent
// reads the verdict at verdictPath and the new ticket is recovered from the
// BUG=<id> sentinel. This operation reads a file and reasons over it, so it
// always uses the MCP rather than the direct API.
func (l *Linear) FileBug(ctx context.Context, id, verdictPath string) (string, error) {
	res, err := l.Runner.Run(ctx, l.fileBugPrompt(id, verdictPath), "file_bug")
	if bug, ok := parseBug(res.Final); ok {
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
		verdictPath, target, id, id, DefaultPrefix)
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

func parseBug(text string) (id string, matched bool) {
	if id, matched := parseBugJSON(text); matched {
		return id, true
	}
	re := regexp.MustCompile(`BUG=(` + regexp.QuoteMeta(DefaultPrefix) + `-[0-9]+)`)
	ms := re.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return "", false
	}
	return ms[len(ms)-1][1], true
}

func parseBugJSON(text string) (id string, matched bool) {
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
		re := regexp.MustCompile(`^` + regexp.QuoteMeta(DefaultPrefix) + `-[0-9]+$`)
		if re.MatchString(*v) {
			return *v, true
		}
		return "", false
	}
	return "", false
}
