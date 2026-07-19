package webserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// uploadReq posts one multipart file to the repo's attachment endpoint, letting
// the caller set the part's declared content type so the mime rules can be
// exercised, including the header a client could lie in.
func uploadReq(t *testing.T, ts *httptest.Server, repo, filename, contentType string, data []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+APIPrefix+"/repos/"+repo+"/attachments", &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return res
}

// TestUploadStoresAndServesImage is the paste happy path: a posted PNG lands as a
// cached upload with the serve URL the editor embeds, and that URL streams the
// bytes back inline, ready to bind to an issue.
func TestUploadStoresAndServesImage(t *testing.T) {
	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)

	res := uploadReq(t, ts, "acme", "paste.png", "image/png", []byte(attachmentPNG))
	var view AttachmentView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatalf("decode upload: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload = %d, want 201", res.StatusCode)
	}
	if view.Source != hubstore.AttachmentSourceUpload || view.State != hubstore.AttachmentCached {
		t.Fatalf("view = %+v, want a cached upload", view)
	}
	if view.MimeType != "image/png" || !view.IsImage {
		t.Fatalf("view = %+v, want a png flagged as an image", view)
	}
	want := APIPrefix + fmt.Sprintf("/repos/acme/attachments/%d", view.ID)
	if view.URL != want {
		t.Fatalf("url = %q, want %q", view.URL, want)
	}

	stored, found, err := testStoresAt(t, home).Attachments().Get(root, view.ID)
	if err != nil || !found {
		t.Fatalf("stored row = found %v err %v, want the upload persisted", found, err)
	}
	if stored.IssueIdentifier != "" {
		t.Fatalf("upload bound to %q before any issue referenced it", stored.IssueIdentifier)
	}

	serve := doReq(t, http.MethodGet, ts.URL+view.URL, nil)
	body, _ := io.ReadAll(serve.Body)
	_ = serve.Body.Close()
	if serve.StatusCode != http.StatusOK {
		t.Fatalf("serve = %d, want 200", serve.StatusCode)
	}
	if string(body) != attachmentPNG {
		t.Fatalf("served bytes = %q, want the uploaded bytes", body)
	}
	if got := serve.Header.Get("Content-Disposition"); got != `inline; filename=paste.png` {
		t.Fatalf("Content-Disposition = %q, want inline", got)
	}
}

// TestUploadRejectsNonImage keeps a spoofed or plain file out of the store: the
// type is judged by the bytes, so a text payload labelled image/png is still
// refused and nothing is written.
func TestUploadRejectsNonImage(t *testing.T) {
	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)

	res := uploadReq(t, ts, "acme", "notes.png", "image/png", []byte("this is plainly text, not an image at all"))
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-image upload = %d, want 400", res.StatusCode)
	}
	if rows, err := testStoresAt(t, home).Attachments().ForIssue(root, ""); err != nil || len(rows) != 0 {
		t.Fatalf("stored %d rows err %v after a rejected upload, want none", len(rows), err)
	}
}

// TestUploadRejectsOversize enforces the 10 MB cap and leaves nothing behind when
// a stream blows past it.
func TestUploadRejectsOversize(t *testing.T) {
	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)

	data := make([]byte, uploadMaxBytes+1024)
	copy(data, attachmentPNG)
	res := uploadReq(t, ts, "acme", "huge.png", "image/png", data)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload = %d, want 413", res.StatusCode)
	}
	if rows, err := testStoresAt(t, home).Attachments().ForIssue(root, ""); err != nil || len(rows) != 0 {
		t.Fatalf("stored %d rows err %v after an oversize upload, want none", len(rows), err)
	}
}

// TestUploadServesSVGAsDownload admits an SVG the client labels as one but never
// lets a browser execute it in the hub's origin: it is served as a download.
func TestUploadServesSVGAsDownload(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)

	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)
	res := uploadReq(t, ts, "acme", "diagram.svg", "image/svg+xml", svg)
	var view AttachmentView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatalf("decode upload: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("svg upload = %d, want 201", res.StatusCode)
	}
	if view.MimeType != "image/svg+xml" || view.IsImage {
		t.Fatalf("view = %+v, want an svg not flagged as an inline image", view)
	}

	serve := doReq(t, http.MethodGet, ts.URL+view.URL, nil)
	_ = serve.Body.Close()
	if got := serve.Header.Get("Content-Disposition"); got != `attachment; filename=diagram.svg` {
		t.Fatalf("Content-Disposition = %q, want a download", got)
	}
}

func TestUploadRejectsNonPOST(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)

	res := doReq(t, http.MethodGet, ts.URL+APIPrefix+"/repos/acme/attachments", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", res.StatusCode)
	}

	res = uploadReq(t, ts, "ghost", "paste.png", "image/png", []byte(attachmentPNG))
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("upload to unknown repo = %d, want 404", res.StatusCode)
	}
}

// TestInternalIssueBindsUploadedAttachments closes the paste-to-issue loop: an
// upload the created issue's markdown references is bound to it, and editing the
// body to reference a different upload binds that one too.
func TestInternalIssueBindsUploadedAttachments(t *testing.T) {
	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)

	upload := func(name string) AttachmentView {
		t.Helper()
		res := uploadReq(t, ts, "acme", name, "image/png", []byte(attachmentPNG))
		var view AttachmentView
		if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
			t.Fatalf("decode upload: %v", err)
		}
		_ = res.Body.Close()
		return view
	}
	atts := testStoresAt(t, home).Attachments()

	first := upload("first.png")
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/internal", InternalIssueRequest{
		Title:       "Has a screenshot",
		Description: fmt.Sprintf("before\n\n![shot](%s)\n\nafter", first.URL),
	})
	var created InternalIssueResponse
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d, want 201", res.StatusCode)
	}

	if row, _, _ := atts.Get(root, first.ID); row.IssueIdentifier != created.ID {
		t.Fatalf("upload bound to %q, want %q", row.IssueIdentifier, created.ID)
	}

	second := upload("second.png")
	patch := patchJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/internal/"+created.ID, InternalIssueRequest{
		Title:       "Has a screenshot",
		Description: fmt.Sprintf("now with ![two](%s)", second.URL),
	})
	_ = patch.Body.Close()
	if patch.StatusCode != http.StatusOK {
		t.Fatalf("update = %d, want 200", patch.StatusCode)
	}
	if row, _, _ := atts.Get(root, second.ID); row.IssueIdentifier != created.ID {
		t.Fatalf("edited-in upload bound to %q, want %q", row.IssueIdentifier, created.ID)
	}
}
