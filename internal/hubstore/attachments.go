package hubstore

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AttachmentsDir is the attachment blob store's directory under the trau home.
const AttachmentsDir = "attachments"

// Attachment sources. upload is a file the user put into a trau-native editor and
// the only source with no upstream copy; the rest name where a discovered file
// lives, which decides the credentials the lazy fetch uses.
const (
	AttachmentSourceUpload   = "upload"
	AttachmentSourceLinear   = "linear"
	AttachmentSourceJira     = "jira"
	AttachmentSourceExternal = "external"
)

// Attachment states. A tracker-hosted file is registered pending and stays that
// way until something asks for its bytes, which caches or fails it. A failed row
// keeps its reason and retries on the next request.
const (
	AttachmentPending = "pending"
	AttachmentCached  = "cached"
	AttachmentFailed  = "failed"
)

// Attachment is one image or file belonging to an issue: where it came from, what
// it is, and whether its bytes are on disk yet. IssueIdentifier is empty for an
// upload not yet bound to an issue, SourceURL is empty for uploads, and SHA256 is
// empty until the bytes are cached. Rows key on (Repo, IssueIdentifier) strings
// rather than an issues row id because sync replaces a repo's synced issues
// wholesale.
type Attachment struct {
	ID              int64  `json:"id"`
	Repo            string `json:"repo"`
	IssueIdentifier string `json:"issue_identifier,omitempty"`
	Source          string `json:"source"`
	SourceURL       string `json:"source_url,omitempty"`
	Filename        string `json:"filename"`
	MimeType        string `json:"mime_type"`
	SizeBytes       int64  `json:"size_bytes"`
	SHA256          string `json:"sha256,omitempty"`
	State           string `json:"state"`
	Error           string `json:"error,omitempty"`
	CreatedAt       string `json:"created_at"`
	FetchedAt       string `json:"fetched_at,omitempty"`
	LastServedAt    string `json:"last_served_at,omitempty"`
	LastAttemptAt   string `json:"last_attempt_at,omitempty"`
}

// RetryReady reports whether a fetch may be attempted for this row now. A failed
// row is self-healing — the next request retries it — but no faster than
// AttachmentRetryFloor, so a file the tracker will never return cannot turn every
// view of its issue into another API call.
func (at Attachment) RetryReady(now time.Time) bool {
	if at.State != AttachmentFailed || at.LastAttemptAt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339Nano, at.LastAttemptAt)
	if err != nil {
		return true
	}
	return now.Sub(last) >= AttachmentRetryFloor
}

// rasterImageTypes are the image types trau will render and serve inline. SVG is
// deliberately absent: it is a document that can carry script, so it is always
// served as a download rather than something a browser executes in the hub's
// origin.
var rasterImageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// AttachmentIsImage reports whether the mime type is one trau displays inline.
func AttachmentIsImage(mime string) bool {
	mime, _, _ = strings.Cut(mime, ";")
	return rasterImageTypes[strings.ToLower(strings.TrimSpace(mime))]
}

// Attachments is the hub's authoritative attachment index, paired with the
// content-addressed blob store holding the bytes. Every delete path here also
// garbage-collects blobs that lost their last referring row, so the two never
// drift apart, and cacheBytes caps what the store may hold on disk. The caller
// owns db's lifecycle.
type Attachments struct {
	db         *sql.DB
	blobs      *AttachmentBlobs
	cacheBytes int64
	now        func() time.Time
}

// NewAttachments returns an attachment store over db whose bytes live under root,
// trimmed back to cacheBytes on disk by EnforceCacheCap. A non-positive cacheBytes
// leaves the cache unbounded.
func NewAttachments(db *sql.DB, root string, cacheBytes int64) *Attachments {
	return &Attachments{db: db, blobs: NewAttachmentBlobs(root), cacheBytes: cacheBytes, now: time.Now}
}

// Blobs returns the content-addressed byte store behind the index.
func (a *Attachments) Blobs() *AttachmentBlobs { return a.blobs }

const attachmentSelect = `SELECT id, repo, issue_identifier, source, source_url, filename, mime_type,
		size_bytes, sha256, state, error, created_at, fetched_at, last_served_at, last_attempt_at
	 FROM attachments`

