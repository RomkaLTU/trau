package tracker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

func TestAttachmentScannerClassifiesDiscoveredURLs(t *testing.T) {
	scanner := NewAttachmentScanner("https://acme.atlassian.net")

	cases := []struct {
		name     string
		body     string
		wantURL  string
		wantName string
		wantSrc  string
	}{
		{
			name:     "linear upload embedded as an image",
			body:     "before\n![shot](https://uploads.linear.app/a/b/screen.png)\nafter",
			wantURL:  "https://uploads.linear.app/a/b/screen.png",
			wantName: "screen.png",
			wantSrc:  AttachmentLinear,
		},
		{
			name:     "jira attachment content URL",
			body:     "![](https://acme.atlassian.net/rest/api/3/attachment/content/10042)",
			wantURL:  "https://acme.atlassian.net/rest/api/3/attachment/content/10042",
			wantName: "10042",
			wantSrc:  AttachmentJira,
		},
		{
			name:     "image hosted anywhere else",
			body:     "![diagram](https://raw.githubusercontent.com/acme/repo/main/docs/flow.png)",
			wantURL:  "https://raw.githubusercontent.com/acme/repo/main/docs/flow.png",
			wantName: "flow.png",
			wantSrc:  AttachmentExternal,
		},
		{
			name:     "bare image URL without markdown syntax",
			body:     "see https://cdn.acme.dev/board.jpeg for the layout",
			wantURL:  "https://cdn.acme.dev/board.jpeg",
			wantName: "board.jpeg",
			wantSrc:  AttachmentExternal,
		},
		{
			name:     "bare URL followed by sentence punctuation",
			body:     "compare against https://cdn.acme.dev/old.png.",
			wantURL:  "https://cdn.acme.dev/old.png",
			wantName: "old.png",
			wantSrc:  AttachmentExternal,
		},
		{
			name:     "angle-bracketed image with a title",
			body:     `![alt](<https://uploads.linear.app/x/y/z.gif> "caption")`,
			wantURL:  "https://uploads.linear.app/x/y/z.gif",
			wantName: "z.gif",
			wantSrc:  AttachmentLinear,
		},
		{
			name:     "percent-escaped filename is decoded",
			body:     "![](https://acme.atlassian.net/files/design%20v2.png)",
			wantURL:  "https://acme.atlassian.net/files/design%20v2.png",
			wantName: "design v2.png",
			wantSrc:  AttachmentJira,
		},
		{
			name:     "extensionless upload falls back to the alt text",
			body:     "![diagram.png](https://uploads.linear.app/)",
			wantURL:  "https://uploads.linear.app/",
			wantName: "diagram.png",
			wantSrc:  AttachmentLinear,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanner.Scan(tc.body)
			if len(got) != 1 {
				t.Fatalf("Scan(%q) = %+v, want exactly one attachment", tc.body, got)
			}
			if got[0].URL != tc.wantURL {
				t.Errorf("URL = %q, want %q", got[0].URL, tc.wantURL)
			}
			if got[0].Filename != tc.wantName {
				t.Errorf("Filename = %q, want %q", got[0].Filename, tc.wantName)
			}
			if got[0].Source != tc.wantSrc {
				t.Errorf("Source = %q, want %q", got[0].Source, tc.wantSrc)
			}
		})
	}
}

func TestAttachmentScannerIgnoresNonFileReferences(t *testing.T) {
	scanner := NewAttachmentScanner("https://acme.atlassian.net")

	cases := []struct {
		name string
		body string
	}{
		{"plain link to a page", "see [the docs](https://acme.dev/docs) for details"},
		{"bare link without an image extension", "ticket at https://acme.atlassian.net/browse/PROJ-1"},
		{"relative trau attachment route", "![](/api/repos/acme/attachments/12)"},
		{"data URI", "![](data:image/png;base64,iVBORw0KGgo=)"},
		{"empty body", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanner.Scan(tc.body); len(got) != 0 {
				t.Fatalf("Scan(%q) = %+v, want nothing registered", tc.body, got)
			}
		})
	}
}

func TestAttachmentScannerDedupesAcrossBodies(t *testing.T) {
	const shared = "https://uploads.linear.app/a/b/screen.png"
	got := AttachmentScanner{}.Scan(
		"description embeds ![]("+shared+")",
		"a comment repeats ![]("+shared+")",
		"another adds ![](https://uploads.linear.app/c/d/second.png)",
	)
	if len(got) != 2 {
		t.Fatalf("Scan = %+v, want the repeated URL registered once", got)
	}
	if got[0].URL != shared {
		t.Errorf("first = %q, want the first-seen URL %q", got[0].URL, shared)
	}
}

func TestAttachmentScannerWithoutJiraHostClassifiesJiraURLsExternal(t *testing.T) {
	got := AttachmentScanner{}.Scan("![](https://acme.atlassian.net/files/a.png)")
	if len(got) != 1 || got[0].Source != AttachmentExternal {
		t.Fatalf("Scan = %+v, want external without a Jira site bound", got)
	}
}

