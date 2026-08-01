package tracker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker/azureapi"
)

const azureRelBase = "https://dev.azure.com/acme/_apis/wit/workItems/"

// azureServer stands in for the organization. Reads are keyed by the route suffix
// the client builds — matched exactly, so /workitems/7 never shadows
// /workitems/7/comments — and every write is recorded under the "patch" key. An
// unrouted request fails the test rather than answering with something wrong.
func azureServer(t *testing.T, routes map[string]string) (*AzureDevOps, *[]recordedPatch) {
	t.Helper()
	patches := &[]recordedPatch{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch || strings.Contains(r.URL.Path, "/workitems/$") {
			body, _ := io.ReadAll(r.Body)
			var ops []recordedOp
			_ = json.Unmarshal(body, &ops)
			*patches = append(*patches, recordedPatch{path: r.URL.Path, ops: ops})
			_, _ = w.Write([]byte(routes["patch"]))
			return
		}
		if body, ok := routes[azureRoute(r)]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		if serveAzureWork(w, r) {
			return
		}
		t.Errorf("unrouted request %s %s (route %q)", r.Method, r.URL.RequestURI(), azureRoute(r))
	}))
	t.Cleanup(srv.Close)
	return &AzureDevOps{
		OrgURL:          srv.URL,
		PAT:             "pat",
		Project:         "Contoso",
		ReadyLabel:      "ready-for-agent",
		QuarantineLabel: "needs-human",
	}, patches
}

// azureRoute reduces a request to the work-item route it addresses: the path with
// the project's API prefix stripped, or the batch read with its id list.
func azureRoute(r *http.Request) string {
	if ids := r.URL.Query().Get("ids"); ids != "" {
		return "/workitems?ids=" + ids
	}
	return strings.TrimPrefix(r.URL.Path, "/Contoso/_apis/wit")
}

// azureBacklogConfig is the Agile process as a project reports it: two portfolio
// backlogs above the requirement one, a taskboard below it, and a bug section the
// team settings place. azureTeamSettings files those bugs as requirements.
const (
	azureBacklogConfig = `{
		"portfolioBacklogs":[
			{"rank":2,"defaultWorkItemType":{"name":"Epic"},"workItemTypes":[{"name":"Epic"}]},
			{"rank":1,"defaultWorkItemType":{"name":"Feature"},"workItemTypes":[{"name":"Feature"}]}],
		"requirementBacklog":{"defaultWorkItemType":{"name":"User Story"},
			"workItemTypes":[{"name":"User Story"},{"name":"Bug"}]},
		"taskBacklog":{"defaultWorkItemType":{"name":"Task"},"workItemTypes":[{"name":"Task"}]},
		"bugWorkItems":{"defaultWorkItemType":{"name":"Bug"},"workItemTypes":[{"name":"Bug"}]}}`
	azureTeamSettings = `{"bugsBehavior":"asRequirements"}`
)

// serveAzureWork answers the Work API reads every azure caller makes before it can
// place a work-item type on a backlog level, reporting whether it handled the
// request so a fake organization keeps its own routes in charge.
func serveAzureWork(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case strings.HasSuffix(r.URL.Path, "/_apis/work/backlogconfiguration"):
		_, _ = w.Write([]byte(azureBacklogConfig))
	case strings.HasSuffix(r.URL.Path, "/_apis/work/teamsettings"):
		_, _ = w.Write([]byte(azureTeamSettings))
	default:
		return false
	}
	return true
}

type recordedPatch struct {
	path string
	ops  []recordedOp
}

