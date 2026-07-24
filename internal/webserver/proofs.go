package webserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// proofsMaxScreenshots caps how many screenshots a run stores, matching the loop's
// harvest cap: a verifier that saves more than this keeps the first few in manifest
// order.
const proofsMaxScreenshots = 8

// proofUploadMaxBytes caps a single decoded screenshot; anything larger is dropped
// rather than stored. proofsRequestMaxBytes bounds the whole request body.
const (
	proofUploadMaxBytes   = 10 << 20
	proofsRequestMaxBytes = 96 << 20
)

// proofUploadRequest is the loop's proofs payload — the recorder's trace directory
// plus the harvested screenshots, each carrying its bytes base64-encoded. It
// mirrors hubclient's uploadProofsBody.
type proofUploadRequest struct {
	TraceDir    string                 `json:"trace_dir"`
	Screenshots []proofScreenshotInput `json:"screenshots"`
}

type proofScreenshotInput struct {
	Filename string `json:"filename"`
	Mime     string `json:"mime"`
	Caption  string `json:"caption"`
	Data     string `json:"data"`
}

// ProofView is one proof on the wire: its stored record plus the two things a
// client cannot derive — whether trau renders it inline, and where to fetch its
// bytes (empty for a video row, which carries only its trace directory).
type ProofView struct {
	hubstore.RunProof
	IsImage bool   `json:"is_image"`
	URL     string `json:"url,omitempty"`
}

// handleRunProofs lists (GET) or ingests (POST) a run's verify browser proofs —
// /repos/{repo}/runs/{ticket}/proofs. The loop posts the screenshots and trace
// path it harvested after a verify attempt drove the browser; the latest attempt
// replaces the run's prior proofs.
func (s *Server) handleRunProofs(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	ticket := strings.TrimSpace(r.PathValue("ticket"))
	switch r.Method {
	case http.MethodGet:
		s.listRunProofs(w, repo, ticket)
	case http.MethodPost:
		s.uploadRunProofs(w, r, repo, ticket)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listRunProofs(w http.ResponseWriter, repo registry.Repo, ticket string) {
	rows, err := s.stores.Proofs().ForRun(repo.Root, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list proofs: " + err.Error()})
		return
	}
	out := make([]ProofView, 0, len(rows))
	for _, pr := range rows {
		out = append(out, proofView(repo, ticket, pr))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) uploadRunProofs(w http.ResponseWriter, r *http.Request, repo registry.Repo, ticket string) {
	r.Body = http.MaxBytesReader(w, r.Body, proofsRequestMaxBytes)
	var req proofUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if tooLarge(err) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "proofs payload too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	proofs := make([]hubstore.RunProof, 0, len(req.Screenshots)+1)
	if req.TraceDir != "" {
		proofs = append(proofs, hubstore.RunProof{
			Seq:       0,
			Kind:      hubstore.ProofVideo,
			TraceDir:  req.TraceDir,
			CreatedAt: now,
		})
	}
	seq := 1
	for _, shot := range req.Screenshots {
		if seq > proofsMaxScreenshots {
			break
		}
		raw, err := base64.StdEncoding.DecodeString(shot.Data)
		if err != nil || len(raw) == 0 {
			continue
		}
		sha, _, err := s.stores.Proofs().Blobs().Put(bytes.NewReader(raw), proofUploadMaxBytes)
		if err != nil {
			if errors.Is(err, hubstore.ErrAttachmentTooLarge) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "screenshot exceeds 10 MB"})
				return
			}
			logger.Verbosef("store proof %s %s: %v", repo.Root, ticket, err)
			continue
		}
		proofs = append(proofs, hubstore.RunProof{
			Seq:       seq,
			Kind:      hubstore.ProofScreenshot,
			SHA256:    sha,
			Mime:      proofMime(shot, raw),
			Caption:   shot.Caption,
			TraceDir:  req.TraceDir,
			CreatedAt: now,
		})
		seq++
	}

	if err := s.stores.Proofs().Replace(repo.Root, ticket, proofs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store proofs: " + err.Error()})
		return
	}
	out := make([]ProofView, 0, len(proofs))
	for _, pr := range proofs {
		out = append(out, proofView(repo, ticket, pr))
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleRunProof serves one proof's bytes — GET
// /repos/{repo}/runs/{ticket}/proofs/{seq}. Only screenshot rows carry bytes; a
// video row (trace path only) has none and answers 404.
func (s *Server) handleRunProof(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	ticket := strings.TrimSpace(r.PathValue("ticket"))
	seq, err := strconv.Atoi(r.PathValue("seq"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown proof"})
		return
	}
	pr, found, err := s.stores.Proofs().Find(repo.Root, ticket, seq)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read proof: " + err.Error()})
		return
	}
	if !found || pr.SHA256 == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown proof"})
		return
	}
	s.serveProofBytes(w, r, pr)
}

func (s *Server) serveProofBytes(w http.ResponseWriter, r *http.Request, pr hubstore.RunProof) {
	f, err := s.stores.Proofs().Blobs().Open(pr.SHA256)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "open proof: " + err.Error()})
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stat proof: " + err.Error()})
		return
	}

	name := proofFilename(pr)
	disposition := "attachment"
	if hubstore.AttachmentIsImage(pr.Mime) {
		disposition = "inline"
	}
	if pr.Mime != "" {
		w.Header().Set("Content-Type", pr.Mime)
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+pr.SHA256+`"`)
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func proofView(repo registry.Repo, ticket string, pr hubstore.RunProof) ProofView {
	v := ProofView{RunProof: pr, IsImage: hubstore.AttachmentIsImage(pr.Mime)}
	if pr.Kind == hubstore.ProofScreenshot && pr.SHA256 != "" {
		v.URL = fmt.Sprintf("%s/repos/%s/runs/%s/proofs/%d",
			APIPrefix, url.PathEscape(repo.Name), url.PathEscape(ticket), pr.Seq)
	}
	return v
}

func proofMime(shot proofScreenshotInput, raw []byte) string {
	m, _, _ := strings.Cut(shot.Mime, ";")
	m = strings.ToLower(strings.TrimSpace(m))
	if hubstore.AttachmentIsImage(m) {
		return m
	}
	sniff, _, _ := strings.Cut(http.DetectContentType(raw), ";")
	return strings.ToLower(strings.TrimSpace(sniff))
}

func proofFilename(pr hubstore.RunProof) string {
	name := fmt.Sprintf("proof-%d", pr.Seq)
	if ext := proofExt(pr.Mime); ext != "" {
		return name + ext
	}
	return name
}

func proofExt(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}