func TestMergeAttachmentsKeepsAPIMetadataOverScannedGuess(t *testing.T) {
	const url = "https://acme.atlassian.net/rest/api/3/attachment/content/10042"
	listed := []Attachment{{
		URL:      url,
		Filename: "architecture.png",
		MimeType: "image/png",
		Size:     2048,
		Source:   AttachmentJira,
	}}
	found := []Attachment{
		{URL: url, Filename: "10042", Source: AttachmentJira},
		{URL: "https://cdn.acme.dev/extra.png", Filename: "extra.png", Source: AttachmentExternal},
	}

	got := mergeAttachments(listed, found)
	if len(got) != 2 {
		t.Fatalf("mergeAttachments = %+v, want the duplicate collapsed", got)
	}
	if got[0].Filename != "architecture.png" || got[0].Size != 2048 {
		t.Errorf("listed entry = %+v, want the API's filename and size preserved", got[0])
	}
	if got[1].URL != "https://cdn.acme.dev/extra.png" {
		t.Errorf("scanned entry = %+v, want the URL the file list did not cover", got[1])
	}
}

func TestLinearSyncPullDiscoversEmbeddedAndUploadedFiles(t *testing.T) {
	const payload = `{"data":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"id":"iss-1","identifier":"COD-1","title":"First",
			 "description":"Look ![](https://uploads.linear.app/a/b/screen.png) and ![](https://cdn.acme.dev/flow.png)",
			 "state":{"name":"Todo","type":"unstarted"},
			 "labels":{"nodes":[]},"children":{"nodes":[]},
			 "comments":{"nodes":[{"id":"c1","body":"also ![](https://uploads.linear.app/c/d/second.png)"}]},
			 "attachments":{"nodes":[{"id":"att-1","title":"spec.pdf","url":"https://uploads.linear.app/e/f/spec.pdf"}]}}
		]}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)

	c := linearapi.New("lin_key")
	c.Endpoint = srv.URL
	r := &linearReader{client: c, team: "COD"}

	issues, err := r.SyncPull(context.Background(), ProjectBinding{TeamID: "team-1"}, "")
	if err != nil {
		t.Fatalf("SyncPull: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}

	got := map[string]string{}
	for _, att := range issues[0].Attachments {
		got[att.URL] = att.Source
	}
	want := map[string]string{
		"https://uploads.linear.app/e/f/spec.pdf":   AttachmentLinear,
		"https://uploads.linear.app/a/b/screen.png": AttachmentLinear,
		"https://uploads.linear.app/c/d/second.png": AttachmentLinear,
		"https://cdn.acme.dev/flow.png":             AttachmentExternal,
	}
	if len(got) != len(want) {
		t.Fatalf("attachments = %+v, want %+v", got, want)
	}
	for url, source := range want {
		if got[url] != source {
			t.Errorf("%s = %q, want %q", url, got[url], source)
		}
	}
}

func TestJiraSyncPullClassifiesSiteFilesAgainstExternalImages(t *testing.T) {
	const payload = `{"issues":[
		{"key":"PROJ-1","fields":{
			"summary":"With files",
			"description":{"type":"doc","version":1,"content":[
				{"type":"mediaSingle","content":[{"type":"media","attrs":{"type":"file","id":"10042","alt":"architecture.png"}}]},
				{"type":"paragraph","content":[{"type":"text","text":"compare ![](https://cdn.acme.dev/old.png)"}]}
			]},
			"attachment":[
				{"id":"10042","filename":"architecture.png","mimeType":"image/png","size":2048,
				 "content":"BASE/rest/api/3/attachment/content/10042"}
			],
			"updated":"2026-07-10T00:00:00.000+0000"
		}}
	]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.ReplaceAll(payload, "BASE", "http://"+r.Host))
	}))
	t.Cleanup(srv.Close)

	r := &jiraReader{client: jiraapi.New(srv.URL, "me@acme.com", "tok"), baseURL: srv.URL, project: "PROJ"}

	issues, err := r.SyncPull(context.Background(), ProjectBinding{ProjectID: "PROJ"}, "")
	if err != nil {
		t.Fatalf("SyncPull: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}

	atts := issues[0].Attachments
	if len(atts) != 2 {
		t.Fatalf("attachments = %+v, want the listed file plus the external image", atts)
	}
	if atts[0].Source != AttachmentJira || atts[0].Filename != "architecture.png" || atts[0].Size != 2048 {
		t.Errorf("listed file = %+v, want the API's metadata under source jira", atts[0])
	}
	if atts[1].URL != "https://cdn.acme.dev/old.png" || atts[1].Source != AttachmentExternal {
		t.Errorf("scanned image = %+v, want the externally hosted PNG", atts[1])
	}
	if !strings.Contains(issues[0].Description, "![architecture.png]("+srv.URL) {
		t.Errorf("description = %q, want the embedded image resolved to its content URL", issues[0].Description)
	}
}