type recordedOp struct {
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func (p recordedPatch) value(field string) any {
	for _, op := range p.ops {
		if op.Path == "/fields/"+field {
			return op.Value
		}
	}
	return nil
}

// Work items carry no per-project key, so trau renders them through the repo's
// issue prefix and parses the number back out before every call.
func TestWorkItemIDRoundTrip(t *testing.T) {
	cases := []struct {
		id      string
		want    int
		wantErr bool
	}{
		{"TRAU-1234", 1234, false},
		{"CON-1", 1, false},
		{"1234", 1234, false},
		{" TRAU-7 ", 7, false},
		{"TRAU-0", 0, true},
		{"TRAU-", 0, true},
		{"TRAU-abc", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		got, err := workItemID(tc.id)
		if (err != nil) != tc.wantErr {
			t.Errorf("workItemID(%q) err = %v, wantErr %v", tc.id, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("workItemID(%q) = %d, want %d", tc.id, got, tc.want)
		}
	}
	if got := azureIdentifier(1234); got != "1234" {
		t.Errorf("azureIdentifier = %q, want 1234", got)
	}
	if got := azureParentIdentifier(0); got != "" {
		t.Errorf("azureParentIdentifier(0) = %q, want empty", got)
	}
}

// An error must name the identifier it rejected, not leak the token.
func TestWorkItemIDErrorNamesTheIdentifier(t *testing.T) {
	_, err := workItemID("nope")
	if err == nil {
		t.Fatal("workItemID returned no error")
	}
	if !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("err = %q, want it to quote the rejected identifier", err)
	}
}

func TestMapAzureStatus(t *testing.T) {
	cases := []struct {
		name     string
		category azureapi.StateCategory
		reason   string
		want     IssueStatus
	}{
		{"proposed is open", azureapi.CategoryProposed, "New", StatusOpen},
		{"in progress is started", azureapi.CategoryInProgress, "", StatusStarted},
		{"resolved is still started", azureapi.CategoryResolved, "", StatusStarted},
		{"completed is done", azureapi.CategoryCompleted, "Fixed and verified", StatusDone},
		{"completed but cut is canceled", azureapi.CategoryCompleted, "Cut", StatusCanceled},
		{"completed but duplicate is canceled", azureapi.CategoryCompleted, "Duplicate", StatusCanceled},
		{"removed is canceled", azureapi.CategoryRemoved, "Removed from the backlog", StatusCanceled},
		{"unknown stays unknown", azureapi.CategoryUnknown, "", StatusUnknown},
	}
	for _, tc := range cases {
		if got := mapAzureStatus(tc.category, tc.reason); got != tc.want {
			t.Errorf("%s: mapAzureStatus = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAzurePickSkipsContainersBlockedAndStartedWork(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/wiql": `{"workItems":[{"id":1},{"id":2},{"id":3},{"id":4}]}`,
		"/workitems?ids=1,2,3,4": `{"value":[
			{"id":1,"fields":{"System.Title":"Epic","System.State":"New","Microsoft.VSTS.Common.Priority":1},
			 "relations":[{"rel":"System.LinkTypes.Hierarchy-Forward","url":"` + azureRelBase + `9"}]},
			{"id":2,"fields":{"System.Title":"Already active","System.State":"Active","Microsoft.VSTS.Common.Priority":1}},
			{"id":3,"fields":{"System.Title":"Blocked","System.State":"New","Microsoft.VSTS.Common.Priority":1},
			 "relations":[{"rel":"System.LinkTypes.Dependency-Reverse","url":"` + azureRelBase + `8"}]},
			{"id":4,"fields":{"System.Title":"Runnable","System.State":"New","Microsoft.VSTS.Common.Priority":2}}
		]}`,
		"/workitems?ids=8": `{"value":[{"id":8,"fields":{"System.State":"Active"}}]}`,
	})

	got, err := az.Pick(context.Background(), Scope{Team: "Contoso", Prefix: "CON"})
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if got != "4" {
		t.Errorf("Pick = %q, want 4 (1 is a container, 2 is started, 3 is blocked)", got)
	}
}

// A childless Feature carrying the ready tag is work nobody has broken down yet,
// not a slice to build. HasChildren cannot tell the two apart, so the level the
// project's own backlog configuration places the type on does.
func TestAzurePickSkipsAFeatureWithNoChildren(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/wiql": `{"workItems":[{"id":1},{"id":2}]}`,
		"/workitems?ids=1,2": `{"value":[
			{"id":1,"fields":{"System.Title":"Keep the hierarchy","System.State":"New",
				"System.WorkItemType":"Feature","System.Tags":"ready-for-agent",
				"Microsoft.VSTS.Common.Priority":1}},
			{"id":2,"fields":{"System.Title":"Carry the level through sync","System.State":"New",
				"System.WorkItemType":"User Story","Microsoft.VSTS.Common.Priority":2}}]}`,
	})

	got, err := az.Pick(context.Background(), Scope{Team: "Contoso", Prefix: "CON"})
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if got != "2" {
		t.Errorf("Pick = %q, want 2 (1 sits above requirement level)", got)
	}
}

func TestAzurePickReturnsEmptyWhenNothingEligible(t *testing.T) {
	az, _ := azureServer(t, map[string]string{"/wiql": `{"workItems":[]}`})

	got, err := az.Pick(context.Background(), Scope{Team: "Contoso", Prefix: "CON"})
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if got != "" {
		t.Errorf("Pick = %q, want empty", got)
	}
}

// Epic scope narrows the ranked queue to the epic's own unfinished leaves.
func TestAzurePickEpicScopeKeepsOnlyLeaves(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/workitems/10": `{"id":10,"fields":{"System.Title":"Epic"},"relations":[
			{"rel":"System.LinkTypes.Hierarchy-Forward","url":"` + azureRelBase + `11"},
			{"rel":"System.LinkTypes.Hierarchy-Forward","url":"` + azureRelBase + `12"}]}`,
		"/workitems?ids=11,12": `{"value":[
			{"id":11,"fields":{"System.Title":"Done leaf","System.State":"Closed"}},
			{"id":12,"fields":{"System.Title":"Open leaf","System.State":"New"}}]}`,
		"/wiql": `{"workItems":[{"id":13},{"id":12},{"id":11}]}`,
		"/workitems?ids=13,12,11": `{"value":[
			{"id":11,"fields":{"System.State":"New","Microsoft.VSTS.Common.Priority":1}},
			{"id":12,"fields":{"System.State":"New","Microsoft.VSTS.Common.Priority":2}},
			{"id":13,"fields":{"System.State":"New","Microsoft.VSTS.Common.Priority":1}}]}`,
	})

	got, err := az.Pick(context.Background(), Scope{Parent: "CON-10", Team: "Contoso", Prefix: "CON"})
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if got != "12" {
		t.Errorf("Pick = %q, want 12 (11 is already closed, 13 is outside the epic)", got)
	}
}

func TestAzurePickEpicWithNoLeavesSkipsTheQueue(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/workitems/10": `{"id":10,"fields":{"System.Title":"Empty epic"}}`,
	})

	got, err := az.Pick(context.Background(), Scope{Parent: "CON-10", Team: "Contoso", Prefix: "CON"})
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if got != "" {
		t.Errorf("Pick = %q, want empty", got)
	}
}

// Unlike Pick, ListEligible keeps containers so the caller can group them.
func TestAzureListEligibleKeepsContainers(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/wiql": `{"workItems":[{"id":1},{"id":2}]}`,
		"/workitems?ids=1,2": `{"value":[
			{"id":1,"fields":{"System.Title":"Epic","System.State":"New","System.Tags":"ready-for-agent"},
			 "relations":[{"rel":"System.LinkTypes.Hierarchy-Forward","url":"` + azureRelBase + `2"}]},
			{"id":2,"fields":{"System.Title":"Leaf","System.State":"New","System.Tags":"ready-for-agent; ui"},
			 "relations":[{"rel":"System.LinkTypes.Hierarchy-Reverse","url":"` + azureRelBase + `1"}]}
		]}`,
	})

	list, err := az.ListEligible(context.Background(), Scope{Team: "Contoso", Prefix: "CON"})
	if err != nil {
		t.Fatalf("ListEligible returned error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d tickets, want 2: %+v", len(list), list)
	}
	if list[0].ID != "1" || !list[0].HasChildren {
		t.Errorf("first ticket = %+v, want 1 with children", list[0])
	}
	if list[1].ID != "2" || list[1].Parent != "1" {
		t.Errorf("second ticket = %+v, want 2 parented to 1", list[1])
	}
	if !slices.Equal(list[1].Labels, []string{"ready-for-agent", "ui"}) {
		t.Errorf("labels = %v, want [ready-for-agent ui]", list[1].Labels)
	}
}

func TestAzureSubIssuesAreWorkItemNumbers(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/workitems/10": `{"id":10,"fields":{},"relations":[
			{"rel":"System.LinkTypes.Hierarchy-Forward","url":"` + azureRelBase + `11"},
			{"rel":"System.LinkTypes.Hierarchy-Forward","url":"` + azureRelBase + `12"}]}`,
		"/workitems?ids=11,12": `{"value":[
			{"id":11,"fields":{"System.Title":"Nested parent","System.State":"New"},
			 "relations":[{"rel":"System.LinkTypes.Hierarchy-Forward","url":"` + azureRelBase + `99"}]},
			{"id":12,"fields":{"System.Title":"Finished","System.State":"Done"}}]}`,
	})

	subs, err := az.SubIssues(context.Background(), "WIDGET-10")
	if err != nil {
		t.Fatalf("SubIssues returned error: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("got %d sub-issues, want 2", len(subs))
	}
	if subs[0].ID != "11" || !subs[0].HasChildren || subs[0].Done {
		t.Errorf("first sub-issue = %+v, want 11 with children and not done", subs[0])
	}
	if subs[1].ID != "12" || !subs[1].Done {
		t.Errorf("second sub-issue = %+v, want 12 done", subs[1])
	}
}

func TestAzureTitleAndProjectAndParent(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/workitems/7": `{"id":7,"fields":{"System.Title":"Ship it","System.TeamProject":"Contoso"},
			"relations":[{"rel":"System.LinkTypes.Hierarchy-Reverse","url":"` + azureRelBase + `3"}]}`,
	})
	ctx := context.Background()

	title, err := az.Title(ctx, "CON-7")
	if err != nil || title != "Ship it" {
		t.Errorf("Title = (%q, %v), want (Ship it, nil)", title, err)
	}
	project, err := az.IssueProject(ctx, "CON-7")
	if err != nil || project != "Contoso" {
		t.Errorf("IssueProject = (%q, %v), want (Contoso, nil)", project, err)
	}
	parent, err := az.ParentIssue(ctx, "CON-7")
	if err != nil || parent != "3" {
		t.Errorf("ParentIssue = (%q, %v), want (3, nil)", parent, err)
	}
}

func TestAzureIssueDetailRendersBodyAndComments(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/workitems/7/comments": `{"comments":[{"text":"<div>Ping</div>","createdBy":{"displayName":"Ada L"}}]}`,
		"/workitems/7": `{"id":7,"fields":{
			"System.Title":"Ship it",
			"System.Tags":"ready-for-agent",
			"System.Description":"<div>Do the <b>thing</b></div>",
			"Microsoft.VSTS.Common.AcceptanceCriteria":"<ul><li>It works</li></ul>"}}`,
	})

	detail, err := az.IssueDetail(context.Background(), "CON-7")
	if err != nil {
		t.Fatalf("IssueDetail returned error: %v", err)
	}
	want := "Do the **thing**\n\n## Acceptance criteria\n\n- It works"
	if detail.Description != want {
		t.Errorf("description = %q, want %q", detail.Description, want)
	}
	if !slices.Equal(detail.Labels, []string{"ready-for-agent"}) {
		t.Errorf("labels = %v, want [ready-for-agent]", detail.Labels)
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Author != "Ada L" || detail.Comments[0].Body != "Ping" {
		t.Errorf("comments = %+v, want one from Ada L saying Ping", detail.Comments)
	}
}

// The discussion is an enrichment on top of an enrichment: losing it must not cost
// the prompt the description that already loaded.
func TestAzureIssueDetailSurvivesCommentFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":7,"fields":{"System.Title":"Ship it","System.Description":"<p>Body</p>"}}`))
	}))
	defer srv.Close()

	az := &AzureDevOps{OrgURL: srv.URL, PAT: "pat", Project: "Contoso"}
	detail, err := az.IssueDetail(context.Background(), "CON-7")
	if err != nil {
		t.Fatalf("IssueDetail returned error: %v", err)
	}
	if detail.Description != "Body" {
		t.Errorf("description = %q, want Body", detail.Description)
	}
	if len(detail.Comments) != 0 {
		t.Errorf("comments = %+v, want none", detail.Comments)
	}
}

