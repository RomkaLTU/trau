package azureapi

import (
	"context"
	"strings"
)

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

// published reports whether the category is one of the buckets Azure DevOps
// itself hands back, so a states response carrying something else falls back to
// the name table rather than bucketing the item as unknown.
func (c StateCategory) published() bool {
	switch c {
	case CategoryProposed, CategoryInProgress, CategoryResolved, CategoryCompleted, CategoryRemoved:
		return true
	default:
		return false
	}
}

// cachedStates is one StateCategories answer, failure included: every caller
// degrades from a failed metadata read the same way, and a board classifying
// hundreds of items must not pay a round trip each to learn that again.
type cachedStates struct {
	states []State
	err    error
}

// StateCategories lists a work-item type's states with the categories Azure
// DevOps reports for them, memoised per project and type. A read classifies
// every item on the board against the same handful of types, and a client lives
// no longer than the sync or operation that built it.
func (c *Client) StateCategories(ctx context.Context, project, itemType string) ([]State, error) {
	key := project + "\x00" + itemType
	c.statesMu.Lock()
	hit, cached := c.states[key]
	c.statesMu.Unlock()
	if cached {
		return hit.states, hit.err
	}
	states, err := c.States(ctx, project, itemType)
	c.statesMu.Lock()
	c.states[key] = cachedStates{states: states, err: err}
	c.statesMu.Unlock()
	return states, err
}

// CategoryOf classifies a state against the categories a work-item type's own
// states report, so a process that renamed its columns resolves by what Azure
// DevOps says rather than by a name this package happens to know. A state the
// workflow does not list — including every state when the metadata read failed —
// falls back to the name table.
func CategoryOf(states []State, state string) StateCategory {
	want := strings.TrimSpace(state)
	for _, s := range states {
		if !strings.EqualFold(strings.TrimSpace(s.Name), want) {
			continue
		}
		if c := StateCategory(strings.TrimSpace(s.Category)); c.published() {
			return c
		}
		break
	}
	return Category(state)
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
