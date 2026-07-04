package linearapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// graphReq is one recorded GraphQL request against the fake Linear API.
type graphReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// fakeLinear stands in for Linear's GraphQL endpoint, dispatching on the
// operation name in each query and recording every request for assertions.
func fakeLinear(t *testing.T) (*Client, *[]graphReq) {
	t.Helper()
	var reqs []graphReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req graphReq
		_ = json.Unmarshal(body, &req)
		reqs = append(reqs, req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "TeamLabels"):
			_, _ = io.WriteString(w, `{"data":{"issueLabels":{"nodes":[{"id":"lbl-ready","name":"ready-for-agent"}]}}}`)
		case strings.Contains(req.Query, "mutation IssueCreate"):
			_, _ = io.WriteString(w, `{"data":{"issueCreate":{"success":true,"issue":{"id":"iss-1","identifier":"COD-42","url":"https://linear.app/acme/issue/COD-42"}}}}`)
		case strings.Contains(req.Query, "query Issue"):
			_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[{"id":"iss-1","identifier":"COD-42","team":{"id":"team-1","key":"COD"}}]}}}`)
		case strings.Contains(req.Query, "mutation CommentCreate"):
			_, _ = io.WriteString(w, `{"data":{"commentCreate":{"success":true}}}`)
		default:
			t.Errorf("unexpected GraphQL query: %s", req.Query)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	c := New("lin_key")
	c.Endpoint = srv.URL
	return c, &reqs
}

func TestSortChildrenForRun(t *testing.T) {
	refs := []IssueRef{
		{Identifier: "COD-500", Priority: 3},                        // medium, no due date
		{Identifier: "COD-494", Priority: 0},                        // no priority -> last
		{Identifier: "COD-498", Priority: 1},                        // urgent
		{Identifier: "COD-497", Priority: 3, DueDate: "2026-07-01"}, // medium, later due
		{Identifier: "COD-496", Priority: 3, DueDate: "2026-06-01"}, // medium, sooner due
		{Identifier: "COD-495", Priority: 2},                        // high
	}

	SortChildrenForRun(refs)

	got := make([]string, len(refs))
	for i, r := range refs {
		got[i] = r.Identifier
	}
	// Priority first (urgent > high > medium > none), then due date (an empty due
	// sorts ahead of a dated one under the picker's existing rule), then number.
	want := []string{"COD-498", "COD-495", "COD-500", "COD-496", "COD-497", "COD-494"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestCreateIssueResolvesLabelsAndReturnsURL(t *testing.T) {
	c, reqs := fakeLinear(t)

	id, url, err := c.CreateIssue(context.Background(), CreateIssueInput{TeamID: "team-1", Title: "Something broke", Description: "Details here", Labels: []string{"ready-for-agent", "unknown-label"}})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if id != "COD-42" {
		t.Errorf("identifier = %q, want COD-42", id)
	}
	if url != "https://linear.app/acme/issue/COD-42" {
		t.Errorf("url = %q, want the issue url", url)
	}

	create := lastMatching(*reqs, "mutation IssueCreate")
	if create == nil {
		t.Fatal("no IssueCreate mutation was sent")
	}
	if create.Variables["title"] != "Something broke" || create.Variables["description"] != "Details here" {
		t.Errorf("create vars = %+v, want the drafted title/description", create.Variables)
	}
	labelIDs, _ := create.Variables["labelIds"].([]any)
	if len(labelIDs) != 1 || labelIDs[0] != "lbl-ready" {
		t.Errorf("labelIds = %v, want only the resolved ready label id (unknown labels dropped)", labelIDs)
	}
}

func TestAddCommentPostsBody(t *testing.T) {
	c, reqs := fakeLinear(t)

	if err := c.AddComment(context.Background(), "COD-42", "a follow-up note"); err != nil {
		t.Fatalf("AddComment error: %v", err)
	}

	comment := lastMatching(*reqs, "mutation CommentCreate")
	if comment == nil {
		t.Fatal("no CommentCreate mutation was sent")
	}
	if comment.Variables["issueId"] != "iss-1" {
		t.Errorf("issueId = %v, want the resolved issue id", comment.Variables["issueId"])
	}
	if comment.Variables["body"] != "a follow-up note" {
		t.Errorf("body = %v, want the comment text", comment.Variables["body"])
	}
}

func lastMatching(reqs []graphReq, needle string) *graphReq {
	for i := len(reqs) - 1; i >= 0; i-- {
		if strings.Contains(reqs[i].Query, needle) {
			return &reqs[i]
		}
	}
	return nil
}

func TestStateIsTerminal(t *testing.T) {
	cases := map[string]bool{
		"completed": true,
		"canceled":  true,
		"started":   false,
		"unstarted": false,
		"backlog":   false,
		"triage":    false,
	}
	for typ, want := range cases {
		if got := (State{Type: typ}).IsTerminal(); got != want {
			t.Errorf("State{%q}.IsTerminal() = %v, want %v", typ, got, want)
		}
	}
}

// TestMutationsDeclareStringVariables guards the GraphQL variable types against
// Linear's schema: mutation arguments and input-object fields are String-typed
// (teamId: String!, labelIds: [String!], parentId: String, …), and Linear rejects
// an ID-typed variable in those positions with GRAPHQL_VALIDATION_FAILED
// ("Variable "$teamId" of type "ID!" used in position expecting type "String!"").
// Only filter comparators (IDComparator.eq) take ID, so ID belongs in queries
// alone — a mutation declaring an ID variable is always a live-API failure.
func TestMutationsDeclareStringVariables(t *testing.T) {
	mutations := map[string]string{
		"issueUpdate":      issueUpdateMutation,
		"commentCreate":    commentCreateMutation,
		"issueLabelCreate": issueLabelCreateMutation,
		"issueCreate":      issueCreateMutation,
	}
	for name, q := range mutations {
		for _, bad := range []string{": ID", ": [ID"} {
			if strings.Contains(q, bad) {
				t.Errorf("%s declares an ID-typed variable (%q) — Linear mutations take String types", name, bad)
			}
		}
	}
}