func TestAzureIssueStatusMapsThroughCategory(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/workitems/7": `{"id":7,"fields":{"System.State":"Closed","System.Reason":"Cut"}}`,
	})

	got, err := az.IssueStatus(context.Background(), "CON-7")
	if err != nil {
		t.Fatalf("IssueStatus returned error: %v", err)
	}
	if got != StatusCanceled {
		t.Errorf("IssueStatus = %q, want %q (Closed with reason Cut was dropped, not delivered)", got, StatusCanceled)
	}
}

func TestAzureQuarantineSwapsTagsAndComments(t *testing.T) {
	az, patches := azureServer(t, map[string]string{
		"/workitems/7": `{"id":7,"fields":{"System.Tags":"ready-for-agent; ui"}}`,
		"patch":        `{"id":7}`,
	})

	if err := az.Quarantine(context.Background(), "CON-7", "QA never went green"); err != nil {
		t.Fatalf("Quarantine returned error: %v", err)
	}
	if len(*patches) != 2 {
		t.Fatalf("got %d patches, want 2 (tags then comment): %+v", len(*patches), *patches)
	}
	if got := (*patches)[0].value("System.Tags"); got != "ui; needs-human" {
		t.Errorf("tags = %v, want %q", got, "ui; needs-human")
	}
	body, _ := (*patches)[1].value("System.History").(string)
	if !strings.Contains(body, "QA never went green") {
		t.Errorf("comment = %q, want it to carry the reason", body)
	}
}

