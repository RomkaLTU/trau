package webserver

import (
	"net/http"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// RepoHealthState is a repo's designed onboarding/sync state, so the Instances
// page and the repo-scoped gates render a state instead of a raw error.
type RepoHealthState string

const (
	HealthReady        RepoHealthState = "ready"
	HealthUnconfigured RepoHealthState = "unconfigured"
	HealthSyncFailed   RepoHealthState = "sync-failed"
	HealthNeverSynced  RepoHealthState = "never-synced"
	HealthSyncing      RepoHealthState = "syncing"
)

// RepoHealth is the /api/v1/repos/{repo}/health resource: a single repo's health
// state with the sync facts behind it, so a gate can poll one repo cheaply
// instead of scanning the whole repos list.
type RepoHealth struct {
	Repo         string          `json:"repo"`
	State        RepoHealthState `json:"state"`
	LastSyncedAt string          `json:"last_synced_at"`
	LastError    string          `json:"last_error"`
	IssueCount   int             `json:"issue_count"`
}

// deriveHealthState reduces a repo's raw signals to its health state. A pull in
// flight reads as syncing whatever the last outcome; a recorded error is
// sync-failed even when a prior pull left a synced stamp — the melga case, where
// Jira credentials with an unset provider record a linear error over an otherwise
// healthy-looking repo; a synced stamp with no error is ready; and a repo with no
// sync bookkeeping is never-synced when its tracker is configured and
// unconfigured when it is not.
func deriveHealthState(configured, syncing bool, st hubstore.SyncState) RepoHealthState {
	switch {
	case syncing:
		return HealthSyncing
	case st.LastError != "":
		return HealthSyncFailed
	case st.LastSyncedAt != "":
		return HealthReady
	case configured:
		return HealthNeverSynced
	default:
		return HealthUnconfigured
	}
}

// repoConfigured reports whether a repo has an effective tracker-provider config:
// an explicit TRACKER_PROVIDER, or credentials that establish a provider on their
// own (present Jira credentials imply Jira). Without either, sync has nothing to
// run as and the repo is unconfigured. It reuses the layered-config read the
// inspection report is built from.
func (s *Server) repoConfigured(repo registry.Repo) bool {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, sources, _ := config.LoadLayeredWithSources(projectPath, userPath, "", "")
	return activeProviderFrom(cfg, sources) != ""
}

// repoHealth builds the health resource for one repo. It reads the same signals
// the repos-list freshness does and feeds them through deriveHealthState, so the
// two endpoints never disagree on a repo's state.
func (s *Server) repoHealth(repo registry.Repo) RepoHealth {
	st, _ := s.stores.Issues().SyncState(repo.Root)
	count, _ := s.stores.Issues().Count(repo.Root)
	syncing := s.syncer.syncing(repo.Root)
	return RepoHealth{
		Repo:         repo.Name,
		State:        deriveHealthState(s.repoConfigured(repo), syncing, st),
		LastSyncedAt: st.LastSyncedAt,
		LastError:    st.LastError,
		IssueCount:   count,
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
