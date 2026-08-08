package webserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// SyncResponse is the JSON body of POST /repos/{repo}/sync: what the pull wrote to
// the local issue store and when, so a caller sees exactly what changed. Removed
// counts the issues the manual sweep tombstoned; it is zero on every path that
// does not sweep. Cleared counts the stale mirrored issues a force-resync dropped
// from a repo whose provider is now internal, and is zero everywhere else.
type SyncResponse struct {
	Repo     string `json:"repo"`
	Provider string `json:"provider"`
	Issues   int    `json:"issues"`
	Comments int    `json:"comments"`
	Removed  int    `json:"removed"`
	Cleared  int    `json:"cleared"`
	SyncedAt string `json:"syncedAt"`
}

// SyncAccepted is the JSON body of a manual sync that coalesced into a pull
// already in flight — background or manual. The caller waits on that pull's
// outcome instead of starting a second one against the same tracker budget.
type SyncAccepted struct {
	Repo  string          `json:"repo"`
	State RepoHealthState `json:"state"`
}

// handleSync pulls a repo's Project into the hub issue store on demand. It is a
// one-way inbound sync (ADR 0007): the tracker owns issue content, so this only
// ever reads. A manual sync means "make the board match the tracker", so the pull
// is followed by the reconcile sweep an incremental pull cannot do on its own —
// and a sweep that fails fails the request rather than reporting a half-done sync.
// It runs under the syncer's per-repo claim, so a sync of the same repo already in
// flight coalesces: 202 with the syncing marker rather than a duplicate pull.
// Unknown repos 404, a repo without direct tracker credentials 422, and a tracker
// error 502; a successful sync returns the counts it wrote and removed.
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
	if !s.syncer.claimManual(repo.Root) {
		writeJSON(w, http.StatusAccepted, SyncAccepted{Repo: repo.Name, State: HealthSyncing})
		return
	}
	resp, err := s.pullAndSweep(r.Context(), repo)
	s.syncer.settleManual(repo.Root, err)
	if err != nil {
		writeSyncErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// pullAndSweep is a manual sync end to end: the incremental pull, then the
// reconcile sweep that tombstones what the tracker no longer returns.
func (s *Server) pullAndSweep(ctx context.Context, repo registry.Repo) (SyncResponse, error) {
	resp, err := s.syncRepo(ctx, repo)
	if err != nil {
		return SyncResponse{}, err
	}
	removed, err := s.reconcileRepo(ctx, repo)
	if err != nil {
		return SyncResponse{}, err
	}
	resp.Removed = len(removed)
	return resp, nil
}

func writeSyncErr(w http.ResponseWriter, err error) {
	if readerConfigErr(err) {
		writeReaderErr(w, err)
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "sync failed: " + err.Error()})
}

// handleForceResync drops a repo's synced issues and cursor and re-pulls the
// Project clean — POST /repos/{repo}/resync, the recovery path when the store's
// sync state is doubted (ADR 0007). Internal issues are preserved, and the pull
// converges to the same content a fresh sync would; the response is that pull's
// counts. It takes the same per-repo claim as handleSync, so a resync asked for
// while a sync is in flight coalesces with 202 rather than dropping the store
// under a running pull. Unknown repos 404, a repo without direct tracker
// credentials 422 (with the store left untouched), a repo whose queue still holds
// the tracker work its stale mirror would retire 409, and a tracker error 502.
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
	if !s.syncer.claimManual(repo.Root) {
		writeJSON(w, http.StatusAccepted, SyncAccepted{Repo: repo.Name, State: HealthSyncing})
		return
	}
	resp, err := s.forceResync(r.Context(), repo)
	s.syncer.settleManual(repo.Root, err)
	if err != nil {
		writeResyncErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeResyncErr maps a failed force-resync onto its response. A mirror drop the
// busy guard refused answers exactly as the settings writes that switch a repo to
// the internal tracker do, so a client reads one shape for that refusal wherever
// it asked for the drop.
func writeResyncErr(w http.ResponseWriter, err error) {
	var busy *trackerSwitchBusy
	switch {
	case errors.As(err, &busy):
		writeInternalDropErr(w, err)
	case readerConfigErr(err):
		writeReaderErr(w, err)
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "resync failed: " + err.Error()})
	}
}