// Create registers an attachment, deduping tracker-hosted files on
// (repo, source_url): re-syncing a ticket or re-opening its drawer returns the
// existing row — keeping whatever state its bytes reached — rather than stacking
// duplicates, though a freshly supplied filename or mime type is folded in. An
// upload carries no source_url and always inserts.
func (a *Attachments) Create(att Attachment) (Attachment, error) {
	if att.State == "" {
		att.State = AttachmentPending
	}
	att.CreatedAt = a.stamp()

	if att.SourceURL != "" {
		existing, found, err := a.bySourceURL(att.Repo, att.SourceURL)
		if err != nil {
			return Attachment{}, err
		}
		if found {
			return a.refresh(existing, att)
		}
	}

	res, err := a.db.Exec(
		`INSERT INTO attachments(repo, issue_identifier, source, source_url, filename, mime_type,
			size_bytes, sha256, state, error, created_at, fetched_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		att.Repo, att.IssueIdentifier, att.Source, att.SourceURL, att.Filename, att.MimeType,
		att.SizeBytes, att.SHA256, att.State, att.Error, att.CreatedAt, att.FetchedAt,
	)
	if err != nil {
		return Attachment{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Attachment{}, err
	}
	return a.byID(id)
}

// refresh folds a re-registration's metadata into the row that already holds the
// source URL. Only fields the caller actually supplied overwrite, so a sparse
// re-discovery never blanks a filename an earlier pass learned, and the issue
// binding follows the ticket the file was last seen on.
func (a *Attachments) refresh(existing, incoming Attachment) (Attachment, error) {
	if _, err := a.db.Exec(
		`UPDATE attachments SET
			issue_identifier = CASE WHEN ? <> '' THEN ? ELSE issue_identifier END,
			filename         = CASE WHEN ? <> '' THEN ? ELSE filename END,
			mime_type        = CASE WHEN ? <> '' THEN ? ELSE mime_type END
		 WHERE id = ?`,
		incoming.IssueIdentifier, incoming.IssueIdentifier,
		incoming.Filename, incoming.Filename,
		incoming.MimeType, incoming.MimeType,
		existing.ID,
	); err != nil {
		return Attachment{}, err
	}
	return a.byID(existing.ID)
}

// ForIssue returns a repo's attachments for one issue, oldest first.
func (a *Attachments) ForIssue(repo, identifier string) ([]Attachment, error) {
	return a.scan(
		attachmentSelect+` WHERE repo = ? AND issue_identifier = ? ORDER BY id`,
		repo, identifier,
	)
}

// Get returns one of a repo's attachments by id. Scoping the lookup to the repo
// keeps an id from another repo's issue from resolving through this repo's route.
func (a *Attachments) Get(repo string, id int64) (Attachment, bool, error) {
	rows, err := a.scan(attachmentSelect+` WHERE repo = ? AND id = ?`, repo, id)
	if err != nil || len(rows) == 0 {
		return Attachment{}, false, err
	}
	return rows[0], true, nil
}

// MarkCached records a successful fetch: the bytes are in the blob store under
// sha, and the row leaves pending with its error cleared. A mime type is only
// adopted when the fetch actually learned one. The fetch counts as a serve — bytes
// are only ever downloaded because something asked for them — so the row starts
// its cache life at the warm end of the eviction order rather than the cold one.
func (a *Attachments) MarkCached(id int64, sha string, size int64, mime string) error {
	stamp := a.stamp()
	_, err := a.db.Exec(
		`UPDATE attachments SET sha256 = ?, size_bytes = ?,
			mime_type = CASE WHEN ? <> '' THEN ? ELSE mime_type END,
			state = ?, error = '', fetched_at = ?, last_attempt_at = ?, last_served_at = ?
		 WHERE id = ?`,
		sha, size, mime, mime, AttachmentCached, stamp, stamp, stamp, id,
	)
	return err
}

// MarkFailed records why a fetch did not produce bytes. The row stays failed —
// surfacing the reason in the drawer — until a later request past the retry floor
// tries again.
func (a *Attachments) MarkFailed(id int64, reason string) error {
	stamp := a.stamp()
	_, err := a.db.Exec(
		`UPDATE attachments SET state = ?, error = ?, fetched_at = ?, last_attempt_at = ? WHERE id = ?`,
		AttachmentFailed, reason, stamp, stamp, id,
	)
	return err
}

// MarkServed records that a row's bytes were just handed out, which is what the
// cache cap ranks eviction on. Serving is a read path, so a failure here is the
// caller's to log rather than to fail the response over.
func (a *Attachments) MarkServed(id int64) error {
	_, err := a.db.Exec(`UPDATE attachments SET last_served_at = ? WHERE id = ?`, a.stamp(), id)
	return err
}

// BindToIssue attaches uploads to the issue whose markdown references them, so
// they follow its lifecycle instead of lingering unowned. Only uploads bind: a
// tracker-sourced id referenced in the markdown keeps its own issue, so pasting
// its URL into another ticket can't steal it out from under the sync sweep.
func (a *Attachments) BindToIssue(repo string, ids []int64, identifier string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, identifier, repo)
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := a.db.Exec(
		`UPDATE attachments SET issue_identifier = ? WHERE repo = ? AND source = '`+AttachmentSourceUpload+`' AND id IN (`+placeholders(len(ids))+`)`,
		args...,
	)
	return err
}

// DeleteForIssue drops an issue's attachments and any blob left unreferenced.
func (a *Attachments) DeleteForIssue(repo, identifier string) error {
	return a.deleteWhere(`repo = ? AND issue_identifier = ?`, repo, identifier)
}

// DeleteForRepo drops every attachment a repo holds — the unregister sweep, so
// dropping a repo leaves neither rows nor files behind.
func (a *Attachments) DeleteForRepo(repo string) error {
	return a.deleteWhere(`repo = ?`, repo)
}

// ReconcileIssues drops the tracker-sourced attachments of a repo's issues that
// live no longer contains — the post-sync sweep that keeps a deleted ticket's
// files from outliving it. Uploads and attachments not yet bound to an issue are
// left alone: they have no upstream to have vanished from. Callers must not pass
// an empty live set for a Project that still has issues; it would drop every
// synced attachment.
func (a *Attachments) ReconcileIssues(repo string, live []string) error {
	clause := `repo = ? AND source <> '` + AttachmentSourceUpload + `' AND issue_identifier <> ''`
	args := []any{repo}
	if len(live) > 0 {
		clause += ` AND issue_identifier NOT IN (` + placeholders(len(live)) + `)`
		for _, id := range live {
			args = append(args, id)
		}
	}
	return a.deleteWhere(clause, args...)
}

// ReconcileIssue drops the tracker-sourced attachments of one issue that live no
// longer lists — the per-issue counterpart of ReconcileIssues, run right after a
// sync re-registers what the ticket currently references, so an image deleted
// upstream stops being listed here. Uploads are exempt: they have no upstream to
// have vanished from. An empty live set drops every discovered file on the issue,
// which is exactly what a ticket that lost its last image means.
func (a *Attachments) ReconcileIssue(repo, identifier string, live []string) error {
	clause := `repo = ? AND issue_identifier = ? AND source <> '` + AttachmentSourceUpload + `' AND source_url <> ''`
	args := []any{repo, identifier}
	if len(live) > 0 {
		clause += ` AND source_url NOT IN (` + placeholders(len(live)) + `)`
		for _, u := range live {
			args = append(args, u)
		}
	}
	return a.deleteWhere(clause, args...)
}

// deleteWhere removes the matching rows and then collects the blobs no surviving
// row references. The sweep runs after the commit so a rolled-back delete can
// never take live bytes with it, and a blob shared by two rows survives the first
// of them. clause is an in-package literal, never caller-supplied text.
func (a *Attachments) deleteWhere(clause string, args ...any) error {
	shas, err := a.shasMatching(clause, args...)
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(`DELETE FROM attachments WHERE `+clause, args...); err != nil {
		return err
	}
	return a.collectBlobs(shas)
}

// collectBlobs removes each digest's file once nothing references it any more.
func (a *Attachments) collectBlobs(shas []string) error {
	var errs []error
	for _, sha := range shas {
		var refs int
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE sha256 = ?`, sha).Scan(&refs); err != nil {
			errs = append(errs, err)
			continue
		}
		if refs > 0 {
			continue
		}
		if err := a.blobs.Remove(sha); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *Attachments) shasMatching(clause string, args ...any) (out []string, err error) {
	q, err := a.db.Query(`SELECT DISTINCT sha256 FROM attachments WHERE sha256 <> '' AND (`+clause+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	for q.Next() {
		var sha string
		if err := q.Scan(&sha); err != nil {
			return nil, err
		}
		out = append(out, sha)
	}
	return out, q.Err()
}

func (a *Attachments) bySourceURL(repo, url string) (Attachment, bool, error) {
	rows, err := a.scan(attachmentSelect+` WHERE repo = ? AND source_url = ?`, repo, url)
	if err != nil || len(rows) == 0 {
		return Attachment{}, false, err
	}
	return rows[0], true, nil
}

func (a *Attachments) byID(id int64) (Attachment, error) {
	rows, err := a.scan(attachmentSelect+` WHERE id = ?`, id)
	if err != nil {
		return Attachment{}, err
	}
	if len(rows) == 0 {
		return Attachment{}, sql.ErrNoRows
	}
	return rows[0], nil
}

func (a *Attachments) scan(query string, args ...any) (out []Attachment, err error) {
	q, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []Attachment{}
	for q.Next() {
		var at Attachment
		if err := q.Scan(
			&at.ID, &at.Repo, &at.IssueIdentifier, &at.Source, &at.SourceURL, &at.Filename,
			&at.MimeType, &at.SizeBytes, &at.SHA256, &at.State, &at.Error, &at.CreatedAt, &at.FetchedAt,
			&at.LastServedAt, &at.LastAttemptAt,
		); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, q.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (a *Attachments) stamp() string { return formatAttachmentTime(a.now()) }

func formatAttachmentTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
