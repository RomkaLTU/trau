package webserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
)

const attachmentPNG = "\x89PNG\r\n\x1a\nfake image bytes"

// seedAttachment registers a pending attachment against the repo the hub knows as
// name, the way tracker sync will once it discovers files.
func seedAttachment(t *testing.T, home, root string, att hubstore.Attachment) hubstore.Attachment {
	t.Helper()
	att.Repo = root
	stored, err := testStoresAt(t, home).Attachments().Create(att)
	if err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	return stored
}

func attachmentURL(ts *httptest.Server, id int64) string {
	return ts.URL + APIPrefix + "/repos/acme/attachments/" + strconv.FormatInt(id, 10)
}

func listAttachments(t *testing.T, ts *httptest.Server, issue string) []AttachmentView {
	t.Helper()
	res := doReq(t, http.MethodGet, ts.URL+APIPrefix+"/repos/acme/issues/"+issue+"/attachments", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list attachments = %d, want 200", res.StatusCode)
	}
	var out []AttachmentView
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode attachment list: %v", err)
	}
	return out
}

// TestAttachmentAPILazyFetchCachesAndServes walks the whole first-view path: a
// pending row with a public source is downloaded, stored, and served with the
// headers a browser needs, and the second view never touches the origin again.
func TestAttachmentAPILazyFetchCachesAndServes(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, attachmentPNG)
	}))
	defer origin.Close()

	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	att := seedAttachment(t, home, root, hubstore.Attachment{
		IssueIdentifier: "COD-1",
		Source:          hubstore.AttachmentSourceExternal,
		SourceURL:       origin.URL + "/shot.png",
		Filename:        "shot.png",
	})
	_, ts := controlServer(t, home, nil)

	res := doReq(t, http.MethodGet, attachmentURL(ts, att.ID), nil)
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first GET = %d, want 200 (%s)", res.StatusCode, body)
	}
	if string(body) != attachmentPNG {
		t.Fatalf("body = %q, want the origin's bytes", body)
	}
	if got := res.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := res.Header.Get("Content-Disposition"); got != `inline; filename=shot.png` {
		t.Fatalf("Content-Disposition = %q, want inline with the filename", got)
	}
	if got := res.Header.Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want the immutable content-addressed policy", got)
	}
	if res.Header.Get("ETag") == "" {
		t.Fatalf("ETag is empty, want the content digest")
	}

	res = doReq(t, http.MethodGet, attachmentURL(ts, att.ID), nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second GET = %d, want 200", res.StatusCode)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("origin hits = %d, want 1 — the second view must read disk", n)
	}

	listed := listAttachments(t, ts, "COD-1")
	if len(listed) != 1 {
		t.Fatalf("list = %d attachments, want 1", len(listed))
	}
	if listed[0].State != hubstore.AttachmentCached || !listed[0].IsImage {
		t.Fatalf("listed = %+v, want cached and flagged as an image", listed[0])
	}
	if listed[0].URL != APIPrefix+"/repos/acme/attachments/"+strconv.FormatInt(att.ID, 10) {
		t.Fatalf("serve URL = %q, want the bytes endpoint", listed[0].URL)
	}
}

// ageAttachmentAttempt backdates a row's last fetch attempt past the retry floor,
// standing in for the minute a caller would otherwise have to wait out.
func ageAttachmentAttempt(t *testing.T, home string, id int64) {
	t.Helper()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	defer func() { _ = db.Close() }()
	stamp := time.Now().Add(-2 * hubstore.AttachmentRetryFloor).UTC().Format(time.RFC3339Nano)
	if _, err := db.SQL().Exec(`UPDATE attachments SET last_attempt_at = ? WHERE id = ?`, stamp, id); err != nil {
		t.Fatalf("age attachment attempt: %v", err)
	}
}

