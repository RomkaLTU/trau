// Package webserver is the trau serve HTTP hub: a versioned JSON API under
// /api/v1 and the embedded web UI at /. It is entirely self-contained — the SPA
// is compiled into the binary via go:embed, so it makes no external requests.
package webserver

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// APIPrefix is the mount path for the versioned JSON API.
const APIPrefix = "/api/v1"

// Server serves the JSON API and the embedded SPA.
type Server struct {
	version       string
	started       time.Time
	assets        fs.FS
	home          string
	bind          string
	token         string
	allowRegister bool
	workspace     []string
	stores        *hubstore.Stores
	events        *eventBroadcaster
	sup           Supervisor
	drain         *drainer
	syncer        *syncer
	drainCtx      context.Context
	newWriter     func(config.Config) (tracker.Writer, error)
	newReader     func(config.Config) (tracker.Reader, error)
	installSkill  func(ctx context.Context, repoRoot, pkg string) error
	removeSkill   func(ctx context.Context, repoRoot, name string) error
	skillsMu      sync.Mutex
	skillsCache   map[string]skillsCacheEntry
}

// New builds a Server that reports version and treats now as its start time. It
// reads the instance registry from the machine's trau home. bind and token
// carry the exposure policy: on a non-loopback bind every API request must
// present token as a bearer credential. allowRegister opens repo (un)registration
// on such a bind; loopback binds ignore it and stay open. workspace is the
// allowlist of repo roots the hub may start loops in; anything outside it is
// observe-only. stores is the hub-owned store set backed by the hub database:
// repo registrations and the per-repo queues.
func New(version, bind, token string, workspace []string, allowRegister bool, stores *hubstore.Stores) *Server {
	s := &Server{
		version:       version,
		started:       time.Now(),
		assets:        assetsFS(),
		home:          registry.Home(),
		bind:          bind,
		token:         token,
		allowRegister: allowRegister,
		workspace:     normalizeRoots(workspace),
		stores:        stores,
		events:        newEventBroadcaster(),
		sup:           newOSSupervisor(),
		drainCtx:      context.Background(),
		newWriter:     defaultWriter,
		newReader:     defaultReader,
		installSkill:  defaultInstallSkill,
		removeSkill:   defaultRemoveSkill,
		skillsCache:   map[string]skillsCacheEntry{},
	}
	s.drain = newDrainer(s)
	s.syncer = newSyncer(s)
	return s
}

// repoSweepInterval bounds how often the hub folds live loops into the known-repos
// set off the request path, so a repo lingers after its loop exits without any
// read handler having to write.
const repoSweepInterval = 30 * time.Second

// Start resumes draining any allowlisted repo whose queue was left draining, so
// a serve restart picks the Queue back up instead of stalling it, and launches
// the known-repos sweep and the background issue-store sync on syncInterval. ctx
// governs them all: cancelling it stops the drain loops between children without
// killing a child already in flight, and ends the tickers. A non-positive
// syncInterval disables the background sync; each sync tick also runs the
// reconciliation sweep for repos due on reconcileInterval, so a disabled sync
// disables the sweep too. Call it once before serving.
func (s *Server) Start(ctx context.Context, syncInterval, reconcileInterval time.Duration) {
	s.drainCtx = ctx
	s.importAllCheckpoints()
	s.importAllArtifacts()
	s.importAllPhaseLogs()
	for _, root := range s.effectiveRoots() {
		items, draining, err := s.stores.Queue(root).Snapshot()
		if err != nil {
			continue
		}
		if _, running := firstWithStatus(items, queue.StatusRunning); draining || running {
			s.drain.ensure(ctx, root)
		}
	}
	go s.sweepKnownRepos(ctx)
	go s.syncer.run(ctx, syncInterval, reconcileInterval)
}

// sweepKnownRepos periodically records the repos of the currently live loops in
// the known set. This is the write side of the known-repos state, kept off every
// read handler so a GET never mutates registration state as a side effect.
func (s *Server) sweepKnownRepos(ctx context.Context) {
	s.rememberLiveRepos()
	t := time.NewTicker(repoSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.rememberLiveRepos()
		}
	}
}

func (s *Server) rememberLiveRepos() {
	_ = s.stores.Registrations().Remember(reposFromEntries(registry.Live(s.home)))
}

