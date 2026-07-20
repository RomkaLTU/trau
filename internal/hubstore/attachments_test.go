package hubstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testAttachments(t *testing.T) *Attachments {
	t.Helper()
	home := t.TempDir()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(home, db.SQL(), nil, Retention{}).Attachments()
}

// cache stores content for an attachment the way a completed fetch does, and
// returns its digest.
func cache(t *testing.T, a *Attachments, id int64, content string) string {
	t.Helper()
	sha, size, err := a.Blobs().Put(strings.NewReader(content), 0)
	if err != nil {
		t.Fatalf("Put %q: %v", content, err)
	}
	if err := a.MarkCached(id, sha, size, "image/png"); err != nil {
		t.Fatalf("MarkCached %d: %v", id, err)
	}
	return sha
}

func blobExists(t *testing.T, a *Attachments, sha string) bool {
	t.Helper()
	_, err := os.Stat(a.Blobs().Path(sha))
	return err == nil
}

func TestAttachmentBlobRoundTripIsContentAddressed(t *testing.T) {
	a := testAttachments(t)
	const content = "screenshot bytes"

	sha, size, err := a.Blobs().Put(strings.NewReader(content), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	want := sha256.Sum256([]byte(content))
	if sha != hex.EncodeToString(want[:]) {
		t.Fatalf("sha = %q, want the sha256 of the content", sha)
	}
	if size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", size, len(content))
	}
	if got := a.Blobs().Path(sha); !strings.Contains(got, sha[:2]+string(os.PathSeparator)+sha) {
		t.Fatalf("Path = %q, want a <sha[:2]>/<sha> fanout", got)
	}

	f, err := a.Blobs().Open(sha)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if string(got) != content {
		t.Fatalf("blob = %q, want %q", got, content)
	}

	// The same content written twice is one file, and re-putting it is not an error.
	again, _, err := a.Blobs().Put(strings.NewReader(content), 0)
	if err != nil || again != sha {
		t.Fatalf("second Put = %q err %v, want the same digest", again, err)
	}
}

func TestAttachmentBlobPutRefusesOversizedStream(t *testing.T) {
	a := testAttachments(t)
	content := strings.Repeat("x", 64)

	sha, _, err := a.Blobs().Put(strings.NewReader(content), 32)
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("Put over cap = %v, want ErrAttachmentTooLarge", err)
	}
	if sha != "" {
		t.Fatalf("sha = %q, want empty on refusal", sha)
	}

	entries, err := os.ReadDir(a.Blobs().Root())
	if err != nil {
		t.Fatalf("read blob root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("blob root holds %d entries, want the partial write discarded", len(entries))
	}

	// Exactly at the cap still stores: the limit is a maximum, not an exclusive bound.
	if _, _, err := a.Blobs().Put(strings.NewReader(content), 64); err != nil {
		t.Fatalf("Put at cap: %v", err)
	}
}

func TestAttachmentCreateDedupesOnSourceURL(t *testing.T) {
	a := testAttachments(t)
	const url = "https://uploads.linear.app/shot.png"

	first, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceLinear, SourceURL: url,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cache(t, a, first.ID, "bytes")

	// A re-sync re-registers the same URL: it must land on the existing row rather
	// than stacking a second one and re-downloading.
	second, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceLinear,
		SourceURL: url, Filename: "shot.png",
	})
	if err != nil {
		t.Fatalf("Create again: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-Create id = %d, want the existing %d", second.ID, first.ID)
	}
	if second.State != AttachmentCached {
		t.Fatalf("state = %q, want the cached state preserved", second.State)
	}
	if second.Filename != "shot.png" {
		t.Fatalf("filename = %q, want the newly learned name folded in", second.Filename)
	}

	rows, err := a.ForIssue("/repo", "COD-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ForIssue = %d rows err %v, want exactly 1", len(rows), err)
	}

	// The unique index is per repo, so another repo tracking the same URL is its own row.
	other, err := a.Create(Attachment{Repo: "/other", Source: AttachmentSourceLinear, SourceURL: url})
	if err != nil {
		t.Fatalf("Create in other repo: %v", err)
	}
	if other.ID == first.ID {
		t.Fatalf("other repo reused id %d, want a distinct row", other.ID)
	}
}

