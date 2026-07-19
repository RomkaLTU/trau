package webserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
)

const (
	linearShot = "https://uploads.linear.app/a/b/screen.png"
	githubShot = "https://raw.githubusercontent.com/acme/repo/main/flow.png"
)

// issueWithAttachments is a pulled issue carrying exactly the files found on it,
// the shape a reader hands sync after scanning a ticket.
func issueWithAttachments(atts ...tracker.Attachment) []tracker.SyncedIssue {
	return []tracker.SyncedIssue{{
		ID:          "COD-1",
		Title:       "First",
		UpdatedAt:   "2026-07-10T12:00:00Z",
		Attachments: atts,
	}}
}

// syncAttachmentServer wires a hub whose only repo is "acme" and returns a sync
// trigger plus a reader of what the attachment store holds for an issue, so a
// test can sync twice and compare.
func syncAttachmentServer(t *testing.T, fake tracker.Reader) (sync func(), rows func(string) []hubstore.Attachment) {
	t.Helper()
	_, sync, rows = attachmentServer(t, fake)
	return sync, rows
}

// attachmentServer builds the hub behind both helpers, handing back the server
// itself for the paths that write issues directly rather than through a sync.
func attachmentServer(t *testing.T, fake tracker.Reader) (ts *httptest.Server, sync func(), rows func(string) []hubstore.Attachment) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, "LINEAR_TEAM=COD\n")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	if fake != nil {
		s.newReader = func(config.Config) (tracker.Reader, error) { return fake, nil }
	}
	ts = httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	sync = func() {
		res, _ := postSync(t, ts, "acme")
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("sync status = %d, want 200", res.StatusCode)
		}
	}
	rows = func(id string) []hubstore.Attachment {
		out, err := testStoresAt(t, home).Attachments().ForIssue(root, id)
		if err != nil {
			t.Fatalf("ForIssue %s: %v", id, err)
		}
		return out
	}
	return ts, sync, rows
}

func TestSyncRegistersDiscoveredAttachmentsAsPending(t *testing.T) {
	fake := &fakeReader{synced: issueWithAttachments(
		tracker.Attachment{URL: linearShot, Filename: "screen.png", Source: tracker.AttachmentLinear},
		tracker.Attachment{URL: githubShot, Filename: "flow.png", Source: tracker.AttachmentExternal},
	)}
	sync, rows := syncAttachmentServer(t, fake)

	sync()

	found := rows("COD-1")
	if len(found) != 2 {
		t.Fatalf("attachments = %+v, want both discovered files", found)
	}
	bySource := map[string]hubstore.Attachment{}
	for _, at := range found {
		bySource[at.Source] = at
	}
	linear, ok := bySource[hubstore.AttachmentSourceLinear]
	if !ok || linear.SourceURL != linearShot || linear.Filename != "screen.png" {
		t.Errorf("linear row = %+v, want the upload URL and filename", linear)
	}
	if linear.State != hubstore.AttachmentPending {
		t.Errorf("state = %q, want %q — sync writes metadata only", linear.State, hubstore.AttachmentPending)
	}
	if external, ok := bySource[hubstore.AttachmentSourceExternal]; !ok || external.SourceURL != githubShot {
		t.Errorf("external row = %+v, want the GitHub-hosted image registered as external", external)
	}
}

func TestResyncDoesNotDuplicateAttachments(t *testing.T) {
	fake := &fakeReader{synced: issueWithAttachments(
		tracker.Attachment{URL: linearShot, Filename: "screen.png", Source: tracker.AttachmentLinear},
	)}
	sync, rows := syncAttachmentServer(t, fake)

	sync()
	first := rows("COD-1")
	if len(first) != 1 {
		t.Fatalf("attachments after first sync = %+v, want 1", first)
	}

	sync()
	second := rows("COD-1")
	if len(second) != 1 {
		t.Fatalf("attachments after re-sync = %+v, want the same single row", second)
	}
	if second[0].ID != first[0].ID {
		t.Errorf("row id = %d, want the original %d rather than a fresh insert", second[0].ID, first[0].ID)
	}
}

func TestSyncDropsAttachmentsRemovedFromTheTicket(t *testing.T) {
	fake := &fakeReader{synced: issueWithAttachments(
		tracker.Attachment{URL: linearShot, Filename: "screen.png", Source: tracker.AttachmentLinear},
		tracker.Attachment{URL: githubShot, Filename: "flow.png", Source: tracker.AttachmentExternal},
	)}
	sync, rows := syncAttachmentServer(t, fake)

	sync()
	if before := rows("COD-1"); len(before) != 2 {
		t.Fatalf("attachments after first sync = %+v, want 2", before)
	}

	fake.synced = issueWithAttachments(
		tracker.Attachment{URL: linearShot, Filename: "screen.png", Source: tracker.AttachmentLinear},
	)
	sync()

	kept := rows("COD-1")
	if len(kept) != 1 || kept[0].SourceURL != linearShot {
		t.Fatalf("attachments = %+v, want only the URL the ticket still references", kept)
	}
}

func TestSyncOfAnIssueWithNoFilesLeavesOtherIssuesAlone(t *testing.T) {
	fake := &fakeReader{synced: []tracker.SyncedIssue{
		{
			ID: "COD-1", Title: "First", UpdatedAt: "2026-07-10T12:00:00Z",
			Attachments: []tracker.Attachment{
				{URL: linearShot, Filename: "screen.png", Source: tracker.AttachmentLinear},
			},
		},
		{ID: "COD-2", Title: "Second", UpdatedAt: "2026-07-10T12:00:00Z"},
	}}
	sync, rows := syncAttachmentServer(t, fake)

	sync()

	if first := rows("COD-1"); len(first) != 1 {
		t.Fatalf("COD-1 attachments = %+v, want the file it references", first)
	}
	if second := rows("COD-2"); len(second) != 0 {
		t.Fatalf("COD-2 attachments = %+v, want none", second)
	}
}

func TestInternalIssueRegistersAndPrunesEmbeddedImages(t *testing.T) {
	ts, _, rows := attachmentServer(t, nil)

	_, created := createInternal(t, ts, "acme", InternalIssueRequest{
		Title:       "Pasted a screenshot",
		Description: "before ![](" + githubShot + ") after",
	})
	found := rows(created.ID)
	if len(found) != 1 || found[0].SourceURL != githubShot {
		t.Fatalf("attachments = %+v, want the embedded image registered", found)
	}
	if found[0].Source != hubstore.AttachmentSourceExternal {
		t.Errorf("source = %q, want external", found[0].Source)
	}

	res := patchJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/internal/"+created.ID, InternalIssueRequest{
		Title:       "Pasted a screenshot",
		Description: "the image is gone now",
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d, want 200", res.StatusCode)
	}
	if after := rows(created.ID); len(after) != 0 {
		t.Fatalf("attachments = %+v, want the removed image pruned", after)
	}
}
