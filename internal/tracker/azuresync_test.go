package tracker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// azureSyncServer stands in for the organization on the hub's read path: the WIQL
// query answers with ids, the batch read with details, and every other route is
// keyed by its request path. Each WIQL body is recorded so a test can assert what
// the pull filtered server-side, and an unrouted request fails the test — a work
// item that reports no comments must never cost a discussion read.
func azureSyncServer(t *testing.T, routes map[string]string, teams ...string) (Reader, *[]string) {
	t.Helper()
	wiql := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wiql") {
			var sent struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&sent)
			*wiql = append(*wiql, sent.Query)
			_, _ = w.Write([]byte(routes["wiql"]))
			return
		}
		if body, ok := routes[azureSyncRoute(r)]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		if serveAzureWork(w, r) {
			return
		}
		t.Errorf("unrouted request %s %s", r.Method, r.URL.RequestURI())
	}))
	t.Cleanup(srv.Close)

	reader, err := NewReader("azure", Config{
		Team:       "Contoso",
		APIKey:     "pat",
		BaseURL:    srv.URL,
		AreaPath:   `Contoso\Platform`,
		BoardTeams: teams,
		ReadyLabel: "ready-for-agent",
	})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return reader, wiql
}

func azureSyncRoute(r *http.Request) string {
	if ids := r.URL.Query().Get("ids"); ids != "" {
		return "ids=" + ids
	}
	return r.URL.Path
}

const (
	azureSyncIDs   = `{"workItems":[{"id":7},{"id":9}]}`
	azureSyncBatch = `{"value":[
		{"id":7,"fields":{
			"System.Title":"Sync the board",
			"System.State":"Active",
			"System.WorkItemType":"User Story",
			"System.TeamProject":"Contoso",
			"System.Tags":"ready-for-agent; platform",
			"System.AreaPath":"Contoso\\Platform",
			"System.Description":"<p>Body</p>",
			"System.CreatedDate":"2026-07-01T09:00:00Z",
			"System.ChangedDate":"2026-07-30T11:22:33.44Z",
			"System.CommentCount":1,
			"Microsoft.VSTS.Common.Priority":2,
			"System.AssignedTo":{"id":"user-1","displayName":"Ada Lovelace"}},
		 "relations":[
			{"rel":"System.LinkTypes.Hierarchy-Reverse","url":"https://dev.azure.com/acme/_apis/wit/workItems/4"},
			{"rel":"System.LinkTypes.Dependency-Reverse","url":"https://dev.azure.com/acme/_apis/wit/workItems/9"}]},
		{"id":9,"fields":{
			"System.Title":"Land the WIQL client",
			"System.State":"Closed",
			"System.Reason":"Completed",
			"System.TeamProject":"Contoso",
			"System.ChangedDate":"2026-07-29T10:00:00Z"}}]}`
	azureSyncBlockers = `{"value":[{"id":9,"fields":{"System.State":"Closed","System.Reason":"Completed"}}]}`
	azureSyncComments = `{"comments":[{"id":31,"text":"<p>Board is dark.</p>",
		"createdBy":{"displayName":"Grace Hopper"},
		"createdDate":"2026-07-28T12:00:00Z","modifiedDate":"2026-07-28T12:05:00Z"}]}`
	azureSyncIdentity = `{"authenticatedUser":{"id":"pat-owner","providerDisplayName":"Ada Lovelace"}}`
)