func TestAzureResetRestoresReadyTagAndUnstartedState(t *testing.T) {
	az, patches := azureServer(t, map[string]string{
		"/workitems/7":               `{"id":7,"fields":{"System.Tags":"needs-human","System.WorkItemType":"Task"}}`,
		"/workitemtypes/Task/states": `{"value":[{"name":"New","category":"Proposed"},{"name":"Active","category":"InProgress"}]}`,
		"patch":                      `{"id":7}`,
	})

	if err := az.Reset(context.Background(), "CON-7"); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}
	if len(*patches) != 2 {
		t.Fatalf("got %d patches, want 2 (tags then state): %+v", len(*patches), *patches)
	}
	if got := (*patches)[0].value("System.Tags"); got != "ready-for-agent" {
		t.Errorf("tags = %v, want ready-for-agent", got)
	}
	if got := (*patches)[1].value("System.State"); got != "New" {
		t.Errorf("state = %v, want New (the template's Proposed state)", got)
	}
}

// The loop asks for "In Review"; a Scrum project has no state by that name and no
// Resolved category either, so the write must still land on live work.
func TestAzureSetStatusResolvesLoopTargetOnScrumTemplate(t *testing.T) {
	az, patches := azureServer(t, map[string]string{
		"/workitems/7": `{"id":7,"fields":{"System.WorkItemType":"Product Backlog Item"}}`,
		"/workitemtypes/Product Backlog Item/states": `{"value":[{"name":"New","category":"Proposed"},{"name":"Approved","category":"Proposed"},
			{"name":"Committed","category":"InProgress"},{"name":"Done","category":"Completed"}]}`,
		"patch": `{"id":7}`,
	})

	if err := az.SetStatus(context.Background(), "CON-7", StageInReview, "PR is up."); err != nil {
		t.Fatalf("SetStatus returned error: %v", err)
	}
	if len(*patches) != 1 {
		t.Fatalf("got %d patches, want 1", len(*patches))
	}
	if got := (*patches)[0].value("System.State"); got != "Committed" {
		t.Errorf("state = %v, want Committed", got)
	}
	body, _ := (*patches)[0].value("System.History").(string)
	if !strings.Contains(body, "PR is up.") {
		t.Errorf("comment = %q, want the extra note attached", body)
	}
}

