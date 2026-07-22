package linearapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAssignLinear answers the identifier lookup, the assign mutation and the
// assignable-user listing, recording every request.
func fakeAssignLinear(t *testing.T) (*Client, *[]graphReq) {
	t.Helper()
	var reqs []graphReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req graphReq
		_ = json.Unmarshal(body, &req)
		reqs = append(reqs, req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "query Issue"):
			_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[{"id":"iss-1","identifier":"COD-42","team":{"id":"team-1","key":"COD"}}]}}}`)
		case strings.Contains(req.Query, "mutation IssueAssign"):
			_, _ = io.WriteString(w, `{"data":{"issueUpdate":{"success":true}}}`)
		case strings.Contains(req.Query, "query AssignableUsers"):
			_, _ = io.WriteString(w, `{"data":{"users":{"nodes":[{"id":"u-1","name":"Ada"},{"id":"u-2","name":"Bob"}]}}}`)
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

func TestAssignIssueSendsAssigneeID(t *testing.T) {
	c, reqs := fakeAssignLinear(t)

	if err := c.AssignIssue(context.Background(), "COD-42", "u-1"); err != nil {
		t.Fatalf("AssignIssue: %v", err)
	}
	assign := lastReq(t, *reqs, "mutation IssueAssign")
	if assign.Variables["id"] != "iss-1" {
		t.Errorf("id = %v, want the resolved node id", assign.Variables["id"])
	}
	if assign.Variables["assigneeId"] != "u-1" {
		t.Errorf("assigneeId = %v, want u-1", assign.Variables["assigneeId"])
	}
}

// Clearing an assignee must send an explicit null: an omitted field leaves the
// current assignee in place.
func TestAssignIssueClearsWithNull(t *testing.T) {
	c, reqs := fakeAssignLinear(t)

	if err := c.AssignIssue(context.Background(), "COD-42", "  "); err != nil {
		t.Fatalf("AssignIssue: %v", err)
	}
	assign := lastReq(t, *reqs, "mutation IssueAssign")
	raw, ok := assign.Variables["assigneeId"]
	if !ok {
		t.Fatal("assigneeId omitted from the mutation variables, want an explicit null")
	}
	if raw != nil {
		t.Errorf("assigneeId = %v, want null", raw)
	}
}

func TestAssignableUsersFiltersByName(t *testing.T) {
	c, reqs := fakeAssignLinear(t)

	users, err := c.AssignableUsers(context.Background(), " ad ")
	if err != nil {
		t.Fatalf("AssignableUsers: %v", err)
	}
	if len(users) != 2 || users[0] != (User{ID: "u-1", Name: "Ada"}) {
		t.Fatalf("users = %+v, want the workspace members mapped to id/name", users)
	}
	q := lastReq(t, *reqs, "query AssignableUsers")
	if q.Variables["name"] != "ad" {
		t.Errorf("name = %v, want the trimmed query", q.Variables["name"])
	}
	if !strings.Contains(q.Query, "active: { eq: true }") {
		t.Errorf("query = %q, want it restricted to active users", q.Query)
	}
}

func TestAssignmentDisabledWithoutKey(t *testing.T) {
	if err := New("").AssignIssue(context.Background(), "COD-42", "u-1"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("AssignIssue disabled = %v, want ErrNotEnabled", err)
	}
	if _, err := New("").AssignableUsers(context.Background(), ""); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("AssignableUsers disabled = %v, want ErrNotEnabled", err)
	}
}

func lastReq(t *testing.T, reqs []graphReq, needle string) graphReq {
	t.Helper()
	for i := len(reqs) - 1; i >= 0; i-- {
		if strings.Contains(reqs[i].Query, needle) {
			return reqs[i]
		}
	}
	t.Fatalf("no %s request was sent", needle)
	return graphReq{}
}
