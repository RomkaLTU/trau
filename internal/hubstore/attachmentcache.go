package hubstore

import (
	"errors"
	"time"
)

// unboundUploadGrace is how long an upload that never landed on an issue may sit
// before the retention sweep drops it. Long enough that an editor left open across
// a lunch break still binds its images when the draft is saved, short enough that
// an abandoned draft stops pinning bytes.
const unboundUploadGrace = 24 * time.Hour

// AttachmentRetryFloor is the shortest interval between two fetch attempts on one
// attachment, so a tracker file that is permanently gone costs one request a
// minute rather than one per view.
const AttachmentRetryFloor = time.Minute

// AttachmentCacheStats is the attachment store's on-disk footprint: the distinct
// blobs it holds and their total size — two rows sharing a digest share one file
// and count once — alongside how many rows are sitting in failed. CapBytes is the
// configured ceiling, zero when the cache is unbounded.
type AttachmentCacheStats struct {
	Bytes    int64 `json:"bytes"`
	Files    int   `json:"files"`
	Failed   int   `json:"failed"`
	CapBytes int64 `json:"cap_bytes"`
}

// Stats reports the cache footprint for the doctor and health surfaces.
func (a *Attachments) Stats() (AttachmentCacheStats, error) {
	st := AttachmentCacheStats{CapBytes: a.cacheBytes}
	err := a.db.QueryRow(
		`SELECT COALESCE(SUM(bytes), 0), COUNT(*) FROM (
			SELECT MAX(size_bytes) AS bytes FROM attachments
			 WHERE sha256 <> '' AND state = ? GROUP BY sha256)`,
		AttachmentCached,
	).Scan(&st.Bytes, &st.Files)
	if err != nil {
		return AttachmentCacheStats{}, err
	}
	if err := a.db.QueryRow(
		`SELECT COUNT(*) FROM attachments WHERE state = ?`, AttachmentFailed,
	).Scan(&st.Failed); err != nil {
		return AttachmentCacheStats{}, err
	}
	return st, nil
}

// PruneUnboundUploads drops uploads that never reached an issue and are older than
// the grace window. An editor closed without saving leaves its pasted images bound
// to nothing, and nothing else will ever come for them; an upload that did bind
// follows its issue's lifecycle instead.
func (a *Attachments) PruneUnboundUploads() error {
	return a.deleteWhere(
		`issue_identifier = '' AND source = '`+AttachmentSourceUpload+`' AND created_at < ?`,
		formatAttachmentTime(a.now().Add(-unboundUploadGrace)),
	)
}

// EnforceCacheCap trims the blob store back under its cap, evicting the least
// recently served tracker-sourced files first, and returns how many blobs went and
// how many bytes that freed. Eviction is safe because it is reversible: the row
// returns to pending and re-downloads from the tracker the next time something
// wants it. Uploads are never evicted — they are the only copy of their bytes —
// which means an upload-heavy store can legitimately sit over the cap with nothing
// left to reclaim. keep spares one digest the caller is about to serve, so a fetch
// that runs the cache over its cap cannot reclaim the very bytes it was made for;
// the periodic sweep has nothing in hand and passes an empty string.
func (a *Attachments) EnforceCacheCap(keep string) (evicted int, freed int64, err error) {
	if a.cacheBytes <= 0 {
		return 0, 0, nil
	}
	stats, err := a.Stats()
	if err != nil || stats.Bytes <= a.cacheBytes {
		return 0, 0, err
	}
	candidates, err := a.evictionCandidates()
	if err != nil {
		return 0, 0, err
	}
	total := stats.Bytes
	for _, c := range candidates {
		if total <= a.cacheBytes {
			break
		}
		if c.sha == keep {
			continue
		}
		if err := a.evict(c.sha); err != nil {
			return evicted, freed, err
		}
		total -= c.bytes
		freed += c.bytes
		evicted++
	}
	return evicted, freed, nil
}

// evictionCandidate is one reclaimable blob: a digest whose bytes are cached and
// which no upload row points at, ranked coldest first.
type evictionCandidate struct {
	sha   string
	bytes int64
}

// evictionCandidates lists the reclaimable blobs least recently served first. The
// NOT EXISTS is what protects uploads: a digest an upload shares with a tracker
// file — the same image pasted into a ticket that already carries it — has no
// upstream to re-fetch from and must survive.
func (a *Attachments) evictionCandidates() (out []evictionCandidate, err error) {
	q, err := a.db.Query(
		`SELECT a.sha256, MAX(a.size_bytes)
		   FROM attachments a
		  WHERE a.sha256 <> '' AND a.state = ?
		    AND NOT EXISTS (SELECT 1 FROM attachments u WHERE u.sha256 = a.sha256 AND u.source = ?)
		  GROUP BY a.sha256
		  ORDER BY MAX(a.last_served_at), a.sha256`,
		AttachmentCached, AttachmentSourceUpload,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	for q.Next() {
		var c evictionCandidate
		if err := q.Scan(&c.sha, &c.bytes); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, q.Err()
}

// evict unlinks a digest's file and returns every row on it to pending, so the
// next request re-downloads the bytes rather than finding a sha256 with nothing
// behind it. The rows keep their size and filename, so the drawer still describes
// the file while its bytes are away.
func (a *Attachments) evict(sha string) error {
	if _, err := a.db.Exec(
		`UPDATE attachments SET state = ?, sha256 = '', error = '' WHERE sha256 = ?`,
		AttachmentPending, sha,
	); err != nil {
		return err
	}
	return a.blobs.Remove(sha)
}