func TestAzureAddAndRemoveLabelIgnoreBlanks(t *testing.T) {
	az, patches := azureServer(t, map[string]string{})
	ctx := context.Background()

	if err := az.AddLabel(ctx, "CON-7", "  "); err != nil {
		t.Errorf("AddLabel returned error: %v", err)
	}
	if err := az.RemoveLabel(ctx, "CON-7", ""); err != nil {
		t.Errorf("RemoveLabel returned error: %v", err)
	}
	if len(*patches) != 0 {
		t.Errorf("patches = %+v, want none", *patches)
	}
}

func TestAzureFileBugCreatesTaggedWorkItem(t *testing.T) {
	dir := t.TempDir()
	verdict := filepath.Join(dir, "verdict.json")
	if err := os.WriteFile(verdict, []byte(`{"summary":"login broken","failures":["auth 500"]}`), 0o600); err != nil {
		t.Fatalf("write verdict: %v", err)
	}
	az, patches := azureServer(t, map[string]string{"patch": `{"id":500}`})

	bug, err := az.FileBug(context.Background(), "CON-7", verdict)
	if err != nil {
		t.Fatalf("FileBug returned error: %v", err)
	}
	if bug != "500" {
		t.Errorf("FileBug = %q, want 500", bug)
	}
	if len(*patches) != 1 {
		t.Fatalf("got %d patches, want 1", len(*patches))
	}
	if !strings.HasSuffix((*patches)[0].path, "/workitems/$Bug") {
		t.Errorf("path = %q, want it to create a $Bug", (*patches)[0].path)
	}
	if got := (*patches)[0].value("System.Tags"); got != "HITL" {
		t.Errorf("tags = %v, want HITL", got)
	}
	title, _ := (*patches)[0].value("System.Title").(string)
	if !strings.Contains(title, "login broken") {
		t.Errorf("title = %q, want the verdict summary", title)
	}
	body, _ := (*patches)[0].value("System.Description").(string)
	if !strings.Contains(body, "auth 500") {
		t.Errorf("description = %q, want the specific failure", body)
	}
}

