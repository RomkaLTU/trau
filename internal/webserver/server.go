// Package webserver is the trau serve HTTP hub: a versioned JSON API under
// /api/v1 and the embedded web UI at /. It is entirely self-contained — the SPA
// is compiled into the binary via go:embed, so it makes no external requests.
package webserver

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// APIPrefix is the mount path for the versioned JSON API.
const APIPrefix = "/api/v1"

// Server serves the JSON API and the embedded SPA.
type Server struct {
	version   string
	started   time.Time
	assets    fs.FS
	home      string
	bind      string
	token     string
	workspace []string
	sup       Supervisor
	newWriter func(config.Config) (tracker.Writer, error)
}

// New builds a Server that reports version and treats now as its start time. It
// reads the instance registry from the machine's trau home. bind and token
// carry the exposure policy: on a non-loopback bind every API request must
// present token as a bearer credential. workspace is the allowlist of repo roots
// the hub may start loops in; anything outside it is observe-only.
func New(version, bind, token string, workspace []string) *Server {
	return &Server{
		version:   version,
		started:   time.Now(),
		assets:    assetsFS(),
		home:      registry.Home(),
		bind:      bind,
		token:     token,
		workspace: normalizeRoots(workspace),
		sup:       newOSSupervisor(),
		newWriter: defaultWriter,
	}
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
	mux.HandleFunc(APIPrefix+"/repos", s.handleRepos)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/dry-run", s.handleDryRun)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/issues", s.handleCreateIssue)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs", s.handleRuns)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}", s.handleRunDetail)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/runs/{ticket}/comment", s.handleRunComment)
	mux.HandleFunc(APIPrefix+"/repos/{repo}/lessons", s.handleLessons)
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
