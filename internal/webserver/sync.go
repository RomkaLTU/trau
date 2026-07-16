package webserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
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

// handleForceResync drops a repo's synced issues and cursor and re-pulls the
// Project clean — POST /repos/{repo}/resync, the recovery path when the store's
// sync state is doubted (ADR 0007). Internal issues are preserved, and the pull
// converges to the same content a fresh sync would; the response is that pull's
// counts. Unknown repos 404, a repo without direct tracker credentials 422 (with
// the store left untouched), and a tracker error 502.
func (s *Server) handleForceResync(w http.ResponseWriter, r *http.Request) {
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
	resp, err := s.forceResync(r.Context(), repo)
	if err != nil {
		if errors.Is(err, tracker.ErrReaderUnavailable) {
			writeReaderErr(w, err)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "resync failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// forceResync drops the repo's synced rows and cursor, then re-pulls the Project
// from an empty cursor — a full pull that re-populates the store with only the
// issues the tracker still holds, so deleted or moved-out tickets simply vanish
// (ADR 0007). Internal issues are preserved. It checks the reader is usable before
// dropping anything, so a repo without direct credentials is refused with the store
// intact rather than emptied with nothing to re-pull it.
func (s *Server) forceResync(ctx context.Context, repo registry.Repo) (SyncResponse, error) {
	if _, _, err := s.readerFor(repo); err != nil {
		return SyncResponse{}, err
	}
	if err := s.stores.Issues().DropSynced(repo.Root); err != nil {
		return SyncResponse{}, err
	}
	return s.syncRepo(ctx, repo)
}

// reconcileRepo diffs the repo's Project identifier set against the store and
// tombstones the synced issues the tracker no longer returns — those deleted,
// archived, or moved out of the Project, which an incremental SyncPull never
// reports (ADR 0007). Tombstoned issues are dropped from the Queue and the backlog
// board but keep their run artifacts and checkpoints; internal issues are never
// touched. A sweep failure is recorded on the same per-repo error surface as sync
// so a broken tracker backs off. An empty identifier set is treated as a no-op
// rather than tombstoning the whole store — it guards against a misresolved binding
// (a wrong project key returns zero) wiping every synced row.
func (s *Server) reconcileRepo(ctx context.Context, repo registry.Repo) error {
	res, err := s.resolveReader(repo)
	if err != nil {
		return err
	}
	store := s.stores.Issues()
	state, err := store.SyncState(repo.Root)
	if err != nil {
		return err
	}
	binding, err := s.resolveBinding(ctx, store, repo.Root, state.Binding, res.reader)
	if err != nil {
		err = res.actionableErr(err)
		_ = store.RecordError(repo.Root, err.Error())
		return err
	}
	live, err := res.reader.ProjectIdentifiers(ctx, binding)
	if err != nil {
		err = res.actionableErr(err)
		_ = store.RecordError(repo.Root, err.Error())
		return err
	}
	if len(live) == 0 {
		return nil
	}
	tombstoned, err := store.Reconcile(repo.Root, live)
	if err != nil {
		return err
	}
	s.dropFromQueue(repo.Root, tombstoned)
	return nil
}

// dropFromQueue removes each tombstoned identifier from the repo's Queue,
// tolerating the ones that were never queued or are mid-run — the cascade prunes
// only what it safely can, leaving a running item to settle on its own.
func (s *Server) dropFromQueue(root string, ids []string) {
	if len(ids) == 0 {
		return
	}
	q := s.stores.Queue(root)
	for _, id := range ids {
		switch _, err := q.Remove(id); {
		case err == nil, errors.Is(err, queue.ErrNotQueued), errors.Is(err, queue.ErrRunning):
		default:
			logger.Verbosef("reconcile %s: drop %s from queue: %v", root, id, err)
		}
	}
}

// syncRepo resolves the repo's tracker binding (caching it on first use so later
// syncs skip the team/project lookup), pulls the Project's issues with their
// comments, upserts them into the store, and records the outcome. The pull is
// incremental off the stored cursor — only issues updated since the last sync —
// falling back to a full Project pull when the cursor is empty. It is the shared
// core of the sync endpoint, the sync that fires on repo registration, and the
// background sync loop.
func (s *Server) syncRepo(ctx context.Context, repo registry.Repo) (SyncResponse, error) {
	res, err := s.resolveReader(repo)
	if err != nil {
		return SyncResponse{}, err
	}
	store := s.stores.Issues()
	state, err := store.SyncState(repo.Root)
	if err != nil {
		return SyncResponse{}, err
	}

	binding, err := s.resolveBinding(ctx, store, repo.Root, state.Binding, res.reader)
	if err != nil {
		err = res.actionableErr(err)
		_ = store.RecordError(repo.Root, err.Error())
		return SyncResponse{}, err
	}

	pulled, err := res.reader.SyncPull(ctx, binding, state.Cursor)
	if err != nil {
		err = res.actionableErr(err)
		_ = store.RecordError(repo.Root, err.Error())
		return SyncResponse{}, err
	}

	issues, comments, err := store.Upsert(repo.Root, res.provider, toStoredIssues(pulled))
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
	s.resolveIdentity(ctx, store, repo.Root, res.reader)
	return SyncResponse{
		Repo:     repo.Name,
		Provider: res.provider,
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

// resolveIdentity refreshes the repo binding's Me — the tracker user behind its
// credentials — and persists it beside the sync bookkeeping. It is best-effort: an
// identity call that fails (bad or missing credentials, tracker hiccup) is logged
// and swallowed so it never blocks or fails the issue sync, leaving the previously
// stored identity in place.
func (s *Server) resolveIdentity(ctx context.Context, store *hubstore.Issues, root string, reader tracker.Reader) {
	id, name, err := reader.Identity(ctx)
	if err != nil {
		logger.Verbosef("sync %s: resolve identity: %v", root, err)
		return
	}
	if err := store.SaveIdentity(root, id, name); err != nil {
		logger.Verbosef("sync %s: persist identity: %v", root, err)
	}
}

func toStoredIssues(pulled []tracker.SyncedIssue) []hubstore.Issue {
	out := make([]hubstore.Issue, 0, len(pulled))
	for _, iss := range pulled {
		stored := hubstore.Issue{
			Identifier:   iss.ID,
			Title:        iss.Title,
			Description:  iss.Description,
			Status:       iss.Status,
			StatusGroup:  string(iss.Group),
			Priority:     iss.Priority,
			Labels:       iss.Labels,
			Parent:       iss.Parent,
			HasChildren:  iss.HasChildren,
			DueDate:      iss.DueDate,
			ExternalID:   iss.ExternalID,
			URL:          iss.URL,
			CreatedAt:    iss.CreatedAt,
			UpdatedAt:    iss.UpdatedAt,
			AssigneeID:   iss.AssigneeID,
			AssigneeName: iss.AssigneeName,
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
