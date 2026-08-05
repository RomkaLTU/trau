package azureapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestMergeTagsIsCaseInsensitiveAndPreservesCasing(t *testing.T) {
	cases := []struct {
		name        string
		current     []string
		add, remove []string
		want        []string
	}{
		{"add one", []string{"backend"}, []string{"ready-for-agent"}, nil, []string{"backend", "ready-for-agent"}},
		{"remove ignores case", []string{"Backend", "needs-human"}, nil, []string{"NEEDS-HUMAN"}, []string{"Backend"}},
		{"add existing keeps the stored casing", []string{"Ready-For-Agent"}, []string{"ready-for-agent"}, nil, []string{"Ready-For-Agent"}},
		{"remove beats add", []string{"a"}, []string{"b"}, []string{"b"}, []string{"a"}},
		{"blanks are dropped", []string{"a"}, []string{"  ", ""}, nil, []string{"a"}},
		{"swap ready for quarantine", []string{"ready-for-agent", "backend"}, []string{"needs-human"}, []string{"ready-for-agent"}, []string{"backend", "needs-human"}},
	}
	for _, tc := range cases {
		if got := MergeTags(tc.current, tc.add, tc.remove); !slices.Equal(got, tc.want) {
			t.Errorf("%s: MergeTags = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// System.Tags is one flat string, so an incremental change is a read-modify-write —
// and a no-op change must not spend a write at all.
func TestUpdateTagsSkipsWriteWhenNothingChanges(t *testing.T) {
	var patches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patches++
		}
		_, _ = w.Write([]byte(`{"id":1,"fields":{"System.Tags":"ready-for-agent"}}`))
	}))
	defer srv.Close()

	if err := New(srv.URL, "pat").UpdateTags(context.Background(), "Contoso", 1, []string{"ready-for-agent"}, nil); err != nil {
		t.Fatalf("UpdateTags returned error: %v", err)
	}
	if patches != 0 {
		t.Errorf("patches = %d, want 0", patches)
	}
}

func TestUpdateTagsWritesMergedField(t *testing.T) {
	var gotOps []patchOp
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			gotContentType = r.Header.Get("Content-Type")
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotOps)
		}
		_, _ = w.Write([]byte(`{"id":1,"fields":{"System.Tags":"ready-for-agent; backend"}}`))
	}))
	defer srv.Close()

	err := New(srv.URL, "pat").UpdateTags(context.Background(), "Contoso", 1, []string{"needs-human"}, []string{"ready-for-agent"})
	if err != nil {
		t.Fatalf("UpdateTags returned error: %v", err)
	}
	if gotContentType != "application/json-patch+json" {
		t.Errorf("Content-Type = %q, want application/json-patch+json", gotContentType)
	}
	if len(gotOps) != 1 {
		t.Fatalf("got %d ops, want 1: %+v", len(gotOps), gotOps)
	}
	if gotOps[0].Path != "/fields/System.Tags" {
		t.Errorf("path = %q, want /fields/System.Tags", gotOps[0].Path)
	}
	if gotOps[0].Value != "backend; needs-human" {
		t.Errorf("value = %v, want %q", gotOps[0].Value, "backend; needs-human")
	}
}

// A state change and its note travel in one PATCH, because writing System.History
// is a field update like any other.
func TestSetStateWritesStateAndCommentInOnePatch(t *testing.T) {
	var gotOps []patchOp
	var patches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patches++
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotOps)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	err := New(srv.URL, "pat").SetState(context.Background(), "Contoso", 1, "Active", "Attached the PR.")
	if err != nil {
		t.Fatalf("SetState returned error: %v", err)
	}
	if patches != 1 {
		t.Errorf("patches = %d, want 1", patches)
	}
	if len(gotOps) != 2 {
		t.Fatalf("got %d ops, want 2: %+v", len(gotOps), gotOps)
	}
	if gotOps[0].Path != "/fields/System.State" || gotOps[0].Value != "Active" {
		t.Errorf("state op = %+v, want System.State=Active", gotOps[0])
	}
	if gotOps[1].Path != "/fields/System.History" {
		t.Errorf("comment op path = %q, want /fields/System.History", gotOps[1].Path)
	}
	if body, _ := gotOps[1].Value.(string); !strings.Contains(body, "Attached the PR.") {
		t.Errorf("comment op value = %v, want it to carry the note", gotOps[1].Value)
	}
}

