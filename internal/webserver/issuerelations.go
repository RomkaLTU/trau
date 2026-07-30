package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// IssueRelationsRequest is the body of POST and DELETE
// /repos/{repo}/issues/{id}/relations: the same-repo identifiers blocking the
// issue, and the ones it blocks.
type IssueRelationsRequest struct {
	BlockedBy []string `json:"blocked_by"`
	Blocks    []string `json:"blocks"`
}

// handleIssueRelations adds (POST) or removes (DELETE) blocking edges around an
// internal issue and answers with its resulting graph. Only internal issues are
// addressable: the store is a working copy for synced tickets (ADR 0007), so a
// local-only edge on one would silently diverge from its tracker — that answers
// 409, for the subject here and for any target the write would block.
func (s *Server) handleIssueRelations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	var req IssueRelationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	issues := s.stores.Issues()
	iss, found, err := issues.Get(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read issue: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " is not an issue in this repo"})
		return
	}
	if iss.Source != hubstore.SourceInternal {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": id + " is a synced ticket; its relations are owned by the tracker",
		})
		return
	}
	edits := hubstore.RelationEdits{
		BlockedBy: identifierList(req.BlockedBy),
		Blocks:    identifierList(req.Blocks),
	}
	if r.Method == http.MethodPost {
		err = issues.AddRelations(repo.Root, id, edits)
	} else {
		err = issues.RemoveRelations(repo.Root, id, edits)
	}
	if writeRelationError(w, err) {
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write relations: " + err.Error()})
		return
	}
	s.writeInternalIssue(w, http.StatusOK, repo, iss)
}

// writeRelationError answers a refused relation write, reporting whether err was
// one: an unknown target is the caller's typo, while a target the tracker owns is
// the same conflict a synced subject answers.
func writeRelationError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, hubstore.ErrRelationTargetMissing):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, hubstore.ErrRelationTargetSynced):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		return false
	}
	return true
}

// identifierList trims issue identifiers, dropping blanks so an empty form field
// never files an edge.
func identifierList(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}
