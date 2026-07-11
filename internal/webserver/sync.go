package webserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// SyncResponse is the JSON body of POST /repos/{repo}/sync: what the pull wrote to
// the local issue store and when, so a caller sees exactly what changed.
type SyncResponse struct {
	Repo     string `json:"repo"`
	Provider string `json:"provider"`
	Issues   int    `json:"issues"`
	Comments int    `json:"comments"`
	SyncedAt string `json:"syncedAt"`
}

// handleSync pulls a repo's Project into the hub issue store on demand. It is a
// one-way inbound sync (ADR 0007): the tracker owns issue content, so this only
// ever reads. Unknown repos 404, a repo without direct tracker credentials 422,
// and a tracker error 502; a successful pull returns the counts it wrote.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	resp, err := s.syncRepo(r.Context(), repo)
	if err != nil {
		if errors.Is(err, tracker.ErrReaderUnavailable) {
			writeReaderErr(w, err)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "sync failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// syncRepo resolves the repo's tracker binding (caching it on first use so later
// syncs skip the team/project lookup), pulls every Project issue with its
// comments, upserts them into the store, and records the outcome. It is the
// shared core of the sync endpoint and the sync that fires on repo registration.
func (s *Server) syncRepo(ctx context.Context, repo registry.Repo) (SyncResponse, error) {
	provider, reader, err := s.readerFor(repo)
	if err != nil {
		return SyncResponse{}, err
	}
	store := s.stores.Issues()

	binding, err := s.resolveBinding(ctx, store, repo.Root, reader)
	if err != nil {
		_ = store.RecordResult(repo.Root, hubstore.SyncResult{Err: err.Error(), SyncedAt: nowStamp()})
		return SyncResponse{}, err
	}

	pulled, err := reader.SyncPull(ctx, binding)
	if err != nil {
		_ = store.RecordResult(repo.Root, hubstore.SyncResult{Err: err.Error(), SyncedAt: nowStamp()})
		return SyncResponse{}, err
	}

	issues, comments, err := store.Upsert(repo.Root, provider, toStoredIssues(pulled))
	if err != nil {
		return SyncResponse{}, err
	}
	syncedAt := nowStamp()
	if err := store.RecordResult(repo.Root, hubstore.SyncResult{
		Issues:   issues,
		Comments: comments,
		Cursor:   latestCursor(pulled),
		SyncedAt: syncedAt,
	}); err != nil {
		return SyncResponse{}, err
	}
	return SyncResponse{
		Repo:     repo.Name,
		Provider: provider,
		Issues:   issues,
		Comments: comments,
		SyncedAt: syncedAt,
	}, nil
}

func (s *Server) resolveBinding(ctx context.Context, store *hubstore.Issues, root string, reader tracker.Reader) (tracker.ProjectBinding, error) {
	state, err := store.SyncState(root)
	if err != nil {
		return tracker.ProjectBinding{}, err
	}
	binding := tracker.ProjectBinding{
		TeamID:    state.Binding.TeamID,
		ProjectID: state.Binding.ProjectID,
		Project:   state.Binding.Project,
	}
	if binding.Resolved() {
		return binding, nil
	}
	binding, err = reader.ResolveBinding(ctx)
	if err != nil {
		return tracker.ProjectBinding{}, err
	}
	if err := store.SaveBinding(root, hubstore.SyncBinding{
		TeamID:    binding.TeamID,
		ProjectID: binding.ProjectID,
		Project:   binding.Project,
	}); err != nil {
		return tracker.ProjectBinding{}, err
	}
	return binding, nil
}

func toStoredIssues(pulled []tracker.SyncedIssue) []hubstore.Issue {
	out := make([]hubstore.Issue, 0, len(pulled))
	for _, iss := range pulled {
		stored := hubstore.Issue{
			Identifier:  iss.ID,
			Title:       iss.Title,
			Description: iss.Description,
			Status:      iss.Status,
			StatusGroup: string(iss.Group),
			Priority:    iss.Priority,
			Labels:      iss.Labels,
			Parent:      iss.Parent,
			HasChildren: iss.HasChildren,
			DueDate:     iss.DueDate,
			ExternalID:  iss.ExternalID,
			URL:         iss.URL,
			CreatedAt:   iss.CreatedAt,
			UpdatedAt:   iss.UpdatedAt,
		}
		for _, c := range iss.Comments {
			stored.Comments = append(stored.Comments, hubstore.Comment{
				ExternalID: c.ExternalID,
				Author:     c.Author,
				Body:       c.Body,
				CreatedAt:  c.CreatedAt,
				UpdatedAt:  c.UpdatedAt,
			})
		}
		out = append(out, stored)
	}
	return out
}

// latestCursor returns the newest updatedAt among the pulled issues, the cursor a
// later incremental sync resumes from.
func latestCursor(pulled []tracker.SyncedIssue) string {
	cursor := ""
	for _, iss := range pulled {
		if iss.UpdatedAt > cursor {
			cursor = iss.UpdatedAt
		}
	}
	return cursor
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
