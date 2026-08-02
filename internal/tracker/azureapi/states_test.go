package azureapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A customized state name is absent from the name table, so the category has to
// come from what the project itself reports — read once and reused, since a board
// classifies every item against the same handful of work-item types.
func TestStateCategoriesPrefersTheReportedCategory(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"value":[{"name":"New","category":"Proposed"},
			{"name":"Ready to Develop","category":"Proposed"},{"name":"Done","category":"Completed"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "pat")
	for range 2 {
		states, err := c.StateCategories(context.Background(), "Contoso", "User Story")
		if err != nil {
			t.Fatalf("StateCategories: %v", err)
		}
		if got := CategoryOf(states, "Ready to Develop"); got != CategoryProposed {
			t.Errorf("CategoryOf(Ready to Develop) = %q, want %q", got, CategoryProposed)
		}
		if got := CategoryOf(states, "Closed"); got != CategoryCompleted {
			t.Errorf("CategoryOf(Closed) = %q, want the name table to answer for a state the process omits", got)
		}
	}
	if calls != 1 {
		t.Errorf("states requests = %d, want the answer memoised after the first", calls)
	}
}

// The stock templates name the same workflow stages differently, which is the
// whole reason state comparisons go through a category.
func TestCategoryCoversStockTemplates(t *testing.T) {
	cases := []struct {
		state string
		want  StateCategory
	}{
		{"New", CategoryProposed},
		{"Proposed", CategoryProposed},
		{"Approved", CategoryProposed},
		{"To Do", CategoryProposed},
		{"Backlog", CategoryProposed},
		{"Active", CategoryInProgress},
		{"Committed", CategoryInProgress},
		{"Doing", CategoryInProgress},
		{"In Progress", CategoryInProgress},
		{"Resolved", CategoryResolved},
		{"Closed", CategoryCompleted},
		{"Done", CategoryCompleted},
		{"Removed", CategoryRemoved},
		{"Cut", CategoryRemoved},
		{"  done  ", CategoryCompleted},
		{"DONE", CategoryCompleted},
		{"Marinating", CategoryUnknown},
		{"", CategoryUnknown},
	}
	for _, tc := range cases {
		if got := Category(tc.state); got != tc.want {
			t.Errorf("Category(%q) = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestTerminalIsCompletedOrRemoved(t *testing.T) {
	cases := []struct {
		category StateCategory
		want     bool
	}{
		{CategoryProposed, false},
		{CategoryInProgress, false},
		{CategoryResolved, false},
		{CategoryCompleted, true},
		{CategoryRemoved, true},
		{CategoryUnknown, false},
	}
	for _, tc := range cases {
		if got := tc.category.Terminal(); got != tc.want {
			t.Errorf("%q.Terminal() = %v, want %v", tc.category, got, tc.want)
		}
	}
}
