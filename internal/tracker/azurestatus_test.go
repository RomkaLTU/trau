package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker/azureapi"
)

// azureStatusOptionsServer stands in for an organization whose Platform team keeps
// one Kanban board, answering the four routes a status-options read makes and
// failing the test on anything else.
func azureStatusOptionsServer(t *testing.T) string {
	t.Helper()
	routes := map[string]string{
		"/Contoso/Platform/_apis/work/backlogconfiguration": `{
			"portfolioBacklogs":[{"rank":1,"defaultWorkItemType":{"name":"Feature"},"workItemTypes":[{"name":"Feature"}]}],
			"requirementBacklog":{"defaultWorkItemType":{"name":"User Story"},"workItemTypes":[{"name":"User Story"}]},
			"taskBacklog":{"defaultWorkItemType":{"name":"Task"},"workItemTypes":[{"name":"Task"}]},
			"bugWorkItems":{"defaultWorkItemType":{"name":"Bug"},"workItemTypes":[{"name":"Bug"}]}}`,
		"/Contoso/Platform/_apis/work/teamsettings": `{"bugsBehavior":"asRequirements"}`,
		"/Contoso/_apis/wit/workitemtypes/User Story/states": `{"value":[
			{"name":"New","category":"Proposed"},
			{"name":"Approved","category":"Proposed"},
			{"name":"Active","category":"InProgress"},
			{"name":"Closed","category":"Completed"}]}`,
		"/Contoso/Platform/_apis/work/boards": `{"value":[{"id":"stories","name":"Stories"}]}`,
		"/Contoso/Platform/_apis/work/boards/stories/columns": `{"value":[
			{"name":"New","columnType":"incoming","stateMappings":{"User Story":"New"}},
			{"name":"Approved","columnType":"inProgress","stateMappings":{"User Story":"Approved"}},
			{"name":"Active","columnType":"inProgress","stateMappings":{"User Story":"Active"}},
			{"name":"Done","columnType":"outgoing","stateMappings":{"User Story":"Closed"}}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unrouted request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The editor's two vocabularies come back from one read: the team's board columns
// in board order, each prefilled with the group its own state mappings suggest,
// and the requirement type's states as the only things a STATUS_* pin may name —
// a board column is never offered as a pin (ADR 0036). The repo pins STATUS_TODO
// to New, so New is the unstarted column and Approved falls to backlog — the
// reverse of what the workflow's own naming would resolve to unpinned.
func TestAzureStatusOptionsReadsBoardColumnsAndPinnableStates(t *testing.T) {
	opts, err := AzureStatusOptions(context.Background(), Config{
		Team:            "Contoso",
		BaseURL:         azureStatusOptionsServer(t),
		APIKey:          "pat",
		BoardTeams:      []string{"Platform"},
		StatusOverrides: map[Stage]string{StageTodo: "New"},
	})
	if err != nil {
		t.Fatalf("AzureStatusOptions: %v", err)
	}

	want := []BoardColumnSuggestion{
		{Name: "New", SuggestedGroup: StatusGroupUnstarted},
		{Name: "Approved", SuggestedGroup: StatusGroupBacklog},
		{Name: "Active", SuggestedGroup: StatusGroupStarted},
		{Name: "Done", SuggestedGroup: StatusGroupDone},
	}
	if len(opts.Columns) != len(want) {
		t.Fatalf("columns = %+v, want %+v", opts.Columns, want)
	}
	for i, col := range want {
		if opts.Columns[i] != col {
			t.Errorf("columns[%d] = %+v, want %+v", i, opts.Columns[i], col)
		}
	}

	wantPins := []WorkflowOption{
		{Name: "New", Category: "Proposed"},
		{Name: "Approved", Category: "Proposed"},
		{Name: "Active", Category: "InProgress"},
		{Name: "Closed", Category: "Completed"},
	}
	if len(opts.Pins) != len(wantPins) {
		t.Fatalf("pins = %+v, want the requirement type's own states %+v", opts.Pins, wantPins)
	}
	for i, pin := range wantPins {
		if opts.Pins[i] != pin {
			t.Errorf("pins[%d] = %+v, want %+v", i, opts.Pins[i], pin)
		}
	}
}

func TestSuggestBoardGroup(t *testing.T) {
	states := []azureapi.State{
		{Name: "New", Category: "Proposed"},
		{Name: "Approved", Category: "Proposed"},
		{Name: "Active", Category: "InProgress"},
		{Name: "Closed", Category: "Completed"},
		{Name: "Removed", Category: "Removed"},
	}
	tests := []struct {
		name   string
		pin    string
		column azureapi.BoardColumn
		want   StatusGroup
	}{
		{
			name:   "the pinned Proposed state is the unstarted column",
			pin:    "Approved",
			column: azureapi.BoardColumn{Name: "Approved", ColumnType: "inProgress", States: []string{"Approved"}},
			want:   StatusGroupUnstarted,
		},
		{
			name:   "every other Proposed column is backlog",
			pin:    "Approved",
			column: azureapi.BoardColumn{Name: "New", ColumnType: "incoming", States: []string{"New"}},
			want:   StatusGroupBacklog,
		},
		{
			name:   "types that disagree take the least advanced group",
			column: azureapi.BoardColumn{Name: "Done", ColumnType: "outgoing", States: []string{"Closed", "Removed"}},
			want:   StatusGroupDone,
		},
		{
			name:   "a column mapping nothing recognizable falls back to its own rung",
			column: azureapi.BoardColumn{Name: "Parked", ColumnType: "inProgress", States: []string{"Snoozed"}},
			want:   StatusGroupStarted,
		},
		{
			name:   "and a column with neither is left unmapped",
			column: azureapi.BoardColumn{Name: "Parked", States: []string{"Snoozed"}},
			want:   StatusGroupUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := suggestBoardGroup(states, tc.pin, tc.column); got != tc.want {
				t.Errorf("suggestBoardGroup(%q) = %q, want %q", tc.column.Name, got, tc.want)
			}
		})
	}
}