// reposFromEntries projects live registry entries onto known-repo rows, naming a
// repo by its root's base and skipping entries with no repo root.
func reposFromEntries(entries []registry.Entry) []registry.Repo {
	repos := make([]registry.Repo, 0, len(entries))
	for _, e := range entries {
		if e.RepoRoot == "" {
			continue
		}
		repos = append(repos, registry.Repo{
			Name:    filepath.Base(e.RepoRoot),
			Root:    e.RepoRoot,
			RunsDir: e.RunsDir,
		})
	}
	return repos
}

// Handler returns the fully wired HTTP handler. On a non-loopback bind the API
// namespace is gated behind the bearer token; the embedded SPA shell stays
// public so a browser can still load it and prompt for the token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", s.apiHandler())
	mux.Handle("/", s.spa())
	return mux
}

func (s *Server) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(APIPrefix+"/health", s.handleHealth)
	mux.HandleFunc(APIPrefix+"/instances", s.handleInstances)
	mux.HandleFunc(APIPrefix+"/instances/{pid}/stop", s.handleStopInstance)
	mux.HandleFunc(APIPrefix+"/costs", s.handleCosts)
	mux.HandleFunc(APIPrefix+"/costs/timeseries", s.handleTimeseries)
	mux.HandleFunc(APIPrefix+"/repos", s.handleRepos)
	mux.HandleFunc(APIPrefix+"/repos/{repo}", s.handleRepo)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/dry-run", s.handleDryRun)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/eligible", s.handleEligible)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/backlog", s.handleBacklog)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/epics/{epic}", s.handleEpicPreview)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues", s.handleCreateIssue)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/search", s.handleIssueSearch)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/internal", s.handleCreateInternalIssue)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/internal/{id}", s.handleInternalIssue)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/internal/{id}/transition", s.handleInternalTransition)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/{id}", s.handleIssue)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/prd", s.handlePublishPRD)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/queue", s.handleQueue)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/queue/drain", s.handleQueueDrain)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/queue/{id}", s.handleQueueItem)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/queue/{id}/move", s.handleQueueMove)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/checkpoints", s.handleRepoCheckpoints)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs", s.handleRuns)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}", s.handleRunDetail)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/checkpoint", s.handleRunCheckpoint)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/artifacts", s.handleRunArtifacts)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/artifacts/{kind}", s.handleRunArtifact)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/drain-outcome", s.handleRunDrainOutcome)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/logs", s.handleRunPhaseLogs)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/logs/{phase}", s.handleRunPhaseLog)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/tokens", s.handleRunTokens)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/anomalies", s.handleRunAnomalies)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/tokens", s.handleRepoTokens)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/tokens/day", s.handleTokenDay)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/comment", s.handleRunComment)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/reset", s.handleResetRun)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/clear", s.handleClearRun)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/sync", s.handleSync)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/resync", s.handleForceResync)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/reconcile", s.handleReconcileRepo)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/lessons", s.handleLessons)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/skills", s.handleSkills)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/skills/search", s.handleSkillsSearch)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/skills/{name}", s.handleSkillItem)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/skills/{$}", s.handleSkillItem)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/config", s.handleConfig)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/events", s.handleEvents)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/events/stream", s.handleEventStream)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/transcripts", s.handleTranscripts)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/transcript/stream", s.handleTranscriptStream)
	mux.HandleFunc(APIPrefix+"/events/stream", s.handleAllEventStream)
	mux.HandleFunc("/api/", handleAPINotFound)

	var h http.Handler = mux
	if !Loopback(s.bind) && s.token != "" {
		h = requireToken(s.token, h)
	}
	return h
}

// Health is the /api/v1/health resource.
type Health struct {
	Status        string  `json:"status"`
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, Health{
		Status:        "ok",
		Version:       s.version,
		UptimeSeconds: time.Since(s.started).Seconds(),
	})
}

// handleAPINotFound keeps the /api namespace JSON-only so an unknown API path
// returns a 404 resource instead of leaking the SPA shell.
func handleAPINotFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

// spa serves the embedded assets, falling back to the SPA shell for paths that
// don't map to a file so client-side routing keeps working.
func (s *Server) spa() http.Handler {
	files := http.FileServerFS(s.assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if _, err := fs.Stat(s.assets, name); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
