package jiraapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A screenshot reaches an issue as a multipart form under the "file" field, with
// the XSRF header Jira's attachment endpoint refuses the request without.
func TestAddAttachmentUploadsMultipartWithTheXSRFHeader(t *testing.T) {
	var (
		method  string
		path    string
		token   string
		name    string
		content string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		token = r.Header.Get("X-Atlassian-Token")
		file, head, err := r.FormFile("file")
		if err != nil {
			t.Errorf("FormFile: %v", err)
		} else {
			defer func() { _ = file.Close() }()
			name = head.Filename
			body, _ := io.ReadAll(file)
			content = string(body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `[{"id":"10042","filename":"proof-1.png"}]`)
	}))
	defer srv.Close()

	id, err := New(srv.URL, "me@acme.com", "tok").
		AddAttachment(context.Background(), "PROJ-7", "proof-1.png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("AddAttachment error: %v", err)
	}
	if method != http.MethodPost || path != "/rest/api/3/issue/PROJ-7/attachments" {
		t.Errorf("request = %s %s, want POST /rest/api/3/issue/PROJ-7/attachments", method, path)
	}
	if token != "no-check" {
		t.Errorf("X-Atlassian-Token = %q, want no-check", token)
	}
	if name != "proof-1.png" || content != "png-bytes" {
		t.Errorf("uploaded %q carrying %q, want proof-1.png carrying png-bytes", name, content)
	}
	if id != "10042" {
		t.Errorf("attachment id = %q, want 10042", id)
	}
}

// The media id an ADF node embeds an attachment by appears only in the redirect
// the content route answers with, between /file/ and /binary.
func TestMediaIDReadsTheRedirectLocation(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Location", "https://api.media.atlassian.com/file/9d3a-4c7f-uuid/binary?token=abc&client=x")
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer srv.Close()

	id, err := New(srv.URL, "me@acme.com", "tok").MediaID(context.Background(), "10042")
	if err != nil {
		t.Fatalf("MediaID error: %v", err)
	}
	if path != "/rest/api/3/attachment/content/10042" {
		t.Errorf("path = %q, want /rest/api/3/attachment/content/10042", path)
	}
	if id != "9d3a-4c7f-uuid" {
		t.Errorf("media id = %q, want 9d3a-4c7f-uuid", id)
	}
}

// A site that answers the content route in any other shape leaves the caller with
// an attachment it cannot embed, which fails rather than inventing a media id.
func TestMediaIDFailsWithoutAReadableMediaID(t *testing.T) {
	cases := []struct {
		name     string
		location string
	}{
		{"no redirect at all", ""},
		{"redirect somewhere other than the media host", "https://acme.atlassian.net/secure/attachment/10042/proof-1.png"},
		{"media URL without the binary segment", "https://api.media.atlassian.com/file/9d3a-4c7f-uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.location == "" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Location", tc.location)
				w.WriteHeader(http.StatusSeeOther)
			}))
			defer srv.Close()

			id, err := New(srv.URL, "me@acme.com", "tok").MediaID(context.Background(), "10042")
			if err == nil {
				t.Fatalf("MediaID = %q, want an error", id)
			}
			if !strings.Contains(err.Error(), "10042") {
				t.Errorf("error = %v, want it to name the attachment", err)
			}
		})
	}
}

// The comment carries the report as ADF nodes with one media node per uploaded
// screenshot appended, which is what renders the images inline.
func TestAddCommentWithMediaAppendsMediaNodes(t *testing.T) {
	var req commentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	err := New(srv.URL, "me@acme.com", "tok").AddCommentWithMedia(context.Background(), "PROJ-7",
		"## Trau QA report\n\nVerify passed: all green\n", []string{"uuid-1", "uuid-2"})
	if err != nil {
		t.Fatalf("AddCommentWithMedia error: %v", err)
	}

	kinds := make([]string, 0, len(req.Body.Content))
	for _, block := range req.Body.Content {
		kinds = append(kinds, block.Type)
	}
	if got := strings.Join(kinds, ","); got != "heading,paragraph,mediaSingle,mediaSingle" {
		t.Fatalf("ADF blocks = %s, want the report followed by one mediaSingle per image", got)
	}
	for i, want := range []string{"uuid-1", "uuid-2"} {
		media := req.Body.Content[2+i].Content
		if len(media) != 1 {
			t.Fatalf("mediaSingle %d holds %d nodes, want 1", i, len(media))
		}
		attrs := media[0].Attrs
		if media[0].Type != "media" || attrs["type"] != "file" || attrs["id"] != want || attrs["collection"] != "" {
			t.Errorf("media node %d = %+v, want a file node for %s in no collection", i, media[0], want)
		}
	}
}