// forceResync re-resolves the repo's tracker binding from current config, then
// drops the synced rows and cursor and re-pulls the Project from an empty cursor —
// a full pull that re-populates the store with only the issues the tracker still
// holds, so deleted or moved-out tickets simply vanish (ADR 0007). Re-resolving
// first is the point of a resync: the cached binding is part of the doubted state,
// so a wrong project key left by an old config is replaced rather than reused for
// the clean re-pull. Internal issues are preserved. The binding is re-resolved
// before anything is dropped, so a repo whose reader cannot resolve one is refused
// with the store intact rather than emptied with nothing to re-pull it. The one
// exception is a repo whose config now names the internal tracker: there is no
// tracker left to re-pull from, so the resync clears the stale mirror instead.
func (s *Server) forceResync(ctx context.Context, repo registry.Repo) (SyncResponse, error) {
	res, err := s.resolveReader(repo)
	if err != nil {
		if res.provider == internalProvider && res.explicit && errors.Is(err, tracker.ErrReaderUnavailable) {
			return s.clearInternalMirror(repo)
		}
		return SyncResponse{}, err
	}
	store := s.stores.Issues()
	if _, err := s.refreshBinding(ctx, store, repo.Root, res); err != nil {
		err = res.actionableErr(err)
		recordSyncErr(store, repo.Root, err)
		return SyncResponse{}, err
	}
	if err := store.DropSynced(repo.Root); err != nil {
		return SyncResponse{}, err
	}
	return s.syncRepo(ctx, repo)
}

// clearInternalMirror is force-resync on a repo pointed at the internal tracker:
// no reader resolves, so there is nothing to pull and the recovery is the guarded
// drop the hub's own switch to internal runs (ADR 0042) — busy guard, identifier
// sequence lifted clear of every id the mirror spoke for, mirror dropped. It is
// the way back from a provider flipped outside the hub's two settings writes (a
// hand-edited .trau.ini, the user layer), which leaves the mirror behind as rows
// no sync will refresh, no editor will touch and no sweep will reconcile away. A
// repo carrying no mirror is a clean no-op. The stamped sync error goes with the
// mirror, so a repo pinned at sync-failed by the tracker it no longer uses reads
// healthy again.
func (s *Server) clearInternalMirror(repo registry.Repo) (SyncResponse, error) {
	cleared, err := s.dropInternalMirrors([]string{repo.Root})
	if err != nil {
		return SyncResponse{}, err
	}
	if err := s.stores.Issues().ClearError(repo.Root); err != nil {
		logger.Verbosef("resync %s: clear stale sync error: %v", repo.Root, err)
	}
	return SyncResponse{
		Repo:     repo.Name,
		Provider: internalProvider,
		Cleared:  cleared,
		SyncedAt: nowStamp(),
	}, nil
}

