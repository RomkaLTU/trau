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

func TestProjectIssuesKeepsUploadedFilesAndDropsIntegrationLinks(t *testing.T) {
	const payload = `{"data":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"id":"iss-1","identifier":"COD-1","title":"First","description":"Body",
			 "state":{"name":"Todo","type":"unstarted"},
			 "labels":{"nodes":[]},"children":{"nodes":[]},"comments":{"nodes":[]},
			 "attachments":{"nodes":[
				{"id":"att-1","title":"spec.pdf","url":"https://uploads.linear.app/a/b/spec.pdf"},
				{"id":"att-2","title":"Pull request","url":"https://github.com/acme/repo/pull/12"}
			 ]}}
		]}}}`

	var req graphReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	c := New("lin_key")
	c.Endpoint = srv.URL
	issues, err := c.ProjectIssues(context.Background(), "team-1", "proj-1", "")
	if err != nil {
		t.Fatalf("ProjectIssues: %v", err)
	}
	if !strings.Contains(req.Query, "attachments") {
		t.Fatalf("query = %q, want the attachment connection requested", req.Query)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}

	atts := issues[0].Attachments
	if len(atts) != 1 {
		t.Fatalf("attachments = %+v, want only the uploaded file", atts)
	}
	if atts[0].URL != "https://uploads.linear.app/a/b/spec.pdf" || atts[0].Filename != "spec.pdf" {
		t.Errorf("attachment = %+v, want the upload with its title as filename", atts[0])
	}
}
