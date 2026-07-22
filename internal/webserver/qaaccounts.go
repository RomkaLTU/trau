package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// QAAccountRequest is the body of a QA account create or update: the login the
// browser verifier signs in with and the cases or flows it covers. A blank Secret
// on update keeps the stored one (write-only, ADR 0011); on create it stores none.
// Source is optional and applies on create only, defaulting to manual.
type QAAccountRequest struct {
	Label       string `json:"label"`
	Username    string `json:"username"`
	Secret      string `json:"secret"`
	Description string `json:"description"`
	Source      string `json:"source,omitempty"`
}

// QAAccountView is a QA account as the settings surface reads it: its identifier,
// label, username, coverage description, and provenance, plus whether a secret is
// stored. The secret itself is write-only and never crosses the wire on a read.
type QAAccountView struct {
	ID          int64  `json:"id"`
	Label       string `json:"label"`
	Username    string `json:"username"`
	Description string `json:"description"`
	Source      string `json:"source"`
	SecretSet   bool   `json:"secret_set"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// QANotesRequest and QANotesView carry the repo's single free-text QA notes value.
type QANotesRequest struct {
	Notes string `json:"notes"`
}

type QANotesView struct {
	Notes string `json:"notes"`
}

// QARosterAccount is one QA account with its full credentials, as the loop's
// verify-time fetch reads them.
type QARosterAccount struct {
	Label       string `json:"label"`
	Username    string `json:"username"`
	Secret      string `json:"secret"`
	Description string `json:"description"`
}

// QARosterResponse is the loop's verify-time view of a repo's QA credentials:
// every account with its full secret, plus the free-text notes. Unmasked because
// the hub is localhost-only and the machine-trust posture applies; the settings
// surface never uses this shape.
type QARosterResponse struct {
	Accounts []QARosterAccount `json:"accounts"`
	Notes    string            `json:"notes"`
}

// handleQAAccounts lists (GET) or creates (POST) the repo's QA accounts. List
// responses mask the secret.
func (s *Server) handleQAAccounts(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listQAAccounts(w, repo)
	case http.MethodPost:
		s.createQAAccount(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listQAAccounts(w http.ResponseWriter, repo registry.Repo) {
	accounts, err := s.stores.QA().List(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list qa accounts: " + err.Error()})
		return
	}
	views := make([]QAAccountView, 0, len(accounts))
	for _, a := range accounts {
		views = append(views, qaAccountView(a))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) createQAAccount(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	req, ok := decodeQAAccount(w, r)
	if !ok {
		return
	}
	if _, exists, err := s.stores.QA().ByLabel(repo.Root, req.Label); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "check qa account: " + err.Error()})
		return
	} else if exists {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a QA account labelled " + strconv.Quote(req.Label) + " already exists"})
		return
	}
	a, err := s.stores.QA().Create(repo.Root, qaInput(req))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create qa account: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, qaAccountView(a))
}

// handleQAAccount reads (GET), updates (PATCH/PUT), or removes (DELETE) a single
// QA account. Read and update responses mask the secret.
func (s *Server) handleQAAccount(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id, ok := qaAccountID(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getQAAccount(w, repo, id)
	case http.MethodPatch, http.MethodPut:
		s.updateQAAccount(w, r, repo, id)
	case http.MethodDelete:
		s.deleteQAAccount(w, repo, id)
	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) getQAAccount(w http.ResponseWriter, repo registry.Repo, id int64) {
	a, found, err := s.stores.QA().Get(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read qa account: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown qa account"})
		return
	}
	writeJSON(w, http.StatusOK, qaAccountView(a))
}

func (s *Server) updateQAAccount(w http.ResponseWriter, r *http.Request, repo registry.Repo, id int64) {
	existing, found, err := s.stores.QA().Get(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read qa account: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown qa account"})
		return
	}
	req, ok := decodeQAAccount(w, r)
	if !ok {
		return
	}
	if req.Label != existing.Label {
		if _, exists, err := s.stores.QA().ByLabel(repo.Root, req.Label); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "check qa account: " + err.Error()})
			return
		} else if exists {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a QA account labelled " + strconv.Quote(req.Label) + " already exists"})
			return
		}
	}
	in := qaInput(req)
	in.Secret = firstNonEmpty(in.Secret, existing.Secret)
	a, err := s.stores.QA().Update(repo.Root, id, in)
	if errors.Is(err, hubstore.ErrQAAccountNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown qa account"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update qa account: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, qaAccountView(a))
}

func (s *Server) deleteQAAccount(w http.ResponseWriter, repo registry.Repo, id int64) {
	deleted, err := s.stores.QA().Delete(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete qa account: " + err.Error()})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown qa account"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleQANotes reads (GET) or replaces (PUT) the repo's free-text QA notes.
func (s *Server) handleQANotes(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		notes, err := s.stores.QA().Notes(repo.Root)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read qa notes: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, QANotesView{Notes: notes})
	case http.MethodPut:
		var req QANotesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if err := s.stores.QA().SetNotes(repo.Root, req.Notes); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write qa notes: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, QANotesView(req))
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleQARoster serves the loop's verify-time fetch: every QA account with its
// full secret plus the free-text notes. Localhost-only and machine-trusted; the
// settings surface never reads this route.
func (s *Server) handleQARoster(w http.ResponseWriter, r *http.Request) {
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
	accounts, err := s.stores.QA().List(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list qa accounts: " + err.Error()})
		return
	}
	notes, err := s.stores.QA().Notes(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read qa notes: " + err.Error()})
		return
	}
	roster := QARosterResponse{Accounts: make([]QARosterAccount, 0, len(accounts)), Notes: notes}
	for _, a := range accounts {
		roster.Accounts = append(roster.Accounts, QARosterAccount{
			Label:       a.Label,
			Username:    a.Username,
			Secret:      a.Secret,
			Description: a.Description,
		})
	}
	writeJSON(w, http.StatusOK, roster)
}

func decodeQAAccount(w http.ResponseWriter, r *http.Request) (QAAccountRequest, bool) {
	var req QAAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return QAAccountRequest{}, false
	}
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "label is required"})
		return QAAccountRequest{}, false
	}
	req.Source = strings.TrimSpace(req.Source)
	switch req.Source {
	case "":
		req.Source = hubstore.QASourceManual
	case hubstore.QASourceManual, hubstore.QASourceAgent:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source must be " + hubstore.QASourceManual + " or " + hubstore.QASourceAgent})
		return QAAccountRequest{}, false
	}
	return req, true
}

func qaAccountID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "qa account id must be a number"})
		return 0, false
	}
	return id, true
}

func qaInput(req QAAccountRequest) hubstore.QAAccountInput {
	return hubstore.QAAccountInput{
		Label:       req.Label,
		Username:    strings.TrimSpace(req.Username),
		Secret:      req.Secret,
		Description: req.Description,
		Source:      req.Source,
	}
}

func qaAccountView(a hubstore.QAAccount) QAAccountView {
	return QAAccountView{
		ID:          a.ID,
		Label:       a.Label,
		Username:    a.Username,
		Description: a.Description,
		Source:      a.Source,
		SecretSet:   a.Secret != "",
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
}
