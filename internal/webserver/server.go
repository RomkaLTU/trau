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
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
	"github.com/RomkaLTU/trau/internal/update"
	"golang.org/x/sync/singleflight"
)

// APIPrefix is the mount path for the versioned JSON API.
const APIPrefix = "/api/v1"

// Server serves the JSON API and the embedded SPA.
type Server struct {
	version          string
	started          time.Time
	assets           fs.FS
	home             string
	bind             string
	token            string
	allowRegister    bool
	workspace        []string
	stores           *hubstore.Stores
	transcripts      *hubstore.Transcripts
	events           *eventBroadcaster
	transcriptEvents *transcriptBroadcaster
	grillEvents      *grillBroadcaster
	startGrill       func(ctx context.Context, sess hubstore.GrillSession)
	runGrillTurn     func(ctx context.Context, sess hubstore.GrillSession)
	pregrillMu       sync.Mutex
	pregrill         map[int64]bool
	shutdownMu       sync.Mutex
	shuttingDown     map[string]bool
	sup              Supervisor
	term             terminalLauncher
	sessionExists    func(sessionID string) bool
	goos             string
	drain            *drainer
	syncer           *syncer
	drainCtx         context.Context
	newWriter        func(config.Config) (tracker.Writer, error)
	newReader        func(config.Config) (tracker.Reader, error)
	newProbe         func(provider string, cfg config.Config) (trackerProbe, error)
	installSkill     func(ctx context.Context, repoRoot, pkg string) error
	removeSkill      func(ctx context.Context, repoRoot, name string) error
	skillsMu         sync.Mutex
	skillsCache      map[string]skillsCacheEntry
	atlas            *atlasRunner
	restart          func()
	restartOnce      sync.Once
	updates          *update.Checker
	attachFetch      singleflight.Group
}

// New builds a Server that reports version and treats now as its start time. It
// reads the instance registry from the machine's trau home. bind and token
// carry the exposure policy: on a non-loopback bind every API request must
// present token as a bearer credential. allowRegister opens repo (un)registration
// on such a bind; loopback binds ignore it and stay open. workspace is the
// allowlist of repo roots the hub may start loops in; anything outside it is
// observe-only. stores is the hub-owned store set backed by the hub databases:
// repo registrations, the per-repo queues, and the separate transcript chunk store.
func New(version, bind, token string, workspace []string, allowRegister bool, stores *hubstore.Stores) *Server {
	s := &Server{
		version:          version,
		started:          time.Now(),
		assets:           assetsFS(),
		home:             registry.Home(),
		bind:             bind,
		token:            token,
		allowRegister:    allowRegister,
		workspace:        normalizeRoots(workspace),
		stores:           stores,
		transcripts:      stores.Transcripts(),
		events:           newEventBroadcaster(),
		transcriptEvents: newTranscriptBroadcaster(),
		grillEvents:      newGrillBroadcaster(),
		pregrill:         map[int64]bool{},
		shuttingDown:     map[string]bool{},
		sup:              newOSSupervisor(),
		term:             osascriptLauncher{},
		sessionExists:    agent.SessionExists,
		goos:             runtime.GOOS,
		drainCtx:         context.Background(),
		newWriter:        defaultWriter,
		newReader:        defaultReader,
		newProbe:         defaultProbe,
		installSkill:     defaultInstallSkill,
		removeSkill:      defaultRemoveSkill,
		skillsCache:      map[string]skillsCacheEntry{},
		updates:          update.NewChecker(version),
	}
	s.drain = newDrainer(s)
	s.syncer = newSyncer(s)
	s.atlas = newAtlasRunner(s)
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
	s.importAllLessons()
	s.importAllPhaseLogs()
	for _, root := range s.effectiveRoots() {
		items, meta, err := s.stores.Queue(root).Snapshot()
		if err != nil {
			continue
		}
		if _, running := firstWithStatus(items, queue.StatusRunning); meta.Draining || running {
			s.drain.ensure(ctx, root)
		}
	}
	go s.sweepKnownRepos(ctx)
	go s.syncer.run(ctx, syncInterval, reconcileInterval)
	go s.pruneRunData(ctx)
	go s.updates.Run(ctx)
}

