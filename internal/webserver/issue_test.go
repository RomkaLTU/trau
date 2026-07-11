package webserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
)

func getIssue(t *testing.T, ts *httptest.Server, repo, id string) (*http.Response, IssueResponse) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/issues/" + id)
	if err != nil {
		t.Fatalf("GET issue: %v", err)
	}
	var out IssueResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode issue: %v", err)
		}
	}
	return res, out
}

func TestIssueReturnsTicket(t *testing.T) {
	fake := &fakeReader{issue: tracker.IssueSummary{
		BacklogItem: tracker.BacklogItem{
			ID:     "COD-712",
			Title:  "Fix booking overlap",
			Status: "Todo",
			Group:  tracker.StatusGroupUnstarted,
			Labels: []string{"ready-for-agent"},
			Ready:  true,
		},
		Project:   "trau",
		InProject: true,
	}}
	_, ts, _, _ := backlogServer(t, fake, nil)

	res, out := getIssue(t, ts, "acme", "COD-712")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.ID != "COD-712" || out.Title != "Fix booking overlap" {
		t.Errorf("issue = %+v, want the COD-712 ticket", out)
	}
	if out.Status != "Todo" || out.Group != "unstarted" || !out.Ready {
		t.Errorf("issue status/group/ready = %q/%q/%v, want Todo/unstarted/true", out.Status, out.Group, out.Ready)
	}
	if !out.InProject || out.Project != "trau" {
		t.Errorf("in_project/project = %v/%q, want true/trau", out.InProject, out.Project)
	}
	if out.Provider != "linear" {
		t.Errorf("provider = %q, want linear (the resolved default)", out.Provider)
	}
}

func TestIssueCrossProjectIsReturnedNotHidden(t *testing.T) {
	// A ticket from another project exists but is out of this repo's scope: it
	// comes back 200 with in_project=false so the form can refuse it clearly.
	fake := &fakeReader{issue: tracker.IssueSummary{
		BacklogItem: tracker.BacklogItem{
			ID:     "M4C-54",
			Title:  "Community resource library",
			Status: "In Progress",
			Group:  tracker.StatusGroupStarted,
		},
		Project:   "M4C",
		InProject: false,
	}}
	_, ts, _, _ := backlogServer(t, fake, nil)

	res, out := getIssue(t, ts, "acme", "M4C-54")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.InProject {
		t.Error("in_project = true, want false for a cross-project ticket")
	}
	if out.Project != "M4C" {
		t.Errorf("project = %q, want M4C", out.Project)
	}
}

func TestIssueNotFound(t *testing.T) {
	_, ts, _, _ := backlogServer(t, &fakeReader{issueErr: tracker.ErrIssueNotFound}, nil)
	res, _ := getIssue(t, ts, "acme", "COD-999")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown ticket", res.StatusCode)
	}
}

func TestIssueUnknownRepo(t *testing.T) {
	_, ts, _, _ := backlogServer(t, &fakeReader{}, nil)
	res, _ := getIssue(t, ts, "nope", "COD-1")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestIssueWithoutCredentials(t *testing.T) {
	_, ts, _, _ := backlogServer(t, nil, tracker.ErrReaderUnavailable)
	res, _ := getIssue(t, ts, "acme", "COD-1")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 when the repo has no direct tracker credentials", res.StatusCode)
	}
}

func TestIssueServedFromStore(t *testing.T) {
	// A stored synced issue is served from the store — content, comments, and all —
	// without consulting the tracker reader (whose title differs to prove it).
	fake := &fakeReader{issue: tracker.IssueSummary{
		BacklogItem: tracker.BacklogItem{ID: "COD-712", Title: "Reader title"},
		InProject:   true,
	}}
	_, ts, root, store := backlogServer(t, fake, nil)
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{{
		Identifier:  "COD-712",
		Title:       "Store title",
		Description: "The full description.",
		Status:      "Todo",
		StatusGroup: "unstarted",
		Labels:      []string{"ready-for-agent"},
		Comments:    []hubstore.Comment{{ExternalID: "c1", Author: "alice", Body: "a comment"}},
	}}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}

	res, out := getIssue(t, ts, "acme", "COD-712")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Title != "Store title" {
		t.Errorf("title = %q, want the store title (reads must not hit the tracker)", out.Title)
	}
	if out.Description != "The full description." {
		t.Errorf("description = %q, want the stored description", out.Description)
	}
	if len(out.Comments) != 1 || out.Comments[0].Body != "a comment" {
		t.Errorf("comments = %+v, want the stored comment", out.Comments)
	}
	if out.Source != "linear" || !out.InProject {
		t.Errorf("source/in_project = %q/%v, want linear/true", out.Source, out.InProject)
	}
}

func TestIssueMirrorUpdatesSyncedRow(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{{
		Identifier:  "COD-712",
		Title:       "Fix booking",
		Status:      "Todo",
		StatusGroup: "unstarted",
		Labels:      []string{"ready-for-agent"},
	}}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}

	res := postIssueMirror(t, ts, "acme", "COD-712", SyncedMirrorRequest{
		Status:       "In Progress",
		StatusGroup:  "started",
		AddLabels:    []string{"in-progress"},
		RemoveLabels: []string{"ready-for-agent"},
	})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	_, out := getIssue(t, ts, "acme", "COD-712")
	if out.Status != "In Progress" || out.Group != "started" {
		t.Errorf("status/group = %q/%q, want In Progress/started", out.Status, out.Group)
	}
	if hasLabel(out.Labels, "ready-for-agent") || !hasLabel(out.Labels, "in-progress") {
		t.Errorf("labels = %v, want the ready→in-progress swap mirrored", out.Labels)
	}
}

func TestIssueMirrorRejectsNonSynced(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, err := store.CreateInternal(root, "LOOP", hubstore.InternalDraft{Title: "internal only"}); err != nil {
		t.Fatalf("seed internal: %v", err)
	}

	// An unknown identifier and an internal issue are both un-mirrorable: only synced
	// rows carry a tracker write.
	for _, id := range []string{"COD-999", "LOOP-1"} {
		res := postIssueMirror(t, ts, "acme", id, SyncedMirrorRequest{StatusGroup: "started"})
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("mirror %s status = %d, want 404", id, res.StatusCode)
		}
	}
}

func postIssueMirror(t *testing.T, ts *httptest.Server, repo, id string, req SyncedMirrorRequest) *http.Response {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal mirror: %v", err)
	}
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/issues/"+id, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST issue mirror: %v", err)
	}
	return res
}

func TestIssueLabelsNeverNull(t *testing.T) {
	fake := &fakeReader{issue: tracker.IssueSummary{
		BacklogItem: tracker.BacklogItem{ID: "COD-5", Title: "No labels", Status: "Todo", Group: tracker.StatusGroupUnstarted},
		InProject:   true,
	}}
	_, ts, _, _ := backlogServer(t, fake, nil)
	res, out := getIssue(t, ts, "acme", "COD-5")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Labels == nil {
		t.Error("labels serialized as null, want an empty array")
	}
}