// reconcileRepo diffs the repo's Project identifier set against the store and
// tombstones the synced issues the tracker no longer returns — those deleted,
// archived, or moved out of the Project, which an incremental SyncPull never
// reports (ADR 0007). Tombstoned issues are dropped from the Queue and the backlog
// board but keep their run artifacts and checkpoints; internal issues are never
// touched. A sweep failure is recorded on
// the same per-repo error surface as sync so a broken tracker backs off. An empty
// identifier set is treated as a no-op rather than tombstoning the whole store —
// it guards against a misresolved binding (a wrong project key returns zero)
// wiping every synced row.
func (s *Server) reconcileRepo(ctx context.Context, repo registry.Repo) ([]string, error) {
	res, err := s.resolveReader(repo)
	if err != nil {
		return nil, err
	}
	store := s.stores.Issues()
	state, err := store.SyncState(repo.Root)
	if err != nil {
		return nil, err
	}
	binding, _, err := s.resolveBinding(ctx, store, repo.Root, state.Binding, res)
	if err != nil {
		err = res.actionableErr(err)
		recordSyncErr(store, repo.Root, err)
		return nil, err
	}
	live, err := res.reader.ProjectIdentifiers(ctx, binding)
	if err != nil {
		err = res.actionableErr(err)
		recordSyncErr(store, repo.Root, err)
		return nil, err
	}
	if len(live) == 0 {
		return nil, nil
	}
	tombstoned, err := store.Reconcile(repo.Root, live)
	if err != nil {
		return nil, err
	}
	s.dropFromQueue(repo.Root, tombstoned)
	// Tracker-sourced attachments follow their ticket out. Uploads are exempt, so an
	// internal issue's pasted image is never swept by a Project reconcile.
	if err := s.stores.Attachments().ReconcileIssues(repo.Root, live); err != nil {
		logger.Verbosef("reconcile %s: prune attachments: %v", repo.Root, err)
	}
	return tombstoned, nil
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
// background sync loop. An explicitly internal provider resolves no reader and
// never records an outcome, so any sync error a previous provider stamped is
// cleared here — otherwise nothing would ever run to clear it and it would pin
// health at sync-failed. A finished pull ends with a queued-label reconcile, so the
// labels it just landed are healed against the queue the hub holds, and a grill
// retire sweep, so a session blocked on an issue the pull just closed is settled.
func (s *Server) syncRepo(ctx context.Context, repo registry.Repo) (SyncResponse, error) {
	res, err := s.resolveReader(repo)
	if err != nil {
		if errors.Is(err, tracker.ErrReaderUnavailable) && res.provider == "internal" && res.explicit {
			_ = s.stores.Issues().ClearError(repo.Root)
		}
		return SyncResponse{}, err
	}
	store := s.stores.Issues()
	state, err := store.SyncState(repo.Root)
	if err != nil {
		return SyncResponse{}, err
	}

	binding, rebound, err := s.resolveBinding(ctx, store, repo.Root, state.Binding, res)
	if err != nil {
		err = res.actionableErr(err)
		recordSyncErr(store, repo.Root, err)
		return SyncResponse{}, err
	}
	if rebound {
		// The config no longer names the target the store mirrors — the rows and
		// cursor belong to the old team/project, and an incremental pull against
		// the new one would keep them and skip everything the cursor predates. So
		// the retarget is a force-resync folded into the ordinary sync: drop the
		// synced rows and pull the new target from an empty cursor.
		logger.Printf("sync %s: tracker target changed — dropping the previous target's mirror and re-pulling clean", repo.Root)
		if err := store.DropSynced(repo.Root); err != nil {
			return SyncResponse{}, err
		}
		state.Cursor = ""
	}
	s.warnTeamOverlap(repo.Root, binding)

	pulled, err := res.reader.SyncPull(ctx, binding, s.pullCursor(repo.Root, res.provider, state.Cursor))
	if err != nil {
		err = res.actionableErr(err)
		recordSyncErr(store, repo.Root, err)
		return SyncResponse{}, err
	}

	issues, comments, err := store.Upsert(repo.Root, res.provider, toStoredIssues(pulled))
	if err != nil {
		recordSyncErr(store, repo.Root, err)
		return SyncResponse{}, err
	}
	for _, iss := range pulled {
		if err := store.ReflectBlockers(repo.Root, iss.ID, blockerRefs(iss.BlockedBy)); err != nil {
			return SyncResponse{}, err
		}
		s.registerAttachments(repo.Root, iss.ID, iss.Attachments)
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
	s.resolveIdentity(ctx, store, repo.Root, state.Me, res.reader)
	s.reconcileQueuedLabels(ctx, repo.Root)
	s.retireClosedGrill(repo.Root)
	return SyncResponse{
		Repo:     repo.Name,
		Provider: res.provider,
		Issues:   issues,
		Comments: comments,
		SyncedAt: syncedAt,
	}, nil
}

// pullCursor is the timestamp the pull resumes from: the stored cursor, unless the
// mirror still holds rows an older trau filed under an identifier the provider has
// since stopped minting. Azure DevOps work items were addressed as PREFIX-n before
// trau settled on the bare organization-wide number (ADR 0024 §1), and an incremental
// pull only re-files what the tracker changed since the cursor — so the reconcile
// sweep behind it would tombstone every untouched row with no bare-number row to
// replace it. One full pull re-files them all; the sweep then clears the retired rows
// and the next tick resumes incrementally.
func (s *Server) pullCursor(root, provider, cursor string) string {
	if cursor == "" || provider != "azure" {
		return cursor
	}
	mirrored, err := s.stores.Issues().SyncedIdentifiers(root, provider)
	if err != nil {
		logger.Verbosef("sync %s: read mirrored identifiers: %v", root, err)
		return cursor
	}
	retired := 0
	for _, id := range mirrored {
		if !bareWorkItemID(id) {
			retired++
		}
	}
	if retired == 0 {
		return cursor
	}
	logger.Printf(
		"sync %s: %d of %d mirrored rows still carry a prefixed work-item id — pulling the whole board once so every row re-files under its number",
		root, retired, len(mirrored),
	)
	return ""
}

// bareWorkItemID reports whether identifier is the bare work-item number Azure
// DevOps work items are addressed by, rather than the PREFIX-n form trau retired.
func bareWorkItemID(identifier string) bool {
	_, err := strconv.Atoi(identifier)
	return err == nil
}

// registerAttachments records the files one issue references — metadata only, so
// a sync costs nothing beyond the pull it already made; the bytes are fetched the
// first time something asks for them. Registration dedupes on the source URL, so
// re-syncing a ticket keeps the rows it already has, and the issue is then
// reconciled against what it currently references so a file that vanished from
// the body stops being listed. Failures are logged rather than failed: the issue
// content already landed and the next sync retries.
func (s *Server) registerAttachments(root, identifier string, found []tracker.Attachment) {
	atts := s.stores.Attachments()
	urls := make([]string, 0, len(found))
	for _, att := range found {
		urls = append(urls, att.URL)
		if _, err := atts.Create(hubstore.Attachment{
			Repo:            root,
			IssueIdentifier: identifier,
			Source:          att.Source,
			SourceURL:       att.URL,
			Filename:        att.Filename,
			MimeType:        att.MimeType,
			SizeBytes:       att.Size,
		}); err != nil {
			logger.Verbosef("attachments %s %s: register %s: %v", root, identifier, att.URL, err)
		}
	}
	if err := atts.ReconcileIssue(root, identifier, urls); err != nil {
		logger.Verbosef("attachments %s %s: prune: %v", root, identifier, err)
	}
}

// resolveBinding returns the repo's tracker binding: the cached ids when the
// cache was stamped with the same config target the repo resolves to now, a
// fresh resolve otherwise. A cache whose stamp differs is the retarget case — a
// .trau.ini whose team/project changed after the ids were cached — which an
// id-only cache would shadow forever, syncing the old target no matter what the
// config says. rebound reports a material retarget: the cache held a different
// target's ids, so the caller must drop the synced rows and pull clean
// (forceResync semantics). A re-resolve that lands on the same ids — a row
// stamped before targets were recorded, or a key renamed tracker-side — only
// restamps the cache, keeping the rows and the incremental cursor.
func (s *Server) resolveBinding(ctx context.Context, store *hubstore.Issues, root string, cached hubstore.SyncBinding, res trackerResolution) (binding tracker.ProjectBinding, rebound bool, err error) {
	binding = tracker.ProjectBinding{
		TeamID:    cached.TeamID,
		ProjectID: cached.ProjectID,
		Project:   cached.Project,
	}
	// A config that names no target cannot retarget the cache — a stored binding
	// stays usable when the team key is dropped — so only a differing named
	// target forces the re-resolve.
	if binding.Resolved() && (res.target == "" || cached.Target == res.target) {
		return binding, false, nil
	}
	fresh, err := s.refreshBinding(ctx, store, root, res)
	if err != nil {
		return tracker.ProjectBinding{}, false, err
	}
	rebound = binding.Resolved() && (fresh.TeamID != binding.TeamID || fresh.ProjectID != binding.ProjectID)
	return fresh, rebound, nil
}

// refreshBinding resolves the repo's tracker binding from current config and
// caches it stamped with the config target it resolved from, overwriting
// whatever was stored. The incremental path reaches it only for an unresolved or
// retargeted cache, so ordinary syncs still skip the team/project lookup; a
// forced resync calls it directly to re-resolve a binding the cache should no
// longer be trusted for.
func (s *Server) refreshBinding(ctx context.Context, store *hubstore.Issues, root string, res trackerResolution) (tracker.ProjectBinding, error) {
	binding, err := res.reader.ResolveBinding(ctx)
	if err != nil {
		return tracker.ProjectBinding{}, err
	}
	if err := store.SaveBinding(root, hubstore.SyncBinding{
		TeamID:    binding.TeamID,
		ProjectID: binding.ProjectID,
		Project:   binding.Project,
		Target:    res.target,
	}); err != nil {
		return tracker.ProjectBinding{}, err
	}
	return binding, nil
}

// identityTTL is how long a repo binding's resolved Me stays good. The tracker
// user behind an API key essentially never changes, so re-resolving it every sync
// tick only spends the request budget the pull itself needs.
const identityTTL = 24 * time.Hour

// resolveIdentity refreshes the repo binding's Me — the tracker user behind its
// credentials — and persists it beside the sync bookkeeping, skipping the call
// while the stored one is still within identityTTL. It is best-effort: an identity
// call that fails (bad or missing credentials, tracker hiccup) is logged and
// swallowed so it never blocks or fails the issue sync, leaving the previously
// stored identity in place.
func (s *Server) resolveIdentity(ctx context.Context, store *hubstore.Issues, root string, cached hubstore.SyncIdentity, reader tracker.Reader) {
	if !identityStale(cached) {
		return
	}
	id, name, err := reader.Identity(ctx)
	if err != nil {
		logger.Verbosef("sync %s: resolve identity: %v", root, err)
		return
	}
	if err := store.SaveIdentity(root, id, name); err != nil {
		logger.Verbosef("sync %s: persist identity: %v", root, err)
	}
}

// identityStale reports whether a repo's cached Me needs resolving again: never
// resolved, resolved longer ago than identityTTL, or invalidated by credentials
// the tracker rejected — which clears the stamp.
func identityStale(me hubstore.SyncIdentity) bool {
	if me.ID == "" || me.ResolvedAt == "" {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, me.ResolvedAt)
	if err != nil {
		return true
	}
	return time.Since(at) > identityTTL
}

// recordSyncErr stamps a failed sync on the repo's health surface, classified by
// what it takes to clear, with one exception: a rate-limit refusal is the shared
// API key's budget running out, not a repo that is misconfigured, and it clears
// itself when the tracker's window rolls — recording it would pin the repo at
// sync-failed with nothing to fix. Credentials the tracker rejected expire the
// cached identity instead, so the next working sync resolves Me again.
func recordSyncErr(store *hubstore.Issues, root string, err error) {
	if _, limited := tracker.RateLimited(err); limited {
		logger.Verbosef("sync %s: tracker rate limited: %v", root, err)
		return
	}
	if tracker.Unauthorized(err) {
		if invalidateErr := store.InvalidateIdentity(root); invalidateErr != nil {
			logger.Verbosef("sync %s: invalidate identity: %v", root, invalidateErr)
		}
	}
	_ = store.RecordError(root, err.Error(), string(tracker.Classify(err)))
}

// warnTeamOverlap logs once per repo and team when another registered repo syncs
// the same tracker team and one of the two pulls it whole: an unfiltered binding
// re-fetches every issue the other repo already pays for, and both spend the same
// API key's hourly request budget doing it.
func (s *Server) warnTeamOverlap(root string, binding tracker.ProjectBinding) {
	key := overlapKey{root: root, team: strings.TrimSpace(binding.TeamID)}
	if key.team == "" || !s.overlapWarningDue(key) {
		return
	}
	bound, err := s.stores.Issues().TeamBindings(key.team)
	if err != nil {
		logger.Verbosef("sync %s: read team bindings: %v", root, err)
		return
	}
	peers, wide := teamOverlap(root, binding, bound)
	if len(peers) == 0 || len(wide) == 0 || !s.claimOverlapWarning(key) {
		return
	}
	logger.Printf(
		"sync %s: shares tracker team %s with %s, and %s pulls the whole team — narrow it with a project (LINEAR_PROJECT) so the repos stop re-fetching the same issues against one API key's hourly budget",
		root, key.team, strings.Join(peers, ", "), strings.Join(wide, ", "),
	)
}

// teamOverlap splits the repos bound to one team into root's peers and the ones —
// root included — whose binding carries no project filter, so the warning can name
// both who overlaps and which binding is the unfiltered one.
func teamOverlap(root string, binding tracker.ProjectBinding, bound []hubstore.TeamBinding) (peers, wide []string) {
	if strings.TrimSpace(binding.ProjectID) == "" {
		wide = append(wide, root)
	}
	for _, b := range bound {
		if b.Repo == root {
			continue
		}
		peers = append(peers, b.Repo)
		if strings.TrimSpace(b.ProjectID) == "" {
			wide = append(wide, b.Repo)
		}
	}
	return peers, wide
}

// overlapKey scopes the team-overlap warning to one repo and tracker team, so a
// hub warns once per pair rather than once per process.
type overlapKey struct {
	root string
	team string
}

// overlapWarningDue reports whether this hub still owes a warning for the pair. It
// is the cheap check the sync path makes before reading the other repos' bindings,
// so a standing misconfiguration costs one query rather than one per tick.
func (s *Server) overlapWarningDue(key overlapKey) bool {
	s.overlapMu.Lock()
	defer s.overlapMu.Unlock()
	return !s.overlapWarned[key]
}

// claimOverlapWarning takes the right to warn about the pair, reporting whether
// this caller got it, so a standing misconfiguration is named once rather than on
// every sync tick.
func (s *Server) claimOverlapWarning(key overlapKey) bool {
	s.overlapMu.Lock()
	defer s.overlapMu.Unlock()
	if s.overlapWarned[key] {
		return false
	}
	s.overlapWarned[key] = true
	return true
}

func blockerRefs(blockers []tracker.SyncedBlocker) []hubstore.BlockerRef {
	out := make([]hubstore.BlockerRef, 0, len(blockers))
	for _, b := range blockers {
		out = append(out, hubstore.BlockerRef{ID: b.ID, Resolved: b.Resolved})
	}
	return out
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
			Type:         iss.Type,
			Level:        iss.Level,
			StackRank:    iss.StackRank,
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
