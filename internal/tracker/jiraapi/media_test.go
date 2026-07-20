package jiraapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// adfBody wraps ADF content nodes in the document envelope Jira returns.
func adfBody(content string) json.RawMessage {
	return json.RawMessage(`{"type":"doc","version":1,"content":[` + content + `]}`)
}

func TestADFMediaBecomesAnImageReference(t *testing.T) {
	files := []Attachment{
		{
			ID:       "10042",
			Filename: "architecture.png",
			MimeType: "image/png",
			Size:     2048,
			Content:  "https://acme.atlassian.net/rest/api/3/attachment/content/10042",
		},
	}

	cases := []struct {
		name  string
		nodes string
		media []Attachment
		want  string
	}{
		{
			name:  "mediaSingle resolved by media id",
			nodes: `{"type":"mediaSingle","content":[{"type":"media","attrs":{"type":"file","id":"10042","alt":"architecture.png"}}]}`,
			media: files,
			want:  "![architecture.png](https://acme.atlassian.net/rest/api/3/attachment/content/10042)",
		},
		{
			name:  "mediaGroup resolved by filename when the id does not match",
			nodes: `{"type":"mediaGroup","content":[{"type":"media","attrs":{"type":"file","id":"uuid-not-in-list","alt":"architecture.png"}}]}`,
			media: files,
			want:  "![architecture.png](https://acme.atlassian.net/rest/api/3/attachment/content/10042)",
		},
		{
			name:  "external media carries its own URL",
			nodes: `{"type":"mediaSingle","content":[{"type":"media","attrs":{"type":"external","url":"https://cdn.acme.dev/flow.png","alt":"flow"}}]}`,
			media: files,
			want:  "![flow](https://cdn.acme.dev/flow.png)",
		},
		{
			name:  "unresolvable media still records that an image was there",
			nodes: `{"type":"mediaSingle","content":[{"type":"media","attrs":{"type":"file","id":"missing","alt":"gone.png"}}]}`,
			media: nil,
			want:  "[image: gone.png]",
		},
		{
			name:  "unresolvable media without alt falls back to the media id",
			nodes: `{"type":"mediaSingle","content":[{"type":"media","attrs":{"type":"file","id":"missing"}}]}`,
			media: nil,
			want:  "[image: missing]",
		},
		{
			name:  "media with no attributes at all",
			nodes: `{"type":"mediaSingle","content":[{"type":"media"}]}`,
			media: nil,
			want:  "[image]",
		},
		{
			name:  "inline media inside a paragraph",
			nodes: `{"type":"paragraph","content":[{"type":"text","text":"see "},{"type":"mediaInline","attrs":{"type":"file","id":"10042"}}]}`,
			media: files,
			want:  "see ![](https://acme.atlassian.net/rest/api/3/attachment/content/10042)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adfToMarkdown(adfBody(tc.nodes), tc.media); got != tc.want {
				t.Fatalf("adfToMarkdown = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestADFKeepsProseAroundEmbeddedMedia(t *testing.T) {
	doc := adfBody(`{"type":"paragraph","content":[{"type":"text","text":"Before"}]},` +
		`{"type":"mediaSingle","content":[{"type":"media","attrs":{"type":"file","id":"7","alt":"shot.png"}}]},` +
		`{"type":"paragraph","content":[{"type":"text","text":"After"}]}`)
	media := []Attachment{{ID: "7", Filename: "shot.png", Content: "https://acme.atlassian.net/c/7"}}

	want := "Before\n\n![shot.png](https://acme.atlassian.net/c/7)\n\nAfter"
	if got := adfToMarkdown(doc, media); got != want {
		t.Fatalf("adfToMarkdown = %q, want %q", got, want)
	}
}

func TestSyncIssuesRegistersAttachmentsAndResolvesEmbeddedMedia(t *testing.T) {
	const payload = `{"issues":[
		{"key":"PROJ-1","fields":{
			"summary":"With a screenshot",
			"description":{"type":"doc","version":1,"content":[
				{"type":"paragraph","content":[{"type":"text","text":"Look:"}]},
				{"type":"mediaSingle","content":[{"type":"media","attrs":{"type":"file","id":"10042","alt":"architecture.png"}}]}
			]},
			"attachment":[
				{"id":"10042","filename":"architecture.png","mimeType":"image/png","size":2048,
				 "content":"https://acme.atlassian.net/rest/api/3/attachment/content/10042"}
			],
			"updated":"2026-07-10T00:00:00.000+0000"
		}}
	]}`

	var gotReq searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	issues, err := New(srv.URL, "me@acme.com", "tok").SyncIssues(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("SyncIssues: %v", err)
	}
	if !containsField(gotReq.Fields, "attachment") {
		t.Fatalf("fields = %v, want attachment requested", gotReq.Fields)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}

	iss := issues[0]
	if len(iss.Attachments) != 1 {
		t.Fatalf("attachments = %+v, want the API's file list", iss.Attachments)
	}
	att := iss.Attachments[0]
	if att.Filename != "architecture.png" || att.MimeType != "image/png" || att.Size != 2048 {
		t.Errorf("attachment = %+v, want filename/mime/size from the API", att)
	}
	want := "Look:\n\n![architecture.png](https://acme.atlassian.net/rest/api/3/attachment/content/10042)"
	if iss.Description != want {
		t.Errorf("description = %q, want the embedded image kept as %q", iss.Description, want)
	}
}

func TestADFHeadingAttrsDriveTheMarkdownLevel(t *testing.T) {
	doc := adfBody(`{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Title"}]},` +
		`{"type":"paragraph","content":[{"type":"text","text":"Body"}]}`)
	if got := adfToMarkdown(doc, nil); got != "## Title\n\nBody" {
		t.Fatalf("adfToMarkdown = %q, want the heading level rendered as hashes", got)
	}
}
