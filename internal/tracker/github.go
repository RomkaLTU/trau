package tracker

import (
	"context"
	"fmt"

	"github.com/RomkaLTU/trau/internal/agent"
)

// GitHub picks tickets through the GitHub MCP via an agent. Issue identifiers
// use a configured prefix (e.g. GH-123); the agent maps this to the real GitHub
// issue number behind the scenes.
type GitHub struct {
	Runner          agent.Runner
	Repo            string // repo slug, e.g. "owner/repo"
	ReadyLabel      string
	QuarantineLabel string
}

// Pick returns the next eligible ticket identifier, or "" when nothing is eligible.
func (g *GitHub) Pick(ctx context.Context, scope Scope) (string, error) {
	res, err := g.Runner.Run(ctx, g.pickPrompt(scope), "pick")
	if id, matched := parsePick(res.Final, scope.prefix()); matched {
		return id, nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

func (g *GitHub) pickPrompt(scope Scope) string {
	return fmt.Sprintf("Use the GitHub MCP. In repository %q, among %s, find open issues that ALL of: "+
		"(a) carry the label '%s'; "+
		"(b) have no linked pull requests that are open; "+
		"(c) have every 'blocked by' issue closed. "+
		"Pick the best one to start next by considering, in order: priority labels (e.g. P0/priority-critical > P1/priority-high > P2/priority-medium > P3/priority-low), milestone due date (sooner is better), then the lowest issue number as a tie-breaker. "+
		"Map the selected GitHub issue #N to the configured prefix by responding 'PICK=<PREFIX>-N' (e.g. PICK=%s-414) or 'PICK=NONE'. No other output.",
		g.Repo, scope.clause(), g.ReadyLabel, scope.prefix())
}

// SubIssues asks the GitHub MCP to inspect task-list items / sub-issue references
// in issue id and return them in the same <prefix>-<n> shape.
func (g *GitHub) SubIssues(ctx context.Context, id string) ([]SubIssue, error) {
	res, err := g.Runner.Run(ctx, g.subIssuesPrompt(id), "sub_issues")
	if subs, matched := parseSubIssues(res.Final); matched {
		return subs, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("could not parse sub-issues for %s", id)
}

func (g *GitHub) subIssuesPrompt(id string) string {
	return fmt.Sprintf("Use the GitHub MCP. Inspect issue %s in repository %q for task-list items, sub-issue references, or linked child issues. "+
		"Respond with exactly one final line of JSON: SUB_ISSUES=[{\"id\":\"%s-414\",\"title\":\"...\"}, ...] "+
		"using each child's mapped identifier and title. If there are none, respond SUB_ISSUES=[]. No other output.", id, g.Repo, prefixOf(id))
}

// Title returns the title of issue id via the GitHub MCP.
func (g *GitHub) Title(ctx context.Context, id string) (string, error) {
	res, err := g.Runner.Run(ctx, g.titlePrompt(id), "title")
	if t, ok := parseTitle(res.Final); ok {
		return t, nil
	}
	return "", err
}

func (g *GitHub) titlePrompt(id string) string {
	return fmt.Sprintf("Use the GitHub MCP. Get the title of issue %s in repository %q. "+
		"Respond with exactly one final line: 'TITLE=<the issue title>'. No other output.", id, g.Repo)
}

// SetStatus emulates a workflow status change via labels and comments.
func (g *GitHub) SetStatus(ctx context.Context, id, status, extra string) error {
	_, err := g.Runner.Run(ctx, g.setStatusPrompt(id, status, extra), "status")
	return err
}

func (g *GitHub) setStatusPrompt(id, status, extra string) string {
	prompt := fmt.Sprintf("Use the GitHub MCP. For issue %s in repository %q, update labels and add a comment to reflect status %q.", id, g.Repo, status)
	if extra != "" {
		prompt += " " + extra
	}
	return prompt + " Reply DONE."
}

// Reset returns a ticket to a ready/unstarted state.
func (g *GitHub) Reset(ctx context.Context, id string) error {
	extra := fmt.Sprintf("Remove the label '%s' if present and ensure '%s' is present so the loop can re-pick it; "+
		"re-open the issue if it is closed; "+
		"add a comment: \"Trau loop reset %s to start fresh.\"", g.QuarantineLabel, g.ReadyLabel, id)
	return g.SetStatus(ctx, id, "open", extra)
}

// Quarantine marks a ticket unrecoverable.
func (g *GitHub) Quarantine(ctx context.Context, id, reason string) error {
	_, err := g.Runner.Run(ctx, g.quarantinePrompt(id, reason), "quarantine")
	return err
}

func (g *GitHub) quarantinePrompt(id, reason string) string {
	return fmt.Sprintf("Use the GitHub MCP on issue %s in repository %q: remove the label '%s', add the label '%s', and add a comment: \"Trau loop stopped: %s (see runs/%s/).\" Reply DONE.",
		id, g.Repo, g.ReadyLabel, g.QuarantineLabel, reason, id)
}

// FileBug files a NEW GitHub issue as a last-resort HITL blocker for a QA failure
// the slice could not self-heal, even after comprehensive bugfix passes.
func (g *GitHub) FileBug(ctx context.Context, id, verdictPath string) (string, error) {
	res, err := g.Runner.Run(ctx, g.fileBugPrompt(id, verdictPath), "file_bug")
	if bug, ok := parseBug(res.Final, prefixOf(id)); ok {
		return bug, nil
	}
	return "", err
}

func (g *GitHub) fileBugPrompt(id, verdictPath string) string {
	return fmt.Sprintf("Use the GitHub MCP. Read the QA verdict at %s. Create a NEW issue in repository %q, labelled 'HITL' and 'bug', describing the failure that blocked %s's QA after automated repair and bugfix passes — a concise title plus a description with the verdict summary and the specific failures, noting it was surfaced by the Trau loop while working on %s and needs human attention. Output exactly one final line: BUG=<IDENTIFIER> (e.g. BUG=%s-500).",
		verdictPath, g.Repo, id, id, prefixOf(id))
}

// EnsureLabels creates the ready and quarantine labels in GitHub if they do not exist.
func (g *GitHub) EnsureLabels(ctx context.Context) error {
	_, err := g.Runner.Run(ctx, g.ensureLabelsPrompt(), "ensure_labels")
	return err
}

func (g *GitHub) ensureLabelsPrompt() string {
	return fmt.Sprintf("Use the GitHub MCP. Ensure two issue labels exist in repository %q: '%s' and '%s'. "+
		"Create them if missing. Reply DONE.", g.Repo, g.ReadyLabel, g.QuarantineLabel)
}
