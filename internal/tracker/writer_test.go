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
		case strings.Contains(req.Query, "query ProjectsByName"):
			_, _ = io.WriteString(w, `{"data":{"projects":{"nodes":[{"id":"proj-1","name":"Trau"}]}}}`)
		case strings.Contains(req.Query, "mutation DocumentCreate"):
			_, _ = io.WriteString(w, `{"data":{"documentCreate":{"success":true,"document":{"id":"doc-1","url":"https://linear.app/acme/document/prd-abc123"}}}}`)
		default:
			t.Errorf("unexpected GraphQL query: %s", req.Query)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	client := linearapi.New("lin_key")
	client.Endpoint = srv.URL
	return &linearWriter{client: client, team: "COD", project: "Trau"}, &reqs
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

func TestLinearWriterCreateUnderParent(t *testing.T) {
	cases := []struct {
		name       string
		parent     string
		wantParent string
	}{
		{name: "top-level issue nests under nothing", parent: "", wantParent: ""},
		{name: "sub-issue resolves the epic and nests under it", parent: "COD-9", wantParent: "iss-1"},
		{name: "whitespace parent is treated as top-level", parent: "   ", wantParent: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, reqs := fakeLinearWriter(t)

			if _, err := w.CreateIssue(context.Background(), IssueDraft{Title: "child", Parent: tc.parent}); err != nil {
				t.Fatalf("CreateIssue error: %v", err)
			}

			resolved := lastLinearReq(*reqs, "query Issue") != nil
			if resolved != (tc.wantParent != "") {
				t.Errorf("parent lookup sent = %v, want %v", resolved, tc.wantParent != "")
			}
			create := lastLinearReq(*reqs, "mutation IssueCreate")
			if create == nil {
				t.Fatal("no IssueCreate mutation was sent")
			}
			got := create.Variables["parentId"]
			if tc.wantParent == "" {
				if got != nil {
					t.Errorf("parentId = %v, want it omitted for a top-level issue", got)
				}
			} else if got != tc.wantParent {
				t.Errorf("parentId = %v, want %q (the resolved epic id)", got, tc.wantParent)
			}
		})
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

func TestLinearWriterPublishDocument(t *testing.T) {
	w, reqs := fakeLinearWriter(t)

	md := "# Payments PRD\n\nGoals and non-goals, verbatim **markdown**.\n\n- one\n- two\n"
	got, err := w.PublishDocument(context.Background(), DocumentDraft{Title: "Payments PRD", Markdown: md})
	if err != nil {
		t.Fatalf("PublishDocument error: %v", err)
	}
	if got.Kind != DocumentKindDocument {
		t.Errorf("kind = %q, want %q", got.Kind, DocumentKindDocument)
	}
	if got.URL != "https://linear.app/acme/document/prd-abc123" {
		t.Errorf("url = %q, want the created document url", got.URL)
	}
	if got.Identifier != "" {
		t.Errorf("identifier = %q, want empty for a Linear document", got.Identifier)
	}

	lookup := lastLinearReq(*reqs, "query ProjectsByName")
	if lookup == nil || lookup.Variables["name"] != "Trau" {
		t.Fatalf("ProjectByName not sent with the repo's project name: %+v", lookup)
	}
	create := lastLinearReq(*reqs, "mutation DocumentCreate")
	if create == nil {
		t.Fatal("no DocumentCreate mutation was sent")
	}
	if create.Variables["projectId"] != "proj-1" {
		t.Errorf("projectId = %v, want the resolved project id", create.Variables["projectId"])
	}
	if create.Variables["title"] != "Payments PRD" {
		t.Errorf("title = %v, want the drafted title", create.Variables["title"])
	}
	if create.Variables["content"] != md {
		t.Errorf("content = %q, want the markdown preserved byte-for-byte", create.Variables["content"])
	}
}

func TestLinearWriterPublishNeedsProject(t *testing.T) {
	w, _ := fakeLinearWriter(t)
	w.project = ""
	if _, err := w.PublishDocument(context.Background(), DocumentDraft{Title: "x", Markdown: "y"}); err == nil {
		t.Error("PublishDocument without a configured project should error")
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
			rec.createRaw = string(body)
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
			Parent    struct{ Key string } `json:"parent"`
		} `json:"fields"`
	}
	createRaw   string
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

func TestJiraWriterCreateUnderParent(t *testing.T) {
	cases := []struct {
		name       string
		parent     string
		wantParent string
	}{
		{name: "top-level issue nests under nothing", parent: "", wantParent: ""},
		{name: "sub-issue nests under the epic key", parent: "PROJ-1", wantParent: "PROJ-1"},
		{name: "whitespace parent is treated as top-level", parent: "  ", wantParent: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, rec := fakeJiraWriter(t)

			if _, err := w.CreateIssue(context.Background(), IssueDraft{Title: "child", Parent: tc.parent}); err != nil {
				t.Fatalf("CreateIssue error: %v", err)
			}
			if rec.create.Fields.Parent.Key != tc.wantParent {
				t.Errorf("parent key = %q, want %q", rec.create.Fields.Parent.Key, tc.wantParent)
			}
			if tc.wantParent == "" && strings.Contains(rec.createRaw, `"parent"`) {
				t.Errorf("create body carried a parent for a top-level issue: %s", rec.createRaw)
			}
		})
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

func TestJiraWriterPublishDocument(t *testing.T) {
	w, rec := fakeJiraWriter(t)

	md := "# Payments PRD\n\nBody line with **markdown** and `code`."
	got, err := w.PublishDocument(context.Background(), DocumentDraft{Title: "Payments PRD", Markdown: md})
	if err != nil {
		t.Fatalf("PublishDocument error: %v", err)
	}
	if got.Kind != DocumentKindIssue {
		t.Errorf("kind = %q, want %q (the Jira fallback)", got.Kind, DocumentKindIssue)
	}
	if got.Identifier != "PROJ-500" {
		t.Errorf("identifier = %q, want the created issue key", got.Identifier)
	}
	if !strings.HasSuffix(got.URL, "/browse/PROJ-500") {
		t.Errorf("url = %q, want it to end with /browse/PROJ-500", got.URL)
	}
	if rec.create.Fields.Summary != "Payments PRD" {
		t.Errorf("summary = %q, want the PRD title", rec.create.Fields.Summary)
	}
	if rec.create.Fields.IssueType.ID != "10001" {
		t.Errorf("issuetype id = %q, want 10001 (Task, the fallback type)", rec.create.Fields.IssueType.ID)
	}
	for _, line := range []string{"# Payments PRD", "Body line with **markdown** and `code`."} {
		if !strings.Contains(rec.createRaw, line) {
			t.Errorf("create body missing PRD line %q; the description must carry the markdown", line)
		}
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