// The hub's Azure DevOps sync: WIQL narrows the team project server-side, the
// batch read fills the details, and the work items come back keyed by the numbers
// the board and the store speak.
func TestAzureReaderSync(t *testing.T) {
	reader, wiql := azureSyncServer(t, map[string]string{
		"wiql":    azureSyncIDs,
		"ids=7,9": azureSyncBatch,
		"ids=9":   azureSyncBlockers,
		"/Contoso/_apis/wit/workitems/7/comments": azureSyncComments,
		"/_apis/connectionData":                   azureSyncIdentity,
	})
	ctx := context.Background()

	binding, err := reader.ResolveBinding(ctx)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	if binding.Project != "Contoso" || binding.ProjectID != "Contoso" {
		t.Fatalf("binding = %+v, want the team project in both fields", binding)
	}

	t.Run("pull", func(t *testing.T) {
		pulled, err := reader.SyncPull(ctx, binding, "2026-07-30T11:00:00.123Z")
		if err != nil {
			t.Fatalf("SyncPull: %v", err)
		}
		if len(pulled) != 2 {
			t.Fatalf("pulled %d issues, want 2", len(pulled))
		}

		iss := pulled[0]
		if iss.ID != "7" || iss.ExternalID != "7" {
			t.Errorf("identifiers = %q/%q, want 7/7", iss.ID, iss.ExternalID)
		}
		if iss.Title != "Sync the board" || iss.Description != "Body" {
			t.Errorf("title/description = %q/%q", iss.Title, iss.Description)
		}
		if iss.Status != "Active" || iss.Group != StatusGroupStarted {
			t.Errorf("status = %q/%q, want Active/started", iss.Status, iss.Group)
		}
		if iss.Type != "User Story" || iss.Level != "requirement" {
			t.Errorf("type/level = %q/%q, want User Story/requirement", iss.Type, iss.Level)
		}
		if iss.Priority != 2 || !slices.Equal(iss.Labels, []string{"ready-for-agent", "platform"}) {
			t.Errorf("priority/labels = %d/%v", iss.Priority, iss.Labels)
		}
		if iss.Parent != "4" {
			t.Errorf("parent = %q, want 4", iss.Parent)
		}
		if iss.AssigneeID != "user-1" || iss.AssigneeName != "Ada Lovelace" {
			t.Errorf("assignee = %q/%q", iss.AssigneeID, iss.AssigneeName)
		}
		if iss.CreatedAt != "2026-07-01T09:00:00Z" || iss.UpdatedAt != "2026-07-30T11:22:33.44Z" {
			t.Errorf("stamps = %q/%q", iss.CreatedAt, iss.UpdatedAt)
		}
		if !strings.HasSuffix(iss.URL, "/Contoso/_workitems/edit/7") {
			t.Errorf("url = %q, want the board's edit link", iss.URL)
		}
		if want := []SyncedBlocker{{ID: "9", Resolved: true}}; !slices.Equal(iss.BlockedBy, want) {
			t.Errorf("blocked by = %+v, want %+v", iss.BlockedBy, want)
		}
		if len(iss.Comments) != 1 {
			t.Fatalf("comments = %d, want 1", len(iss.Comments))
		}
		if c := iss.Comments[0]; c.ExternalID != "31" || c.Author != "Grace Hopper" || c.Body != "Board is dark." {
			t.Errorf("comment = %+v", c)
		}

		if closed := pulled[1]; closed.ID != "9" || closed.Group != StatusGroupDone || len(closed.Comments) != 0 {
			t.Errorf("closed issue = %q/%q with %d comments, want 9/done with none",
				closed.ID, closed.Group, len(closed.Comments))
		}

		query := (*wiql)[0]
		if !strings.Contains(query, `[System.AreaPath] UNDER 'Contoso\Platform'`) {
			t.Errorf("query %q does not narrow to the configured area path", query)
		}
		if !strings.Contains(query, `[System.ChangedDate] >= '2026-07-30T11:00:00Z'`) {
			t.Errorf("query %q does not resume from the cursor at second precision", query)
		}
	})

	t.Run("identifiers", func(t *testing.T) {
		ids, err := reader.ProjectIdentifiers(ctx, binding)
		if err != nil {
			t.Fatalf("ProjectIdentifiers: %v", err)
		}
		if want := []string{"7", "9"}; !slices.Equal(ids, want) {
			t.Errorf("identifiers = %v, want %v", ids, want)
		}
		if query := (*wiql)[len(*wiql)-1]; strings.Contains(query, "System.ChangedDate") {
			t.Errorf("query %q is incremental, want the full identifier set", query)
		}
	})

	t.Run("identity", func(t *testing.T) {
		id, name, err := reader.Identity(ctx)
		if err != nil {
			t.Fatalf("Identity: %v", err)
		}
		if id != "pat-owner" || name != "Ada Lovelace" {
			t.Errorf("identity = %q/%q, want pat-owner/Ada Lovelace", id, name)
		}
	})
}

