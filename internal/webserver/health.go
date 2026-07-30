package webserver

import (
	"net/http"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// RepoHealthState is a repo's designed onboarding/sync state, so the Instances
// page and the repo-scoped gates render a state instead of a raw error.
type RepoHealthState string

const (
	HealthReady        RepoHealthState = "ready"
	HealthUnconfigured RepoHealthState = "unconfigured"
	HealthDegraded     RepoHealthState = "degraded"
	HealthSyncFailed   RepoHealthState = "sync-failed"
	HealthNeverSynced  RepoHealthState = "never-synced"
	HealthSyncing      RepoHealthState = "syncing"
)

// RepoHealth is the /api/v1/repos/{repo}/health resource: a single repo's health
// state with the sync facts behind it, so a gate can poll one repo cheaply
// instead of scanning the whole repos list. Provider is the tracker the state was
// derived from, so a caller can tell an internal-only or unconfigured repo —
// nothing to pull — from one that syncs.
type RepoHealth struct {
	Repo          string          `json:"repo"`
	Provider      string          `json:"provider"`
	State         RepoHealthState `json:"state"`
	LastSyncedAt  string          `json:"last_synced_at"`
	LastError     string          `json:"last_error"`
	LastErrorKind string          `json:"last_error_kind"`
	IssueCount    int             `json:"issue_count"`
}

// deriveHealthState reduces a repo's raw signals to its health state. An
// explicitly internal provider is ready whatever the bookkeeping says: its issues
// live in the hub store (ADR 0007), there is no pull to fail, and any recorded
// error is a leftover from a previous provider. Otherwise a pull in flight reads
// as syncing whatever the last outcome; a recorded error over an earlier synced
// stamp is degraded rather than sync-failed, because the store still holds that
// pull's issues and a page can serve them; an error with nothing synced behind it
// is sync-failed — the melga case, where Jira credentials with an unset provider
// record a linear error and there is no local data to fall back on; a synced stamp
// with no error is ready; and a repo with no sync bookkeeping is never-synced when
// its tracker is configured and unconfigured when it is not.
func deriveHealthState(provider string, syncing bool, st hubstore.SyncState) RepoHealthState {
	switch {
	case provider == "internal":
		return HealthReady
	case syncing:
		return HealthSyncing
	case st.LastError != "" && st.LastSyncedAt != "":
		return HealthDegraded
	case st.LastError != "" && !selfHealing(st.LastErrorKind):
		return HealthSyncFailed
	case st.LastSyncedAt != "":
		return HealthReady
	case provider != "":
		return HealthNeverSynced
	default:
		return HealthUnconfigured
	}
}

// selfHealing reports whether a recorded error clears without anyone touching the
// repo. A repo whose first pull was only rate-limited has nothing to show yet, but
// nothing to fix either, so it must not be gated as though it were misconfigured.
func selfHealing(kind string) bool {
	return kind == string(tracker.ErrorRateLimit) || kind == string(tracker.ErrorTransient)
}

// repoActiveProvider is the effective tracker provider a repo's layered config
// establishes: an explicit TRACKER_PROVIDER, or credentials that establish a
// provider on their own (present Jira credentials imply Jira). Empty means no
// effective tracker-provider config — the repo is unconfigured. It reuses the
// layered-config read the inspection report is built from. It deliberately does
// not fall back the way a pull does: the user layer's shared LINEAR_API_KEY would
// otherwise make every repo on the machine, including one with no config at all,
// report a tracker it was never bound to.
func (s *Server) repoActiveProvider(repo registry.Repo) string {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, sources, _ := config.LoadLayeredWithSources(projectPath, userPath, "", "")
	return activeProviderFrom(cfg, sources)
}

// repoHealth builds the health resource for one repo. It reads the same signals
// the repos-list freshness does and feeds them through deriveHealthState, so the
// two endpoints never disagree on a repo's state.
func (s *Server) repoHealth(repo registry.Repo) RepoHealth {
	st, _ := s.stores.Issues().SyncState(repo.Root)
	count, _ := s.stores.Issues().Count(repo.Root)
	syncing := s.syncer.syncing(repo.Root)
	provider := s.repoActiveProvider(repo)
	return RepoHealth{
		Repo:          repo.Name,
		Provider:      provider,
		State:         deriveHealthState(provider, syncing, st),
		LastSyncedAt:  st.LastSyncedAt,
		LastError:     st.LastError,
		LastErrorKind: st.LastErrorKind,
		IssueCount:    count,
	}
}

func (s *Server) handleRepoHealth(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, s.repoHealth(repo))
}
