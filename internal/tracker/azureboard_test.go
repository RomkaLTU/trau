package tracker

import (
	"context"
	"testing"
)

// Where a repo maps its board columns, the mapping is the whole answer: a mapped
// column decides the group, a column it does not name is unknown rather than
// category-derived, and only a work item the board places nowhere falls back to
// matching its state name. The System.Reason cancel override survives all of it.
func TestAzureBoardStatesGroupByColumn(t *testing.T) {
	mapping := parseAzureBoardStates("New=backlog, Ready to Develop=unstarted, Ready to test=started, Done=done")
	cases := []struct {
		name          string
		column, state string
		reason        string
		want          StatusGroup
		wantMapped    bool
	}{
		{name: "mapped column", column: "Ready to Develop", state: "New", want: StatusGroupUnstarted, wantMapped: true},
		{name: "second column on the same state", column: "Ready to test", state: "In Progress", want: StatusGroupStarted, wantMapped: true},
		{name: "unlisted column", column: "Blocked", state: "New", want: StatusGroupUnknown, wantMapped: true},
		{name: "off-board item reads its state", state: "Done", want: StatusGroupDone, wantMapped: true},
		{name: "off-board item the mapping cannot name", state: "Triaging", want: StatusGroupUnknown},
		{name: "a cancel reason beats a done column", column: "Done", state: "Closed", reason: "Cut", want: StatusGroupCanceled, wantMapped: true},
	}
	for _, tc := range cases {
		got, mapped := mapping.group(tc.column, tc.state, tc.reason)
		if got != tc.want || mapped != tc.wantMapped {
			t.Errorf("%s: group(%q, %q, %q) = %q/%v, want %q/%v",
				tc.name, tc.column, tc.state, tc.reason, got, mapped, tc.want, tc.wantMapped)
		}
	}
}

// Reconcile and epic finalization read the same mapping the board groups by, so
// neither can disagree with the column a human is looking at.
func TestAzureIssueStatusFollowsTheBoardColumn(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/workitems/7": `{"id":7,"fields":{"System.State":"New","System.BoardColumn":"Ready to test",
			"System.WorkItemType":"Task"}}`,
	})
	az.boardStates = parseAzureBoardStates("New=backlog,Ready to test=started")

	got, err := az.IssueStatus(context.Background(), "7")
	if err != nil {
		t.Fatalf("IssueStatus returned error: %v", err)
	}
	if got != StatusStarted {
		t.Errorf("IssueStatus = %q, want %q (the Ready to test column is started work)", got, StatusStarted)
	}
}
