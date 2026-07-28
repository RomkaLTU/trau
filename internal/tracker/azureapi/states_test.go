package azureapi

import "testing"

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

// TargetCategory reads the loop's own vocabulary, which no template uses verbatim.
func TestTargetCategoryAcceptsLoopStatusNames(t *testing.T) {
	cases := []struct {
		status string
		want   StateCategory
	}{
		{"In Progress", CategoryInProgress},
		{"In Review", CategoryResolved},
		{"Done", CategoryCompleted},
		{"To Do", CategoryProposed},
		{"started", CategoryInProgress},
		{"review", CategoryResolved},
		{"shipped", CategoryCompleted},
		{"Canceled", CategoryRemoved},
		{"Marinating", CategoryUnknown},
	}
	for _, tc := range cases {
		if got := TargetCategory(tc.status); got != tc.want {
			t.Errorf("TargetCategory(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}
