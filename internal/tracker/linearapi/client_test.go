package linearapi

import (
	"strings"
	"testing"
)

func TestSortChildrenForRun(t *testing.T) {
	refs := []IssueRef{
		{Identifier: "COD-500", Priority: 3},                        // medium, no due date
		{Identifier: "COD-494", Priority: 0},                        // no priority -> last
		{Identifier: "COD-498", Priority: 1},                        // urgent
		{Identifier: "COD-497", Priority: 3, DueDate: "2026-07-01"}, // medium, later due
		{Identifier: "COD-496", Priority: 3, DueDate: "2026-06-01"}, // medium, sooner due
		{Identifier: "COD-495", Priority: 2},                        // high
	}

	SortChildrenForRun(refs)

	got := make([]string, len(refs))
	for i, r := range refs {
		got[i] = r.Identifier
	}
	// Priority first (urgent > high > medium > none), then due date (an empty due
	// sorts ahead of a dated one under the picker's existing rule), then number.
	want := []string{"COD-498", "COD-495", "COD-500", "COD-496", "COD-497", "COD-494"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestStateIsTerminal(t *testing.T) {
	cases := map[string]bool{
		"completed": true,
		"canceled":  true,
		"started":   false,
		"unstarted": false,
		"backlog":   false,
		"triage":    false,
	}
	for typ, want := range cases {
		if got := (State{Type: typ}).IsTerminal(); got != want {
			t.Errorf("State{%q}.IsTerminal() = %v, want %v", typ, got, want)
		}
	}
}

// TestMutationsDeclareStringVariables guards the GraphQL variable types against
// Linear's schema: mutation arguments and input-object fields are String-typed
// (teamId: String!, labelIds: [String!], parentId: String, …), and Linear rejects
// an ID-typed variable in those positions with GRAPHQL_VALIDATION_FAILED
// ("Variable "$teamId" of type "ID!" used in position expecting type "String!"").
// Only filter comparators (IDComparator.eq) take ID, so ID belongs in queries
// alone — a mutation declaring an ID variable is always a live-API failure.
func TestMutationsDeclareStringVariables(t *testing.T) {
	mutations := map[string]string{
		"issueUpdate":      issueUpdateMutation,
		"commentCreate":    commentCreateMutation,
		"issueLabelCreate": issueLabelCreateMutation,
		"issueCreate":      issueCreateMutation,
	}
	for name, q := range mutations {
		for _, bad := range []string{": ID", ": [ID"} {
			if strings.Contains(q, bad) {
				t.Errorf("%s declares an ID-typed variable (%q) — Linear mutations take String types", name, bad)
			}
		}
	}
}
