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
// syncs skip the team/project lookup), pulls the Project's issues with their
// comments, upserts them into the store, and records the outcome. The pull is
// incremental off the stored cursor — only issues updated since the last sync —
// falling back to a full Project pull when the cursor is empty. It is the shared
// core of the sync endpoint, the sync that fires on repo registration, and the
// background sync loop.
func (s *Server) syncRepo(ctx context.Context, repo registry.Repo) (SyncResponse, error) {
	provider, reader, err := s.readerFor(repo)
	if err != nil {
		return SyncResponse{}, err
	}
	store := s.stores.Issues()
	state, err := store.SyncState(repo.Root)
	if err != nil {
		return SyncResponse{}, err
	}

	binding, err := s.resolveBinding(ctx, store, repo.Root, state.Binding, reader)
	if err != nil {
		_ = store.RecordError(repo.Root, err.Error())
		return SyncResponse{}, err
	}

	pulled, err := reader.SyncPull(ctx, binding, state.Cursor)
	if err != nil {
		_ = store.RecordError(repo.Root, err.Error())
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
		Cursor:   advanceCursor(state.Cursor, pulled),
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

func (s *Server) resolveBinding(ctx context.Context, store *hubstore.Issues, root string, cached hubstore.SyncBinding, reader tracker.Reader) (tracker.ProjectBinding, error) {
	binding := tracker.ProjectBinding{
		TeamID:    cached.TeamID,
		ProjectID: cached.ProjectID,
		Project:   cached.Project,
	}
	if binding.Resolved() {
		return binding, nil
	}
	binding, err := reader.ResolveBinding(ctx)
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

// advanceCursor returns the cursor a later incremental sync resumes from: the
// newest updatedAt among the pulled issues, never behind prev. Keeping prev when
// an incremental pull returns nothing newer stops an empty pull from blanking the
// cursor and forcing a full re-pull next time. Timestamps from one tracker share
// a format, so the lexical max is the chronological one.
func advanceCursor(prev string, pulled []tracker.SyncedIssue) string {
	cursor := prev
	for _, iss := range pulled {
		if iss.UpdatedAt > cursor {
			cursor = iss.UpdatedAt
		}
	}
	return cursor
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