// runDataPruneInterval bounds how often the hub prunes run data past its retention
// window, on top of the startup pass.
const runDataPruneInterval = time.Hour

// grillIdleAbandon is how long a parked grill session may sit untouched before the
// sweep settles it as abandoned (grilling-prd.md).
const grillIdleAbandon = 30 * 24 * time.Hour

// pruneRunData drops run data past its retention window on startup and on a
// periodic timer (ADR 0008): transcript sessions from transcripts.db, reclaiming
// their freed pages, and event and token-call rows from the authoritative store.
// Checkpoints — the run summaries — are never pruned. The attachment sweeps ride
// along here rather than on a timer of their own. Each pass is best-effort
// hygiene, so a failure is logged and retried on the next tick rather than
// surfaced.
func (s *Server) pruneRunData(ctx context.Context) {
	prune := func() {
		if err := s.transcripts.Prune(); err != nil {
			logger.Verbosef("prune transcripts: %v", err)
		}
		if err := s.stores.Events().Prune(); err != nil {
			logger.Verbosef("prune events: %v", err)
		}
		if err := s.stores.Tokens().Prune(); err != nil {
			logger.Verbosef("prune token calls: %v", err)
		}
		s.sweepIdleGrill()
		if err := s.stores.Grill().Prune(); err != nil {
			logger.Verbosef("prune grill sessions: %v", err)
		}
		if err := s.stores.Attachments().PruneUnboundUploads(); err != nil {
			logger.Verbosef("prune unbound uploads: %v", err)
		}
		s.evictAttachmentCache("")
	}
	prune()
	t := time.NewTicker(runDataPruneInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
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
	_ = s.stores.Registrations().Remember(reposFromEntries(s.liveInstances()))
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
	mux.HandleFunc(APIPrefix+"/hub/restart", s.handleHubRestart)
	mux.HandleFunc(APIPrefix+"/update", s.handleUpdate)
	mux.HandleFunc(APIPrefix+"/update/check", s.handleUpdateCheck)
	mux.HandleFunc(APIPrefix+"/update/apply", s.handleUpdateApply)
	mux.HandleFunc(APIPrefix+"/instances", s.handleInstances)
	mux.HandleFunc(APIPrefix+"/instances/{pid}", s.handleInstance)
	mux.HandleFunc(APIPrefix+"/instances/{pid}/stop", s.handleStopInstance)
	mux.HandleFunc(APIPrefix+"/costs", s.handleCosts)
	mux.HandleFunc(APIPrefix+"/costs/timeseries", s.handleTimeseries)
	mux.HandleFunc(APIPrefix+"/prompts", s.handlePrompts)
	mux.HandleFunc(APIPrefix+"/prompts/{name}", s.handlePromptItem)
	mux.HandleFunc(APIPrefix+"/trackers/{provider}/test-connection", s.handleTrackerTestConnection)
	mux.HandleFunc(APIPrefix+"/repos", s.handleRepos)
	mux.HandleFunc(APIPrefix+"/repos/inspect", s.handleReposInspect)
	mux.HandleFunc(APIPrefix+"/repos/{repo}", s.handleRepo)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/dry-run", s.handleDryRun)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/eligible", s.handleEligible)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/backlog", s.handleBacklog)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/labels", s.handleLabels)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/assignees", s.handleAssignees)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/assignable-users", s.handleAssignableUsers)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/epics/{epic}", s.handleEpicPreview)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/search", s.handleIssueSearch)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/internal", s.handleCreateInternalIssue)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/internal/{id}", s.handleInternalIssue)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/internal/{id}/transition", s.handleInternalTransition)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/{id}", s.handleIssue)
	// A literal issues/{id}/archive would conflict with issues/internal/{id} in
	// net/http's mux (both match issues/internal/archive, neither more specific), so
	// the archive action rides a wildcard segment that stays clear of the internal
	// subtree while still serving the exact .../issues/{id}/archive path.
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues/{id}/{action}", s.handleIssueAction)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/attachments", s.handleUploadAttachment)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/attachments/{id}", s.handleAttachment)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/queue", s.handleQueue)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/queue/drain", s.handleQueueDrain)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/queue/shutdown", s.handleQueueShutdown)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/queue/{id}", s.handleQueueItem)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/queue/{id}/move", s.handleQueueMove)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/atlas", s.handleAtlas)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/atlas/{view}", s.handleAtlasView)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/atlas/{view}/generate", s.handleAtlasGenerate)
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
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/spend", s.handleRunSpend)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/anomalies", s.handleRunAnomalies)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/proofs", s.handleRunProofs)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/proofs/{seq}", s.handleRunProof)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/tokens", s.handleRepoTokens)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/tokens/day", s.handleTokenDay)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/routing", s.handleRepoRouting)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/metrics/config-cohorts", s.handleConfigCohorts)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/comment", s.handleRunComment)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/reset", s.handleResetRun)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/clear", s.handleClearRun)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/takeover", s.handleRunTakeover)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/advance", s.handleAdvanceRun)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/sync", s.handleSync)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/resync", s.handleForceResync)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/reconcile", s.handleReconcileRepo)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/gitignore", s.handleRepoGitignore)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/health", s.handleRepoHealth)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/lessons", s.handleLessons)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/skills", s.handleSkills)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/skills/search", s.handleSkillsSearch)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/skills/rules", s.handleSkillRules)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/skills/{name}", s.handleSkillItem)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/skills/{$}", s.handleSkillItem)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/config", s.handleConfig)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/qa/accounts", s.handleQAAccounts)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/qa/accounts/{id}", s.handleQAAccount)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/qa/notes", s.handleQANotes)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/qa/roster", s.handleQARoster)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/steer", s.handleSteerNotes)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/steer/expire", s.handleSteerExpire)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/steer/{id}/ack", s.handleSteerAck)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/prompts", s.handleRepoPrompts)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/prompts/{name}", s.handleRepoPromptItem)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/events", s.handleEvents)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/events/query", s.handleEventsQuery)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/events/stream", s.handleEventStream)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/transcripts", s.handleTranscripts)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/transcript/chunks", s.handleTranscriptChunks)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/transcript/stream", s.handleTranscriptStream)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/grill", s.handleRepoGrill)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/grill/pregrill", s.handleRepoPregrill)
	mux.HandleFunc(APIPrefix+"/grill/{sid}", s.handleGrillSession)
	mux.HandleFunc(APIPrefix+"/grill/{sid}/answer", s.handleGrillAnswer)
	mux.HandleFunc(APIPrefix+"/grill/{sid}/apply", s.handleGrillApply)
	mux.HandleFunc(APIPrefix+"/grill/{sid}/abandon", s.handleGrillAbandon)
	mux.HandleFunc(APIPrefix+"/grill/{sid}/model", s.handleGrillModel)
	mux.HandleFunc(APIPrefix+"/grill/{sid}/stream", s.handleGrillStream)
	mux.HandleFunc(APIPrefix+"/grill/{sid}/mcp", s.handleGrillMCP)
	mux.HandleFunc(APIPrefix+"/events/stream", s.handleAllEventStream)
	mux.HandleFunc(APIPrefix+"/notifications", s.handleNotifications)
	mux.HandleFunc(APIPrefix+"/notifications/read-all", s.handleNotificationsReadAll)
	mux.HandleFunc(APIPrefix+"/notifications/{id}/read", s.handleNotificationRead)
	mux.HandleFunc("/api/", handleAPINotFound)

	var h http.Handler = mux
	if !Loopback(s.bind) && s.token != "" {
		h = requireToken(s.token, h)
	}
	return h
}

// Health is the /api/v1/health resource.
type Health struct {
	Status        string                        `json:"status"`
	Version       string                        `json:"version"`
	UptimeSeconds float64                       `json:"uptime_seconds"`
	Attachments   hubstore.AttachmentCacheStats `json:"attachments"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	stats, _ := s.stores.Attachments().Stats()
	writeJSON(w, http.StatusOK, Health{
		Status:        "ok",
		Version:       s.version,
		UptimeSeconds: time.Since(s.started).Seconds(),
		Attachments:   stats,
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
