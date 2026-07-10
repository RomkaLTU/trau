package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	ts := backlogServer(t, fake, nil)

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
	ts := backlogServer(t, fake, nil)

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
	ts := backlogServer(t, &fakeReader{issueErr: tracker.ErrIssueNotFound}, nil)
	res, _ := getIssue(t, ts, "acme", "COD-999")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown ticket", res.StatusCode)
	}
}

func TestIssueUnknownRepo(t *testing.T) {
	ts := backlogServer(t, &fakeReader{}, nil)
	res, _ := getIssue(t, ts, "nope", "COD-1")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestIssueWithoutCredentials(t *testing.T) {
	ts := backlogServer(t, nil, tracker.ErrReaderUnavailable)
	res, _ := getIssue(t, ts, "acme", "COD-1")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 when the repo has no direct tracker credentials", res.StatusCode)
	}
}

func TestIssueLabelsNeverNull(t *testing.T) {
	fake := &fakeReader{issue: tracker.IssueSummary{
		BacklogItem: tracker.BacklogItem{ID: "COD-5", Title: "No labels", Status: "Todo", Group: tracker.StatusGroupUnstarted},
		InProject:   true,
	}}
	ts := backlogServer(t, fake, nil)
	res, out := getIssue(t, ts, "acme", "COD-5")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Labels == nil {
		t.Error("labels serialized as null, want an empty array")
	}
}
