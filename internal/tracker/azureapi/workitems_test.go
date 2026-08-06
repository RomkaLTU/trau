package azureapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

const relBase = "https://dev.azure.com/acme/_apis/wit/workItems/"

func TestToWorkItemSplitsRelationsByLinkType(t *testing.T) {
	body := `{
		"id": 42,
		"fields": {
			"System.Title": "Leaf",
			"System.State": "Active",
			"System.Reason": "Implementation started",
			"System.WorkItemType": "Task",
			"System.TeamProject": "Contoso",
			"System.Tags": "ready-for-agent; backend",
			"Microsoft.VSTS.Common.Priority": 2
		},
		"relations": [
			{"rel": "System.LinkTypes.Hierarchy-Reverse", "url": "` + relBase + `10"},
			{"rel": "System.LinkTypes.Hierarchy-Forward", "url": "` + relBase + `43"},
			{"rel": "System.LinkTypes.Hierarchy-Forward", "url": "` + relBase + `44"},
			{"rel": "System.LinkTypes.Dependency-Reverse", "url": "` + relBase + `9"},
			{"rel": "AttachedFile", "url": "https://dev.azure.com/acme/_apis/wit/attachments/abc-guid"}
		]
	}`
	var raw workItemResponse
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	item := raw.toWorkItem()

	if item.Parent != 10 {
		t.Errorf("parent = %d, want 10", item.Parent)
	}
	if !slices.Equal(item.Children, []int{43, 44}) {
		t.Errorf("children = %v, want [43 44]", item.Children)
	}
	if !slices.Equal(item.BlockedBy, []int{9}) {
		t.Errorf("blockedBy = %v, want [9] (the attachment relation must be ignored)", item.BlockedBy)
	}
	if !slices.Equal(item.Tags, []string{"ready-for-agent", "backend"}) {
		t.Errorf("tags = %v, want [ready-for-agent backend]", item.Tags)
	}
	if item.Priority != 2 {
		t.Errorf("priority = %d, want 2", item.Priority)
	}
	if !item.HasChildren() {
		t.Error("HasChildren() = false, want true")
	}
	if item.Done() {
		t.Error("Done() = true, want false for an Active item")
	}
}

