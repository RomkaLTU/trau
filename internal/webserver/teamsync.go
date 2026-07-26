package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/teamsync"
)

// teamSyncTimeout bounds one repo's pass so a hung git transport never wedges the
// sweep behind it.
const teamSyncTimeout = 2 * time.Minute

// errNoRemote is the pass outcome for a repo with nothing to sync against. It is
// recorded like any other failure rather than raised: the toggle can be on while a
// repo waits for its remote.
var errNoRemote = errors.New("repo has no git remote to sync against")

// teamSyncer owns every git ref operation team sync makes (ADR 0007: the hub is
// the single writer). Passes are best-effort and blameless — a failure is recorded
// as the repo's last error and never reaches the run that triggered it.
type teamSyncer struct {
	srv     *Server
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	pending map[string]bool
}

func newTeamSyncer(s *Server) *teamSyncer {
	return &teamSyncer{srv: s, locks: map[string]*sync.Mutex{}, pending: map[string]bool{}}
}

// run folds teammates' records in on the hub's periodic sync cadence, on top of
// the startup pass. A non-positive interval disables the sweep; the manual "Sync
// now" gesture and the run-lifecycle triggers still work.
func (ts *teamSyncer) run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ts.sweep(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ts.sweep(ctx)
		}
	}
}

func (ts *teamSyncer) sweep(ctx context.Context) {
	for _, repo := range ts.srv.knownRepos(ts.srv.liveInstances()) {
		ts.sync(ctx, repo.Root)
	}
}

// kick runs a repo's pass off the request path, so a lifecycle trigger never adds
// git latency to the call that fired it. A repo that already has a pass waiting
// collapses into it, so a run recording several lessons publishes once.
func (ts *teamSyncer) kick(root string) {
	if root == "" || !ts.markPending(root) {
		return
	}
	go ts.sync(ts.srv.drainCtx, root)
}

// sync runs one repo's pass: fetch the teammates' refs and fold them in, then
// publish this machine's own records. A repo that has not opted in does nothing at
// all — no fetch, no push, no ref. Passes for the same repo are serialized, so a
// caller that asked for one always gets one and never reads another pass's state.
// A pass frees the repo's pending slot only once it is under way, so every kick
// that lands while it waits collapses into it.
func (ts *teamSyncer) sync(ctx context.Context, root string) {
	lock := ts.repoLock(root)
	lock.Lock()
	defer lock.Unlock()
	ts.clearPending(root)

	cfg, ok := ts.srv.repoLayeredConfig(root)
	if !ok || !cfg.TeamSync {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, teamSyncTimeout)
	defer cancel()

	writer, err := ts.pass(ctx, root, teamsync.Config{RepoDir: root, Remote: cfg.Remote})
	st := hubstore.TeamSyncState{
		WriterID:   writer.ID,
		LastSyncAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err != nil {
		st.LastError = err.Error()
	}
	if err := ts.srv.stores.TeamSync().SaveState(root, st); err != nil {
		logger.Verbosef("team sync: save state %s: %v", root, err)
	}
}

// pass runs the fetch and the push independently and joins their outcomes, so a
// remote that refuses one direction — a read-only clone, a host that rejects
// custom refs — still gets the value of the other.
func (ts *teamSyncer) pass(ctx context.Context, root string, cfg teamsync.Config) (teamsync.Writer, error) {
	writer := teamsync.ResolveWriter(ctx, cfg)
	if !teamsync.HasRemote(ctx, cfg) {
		return writer, errNoRemote
	}
	return writer, errors.Join(
		ts.foldIn(ctx, root, cfg, writer),
		ts.publish(ctx, root, cfg, writer),
	)
}

// foldIn replaces the repo's teammate records with what the remote now carries.
// Locally recorded lessons are never touched.
func (ts *teamSyncer) foldIn(ctx context.Context, root string, cfg teamsync.Config, writer teamsync.Writer) error {
	payloads, err := teamsync.Fetch(ctx, cfg, writer.ID)
	if err != nil {
		return fmt.Errorf("fetch teammate refs: %w", err)
	}
	fetchedAt := time.Now().UTC().Format(time.RFC3339)
	records := make([]hubstore.TeamRecord, 0, len(payloads))
	for _, p := range payloads {
		raw, err := json.Marshal(p)
		if err != nil {
			continue
		}
		records = append(records, hubstore.TeamRecord{
			WriterID:    p.Writer,
			AuthorName:  p.Name,
			AuthorEmail: p.Email,
			UpdatedAt:   p.UpdatedAt,
			FetchedAt:   fetchedAt,
			Payload:     string(raw),
		})
	}
	if err := ts.srv.stores.TeamSync().Replace(root, records); err != nil {
		return fmt.Errorf("store teammate records: %w", err)
	}
	return nil
}

// publish snapshots this machine's own lessons onto its ref. Teammates' records
// live in a separate table and are never re-exported, so a payload only ever
// carries what this machine recorded.
func (ts *teamSyncer) publish(ctx context.Context, root string, cfg teamsync.Config, writer teamsync.Writer) error {
	local, err := ts.srv.stores.Lessons().All(root)
	if err != nil {
		return fmt.Errorf("read local lessons: %w", err)
	}
	shared := make([]teamsync.Lesson, len(local))
	for i, l := range local {
		shared[i] = teamsync.Lesson{
			Ticket:       l.Ticket,
			Phase:        l.Phase,
			FailureType:  l.FailureType,
			AttemptedFix: l.AttemptedFix,
			Evidence:     l.Evidence,
			Result:       l.Result,
			Lesson:       l.Lesson,
			Tags:         l.Tags,
			RecordedAt:   l.RecordedAt,
		}
	}
	if err := teamsync.Push(ctx, cfg, writer, shared); err != nil {
		return fmt.Errorf("publish snapshot: %w", err)
	}
	return nil
}

func (ts *teamSyncer) repoLock(root string) *sync.Mutex {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	lock := ts.locks[root]
	if lock == nil {
		lock = &sync.Mutex{}
		ts.locks[root] = lock
	}
	return lock
}

func (ts *teamSyncer) markPending(root string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.pending[root] {
		return false
	}
	ts.pending[root] = true
	return true
}

func (ts *teamSyncer) clearPending(root string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.pending, root)
}

