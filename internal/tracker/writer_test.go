package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// linearGraphReq is one recorded GraphQL request against the fake Linear API.
type linearGraphReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// fakeLinearWriter wires a linearWriter to a fake GraphQL endpoint that answers
// the team lookup, label resolution, create and comment operations the writer
// performs, recording every request for wire assertions.
func fakeLinearWriter(t *testing.T) (*linearWriter, *[]linearGraphReq) {
	t.Helper()
	var reqs []linearGraphReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "lin_key" {
			t.Errorf("Authorization = %q, want the raw api key", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req linearGraphReq
		_ = json.Unmarshal(body, &req)
		reqs = append(reqs, req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "query Teams"):
			_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"id":"team-1","key":"COD","name":"Codesome"}]}}}`)
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
	client := linearapi.New("lin_key")
	client.Endpoint = srv.URL
	return &linearWriter{client: client, team: "COD"}, &reqs
}

func TestLinearWriterCreateIssue(t *testing.T) {
	w, reqs := fakeLinearWriter(t)

	got, err := w.CreateIssue(context.Background(), IssueDraft{
		Title:       "Follow-up: COD-9 quarantine",
		Description: "Build faulted",
		Labels:      []string{"ready-for-agent"},
	})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if got.Identifier != "COD-42" {
		t.Errorf("identifier = %q, want COD-42", got.Identifier)
	}
	if got.URL != "https://linear.app/acme/issue/COD-42" {
		t.Errorf("url = %q, want the created issue url", got.URL)
	}

	create := lastLinearReq(*reqs, "mutation IssueCreate")
	if create == nil {
		t.Fatal("no IssueCreate mutation was sent")
	}
	if create.Variables["teamId"] != "team-1" {
		t.Errorf("teamId = %v, want the resolved team id", create.Variables["teamId"])
	}
	if create.Variables["title"] != "Follow-up: COD-9 quarantine" {
		t.Errorf("title = %v, want the drafted title", create.Variables["title"])
	}
}

func TestLinearWriterAddComment(t *testing.T) {
	w, reqs := fakeLinearWriter(t)

	if err := w.AddComment(context.Background(), "COD-42", "filed a follow-up"); err != nil {
		t.Fatalf("AddComment error: %v", err)
	}
	comment := lastLinearReq(*reqs, "mutation CommentCreate")
	if comment == nil {
		t.Fatal("no CommentCreate mutation was sent")
	}
	if comment.Variables["issueId"] != "iss-1" || comment.Variables["body"] != "filed a follow-up" {
		t.Errorf("comment vars = %+v, want the resolved issue id and body", comment.Variables)
	}
}

func lastLinearReq(reqs []linearGraphReq, needle string) *linearGraphReq {
	for i := len(reqs) - 1; i >= 0; i-- {
		if strings.Contains(reqs[i].Query, needle) {
			return &reqs[i]
		}
	}
	return nil
}

// fakeJiraWriter wires a jiraWriter to a fake REST endpoint that resolves the
// issue type, accepts the create, and accepts a comment, recording the create
// body and comment path.
func fakeJiraWriter(t *testing.T) (*jiraWriter, *jiraCapture) {
	t.Helper()
	rec := &jiraCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issue/createmeta/PROJ/issuetypes"):
			_, _ = w.Write([]byte(`{"values":[{"id":"10001","name":"Task"},{"id":"10004","name":"Bug"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comment"):
			rec.commentPath = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			rec.commentBody = string(body)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issue"):
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &rec.create)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"10500","key":"PROJ-500"}`))
		default:
			t.Errorf("unexpected Jira request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return &jiraWriter{
		client:    jiraapi.New(srv.URL, "me@acme.com", "tok"),
		project:   "PROJ",
		baseURL:   srv.URL,
		issueType: jiraDefaultIssueType,
	}, rec
}

type jiraCapture struct {
	create struct {
		Fields struct {
			Project   struct{ Key string } `json:"project"`
			IssueType struct{ ID string }  `json:"issuetype"`
			Summary   string               `json:"summary"`
			Labels    []string             `json:"labels"`
		} `json:"fields"`
	}
	commentPath string
	commentBody string
}

func TestJiraWriterCreateIssue(t *testing.T) {
	w, rec := fakeJiraWriter(t)

	got, err := w.CreateIssue(context.Background(), IssueDraft{
		Title:  "New tracked work",
		Labels: []string{"ready-for-agent"},
	})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if got.Identifier != "PROJ-500" {
		t.Errorf("identifier = %q, want PROJ-500", got.Identifier)
	}
	if !strings.HasSuffix(got.URL, "/browse/PROJ-500") {
		t.Errorf("url = %q, want it to end with /browse/PROJ-500", got.URL)
	}
	if rec.create.Fields.Project.Key != "PROJ" {
		t.Errorf("project key = %q, want PROJ", rec.create.Fields.Project.Key)
	}
	if rec.create.Fields.IssueType.ID != "10001" {
		t.Errorf("issuetype id = %q, want 10001 (Task, the default)", rec.create.Fields.IssueType.ID)
	}
	if rec.create.Fields.Summary != "New tracked work" {
		t.Errorf("summary = %q, want the drafted title", rec.create.Fields.Summary)
	}
	if len(rec.create.Fields.Labels) != 1 || rec.create.Fields.Labels[0] != "ready-for-agent" {
		t.Errorf("labels = %v, want [ready-for-agent]", rec.create.Fields.Labels)
	}
}

func TestJiraWriterAddComment(t *testing.T) {
	w, rec := fakeJiraWriter(t)

	if err := w.AddComment(context.Background(), "PROJ-7", "a note"); err != nil {
		t.Fatalf("AddComment error: %v", err)
	}
	if rec.commentPath != "/rest/api/3/issue/PROJ-7/comment" {
		t.Errorf("comment path = %q, want the v3 comment endpoint for PROJ-7", rec.commentPath)
	}
	if !strings.Contains(rec.commentBody, "a note") {
		t.Errorf("comment body = %q, want it to carry the note text", rec.commentBody)
	}
}

func TestNewWriterRequiresCredentials(t *testing.T) {
	if _, err := NewWriter("linear", Config{}); !errors.Is(err, ErrWriterUnavailable) {
		t.Errorf("linear without an api key: err = %v, want ErrWriterUnavailable", err)
	}
	if _, err := NewWriter("jira", Config{BaseURL: "https://x.atlassian.net", Email: "e@x.com"}); !errors.Is(err, ErrWriterUnavailable) {
		t.Errorf("jira without a full credential set: err = %v, want ErrWriterUnavailable", err)
	}
	if _, err := NewWriter("github", Config{}); err == nil {
		t.Error("github writer should be unsupported")
	}
	if _, err := NewWriter("nope", Config{}); err == nil {
		t.Error("unknown provider should error")
	}
}

func TestNewWriterBuildsDirectClients(t *testing.T) {
	lw, err := NewWriter("linear", Config{APIKey: "k", Team: "COD"})
	if err != nil {
		t.Fatalf("linear writer: %v", err)
	}
	if _, ok := lw.(*linearWriter); !ok {
		t.Errorf("linear writer type = %T, want *linearWriter", lw)
	}
	jw, err := NewWriter("jira", Config{APIKey: "t", Email: "e@x.com", BaseURL: "https://x.atlassian.net", Team: "PROJ"})
	if err != nil {
		t.Fatalf("jira writer: %v", err)
	}
	if _, ok := jw.(*jiraWriter); !ok {
		t.Errorf("jira writer type = %T, want *jiraWriter", jw)
	}
}