func TestSetStateWithoutCommentWritesOnlyTheState(t *testing.T) {
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotOps)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	if err := New(srv.URL, "pat").SetState(context.Background(), "Contoso", 1, "Closed", "  "); err != nil {
		t.Fatalf("SetState returned error: %v", err)
	}
	if len(gotOps) != 1 {
		t.Fatalf("got %d ops, want 1: %+v", len(gotOps), gotOps)
	}
}

func TestSetStateEmptyStateIsAnError(t *testing.T) {
	if err := New("https://dev.azure.com/acme", "pat").SetState(context.Background(), "Contoso", 1, " ", ""); err == nil {
		t.Error("SetState with an empty state returned no error")
	}
}

// The work-item type is addressed as a "$Type" path segment on a create.
func TestCreateWorkItemPostsToDollarType(t *testing.T) {
	var gotPath, gotContentType string
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotOps)
		_, _ = w.Write([]byte(`{"id":500}`))
	}))
	defer srv.Close()

	id, err := New(srv.URL, "pat").CreateWorkItem(context.Background(), "Contoso", NewWorkItem{
		Type:        "Bug",
		Title:       "QA blocked",
		Description: "It broke",
		Tags:        []string{"HITL"},
		Parent:      42,
	})
	if err != nil {
		t.Fatalf("CreateWorkItem returned error: %v", err)
	}
	if id != 500 {
		t.Errorf("id = %d, want 500", id)
	}
	if gotPath != "/Contoso/_apis/wit/workitems/$Bug" {
		t.Errorf("path = %q, want /Contoso/_apis/wit/workitems/$Bug", gotPath)
	}
	if gotContentType != "application/json-patch+json" {
		t.Errorf("Content-Type = %q, want application/json-patch+json", gotContentType)
	}
	wantPaths := []string{"/fields/System.Title", "/fields/System.Description", "/fields/System.Tags", "/relations/-"}
	got := make([]string, len(gotOps))
	for i, op := range gotOps {
		got[i] = op.Path
	}
	if !slices.Equal(got, wantPaths) {
		t.Errorf("op paths = %v, want %v", got, wantPaths)
	}
}

// The comments route is served only under a preview api-version: Azure DevOps
// rejects it on plain 7.1, which would cost the build prompt the whole discussion.
func TestCommentsRendersBodiesToMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/workitems/1/comments") {
			t.Errorf("path = %q, want the comments route", r.URL.Path)
		}
		if got := r.URL.Query().Get("api-version"); got != commentsAPIVersion {
			t.Errorf("api-version = %q, want %q", got, commentsAPIVersion)
		}
		_, _ = w.Write([]byte(`{"comments":[
			{"text":"<div>Looks <b>good</b></div>","createdBy":{"displayName":"Ada L"}},
			{"text":"   ","createdBy":{"displayName":"Empty"}}]}`))
	}))
	defer srv.Close()

	comments, err := New(srv.URL, "pat").Comments(context.Background(), "Contoso", 1)
	if err != nil {
		t.Fatalf("Comments returned error: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("got %d comments, want 1 (the blank one is dropped): %+v", len(comments), comments)
	}
	if comments[0].Author != "Ada L" || comments[0].Body != "Looks **good**" {
		t.Errorf("comment = %+v, want Ada L / 'Looks **good**'", comments[0])
	}
}

func TestAddCommentWithBlankBodyMakesNoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("AddComment must not call the API with a blank body")
	}))
	defer srv.Close()

	if err := New(srv.URL, "pat").AddComment(context.Background(), "Contoso", 1, "  "); err != nil {
		t.Errorf("AddComment returned error: %v", err)
	}
}

// A screenshot reaches the attachment store as raw bytes named through the query
// string, and the response's URL is the handle a comment and a relation use.
func TestUploadAttachmentPostsRawBytes(t *testing.T) {
	var (
		method      string
		path        string
		query       string
		contentType string
		sent        string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		query = r.URL.Query().Get("fileName")
		contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		sent = string(body)
		_, _ = w.Write([]byte(`{"id":"guid-1","url":"https://dev.azure.com/acme/_apis/wit/attachments/guid-1?fileName=proof-1.png"}`))
	}))
	defer srv.Close()

	at, err := New(srv.URL, "pat").UploadAttachment(context.Background(), "Contoso", "proof-1.png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("UploadAttachment returned error: %v", err)
	}
	if method != http.MethodPost || path != "/Contoso/_apis/wit/attachments" {
		t.Errorf("request = %s %s, want POST /Contoso/_apis/wit/attachments", method, path)
	}
	if query != "proof-1.png" {
		t.Errorf("fileName = %q, want proof-1.png", query)
	}
	if contentType != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", contentType)
	}
	if sent != "png-bytes" {
		t.Errorf("body = %q, want the raw bytes", sent)
	}
	if at != "https://dev.azure.com/acme/_apis/wit/attachments/guid-1?fileName=proof-1.png" {
		t.Errorf("attachment URL = %q, want the one the API returned", at)
	}
}