// TeamSyncStatus is the /api/v1/repos/{repo}/team-sync resource: whether sharing
// is on and possible for the repo, the identity this machine publishes under, how
// many teammates it currently reads, and the last pass's outcome.
type TeamSyncStatus struct {
	Repo       string `json:"repo"`
	Enabled    bool   `json:"enabled"`
	HasRemote  bool   `json:"has_remote"`
	Remote     string `json:"remote"`
	WriterID   string `json:"writer_id,omitempty"`
	Author     string `json:"author,omitempty"`
	Teammates  int    `json:"teammates"`
	LastSyncAt string `json:"last_sync_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

// handleTeamSync reports a repo's sharing status and runs the manual "Sync now"
// gesture. The POST syncs inline so the caller sees the outcome it caused, and
// answers with the same status either way.
func (s *Server) handleTeamSync(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.teamSyncStatus(r.Context(), repo))
	case http.MethodPost:
		s.team.sync(r.Context(), repo.Root)
		writeJSON(w, http.StatusOK, s.teamSyncStatus(r.Context(), repo))
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) teamSyncStatus(ctx context.Context, repo registry.Repo) TeamSyncStatus {
	cfg, _ := s.repoLayeredConfig(repo.Root)
	tcfg := teamsync.Config{RepoDir: repo.Root, Remote: cfg.Remote}
	status := TeamSyncStatus{
		Repo:      repo.Name,
		Enabled:   cfg.TeamSync,
		HasRemote: teamsync.HasRemote(ctx, tcfg),
		Remote:    cfg.Remote,
	}
	if status.Remote == "" {
		status.Remote = "origin"
	}
	if st, err := s.stores.TeamSync().State(repo.Root); err == nil {
		status.WriterID = st.WriterID
		status.LastSyncAt = st.LastSyncAt
		status.LastError = st.LastError
	} else {
		logger.Verbosef("team sync: read state %s: %v", repo.Root, err)
	}
	if records, err := s.stores.TeamSync().Records(repo.Root); err == nil {
		status.Teammates = len(records)
	}
	status.Author = teamsync.ResolveWriter(ctx, tcfg).Name
	return status
}

// teamSyncUnavailable explains why a repo cannot share, or is empty when it can.
// The settings surface uses it to disable the toggle with copy rather than letting
// a user turn on something that would never do anything.
func (s *Server) teamSyncUnavailable(ctx context.Context, root string) string {
	cfg, _ := s.repoLayeredConfig(root)
	if teamsync.HasRemote(ctx, teamsync.Config{RepoDir: root, Remote: cfg.Remote}) {
		return ""
	}
	return "This repo has no git remote, and team sync travels over the repo's own remote. Add one to share lessons with teammates."
}

// repoLayeredConfig resolves a repo's effective config the way its loop would,
// from the project and user layers only — the hub's own cwd has no bearing on
// another repo's settings.
func (s *Server) repoLayeredConfig(root string) (config.Config, bool) {
	projectPath := config.ProjectConfigPath(root)
	userPath := ""
	if home, err := os.UserHomeDir(); err == nil {
		userPath = config.ProjectConfigPath(home)
	}
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return config.Config{}, false
	}
	return cfg, true
}