// TestAttachmentAPIFailedFetchSurfacesAndRetries covers the failure half: the
// reason reaches both the 502 and the drawer's list, the row is not poisoned —
// a later request tries again — and the retry floor keeps the views in between
// from turning a permanently missing file into a stream of tracker calls.
func TestAttachmentAPIFailedFetchSurfacesAndRetries(t *testing.T) {
	var broken atomic.Bool
	var hits atomic.Int32
	broken.Store(true)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if broken.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, attachmentPNG)
	}))
	defer origin.Close()

	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	att := seedAttachment(t, home, root, hubstore.Attachment{
		IssueIdentifier: "COD-1",
		Source:          hubstore.AttachmentSourceExternal,
		SourceURL:       origin.URL + "/missing.png",
		Filename:        "missing.png",
	})
	_, ts := controlServer(t, home, nil)

	res := doReq(t, http.MethodGet, attachmentURL(ts, att.ID), nil)
	var failure map[string]string
	_ = json.NewDecoder(res.Body).Decode(&failure)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("GET of a broken source = %d, want 502", res.StatusCode)
	}
	if failure["error"] == "" {
		t.Fatalf("502 body = %v, want the fetch reason", failure)
	}

	listed := listAttachments(t, ts, "COD-1")
	if listed[0].State != hubstore.AttachmentFailed || listed[0].Error == "" {
		t.Fatalf("listed = %+v, want failed with the reason", listed[0])
	}

	attempts := hits.Load()
	res = doReq(t, http.MethodGet, attachmentURL(ts, att.ID), nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("view inside the retry floor = %d, want the stored 502", res.StatusCode)
	}
	if got := hits.Load(); got != attempts {
		t.Fatalf("origin saw %d request(s) after the failure, want it left alone inside the retry floor", got-attempts)
	}

	ageAttachmentAttempt(t, home, att.ID)
	broken.Store(false)
	res = doReq(t, http.MethodGet, attachmentURL(ts, att.ID), nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("retry after the source recovered = %d, want 200 (%s)", res.StatusCode, body)
	}
	if listed = listAttachments(t, ts, "COD-1"); listed[0].State != hubstore.AttachmentCached || listed[0].Error != "" {
		t.Fatalf("after retry = %+v, want cached with the error cleared", listed[0])
	}
}

// TestAttachmentAPISingleFlightsConcurrentFirstViews guards the case a drawer
// full of images creates: several first views racing on one row must produce one
// download, not one per request.
func TestAttachmentAPISingleFlightsConcurrentFirstViews(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, attachmentPNG)
	}))
	defer origin.Close()

	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	att := seedAttachment(t, home, root, hubstore.Attachment{
		IssueIdentifier: "COD-1",
		Source:          hubstore.AttachmentSourceExternal,
		SourceURL:       origin.URL + "/shot.png",
	})
	_, ts := controlServer(t, home, nil)

	const viewers = 5
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range viewers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := http.Get(attachmentURL(ts, att.ID))
			if err != nil {
				t.Errorf("concurrent GET: %v", err)
				return
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusOK {
				t.Errorf("concurrent GET = %d, want 200", res.StatusCode)
			}
		}()
	}
	close(start)
	wg.Wait()

	if n := hits.Load(); n != 1 {
		t.Fatalf("origin hits = %d across %d concurrent first views, want 1", n, viewers)
	}
}

// TestAttachmentAPIFetchesLinearFilesWithTheRepoKey checks the credential seam: a
// Linear-hosted file is pulled with the repo's raw API key, the same header the
// sync client sends.
func TestAttachmentAPIFetchesLinearFilesWithTheRepoKey(t *testing.T) {
	gotAuth := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, attachmentPNG)
	}))
	defer origin.Close()

	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".trau.ini"), []byte("LINEAR_API_KEY=lin_api_secret\n"), 0o600); err != nil {
		t.Fatalf("write repo config: %v", err)
	}
	att := seedAttachment(t, home, root, hubstore.Attachment{
		IssueIdentifier: "COD-1",
		Source:          hubstore.AttachmentSourceLinear,
		SourceURL:       origin.URL + "/uploads/shot.png",
	})
	_, ts := controlServer(t, home, nil)

	res := doReq(t, http.MethodGet, attachmentURL(ts, att.ID), nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200", res.StatusCode)
	}
	// Linear takes the raw key, with no Bearer scheme.
	if auth := <-gotAuth; auth != "lin_api_secret" {
		t.Fatalf("Authorization = %q, want the raw Linear key", auth)
	}
}

// TestAttachmentAPIServesNonImagesAsDownloads keeps SVG and other file types out
// of the inline path, where a browser would execute them in the hub's origin.
func TestAttachmentAPIServesNonImagesAsDownloads(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	}))
	defer origin.Close()

	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	att := seedAttachment(t, home, root, hubstore.Attachment{
		IssueIdentifier: "COD-1",
		Source:          hubstore.AttachmentSourceExternal,
		SourceURL:       origin.URL + "/diagram.svg",
		Filename:        "diagram.svg",
	})
	_, ts := controlServer(t, home, nil)

	res := doReq(t, http.MethodGet, attachmentURL(ts, att.ID), nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Disposition"); got != `attachment; filename=diagram.svg` {
		t.Fatalf("Content-Disposition = %q, want a download", got)
	}
	if listed := listAttachments(t, ts, "COD-1"); listed[0].IsImage {
		t.Fatalf("SVG reported as an inline image")
	}
}