// The discussion body renders the report and then each uploaded screenshot, so
// the note shows its proof inline.
func TestAddCommentWithImagesEmbedsImgTags(t *testing.T) {
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotOps)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	images := []Attachment{
		{URL: "https://dev.azure.com/acme/_apis/wit/attachments/guid-1?fileName=proof-1.png", Caption: "home"},
		{URL: "https://dev.azure.com/acme/_apis/wit/attachments/guid-2?fileName=proof-2.png", Caption: `settings "wide"`},
	}
	err := New(srv.URL, "pat").AddCommentWithImages(context.Background(), "Contoso", 1, "Verify passed", images)
	if err != nil {
		t.Fatalf("AddCommentWithImages returned error: %v", err)
	}
	if len(gotOps) != 1 || gotOps[0].Path != "/fields/System.History" {
		t.Fatalf("ops = %+v, want one System.History write", gotOps)
	}
	want := "<div>Verify passed</div>" +
		`<div><img src="https://dev.azure.com/acme/_apis/wit/attachments/guid-1?fileName=proof-1.png" alt="home"></div>` +
		`<div><img src="https://dev.azure.com/acme/_apis/wit/attachments/guid-2?fileName=proof-2.png" alt="settings &#34;wide&#34;"></div>`
	if body, _ := gotOps[0].Value.(string); body != want {
		t.Errorf("discussion body = %q, want %q", body, want)
	}
}

// The same uploads are linked as AttachedFile relations, so the work item lists
// them under Attachments whatever the discussion renders.
func TestAttachFilesAddsAttachedFileRelations(t *testing.T) {
	var gotOps []patchOp
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotOps)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	files := []Attachment{{URL: "https://dev.azure.com/acme/_apis/wit/attachments/guid-1", Caption: "home"}}
	if err := New(srv.URL, "pat").AttachFiles(context.Background(), "Contoso", 1, files); err != nil {
		t.Fatalf("AttachFiles returned error: %v", err)
	}
	if gotPath != "/Contoso/_apis/wit/workitems/1" {
		t.Errorf("path = %q, want the work item", gotPath)
	}
	if len(gotOps) != 1 || gotOps[0].Path != "/relations/-" {
		t.Fatalf("ops = %+v, want one relation add", gotOps)
	}
	rel, _ := gotOps[0].Value.(map[string]any)
	if rel["rel"] != relAttachedFile || rel["url"] != files[0].URL {
		t.Errorf("relation = %+v, want an %s pointing at the upload", rel, relAttachedFile)
	}
}

// No screenshot, no relation write: the note is a comment on its own.
func TestAttachFilesWithoutFilesMakesNoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("AttachFiles must not call the API with no files")
	}))
	defer srv.Close()

	if err := New(srv.URL, "pat").AttachFiles(context.Background(), "Contoso", 1, nil); err != nil {
		t.Errorf("AttachFiles returned error: %v", err)
	}
}

func TestWriteOpsWithoutCredentialsAreNotEnabled(t *testing.T) {
	c := New("", "")
	ctx := context.Background()
	if err := c.SetState(ctx, "Contoso", 1, "Done", ""); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("SetState err = %v, want ErrNotEnabled", err)
	}
	if err := c.UpdateTags(ctx, "Contoso", 1, []string{"a"}, nil); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("UpdateTags err = %v, want ErrNotEnabled", err)
	}
	if err := c.AddComment(ctx, "Contoso", 1, "hi"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("AddComment err = %v, want ErrNotEnabled", err)
	}
	if _, err := c.CreateWorkItem(ctx, "Contoso", NewWorkItem{Type: "Bug", Title: "t"}); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("CreateWorkItem err = %v, want ErrNotEnabled", err)
	}
}
