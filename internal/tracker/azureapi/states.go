package azureapi

import "strings"

// StateCategory is the process-agnostic bucket a System.State value falls into.
// The stock process templates each name the same workflow stages differently —
// Agile calls a started item Active, Scrum calls it Committed, Basic calls it
// Doing — so every state comparison in this package goes through the category
// rather than the raw name. The values match the categories Azure DevOps itself
// reports from the work-item-type states endpoint.
type StateCategory string

const (
	CategoryProposed   StateCategory = "Proposed"
	CategoryInProgress StateCategory = "InProgress"
	CategoryResolved   StateCategory = "Resolved"
	CategoryCompleted  StateCategory = "Completed"
	CategoryRemoved    StateCategory = "Removed"
	// CategoryUnknown means the state name belongs to a customized process this
	// mapping does not recognize. Callers must treat it as "leave intact" rather
	// than guess, so a custom workflow never causes a wrong lifecycle decision.
	CategoryUnknown StateCategory = ""
)

// Terminal reports whether the category means Azure DevOps considers the work
// item finished — completed, or removed from the board.
func (c StateCategory) Terminal() bool {
	return c == CategoryCompleted || c == CategoryRemoved
}

// Category classifies a System.State value, covering the state names the Agile,
// Scrum, CMMI and Basic templates ship with.
func Category(state string) StateCategory {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "new", "proposed", "approved", "to do", "todo", "open", "backlog":
		return CategoryProposed
	case "active", "committed", "doing", "in progress", "in development":
		return CategoryInProgress
	case "resolved", "in review", "code review":
		return CategoryResolved
	case "closed", "done", "completed":
		return CategoryCompleted
	case "removed", "cut", "canceled", "cancelled", "won't do", "wont do", "abandoned", "rejected", "duplicate":
		return CategoryRemoved
	default:
		return CategoryUnknown
	}
}

// TargetCategory classifies the status name a caller asks to move a work item
// to. It is deliberately separate from Category: the loop names its targets in
// its own vocabulary ("In Review"), which has to survive being pointed at a
// project whose template has no state by that name.
func TargetCategory(status string) StateCategory {
	if c := Category(status); c != CategoryUnknown {
		return c
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "started", "in-progress", "wip":
		return CategoryInProgress
	case "review", "in-review", "ready for review":
		return CategoryResolved
	case "complete", "shipped", "delivered":
		return CategoryCompleted
	default:
		return CategoryUnknown
	}
}
