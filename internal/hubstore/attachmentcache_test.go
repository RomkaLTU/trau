package hubstore

import (
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func cappedAttachments(t *testing.T, capBytes int64) *Attachments {
	t.Helper()
	home := t.TempDir()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(home, db.SQL(), nil, Retention{AttachmentCacheBytes: capBytes}).Attachments()
}

func trackerAttachment(t *testing.T, a *Attachments, identifier, url string) Attachment {
	t.Helper()
	att, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: identifier, Source: AttachmentSourceLinear, SourceURL: url,
	})
	if err != nil {
		t.Fatalf("Create %s: %v", url, err)
	}
	return att
}

func uploadAttachment(t *testing.T, a *Attachments, identifier string) Attachment {
	t.Helper()
	att, err := a.Create(Attachment{
		Repo: "/repo", IssueIdentifier: identifier, Source: AttachmentSourceUpload, State: AttachmentCached,
	})
	if err != nil {
		t.Fatalf("Create upload: %v", err)
	}
	return att
}

var cacheEpoch = time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)

func TestAttachmentCacheEvictsLeastRecentlyServedFirst(t *testing.T) {
	a := cappedAttachments(t, 10)
	cold := trackerAttachment(t, a, "COD-1", "https://tracker.example/cold.png")
	warm := trackerAttachment(t, a, "COD-1", "https://tracker.example/warm.png")
	warmest := trackerAttachment(t, a, "COD-1", "https://tracker.example/warmest.png")

	a.now = func() time.Time { return cacheEpoch }
	coldSHA := cache(t, a, cold.ID, "aaa")
	a.now = func() time.Time { return cacheEpoch.Add(time.Hour) }
	warmSHA := cache(t, a, warm.ID, "bbbb")
	a.now = func() time.Time { return cacheEpoch.Add(2 * time.Hour) }
	warmestSHA := cache(t, a, warmest.ID, "ccccc")

	evicted, freed, err := a.EnforceCacheCap("")
	if err != nil {
		t.Fatalf("EnforceCacheCap: %v", err)
	}
	if evicted != 1 || freed != 3 {
		t.Fatalf("EnforceCacheCap = %d file(s) / %d bytes, want 1 / 3 — just enough to get under the cap", evicted, freed)
	}
	if blobExists(t, a, coldSHA) {
		t.Fatal("the least recently served blob survived; eviction is not ranking on recency")
	}
	if !blobExists(t, a, warmSHA) || !blobExists(t, a, warmestSHA) {
		t.Fatal("eviction reclaimed more than it needed to")
	}

	got, _, err := a.Get("/repo", cold.ID)
	if err != nil {
		t.Fatalf("Get evicted row: %v", err)
	}
	if got.State != AttachmentPending || got.SHA256 != "" {
		t.Fatalf("evicted row = state %q sha %q, want pending with no digest so the next view re-fetches it", got.State, got.SHA256)
	}
	if got.SizeBytes != 3 {
		t.Fatalf("evicted row size = %d, want 3 kept so the drawer still describes the file", got.SizeBytes)
	}
}

func TestAttachmentCacheNeverEvictsUploads(t *testing.T) {
	a := cappedAttachments(t, 1)
	upload := uploadAttachment(t, a, "COD-1")
	tracked := trackerAttachment(t, a, "COD-1", "https://tracker.example/shot.png")

	a.now = func() time.Time { return cacheEpoch }
	uploadSHA := cache(t, a, upload.ID, "pasted screenshot")
	a.now = func() time.Time { return cacheEpoch.Add(time.Hour) }
	trackedSHA := cache(t, a, tracked.ID, "downloaded file")

	evicted, _, err := a.EnforceCacheCap("")
	if err != nil {
		t.Fatalf("EnforceCacheCap: %v", err)
	}
	if evicted != 1 {
		t.Fatalf("EnforceCacheCap evicted %d file(s), want only the tracker-sourced one", evicted)
	}
	if !blobExists(t, a, uploadSHA) {
		t.Fatal("an upload was evicted — it has no upstream copy to re-fetch from")
	}
	if blobExists(t, a, trackedSHA) {
		t.Fatal("the tracker file survived a cap it blew past")
	}
	got, _, err := a.Get("/repo", upload.ID)
	if err != nil {
		t.Fatalf("Get upload: %v", err)
	}
	if got.State != AttachmentCached || got.SHA256 != uploadSHA {
		t.Fatalf("upload row = state %q sha %q, want it left cached", got.State, got.SHA256)
	}
}

func TestAttachmentCacheKeepsADigestAnUploadShares(t *testing.T) {
	a := cappedAttachments(t, 1)
	const content = "the same image on both tickets"

	upload := uploadAttachment(t, a, "COD-1")
	tracked := trackerAttachment(t, a, "COD-2", "https://tracker.example/same.png")
	a.now = func() time.Time { return cacheEpoch }
	sha := cache(t, a, upload.ID, content)
	if got := cache(t, a, tracked.ID, content); got != sha {
		t.Fatalf("identical content produced digests %q and %q", sha, got)
	}

	evicted, _, err := a.EnforceCacheCap("")
	if err != nil {
		t.Fatalf("EnforceCacheCap: %v", err)
	}
	if evicted != 0 {
		t.Fatalf("EnforceCacheCap evicted %d file(s), want 0 — the upload pins the digest", evicted)
	}
	if !blobExists(t, a, sha) {
		t.Fatal("evicting the tracker row took the upload's only copy of the bytes with it")
	}
}