func TestAzureListTeamsUsesProjectNameAsKey(t *testing.T) {
	az, _ := azureServer(t, map[string]string{
		"/_apis/projects": `{"value":[{"id":"g1","name":"Contoso"},{"id":"g2","name":"Fabrikam Fiber"}]}`,
	})

	teams, err := az.ListTeams(context.Background())
	if err != nil {
		t.Fatalf("ListTeams returned error: %v", err)
	}
	if len(teams) != 2 {
		t.Fatalf("got %d teams, want 2", len(teams))
	}
	if teams[1].Key != "Fabrikam Fiber" || teams[1].Name != "Fabrikam Fiber" {
		t.Errorf("team = %+v, want key and name both Fabrikam Fiber", teams[1])
	}
}

// Tags are freeform, so there is nothing to pre-create.
func TestAzureEnsureLabelsIsANoOp(t *testing.T) {
	az, _ := azureServer(t, map[string]string{})
	if err := az.EnsureLabels(context.Background()); err != nil {
		t.Errorf("EnsureLabels returned error: %v", err)
	}
}

func TestNewBuildsAzureFromRESTConfig(t *testing.T) {
	pm, err := New("azure", nil, Config{
		Team:            "Contoso",
		BaseURL:         "https://dev.azure.com/acme",
		APIKey:          "pat",
		ReadyLabel:      "ready-for-agent",
		QuarantineLabel: "needs-human",
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	az, ok := pm.(*AzureDevOps)
	if !ok {
		t.Fatalf("New returned %T, want *AzureDevOps", pm)
	}
	if az.OrgURL != "https://dev.azure.com/acme" || az.PAT != "pat" || az.Project != "Contoso" {
		t.Errorf("tracker = %+v, want the REST config mapped onto it", az)
	}
}

// The optional capabilities the loop type-asserts for must all be satisfied,
// otherwise an azure repo silently loses epic stacking, status reconcile or the
// injected ticket context.
func TestAzureSatisfiesOptionalCapabilities(t *testing.T) {
	var pm Tracker = &AzureDevOps{}
	if _, ok := pm.(TicketLister); !ok {
		t.Error("AzureDevOps does not implement TicketLister")
	}
	if _, ok := pm.(IssueDetailer); !ok {
		t.Error("AzureDevOps does not implement IssueDetailer")
	}
	if _, ok := pm.(IssueStatuser); !ok {
		t.Error("AzureDevOps does not implement IssueStatuser")
	}
	if _, ok := pm.(IssueProjecter); !ok {
		t.Error("AzureDevOps does not implement IssueProjecter")
	}
	if _, ok := pm.(IssueParenter); !ok {
		t.Error("AzureDevOps does not implement IssueParenter")
	}
	if _, ok := pm.(IssueLabeler); !ok {
		t.Error("AzureDevOps does not implement IssueLabeler")
	}
	if _, ok := pm.(IssueLabelRemover); !ok {
		t.Error("AzureDevOps does not implement IssueLabelRemover")
	}
	if _, ok := pm.(TeamLister); !ok {
		t.Error("AzureDevOps does not implement TeamLister")
	}
}
