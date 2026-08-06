package bitbucketapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestFindPullRequestFiltersByStateAndSourceBranch(t *testing.T) {
	var gotQuery string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		_, _ = w.Write([]byte(`{"values":[
			{"id":7,"state":"DECLINED","links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/7"}}},
			{"id":9,"state":"OPEN","links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/9"}}}
		]}`))
	})
	pr, err := c.FindPullRequest(context.Background(), "feature/COD-1", StateOpen)
	if err != nil {
		t.Fatalf("FindPullRequest: %v", err)
	}
	if pr == nil || pr.ID != 9 {
		t.Fatalf("pr = %+v, want the open one, never the declined one", pr)
	}
	if gotQuery != `source.branch.name="feature/COD-1"` {
		t.Errorf("q = %q, want the source-branch filter", gotQuery)
	}
}

func TestFindPullRequestReportsNoneWhenNothingMatches(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[]}`))
	})
	pr, err := c.FindPullRequest(context.Background(), "feature/COD-1", StateMerged)
	if err != nil || pr != nil {
		t.Fatalf("FindPullRequest = (%+v, %v), want (nil, nil)", pr, err)
	}
}

func TestCreatePullRequestSendsTheBranchPairAndDraftFlag(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":12,"state":"OPEN","draft":true,"links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/12"}}}`))
	})
	pr, err := c.CreatePullRequest(context.Background(), "main", "feature/COD-1", "Add a thing", "## Summary", true)
	if err != nil {
		t.Fatalf("CreatePullRequest: %v", err)
	}
	if pr.URL != "https://bitbucket.org/acme/widgets/pull-requests/12" {
		t.Errorf("url = %q, want the html link Bitbucket returned", pr.URL)
	}
	if body["draft"] != true || body["title"] != "Add a thing" || body["description"] != "## Summary" {
		t.Errorf("body = %v, want the title, description and draft flag", body)
	}
	source := body["source"].(map[string]any)["branch"].(map[string]any)["name"]
	dest := body["destination"].(map[string]any)["branch"].(map[string]any)["name"]
	if source != "feature/COD-1" || dest != "main" {
		t.Errorf("source/destination = %v/%v, want feature/COD-1 into main", source, dest)
	}
}

// TestUpdatePullRequestKeepsTheTitle guards the replace-semantics trap: a PUT
// that omits the title blanks it.
func TestUpdatePullRequestKeepsTheTitle(t *testing.T) {
	var put map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":12,"title":"Add a thing","state":"OPEN","draft":true}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &put)
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.UpdatePullRequest(context.Background(), "12", "new body", true); err != nil {
		t.Fatalf("UpdatePullRequest: %v", err)
	}
	if put["title"] != "Add a thing" {
		t.Errorf("title = %v, want the current title re-sent unchanged", put["title"])
	}
	if put["description"] != "new body" || put["draft"] != false {
		t.Errorf("put = %v, want the new body and draft cleared", put)
	}
}

func TestMergeStrategyMapsEveryMergeMethod(t *testing.T) {
	cases := map[string]string{
		"squash":   "squash",
		"merge":    "merge_commit",
		"rebase":   "fast_forward",
		"":         "squash",
		"nonsense": "squash",
	}
	for method, want := range cases {
		if got := MergeStrategy(method); got != want {
			t.Errorf("MergeStrategy(%q) = %q, want %q", method, got, want)
		}
	}
}

func TestBuildStatusBucketMapsEveryState(t *testing.T) {
	cases := map[string]string{
		"SUCCESSFUL": "pass",
		"FAILED":     "fail",
		"STOPPED":    "cancel",
		"INPROGRESS": "pending",
		"":           "pending",
	}
	for state, want := range cases {
		if got := (BuildStatus{State: state}).Bucket(); got != want {
			t.Errorf("Bucket(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestBuildStatusesNamesAStatusByItsKeyWhenUnnamed(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"key":"BUILD-42","name":"","state":"SUCCESSFUL"}]}`))
	})
	got, err := c.BuildStatuses(context.Background(), "12")
	if err != nil {
		t.Fatalf("BuildStatuses: %v", err)
	}
	if len(got) != 1 || got[0].Name != "BUILD-42" || got[0].Bucket() != "pass" {
		t.Errorf("statuses = %+v, want one passing check named by its key", got)
	}
}

func TestSizeReadsTheReportedTotalsAndFallsBackToThePage(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repositories/acme/widgets/pullrequests/12/commits":
			_, _ = w.Write([]byte(`{"size":3,"values":[{},{}]}`))
		default:
			_, _ = w.Write([]byte(`{"values":[{},{},{},{}]}`))
		}
	})
	commits, files, err := c.Size(context.Background(), "12")
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if commits != 3 {
		t.Errorf("commits = %d, want the reported total 3", commits)
	}
	if files != 4 {
		t.Errorf("files = %d, want the page count when no total is reported", files)
	}
}

func TestPrPathRejectsAnIdentifierThatIsNotAPullRequestNumber(t *testing.T) {
	c := New("acme", "widgets", "rd@acme.com", "tok")
	for _, id := range []string{"", "abc", "0", "-4"} {
		if _, err := c.prPath(id); err == nil {
			t.Errorf("prPath(%q) = nil error, want it refused", id)
		}
	}
	if got, err := c.prPath("#12"); err != nil || got != "/repositories/acme/widgets/pullrequests/12" {
		t.Errorf("prPath(#12) = (%q, %v), want the numbered path", got, err)
	}
}