// AZURE_TEAMS scopes a repo's mirror to the areas the named team's own board covers,
// so two repos on one team project stop mirroring each other's work.
func TestAzureReaderScopesToNamedTeams(t *testing.T) {
	reader, wiql := azureSyncServer(t, map[string]string{
		"wiql":    azureSyncIDs,
		"ids=7,9": azureSyncBatch,
		"ids=9":   azureSyncBlockers,
		"/Contoso/_apis/wit/workitems/7/comments": azureSyncComments,
		"/Contoso/Contoso Platform/_apis/work/teamsettings/teamfieldvalues": `{"field":{"referenceName":"System.AreaPath"},` +
			`"values":[{"value":"Contoso\\Platform\\Api","includeChildren":true}]}`,
	}, "Contoso Platform")

	pulled, err := reader.SyncPull(context.Background(), ProjectBinding{}, "")
	if err != nil {
		t.Fatalf("SyncPull: %v", err)
	}
	if len(pulled) != 2 || pulled[0].ID != "7" {
		t.Fatalf("pulled = %+v, want the board's work items keyed by number", pulled)
	}
	if want := `[System.AreaPath] UNDER 'Contoso\Platform\Api'`; !strings.Contains((*wiql)[0], want) {
		t.Errorf("query %q does not narrow to the team's own area", (*wiql)[0])
	}
}

// A by-id read has to answer for the same slice of the board the hub mirrors: a work
// item in the team project but under another team's area is work the loop would never
// pick, so queue-by-id must not confirm it either.
func TestAzureReaderIssueScopesToTheBoard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Contoso/Contoso Platform/_apis/work/teamsettings/teamfieldvalues":
			_, _ = w.Write([]byte(`{"field":{"referenceName":"System.AreaPath"},` +
				`"values":[{"value":"Contoso\\Platform","includeChildren":true}]}`))
		case "/_apis/wit/workitems/6694":
			_, _ = w.Write([]byte(`{"id":6694,"fields":{"System.Title":"Widen the id contract",` +
				`"System.State":"New","System.TeamProject":"Contoso","System.AreaPath":"Contoso\\Platform\\Api"}}`))
		case "/_apis/wit/workitems/7001":
			_, _ = w.Write([]byte(`{"id":7001,"fields":{"System.Title":"Refund ledger",` +
				`"System.State":"New","System.TeamProject":"Contoso","System.AreaPath":"Contoso\\Payments"}}`))
		case "/Contoso/_apis/wit/wiql":
			var sent struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&sent)
			if strings.Contains(sent.Query, "[System.Id] = 6694") {
				_, _ = w.Write([]byte(`{"workItems":[{"id":6694}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"workItems":[]}`))
		default:
			t.Errorf("unrouted request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	t.Cleanup(srv.Close)

	reader, err := NewReader("azure", Config{
		Team:       "Contoso",
		APIKey:     "pat",
		BaseURL:    srv.URL,
		BoardTeams: []string{"Contoso Platform"},
	})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	ctx := context.Background()

	off, err := reader.Issue(ctx, "7001")
	if err != nil {
		t.Fatalf("Issue 7001: %v", err)
	}
	if off.InProject {
		t.Error(`in_project = true for 7001 under Contoso\Payments, want it refused as off the board`)
	}
	if off.Project != "Contoso" {
		t.Errorf("project = %q, want the team project the work item really belongs to", off.Project)
	}

	on, err := reader.Issue(ctx, "6694")
	if err != nil {
		t.Fatalf("Issue 6694: %v", err)
	}
	if !on.InProject {
		t.Error("in_project = false for 6694, want the team's own work item confirmed")
	}
}
