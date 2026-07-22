package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// AssignRequest is the body of PUT /repos/{repo}/issues/{id}/assignee: the
// assignee's tracker id and the display name to mirror alongside it. An empty or
// absent id unassigns the issue.
type AssignRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AssignableUser is one candidate the assignee picker offers. Me is computed
// server-side against the repo binding's resolved identity, which never leaves the
// hub (ADR 0014).
type AssignableUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Me   bool   `json:"me"`
}

type AssignableUsersResponse struct {
	Users []AssignableUser `json:"users"`
}

// handleAssignableUsers lists who a repo's issues can be assigned to, live from
// the tracker — unlike the assignees facet it includes team members who hold no
// issue in the repo yet. Nothing is persisted; trau keeps no users of its own
// (ADR 0020).
func (s *Server) handleAssignableUsers(w http.ResponseWriter, r *http.Request) {
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
	writer, err := s.assignWriterFor(repo)
	if err != nil {
		writeAssignErr(w, err)
		return
	}
	users, err := writer.AssignableUsers(r.Context(), strings.TrimSpace(r.URL.Query().Get("query")))
	if err != nil {
		writeAssignErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, AssignableUsersResponse{Users: toAssignableUsers(users, s.repoMeID(repo.Root))})
}

// handleIssueAssignee writes an issue's assignee to the tracker and mirrors the
// result into the store, in that order: the tracker is the authority, so a refused
// write leaves the stored row untouched rather than showing an assignment that
// does not exist upstream (ADR 0020). An empty id unassigns.
func (s *Server) handleIssueAssignee(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var req AssignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	writer, err := s.assignWriterFor(repo)
	if err != nil {
		writeAssignErr(w, err)
		return
	}
	assigneeID := strings.TrimSpace(req.ID)
	if err := writer.AssignIssue(r.Context(), id, assigneeID); err != nil {
		writeAssignErr(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if assigneeID == "" {
		name = ""
	}
	iss, found, err := s.stores.Issues().UpdateSynced(repo.Root, id, hubstore.SyncedPatch{
		AssigneeID:   &assigneeID,
		AssigneeName: &name,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mirror assignee: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " is not a synced issue in this repo"})
		return
	}
	writeJSON(w, http.StatusOK, s.storeIssueResponse(repo, iss))
}

// assignWriterFor resolves the Writer an assignment reaches, taking the same
// internal-vs-direct split a grill apply takes: an empty destination means the
// repo's own provider, so an internal-provider repo gets the hub's store-backed
// writer and refuses the write from there.
func (s *Server) assignWriterFor(repo registry.Repo) (tracker.Writer, error) {
	_, writer, err := s.grillWriterFor(repo, "")
	return writer, err
}

// writeAssignErr maps an assignment failure onto a response. A provider with no
// assignment API answers 409 so the client hides the picker instead of showing an
// error; a repo with no direct credentials keeps the 422 every other tracker write
// uses; anything else is the tracker refusing the write.
func writeAssignErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tracker.ErrUnsupported):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "this repo's tracker does not support assignment",
		})
	case errors.Is(err, tracker.ErrWriterUnavailable):
		writeWriterErr(w, err)
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "assign issue: " + err.Error()})
	}
}

// toAssignableUsers flags the repo's Me and pins it first, keeping the tracker's
// order for the rest.
func toAssignableUsers(users []tracker.AssignableUser, meID string) []AssignableUser {
	out := make([]AssignableUser, 0, len(users))
	for _, u := range users {
		out = append(out, AssignableUser{ID: u.ID, Name: u.Name, Me: meID != "" && u.ID == meID})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Me && !out[j].Me
	})
	return out
}