func TestAttachmentUploadsNeverDedupe(t *testing.T) {
	a := testAttachments(t)
	upload := Attachment{Repo: "/repo", Source: AttachmentSourceUpload, Filename: "paste.png"}

	first, err := a.Create(upload)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := a.Create(upload)
	if err != nil {
		t.Fatalf("Create again: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("two uploads share id %d, want distinct rows", first.ID)
	}
}

func TestAttachmentGetIsScopedToRepo(t *testing.T) {
	a := testAttachments(t)
	att, err := a.Create(Attachment{Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, found, err := a.Get("/repo", att.ID); !found || err != nil {
		t.Fatalf("Get in owning repo = found %v err %v, want found", found, err)
	}
	if _, found, err := a.Get("/other", att.ID); found || err != nil {
		t.Fatalf("Get from another repo = found %v err %v, want absent", found, err)
	}
}

func TestAttachmentMarkFailedThenCachedClearsError(t *testing.T) {
	a := testAttachments(t)
	att, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceExternal,
		SourceURL: "https://example.test/a.png",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if att.State != AttachmentPending {
		t.Fatalf("state = %q, want pending", att.State)
	}

	if err := a.MarkFailed(att.ID, "source responded 404 Not Found"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	failed, _, err := a.Get("/repo", att.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if failed.State != AttachmentFailed || failed.Error == "" {
		t.Fatalf("after MarkFailed = state %q error %q, want failed with a reason", failed.State, failed.Error)
	}

	// A retry that succeeds must clear the stale reason, not leave it beside cached bytes.
	cache(t, a, att.ID, "bytes")
	cached, _, err := a.Get("/repo", att.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cached.State != AttachmentCached || cached.Error != "" || cached.SHA256 == "" {
		t.Fatalf("after MarkCached = %+v, want cached with no error and a digest", cached)
	}
	if cached.FetchedAt == "" {
		t.Fatalf("fetched_at is empty, want the fetch stamped")
	}
}

func TestAttachmentDeleteRefcountsSharedBlob(t *testing.T) {
	a := testAttachments(t)
	first, err := a.Create(Attachment{Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create COD-1: %v", err)
	}
	second, err := a.Create(Attachment{Repo: "/repo", IssueIdentifier: "COD-2", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create COD-2: %v", err)
	}

	// The same screenshot on two tickets is one blob with two referring rows.
	sha := cache(t, a, first.ID, "shared bytes")
	if got := cache(t, a, second.ID, "shared bytes"); got != sha {
		t.Fatalf("second digest = %q, want the same blob %q", got, sha)
	}

	if err := a.DeleteForIssue("/repo", "COD-1"); err != nil {
		t.Fatalf("DeleteForIssue COD-1: %v", err)
	}
	if !blobExists(t, a, sha) {
		t.Fatalf("blob removed while COD-2 still references it")
	}

	if err := a.DeleteForIssue("/repo", "COD-2"); err != nil {
		t.Fatalf("DeleteForIssue COD-2: %v", err)
	}
	if blobExists(t, a, sha) {
		t.Fatalf("blob survived its last referring row")
	}
}

func TestAttachmentDeleteForRepoLeavesNothing(t *testing.T) {
	a := testAttachments(t)
	mine, err := a.Create(Attachment{Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	theirs, err := a.Create(Attachment{Repo: "/other", IssueIdentifier: "OTH-1", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	mineSHA := cache(t, a, mine.ID, "mine")
	theirsSHA := cache(t, a, theirs.ID, "theirs")

	if err := a.DeleteForRepo("/repo"); err != nil {
		t.Fatalf("DeleteForRepo: %v", err)
	}
	rows, err := a.ForIssue("/repo", "COD-1")
	if err != nil || len(rows) != 0 {
		t.Fatalf("ForIssue after DeleteForRepo = %d rows err %v, want none", len(rows), err)
	}
	if blobExists(t, a, mineSHA) {
		t.Fatalf("unregistered repo's blob survived")
	}
	if !blobExists(t, a, theirsSHA) {
		t.Fatalf("another repo's blob was collected")
	}
}

func TestAttachmentReconcileIssuesDropsVanishedTickets(t *testing.T) {
	a := testAttachments(t)
	live, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceLinear,
		SourceURL: "https://uploads.linear.app/live.png",
	})
	if err != nil {
		t.Fatalf("Create live: %v", err)
	}
	gone, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "COD-2", Source: AttachmentSourceLinear,
		SourceURL: "https://uploads.linear.app/gone.png",
	})
	if err != nil {
		t.Fatalf("Create gone: %v", err)
	}
	upload, err := a.Create(Attachment{Repo: "/repo", IssueIdentifier: "COD-2", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create upload: %v", err)
	}
	unbound, err := a.Create(Attachment{Repo: "/repo", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create unbound: %v", err)
	}
	liveSHA := cache(t, a, live.ID, "live")
	goneSHA := cache(t, a, gone.ID, "gone")

	if err := a.ReconcileIssues("/repo", []string{"COD-1"}); err != nil {
		t.Fatalf("ReconcileIssues: %v", err)
	}

	if _, found, _ := a.Get("/repo", live.ID); !found {
		t.Fatalf("still-live issue's attachment was dropped")
	}
	if _, found, _ := a.Get("/repo", gone.ID); found {
		t.Fatalf("vanished issue's attachment survived")
	}
	// An upload has no upstream to have vanished from, so reconcile leaves it to the
	// issue's own delete path.
	if _, found, _ := a.Get("/repo", upload.ID); !found {
		t.Fatalf("upload was dropped by tracker reconciliation")
	}
	if _, found, _ := a.Get("/repo", unbound.ID); !found {
		t.Fatalf("unbound upload was dropped by tracker reconciliation")
	}
	if !blobExists(t, a, liveSHA) {
		t.Fatalf("live blob was collected")
	}
	if blobExists(t, a, goneSHA) {
		t.Fatalf("dropped row's blob survived")
	}
}

func TestAttachmentReconcileIssueDropsUnreferencedURLs(t *testing.T) {
	a := testAttachments(t)
	kept, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceLinear,
		SourceURL: "https://uploads.linear.app/kept.png",
	})
	if err != nil {
		t.Fatalf("Create kept: %v", err)
	}
	removed, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceLinear,
		SourceURL: "https://uploads.linear.app/removed.png",
	})
	if err != nil {
		t.Fatalf("Create removed: %v", err)
	}
	upload, err := a.Create(Attachment{Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create upload: %v", err)
	}
	other, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "COD-2", Source: AttachmentSourceLinear,
		SourceURL: "https://uploads.linear.app/other.png",
	})
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	removedSHA := cache(t, a, removed.ID, "removed")

	if err := a.ReconcileIssue("/repo", "COD-1", []string{"https://uploads.linear.app/kept.png"}); err != nil {
		t.Fatalf("ReconcileIssue: %v", err)
	}

	if _, found, _ := a.Get("/repo", kept.ID); !found {
		t.Fatalf("still-referenced URL was dropped")
	}
	if _, found, _ := a.Get("/repo", removed.ID); found {
		t.Fatalf("URL the issue no longer references survived")
	}
	if _, found, _ := a.Get("/repo", upload.ID); !found {
		t.Fatalf("upload was dropped by a tracker reconcile")
	}
	if _, found, _ := a.Get("/repo", other.ID); !found {
		t.Fatalf("another issue's attachment was dropped")
	}
	if blobExists(t, a, removedSHA) {
		t.Fatalf("dropped row's blob survived")
	}
}

func TestAttachmentReconcileIssueWithNoLiveURLsClearsTheIssue(t *testing.T) {
	a := testAttachments(t)
	last, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceLinear,
		SourceURL: "https://uploads.linear.app/last.png",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	upload, err := a.Create(Attachment{Repo: "/repo", IssueIdentifier: "COD-1", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create upload: %v", err)
	}

	if err := a.ReconcileIssue("/repo", "COD-1", nil); err != nil {
		t.Fatalf("ReconcileIssue: %v", err)
	}

	if _, found, _ := a.Get("/repo", last.ID); found {
		t.Fatalf("a ticket that lost its last image kept the row")
	}
	if _, found, _ := a.Get("/repo", upload.ID); !found {
		t.Fatalf("upload was dropped by a tracker reconcile")
	}
}

func TestAttachmentBindToIssue(t *testing.T) {
	a := testAttachments(t)
	first, err := a.Create(Attachment{Repo: "/repo", Source: AttachmentSourceUpload, Filename: "a.png"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := a.Create(Attachment{Repo: "/repo", Source: AttachmentSourceUpload, Filename: "b.png"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stranger, err := a.Create(Attachment{Repo: "/other", Source: AttachmentSourceUpload})
	if err != nil {
		t.Fatalf("Create stranger: %v", err)
	}

	if err := a.BindToIssue("/repo", []int64{first.ID, second.ID, stranger.ID}, "COD-9"); err != nil {
		t.Fatalf("BindToIssue: %v", err)
	}
	rows, err := a.ForIssue("/repo", "COD-9")
	if err != nil || len(rows) != 2 {
		t.Fatalf("ForIssue = %d rows err %v, want the 2 bound uploads", len(rows), err)
	}
	// Binding is repo-scoped: an id from another repo is not swept along.
	bound, _, err := a.Get("/other", stranger.ID)
	if err != nil {
		t.Fatalf("Get stranger: %v", err)
	}
	if bound.IssueIdentifier != "" {
		t.Fatalf("stranger bound to %q, want untouched", bound.IssueIdentifier)
	}
}

func TestAttachmentBindToIssueLeavesTrackerAttachments(t *testing.T) {
	a := testAttachments(t)
	synced, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: "SYNC-500", Source: AttachmentSourceLinear,
		SourceURL: "https://uploads.linear.app/live.png",
	})
	if err != nil {
		t.Fatalf("Create synced: %v", err)
	}

	if err := a.BindToIssue("/repo", []int64{synced.ID}, "ACME-1"); err != nil {
		t.Fatalf("BindToIssue: %v", err)
	}

	// A pasted tracker URL must not steal the synced issue's attachment, or the
	// next ReconcileIssues would drop it from the still-live ticket.
	row, _, err := a.Get("/repo", synced.ID)
	if err != nil {
		t.Fatalf("Get synced: %v", err)
	}
	if row.IssueIdentifier != "SYNC-500" {
		t.Fatalf("tracker attachment rebound to %q, want SYNC-500", row.IssueIdentifier)
	}
	if err := a.ReconcileIssues("/repo", []string{"SYNC-500"}); err != nil {
		t.Fatalf("ReconcileIssues: %v", err)
	}
	if _, found, _ := a.Get("/repo", synced.ID); !found {
		t.Fatalf("still-live synced ticket lost its attachment after a paste + reconcile")
	}
}

func TestAttachmentIsImage(t *testing.T) {
	cases := []struct {
		mime string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"IMAGE/PNG", true},
		{"image/png; charset=binary", true},
		// SVG is a scriptable document, so it is never treated as an inline image.
		{"image/svg+xml", false},
		{"application/pdf", false},
		{"", false},
	}
	for _, c := range cases {
		if got := AttachmentIsImage(c.mime); got != c.want {
			t.Errorf("AttachmentIsImage(%q) = %v, want %v", c.mime, got, c.want)
		}
	}
}