func TestAttachmentPruneDropsStaleUnboundUploads(t *testing.T) {
	a := testAttachments(t)

	a.now = func() time.Time { return cacheEpoch }
	abandoned := uploadAttachment(t, a, "")
	abandonedSHA := cache(t, a, abandoned.ID, "never saved")
	bound := uploadAttachment(t, a, "COD-1")
	boundSHA := cache(t, a, bound.ID, "landed on a ticket")

	a.now = func() time.Time { return cacheEpoch.Add(25 * time.Hour) }
	inFlight := uploadAttachment(t, a, "")
	inFlightSHA := cache(t, a, inFlight.ID, "editor still open")

	if err := a.PruneUnboundUploads(); err != nil {
		t.Fatalf("PruneUnboundUploads: %v", err)
	}
	if _, found, err := a.Get("/repo", abandoned.ID); found || err != nil {
		t.Fatalf("abandoned upload = found %v err %v, want it swept past the grace window", found, err)
	}
	if blobExists(t, a, abandonedSHA) {
		t.Fatal("the abandoned upload's row went but its bytes stayed on disk")
	}
	if _, found, err := a.Get("/repo", bound.ID); !found || err != nil {
		t.Fatalf("bound upload = found %v err %v, want it kept with its issue", found, err)
	}
	if _, found, err := a.Get("/repo", inFlight.ID); !found || err != nil {
		t.Fatalf("recent unbound upload = found %v err %v, want it inside the grace window", found, err)
	}
	if !blobExists(t, a, boundSHA) || !blobExists(t, a, inFlightSHA) {
		t.Fatal("the sweep took bytes belonging to uploads it should have left alone")
	}
}

func TestAttachmentFailedRowBacksOffBeforeRetrying(t *testing.T) {
	a := testAttachments(t)
	a.now = func() time.Time { return cacheEpoch }

	att := trackerAttachment(t, a, "COD-1", "https://tracker.example/gone.png")
	if !att.RetryReady(cacheEpoch) {
		t.Fatal("a pending row that has never been fetched is not ready; nothing would ever fetch it")
	}
	if err := a.MarkFailed(att.ID, "source responded 404 Not Found"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	failed, _, err := a.Get("/repo", att.ID)
	if err != nil {
		t.Fatalf("Get failed row: %v", err)
	}
	if failed.RetryReady(cacheEpoch.Add(30 * time.Second)) {
		t.Fatal("a row that just failed is ready again; a permanently missing file would hit the tracker on every view")
	}
	if !failed.RetryReady(cacheEpoch.Add(AttachmentRetryFloor)) {
		t.Fatal("a failed row never became retryable; it would stay broken until a re-sync")
	}
}

func TestAttachmentServingStampsRecency(t *testing.T) {
	a := testAttachments(t)
	a.now = func() time.Time { return cacheEpoch }

	att := trackerAttachment(t, a, "COD-1", "https://tracker.example/shot.png")
	cache(t, a, att.ID, "bytes")

	fetched, _, err := a.Get("/repo", att.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.LastServedAt == "" {
		t.Fatal("caching left no served stamp; a freshly downloaded file would rank as the coldest in the cache")
	}
	if fetched.LastAttemptAt == "" {
		t.Fatal("caching left no attempt stamp")
	}

	a.now = func() time.Time { return cacheEpoch.Add(time.Hour) }
	if err := a.MarkServed(att.ID); err != nil {
		t.Fatalf("MarkServed: %v", err)
	}
	served, _, err := a.Get("/repo", att.ID)
	if err != nil {
		t.Fatalf("Get after MarkServed: %v", err)
	}
	if served.LastServedAt == fetched.LastServedAt {
		t.Fatalf("LastServedAt stayed %q after a serve", served.LastServedAt)
	}
}

func TestAttachmentStatsCountDistinctBlobs(t *testing.T) {
	a := cappedAttachments(t, 64)
	const shared = "one image, two tickets"

	first := trackerAttachment(t, a, "COD-1", "https://tracker.example/a.png")
	second := trackerAttachment(t, a, "COD-2", "https://tracker.example/b.png")
	cache(t, a, first.ID, shared)
	cache(t, a, second.ID, shared)

	missing := trackerAttachment(t, a, "COD-3", "https://tracker.example/gone.png")
	if err := a.MarkFailed(missing.ID, "source responded 404 Not Found"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	st, err := a.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Files != 1 {
		t.Fatalf("Files = %d, want 1 — two rows sharing a digest share one file", st.Files)
	}
	if st.Bytes != int64(len(shared)) {
		t.Fatalf("Bytes = %d, want %d counted once", st.Bytes, len(shared))
	}
	if st.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", st.Failed)
	}
	if st.CapBytes != 64 {
		t.Fatalf("CapBytes = %d, want the configured 64", st.CapBytes)
	}
}
