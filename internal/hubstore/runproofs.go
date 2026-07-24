package hubstore

import (
	"database/sql"
	"errors"
)

// ProofsDir is the verify-proof blob store's directory under the trau home. It is
// kept apart from the attachment blobs so the attachment retention sweep, which
// only knows attachment rows, never mistakes a proof digest for an orphan.
const ProofsDir = "proofs"

// Proof kinds. A screenshot has bytes in the blob store; a video row records only
// the local trace directory the recorder wrote, which is not uploaded.
const (
	ProofScreenshot = "screenshot"
	ProofVideo      = "video"
)

// RunProof is one harvested verify proof: a screenshot's stored bytes, or a video
// row that carries only the trace directory path. Seq orders a run's proofs.
type RunProof struct {
	Seq       int    `json:"seq"`
	Kind      string `json:"kind"`
	SHA256    string `json:"sha256,omitempty"`
	Mime      string `json:"mime,omitempty"`
	Caption   string `json:"caption,omitempty"`
	TraceDir  string `json:"trace_dir,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// RunProofs is the hub's store of verify browser proofs, paired with the
// content-addressed blob store holding the screenshot bytes. The caller owns db's
// lifecycle.
type RunProofs struct {
	db    *sql.DB
	blobs *AttachmentBlobs
}

// NewRunProofs builds the proof store over db, with screenshot bytes under root.
func NewRunProofs(db *sql.DB, root string) *RunProofs {
	return &RunProofs{db: db, blobs: NewAttachmentBlobs(root)}
}

// Blobs returns the content-addressed store the screenshot bytes live in.
func (p *RunProofs) Blobs() *AttachmentBlobs { return p.blobs }

// Replace swaps a run's proof rows for proofs in one transaction, so the latest
// verify attempt supersedes the prior one rather than appending. An empty set
// clears the run's proofs.
func (p *RunProofs) Replace(repo, ticket string, proofs []RunProof) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM run_proofs WHERE repo = ? AND ticket = ?`, repo, ticket); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	for _, pr := range proofs {
		if _, err := tx.Exec(
			`INSERT INTO run_proofs(repo, ticket, seq, kind, sha256, mime, caption, trace_dir, created_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repo, ticket, pr.Seq, pr.Kind, pr.SHA256, pr.Mime, pr.Caption, pr.TraceDir, pr.CreatedAt,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// ForRun returns a run's proofs in seq order.
func (p *RunProofs) ForRun(repo, ticket string) ([]RunProof, error) {
	rows, err := p.db.Query(
		`SELECT seq, kind, sha256, mime, caption, trace_dir, created_at
		 FROM run_proofs WHERE repo = ? AND ticket = ? ORDER BY seq`,
		repo, ticket,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []RunProof{}
	for rows.Next() {
		var pr RunProof
		if err := rows.Scan(&pr.Seq, &pr.Kind, &pr.SHA256, &pr.Mime, &pr.Caption, &pr.TraceDir, &pr.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// Find returns a run's proof at seq.
func (p *RunProofs) Find(repo, ticket string, seq int) (RunProof, bool, error) {
	var pr RunProof
	err := p.db.QueryRow(
		`SELECT seq, kind, sha256, mime, caption, trace_dir, created_at
		 FROM run_proofs WHERE repo = ? AND ticket = ? AND seq = ?`,
		repo, ticket, seq,
	).Scan(&pr.Seq, &pr.Kind, &pr.SHA256, &pr.Mime, &pr.Caption, &pr.TraceDir, &pr.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RunProof{}, false, nil
	}
	if err != nil {
		return RunProof{}, false, err
	}
	return pr, true, nil
}