// TestAttachmentAPIRejectsUnknownRefs keeps a stray id or another repo's
// attachment from resolving through this repo's route.
func TestAttachmentAPIRejectsUnknownRefs(t *testing.T) {
	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	att := seedAttachment(t, home, root, hubstore.Attachment{
		IssueIdentifier: "COD-1",
		Source:          hubstore.AttachmentSourceUpload,
		Filename:        "paste.png",
	})
	stray := seedAttachment(t, home, filepath.Join(home, "elsewhere"), hubstore.Attachment{
		IssueIdentifier: "OTH-1",
		Source:          hubstore.AttachmentSourceUpload,
	})
	_, ts := controlServer(t, home, nil)

	for _, tc := range []struct {
		name string
		url  string
		want int
	}{
		{"unknown id", attachmentURL(ts, att.ID+9999), http.StatusNotFound},
		{"non-numeric id", ts.URL + APIPrefix + "/repos/acme/attachments/nope", http.StatusNotFound},
		{"another repo's attachment", attachmentURL(ts, stray.ID), http.StatusNotFound},
		// An upload with no bytes and no source has nothing to fetch or serve.
		{"upload without bytes", attachmentURL(ts, att.ID), http.StatusNotFound},
	} {
		res := doReq(t, http.MethodGet, tc.url, nil)
		_ = res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, res.StatusCode, tc.want)
		}
	}

	res := doReq(t, http.MethodPost, attachmentURL(ts, att.ID), nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", res.StatusCode)
	}
}

// TestAttachmentAPIUnregisterDropsAttachments closes the orphan path: dropping a
// repo takes its rows and cached files with it.
func TestAttachmentAPIUnregisterDropsAttachments(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	stores := testStoresAt(t, home)
	if err := stores.Registrations().Register(root); err != nil {
		t.Fatalf("register repo: %v", err)
	}
	att := seedAttachment(t, home, root, hubstore.Attachment{
		IssueIdentifier: "COD-1",
		Source:          hubstore.AttachmentSourceUpload,
	})
	atts := stores.Attachments()
	sha, size, err := atts.Blobs().Put(strings.NewReader(attachmentPNG), 0)
	if err != nil {
		t.Fatalf("store bytes: %v", err)
	}
	if err := atts.MarkCached(att.ID, sha, size, "image/png"); err != nil {
		t.Fatalf("MarkCached: %v", err)
	}

	_, ts := controlServer(t, home, nil)
	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unregister = %d, want 200", res.StatusCode)
	}

	rows, err := atts.ForIssue(root, "COD-1")
	if err != nil || len(rows) != 0 {
		t.Fatalf("attachments after unregister = %d rows err %v, want none", len(rows), err)
	}
	if _, err := os.Stat(atts.Blobs().Path(sha)); !os.IsNotExist(err) {
		t.Fatalf("cached file survived the unregister")
	}
}

// TestAttachmentSyncReconcileDropsVanishedIssues closes the loop the store hook
// exists for: an issue that left the tracker takes its attachment rows and cached
// bytes with it on the next reconcile, while a still-live issue keeps both.
func TestAttachmentSyncReconcileDropsVanishedIssues(t *testing.T) {
	fake := &fakeReader{synced: []tracker.SyncedIssue{syncedIssue("COD-1"), syncedIssue("COD-2")}}
	s, root := reconcileServer(t, fake)
	repo := workspaceRepo(root)
	if _, err := s.syncRepo(context.Background(), repo); err != nil {
		t.Fatalf("seed sync: %v", err)
	}

	atts := s.stores.Attachments()
	seed := func(issue, url, content string) (hubstore.Attachment, string) {
		t.Helper()
		att, err := atts.Create(hubstore.Attachment{
			Repo: root, IssueIdentifier: issue, Source: hubstore.AttachmentSourceLinear, SourceURL: url,
		})
		if err != nil {
			t.Fatalf("create attachment for %s: %v", issue, err)
		}
		sha, size, err := atts.Blobs().Put(strings.NewReader(content), 0)
		if err != nil {
			t.Fatalf("store bytes for %s: %v", issue, err)
		}
		if err := atts.MarkCached(att.ID, sha, size, "image/png"); err != nil {
			t.Fatalf("MarkCached %s: %v", issue, err)
		}
		return att, sha
	}
	gone, goneSHA := seed("COD-1", "https://uploads.linear.app/gone.png", "gone bytes")
	live, liveSHA := seed("COD-2", "https://uploads.linear.app/live.png", "live bytes")

	fake.identifiers = []string{"COD-2"}
	if err := s.reconcileRepo(context.Background(), repo); err != nil {
		t.Fatalf("reconcileRepo: %v", err)
	}

	if _, found, _ := atts.Get(root, gone.ID); found {
		t.Fatal("attachment of the vanished issue survived the reconcile")
	}
	if _, err := os.Stat(atts.Blobs().Path(goneSHA)); !os.IsNotExist(err) {
		t.Fatal("cached bytes of the vanished issue survived the reconcile")
	}
	if _, found, _ := atts.Get(root, live.ID); !found {
		t.Fatal("attachment of a still-live issue was dropped")
	}
	if _, err := os.Stat(atts.Blobs().Path(liveSHA)); err != nil {
		t.Fatalf("cached bytes of a still-live issue were collected: %v", err)
	}
}
