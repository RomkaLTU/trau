package tracker

import "strings"

// Stage is a point in the loop's ticket lifecycle. The pipeline asks for a stage,
// never a status name: workflow statuses are the project's own vocabulary, and a
// Jira project that calls review "READY FOR QA" has no status named "In Review"
// to transition to. Each provider resolves the stage against the transitions its
// tracker actually reports.
type Stage string

const (
	StageTodo       Stage = "todo"
	StageInProgress Stage = "in-progress"
	StageInReview   Stage = "in-review"
	StageDone       Stage = "done"
)

// Display names the stage in the loop's own vocabulary, for log lines and for the
// MCP-only providers that have no workflow to resolve against.
func (s Stage) Display() string {
	switch s {
	case StageTodo:
		return "To Do"
	case StageInProgress:
		return "In Progress"
	case StageInReview:
		return "In Review"
	case StageDone:
		return "Done"
	default:
		return string(s)
	}
}

// ConfigKey is the INI key that pins the stage to an exact status name, for
// workflows where neither the names nor the category single one out.
func (s Stage) ConfigKey() string {
	return "STATUS_" + strings.ToUpper(strings.ReplaceAll(string(s), "-", "_"))
}

// names lists the status names the stage commonly goes by across workflows, most
// canonical first.
func (s Stage) names() []string {
	switch s {
	case StageTodo:
		return []string{"To Do", "Todo", "Open", "Backlog", "New", "Ready"}
	case StageInProgress:
		return []string{"In Progress", "In Development", "Doing", "Active", "Started", "Committed"}
	case StageInReview:
		return []string{
			"In Review", "Review", "Code Review", "Peer Review",
			"Ready for Review", "Ready for QA", "In QA", "QA", "Testing", "Resolved",
		}
	case StageDone:
		return []string{"Done", "Completed", "Complete", "Closed", "Shipped"}
	default:
		return nil
	}
}

// hints are the substrings that mark a status name as belonging to the stage when
// only its category matched. A workflow category holds several statuses — Jira's
// In Progress category covers both development and review — so the hint decides
// which of them the stage means.
func (s Stage) hints() []string {
	switch s {
	case StageTodo:
		return []string{"todo", "open", "backlog", "new", "ready", "triage"}
	case StageInProgress:
		return []string{"progress", "develop", "doing", "active", "start", "wip", "build"}
	case StageInReview:
		return []string{"review", "qa", "test", "verif", "approv", "resolved"}
	case StageDone:
		return []string{"done", "complete", "closed", "resolved", "shipped", "merged", "released"}
	default:
		return nil
	}
}

// WorkflowOption is one destination a tracker's workflow offers: the status name
// as the project spells it, plus the provider's own category key for it (Jira's
// statusCategory, Linear's state type, Azure DevOps' state category).
type WorkflowOption struct {
	Name     string
	Category string
}

// ResolveStage picks the destination that means stage among the ones a workflow
// offers, returning its index. A status name pinned in config wins, then one of
// the stage's own names, then the best option in one of categories — the buckets
// every workflow has, tried in the given preference order. Options that read as
// an abandonment never stand in for a stage: a workflow whose only Done-category
// destination is "Won't Do" resolves to nothing rather than closing the ticket as
// unwanted.
func ResolveStage(stage Stage, override string, categories []string, options []WorkflowOption) (int, bool) {
	if pinned := strings.TrimSpace(override); pinned != "" {
		if i, ok := matchName(options, pinned); ok {
			return i, true
		}
	}
	for _, name := range stage.names() {
		if i, ok := matchName(options, name); ok {
			return i, true
		}
	}
	for _, category := range categories {
		if i, ok := matchCategory(options, category, stage); ok {
			return i, true
		}
	}
	return 0, false
}

// optionNames lists the destinations a workflow offers, for an actionable
// resolution-failure message.
func optionNames(options []WorkflowOption) string {
	if len(options) == 0 {
		return "none"
	}
	names := make([]string, len(options))
	for i, opt := range options {
		names[i] = opt.Name
	}
	return strings.Join(names, ", ")
}

func matchName(options []WorkflowOption, name string) (int, bool) {
	want := normalizeStatus(name)
	for i, opt := range options {
		if normalizeStatus(opt.Name) == want {
			return i, true
		}
	}
	return 0, false
}

// matchCategory picks the option to use for stage within one category: a hinted
// name if the category holds one, otherwise the first option that is not an
// abandonment.
func matchCategory(options []WorkflowOption, category string, stage Stage) (int, bool) {
	first, found := 0, false
	for i, opt := range options {
		if !strings.EqualFold(strings.TrimSpace(opt.Category), category) || abandons(opt.Name) {
			continue
		}
		if hasAny(opt.Name, stage.hints()) {
			return i, true
		}
		if !found {
			first, found = i, true
		}
	}
	return first, found
}

// abandons reports whether a status name means the work was dropped rather than
// carried through the stage.
func abandons(name string) bool {
	return hasAny(name, []string{"cancel", "won't", "wont", "duplicate", "reject", "invalid", "abandon", "obsolete", "declin"})
}

func hasAny(name string, needles []string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// normalizeStatus folds the spelling differences between workflows that name the
// same status — case, spacing and punctuation — so "READY FOR QA", "Ready For QA"
// and "ready-for-qa" all compare equal.
func normalizeStatus(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