// A work-item type without a priority field must rank behind every explicit
// priority rather than ahead of it.
func TestToWorkItemDefaultsMissingPriorityLast(t *testing.T) {
	var raw workItemResponse
	if err := json.Unmarshal([]byte(`{"id":1,"fields":{"System.Title":"No priority"}}`), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := raw.toWorkItem().Priority; got != priorityUnset {
		t.Errorf("priority = %d, want %d", got, priorityUnset)
	}
}

// The loop picks in the order the board shows: Stack Rank ascending, an unranked
// item last, and the newest work-item number first among equals.
func TestRankOrdersByStackRankThenNewestID(t *testing.T) {
	stack := func(v float64) *float64 { return &v }
	items := []WorkItem{
		{ID: 10},
		{ID: 5, StackRank: stack(200)},
		{ID: 30, StackRank: stack(100)},
		{ID: 40, StackRank: stack(100)},
	}
	rank(items)
	got := make([]int, len(items))
	for i, item := range items {
		got[i] = item.ID
	}
	if want := []int{40, 30, 5, 10}; !slices.Equal(got, want) {
		t.Errorf("ranked ids = %v, want %v", got, want)
	}
}

func TestParseAndJoinTagsRoundTrip(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  ", nil},
		{"one", []string{"one"}},
		{"one; two;three ; ", []string{"one", "two", "three"}},
	}
	for _, tc := range cases {
		if got := ParseTags(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("ParseTags(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if got := JoinTags([]string{"a", "b"}); got != "a; b" {
		t.Errorf("JoinTags = %q, want %q", got, "a; b")
	}
}

// A Bug keeps its body in ReproSteps rather than Description, and the acceptance
// criteria always land in their own markdown section.
func TestDescribePrefersDescriptionThenReproSteps(t *testing.T) {
	cases := []struct {
		name                           string
		description, repro, acceptance string
		want                           string
	}{
		{"description wins", "<p>Body</p>", "<p>Repro</p>", "", "Body"},
		{"falls back to repro steps", "", "<p>Repro</p>", "", "Repro"},
		{"appends acceptance criteria", "<p>Body</p>", "", "<p>It works</p>", "Body\n\n## Acceptance criteria\n\nIt works"},
		{"criteria only", "", "", "<p>It works</p>", "## Acceptance criteria\n\nIt works"},
		{"all empty", "", "", "", ""},
	}
	for _, tc := range cases {
		var item workItemResponse
		item.Fields.Description, item.Fields.ReproSteps, item.Fields.AcceptanceCriteria = tc.description, tc.repro, tc.acceptance
		if got := item.describe(); got != tc.want {
			t.Errorf("%s: describe = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEligibleQueriesTagsRanksAndResolvesBlockers(t *testing.T) {
	var gotWIQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wiql"):
			body, _ := io.ReadAll(r.Body)
			var req struct{ Query string }
			_ = json.Unmarshal(body, &req)
			gotWIQL = req.Query
			if got := r.URL.Query().Get("$top"); got != "200" {
				t.Errorf("$top = %q, want 200", got)
			}
			_, _ = w.Write([]byte(`{"workItems":[{"id":11},{"id":12}]}`))
		case r.URL.Query().Get("ids") == "11,12":
			_, _ = w.Write([]byte(`{"value":[
				{"id":11,"fields":{"System.Title":"Blocked","System.State":"New","Microsoft.VSTS.Common.StackRank":100},
				 "relations":[{"rel":"System.LinkTypes.Dependency-Reverse","url":"` + relBase + `99"}]},
				{"id":12,"fields":{"System.Title":"Clear","System.State":"New","Microsoft.VSTS.Common.StackRank":200}}
			]}`))
		case r.URL.Query().Get("ids") == "99":
			_, _ = w.Write([]byte(`{"value":[{"id":99,"fields":{"System.Title":"Blocker","System.State":"Active"}}]}`))
		default:
			t.Errorf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer srv.Close()

	candidates, err := New(srv.URL, "pat").Eligible(context.Background(), "Contoso", BoardScope{}, "ready-for-agent")
	if err != nil {
		t.Fatalf("Eligible returned error: %v", err)
	}
	if !strings.Contains(gotWIQL, "[System.Tags] CONTAINS 'ready-for-agent'") {
		t.Errorf("WIQL = %q, want a tag filter", gotWIQL)
	}
	if !strings.Contains(gotWIQL, "[System.TeamProject] = 'Contoso'") {
		t.Errorf("WIQL = %q, want a project filter", gotWIQL)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(candidates))
	}
	if candidates[0].ID != 11 || candidates[1].ID != 12 {
		t.Errorf("candidate order = %d,%d, want 11,12 (Stack Rank 100 before 200)", candidates[0].ID, candidates[1].ID)
	}
	if candidates[0].BlockersResolved {
		t.Error("candidate 11 has an Active blocker, want BlockersResolved false")
	}
	if !candidates[1].BlockersResolved {
		t.Error("candidate 12 has no blockers, want BlockersResolved true")
	}
}

// An unreadable blocker must hold the ticket back rather than letting the loop
// start work whose dependency it cannot see.
func TestEligibleTreatsUnreadableBlockerAsUnresolved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wiql"):
			_, _ = w.Write([]byte(`{"workItems":[{"id":11}]}`))
		case r.URL.Query().Get("ids") == "11":
			_, _ = w.Write([]byte(`{"value":[{"id":11,"fields":{"System.State":"New"},
				"relations":[{"rel":"System.LinkTypes.Dependency-Reverse","url":"` + relBase + `77"}]}]}`))
		default:
			_, _ = w.Write([]byte(`{"value":[]}`))
		}
	}))
	defer srv.Close()

	candidates, err := New(srv.URL, "pat").Eligible(context.Background(), "Contoso", BoardScope{}, "ready")
	if err != nil {
		t.Fatalf("Eligible returned error: %v", err)
	}
	if len(candidates) != 1 || candidates[0].BlockersResolved {
		t.Errorf("candidates = %+v, want one with BlockersResolved false", candidates)
	}
}

func TestEligibleWithoutReadyLabelIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("Eligible must not call the API with no ready label")
	}))
	defer srv.Close()

	candidates, err := New(srv.URL, "pat").Eligible(context.Background(), "Contoso", BoardScope{}, "  ")
	if err != nil {
		t.Fatalf("Eligible returned error: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none", candidates)
	}
}

func TestChildrenReadsHierarchyForwardRelations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/workitems/10") {
			_, _ = w.Write([]byte(`{"id":10,"fields":{"System.Title":"Epic"},"relations":[
				{"rel":"System.LinkTypes.Hierarchy-Forward","url":"` + relBase + `11"},
				{"rel":"System.LinkTypes.Hierarchy-Forward","url":"` + relBase + `12"}]}`))
			return
		}
		if got := r.URL.Query().Get("ids"); got != "11,12" {
			t.Errorf("ids = %q, want 11,12", got)
		}
		_, _ = w.Write([]byte(`{"value":[
			{"id":11,"fields":{"System.Title":"Open leaf","System.State":"New"}},
			{"id":12,"fields":{"System.Title":"Closed leaf","System.State":"Closed"}}]}`))
	}))
	defer srv.Close()

	children, err := New(srv.URL, "pat").Children(context.Background(), "Contoso", 10)
	if err != nil {
		t.Fatalf("Children returned error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("got %d children, want 2", len(children))
	}
	if children[0].Done() {
		t.Error("child 11 is New, want Done() false")
	}
	if !children[1].Done() {
		t.Error("child 12 is Closed, want Done() true")
	}
}

func TestChildrenOfLeafMakesNoBatchRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ids") != "" {
			t.Error("a childless work item must not trigger a batch read")
		}
		_, _ = w.Write([]byte(`{"id":10,"fields":{"System.Title":"Leaf"}}`))
	}))
	defer srv.Close()

	children, err := New(srv.URL, "pat").Children(context.Background(), "Contoso", 10)
	if err != nil {
		t.Fatalf("Children returned error: %v", err)
	}
	if len(children) != 0 {
		t.Errorf("children = %+v, want none", children)
	}
}

func TestWorkItemsChunksBeyondBatchLimit(t *testing.T) {
	ids := make([]int, batchLimit+5)
	for i := range ids {
		ids[i] = i + 1
	}
	var batches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batches++
		count := len(strings.Split(r.URL.Query().Get("ids"), ","))
		if batches == 1 && count != batchLimit {
			t.Errorf("first batch carried %d ids, want %d", count, batchLimit)
		}
		if batches == 2 && count != 5 {
			t.Errorf("second batch carried %d ids, want 5", count)
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "pat").WorkItems(context.Background(), "Contoso", ids); err != nil {
		t.Fatalf("WorkItems returned error: %v", err)
	}
	if batches != 2 {
		t.Errorf("batches = %d, want 2", batches)
	}
}

func TestWIQLStringDoublesEmbeddedQuotes(t *testing.T) {
	if got := wiqlString("Ann's team"); got != "'Ann''s team'" {
		t.Errorf("wiqlString = %q, want %q", got, "'Ann''s team'")
	}
}

func TestStatesReadsNameAndCategory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/Contoso/_apis/wit/workitemtypes/User%20Story/states"; r.URL.EscapedPath() != want {
			t.Errorf("path = %q, want %q", r.URL.EscapedPath(), want)
		}
		_, _ = w.Write([]byte(`{"value":[{"name":"New","category":"Proposed"},{"name":"Active","category":"InProgress"}]}`))
	}))
	defer srv.Close()

	states, err := New(srv.URL, "pat").States(context.Background(), "Contoso", "User Story")
	if err != nil {
		t.Fatalf("States returned error: %v", err)
	}
	if len(states) != 2 || states[1].Name != "Active" || states[1].Category != "InProgress" {
		t.Errorf("states = %+v, want New/Proposed then Active/InProgress", states)
	}
}
