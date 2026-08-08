package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

const (
	// appReadyTimeout bounds how long a freshly started app has to accept on its
	// port before the hub calls it failed. A cold dev server that installs or
	// compiles on boot needs most of a minute; anything slower is not a start the
	// run can wait on.
	appReadyTimeout = 60 * time.Second
	// appReadyPoll is how often the port is dialled while the app is starting.
	appReadyPoll = 250 * time.Millisecond
	// appDialTimeout bounds one readiness dial, so a port that black-holes packets
	// costs a tick rather than the whole window.
	appDialTimeout = 750 * time.Millisecond
	// appPortScan is how far above the base the allocator looks before giving up.
	// It is a whole machine's worth of lanes; running out means something other
	// than trau owns the range.
	appPortScan = 200
	// appLogTailBytes bounds the captured output handed back with a failed start —
	// the last few KB is where a command that would not serve says why.
	appLogTailBytes = 8 << 10
	// appLogMaxBytes caps the log file itself. A dev server logging every request
	// would otherwise fill the disk over the days a parked tree stays up, so the
	// file is trimmed to its tail whenever the hub looks at it.
	appLogMaxBytes = 1 << 20
)

// WorktreeAppView is the app a worktree serves, as the run page and the run child
// both see it. Serving is false for a repo with no APP_START_CMD: there is no app
// to start, which is a different answer from an app that is merely stopped, and
// the child needs the distinction to know whether to keep its configured APP_URL.
type WorktreeAppView struct {
	Ticket    string `json:"ticket"`
	Port      int    `json:"port,omitempty"`
	URL       string `json:"url,omitempty"`
	State     string `json:"state"`
	PID       int    `json:"pid,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	// Output is the tail of what the start command printed, carried only when the
	// app failed to come up — the one place it is the answer to the request.
	Output  string `json:"output,omitempty"`
	Serving bool   `json:"serving"`
}

// worktreeAppRequest names the ticket whose app to bring up. It is the tree's own
// key, which for an epic's shared tree is the epic id — the same key the child
// reported the tree under.
type worktreeAppRequest struct {
	Ticket string `json:"ticket"`
}

// handleWorktreeApp brings the ticket's worktree app up and answers with its URL.
// It is the "ensure app running" every trigger lands on: the run child asking for
// a verify URL, and the Start button on the run page. An app already up is
// answered from the row without touching a process.
func (s *Server) handleWorktreeApp(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req worktreeAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	ticket := strings.TrimSpace(req.Ticket)
	if ticket == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ticket is required"})
		return
	}
	view, err := s.ensureWorktreeApp(r.Context(), repo, ticket)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// ensureWorktreeApp has the ticket's app running in its worktree and reports where
// it is. A repo with no APP_START_CMD answers Serving=false without starting
// anything; a tree that is gone is an error, since there is nowhere to serve from.
// An app that will not come up is reported failed with its output rather than
// raised as an error: the caller's fallback is to verify without a URL, never to
// fail the run.
//
// Starts are collapsed per tree: two lanes racing to the same ticket — the child
// asking for its URL while a person presses Start — must not spawn two apps on two
// ports for one tree.
func (s *Server) ensureWorktreeApp(ctx context.Context, repo registry.Repo, ticket string) (WorktreeAppView, error) {
	cfg, err := repoConfig(repo.Root)
	if err != nil {
		return WorktreeAppView{}, fmt.Errorf("read %s config: %w", repo.Name, err)
	}
	command := strings.TrimSpace(cfg.AppStartCmd)
	if command == "" {
		return WorktreeAppView{Ticket: ticket, State: hubstore.AppStopped}, nil
	}
	res, err, _ := s.appStart.Do(repo.Root+"\x00"+ticket, func() (any, error) {
		return s.startWorktreeApp(ctx, repo, ticket, cfg, command)
	})
	if err != nil {
		return WorktreeAppView{}, err
	}
	return res.(WorktreeAppView), nil
}

// startWorktreeApp is ensureWorktreeApp's body, run one at a time per tree.
func (s *Server) startWorktreeApp(
	ctx context.Context, repo registry.Repo, ticket string, cfg config.Config, command string,
) (WorktreeAppView, error) {
	row, exists, err := s.stores.Worktrees().ByTicket(repo.Root, ticket)
	if err != nil {
		return WorktreeAppView{}, fmt.Errorf("read the worktree for %s: %w", ticket, err)
	}
	if !exists || row.State != hubstore.WorktreeActive {
		return WorktreeAppView{}, errors.New("no active worktree for " + ticket + " to serve an app from")
	}
	if !dirExists(row.Path) {
		return WorktreeAppView{}, errors.New("the worktree for " + ticket + " is no longer on disk")
	}
	if row.App.State == hubstore.AppRunning && row.App.Port > 0 && s.alive(row.App.PID) {
		return worktreeAppView(row, true, ""), nil
	}
	// A pid recorded against a tree whose app is not running is a leftover — a hub
	// that died mid-start, a readiness window that expired. It goes before another
	// app is put on another port for the same tree.
	if row.App.PID > 0 && s.alive(row.App.PID) {
		if err := s.sup.Kill(row.App.PID); err != nil {
			logger.Verbosef("stop the stale app for %s: %v", ticket, err)
		}
	}

	port, release, err := s.allocateAppPort(cfg.AppPortBase())
	if err != nil {
		return WorktreeAppView{}, err
	}
	// The port stays reserved until this start has written it to the row, which is
	// the moment another lane's allocation can see it for itself.
	defer release()
	logPath := s.appLogPath(repo.Root, ticket)
	pid, err := s.sup.SpawnApp(AppSpec{
		Dir:     row.Path,
		Command: command,
		Env:     appEnv(port),
		LogPath: logPath,
	})
	if err != nil {
		return s.recordAppState(repo.Root, row, hubstore.WorktreeApp{
			Port: port, State: hubstore.AppFailed,
		}, err.Error()), nil
	}

	started := hubstore.WorktreeApp{
		Port:      port,
		PID:       pid,
		State:     hubstore.AppStarting,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := s.stores.Worktrees().SetApp(repo.Root, row.ID, started); err != nil {
		logger.Verbosef("record the starting app for %s: %v", ticket, err)
	}
	if waitForPort(ctx, port, logPath, s.appReadyWait) {
		started.State = hubstore.AppRunning
		return s.recordAppState(repo.Root, row, started, ""), nil
	}
	// Nothing ever listened, so the process is not the app it claimed to be: it
	// goes, and the port with it, but the output it left is the whole point.
	if err := s.sup.Kill(pid); err != nil {
		logger.Verbosef("stop the app that never listened for %s: %v", ticket, err)
	}
	started.PID, started.State = 0, hubstore.AppFailed
	view := s.recordAppState(repo.Root, row, started, appLogTail(logPath))
	logger.Verbosef("app for %s never listened on port %d within %s", ticket, port, s.appReadyWait)
	return view, nil
}

// recordAppState writes the app's standing to the row and renders the view the
// caller answers with, falling back to the in-hand values if the write fails —
// a store hiccup must not turn a started app into a reported failure.
func (s *Server) recordAppState(
	root string, row hubstore.Worktree, app hubstore.WorktreeApp, output string,
) WorktreeAppView {
	updated, err := s.stores.Worktrees().SetApp(root, row.ID, app)
	if err != nil {
		logger.Verbosef("record the app for %s: %v", row.Ticket, err)
		row.App = app
		return worktreeAppView(row, true, output)
	}
	return worktreeAppView(updated, true, output)
}

// stopWorktreeApp ends whatever a tree is serving and returns its row to stopped.
// Every removal path calls it first: a tree that is about to leave the disk must
// not leave a dev server behind holding its port and its files open.
func (s *Server) stopWorktreeApp(root string, row hubstore.Worktree) {
	if row.App.PID > 0 && s.alive(row.App.PID) {
		if err := s.sup.Kill(row.App.PID); err != nil {
			logger.Verbosef("stop the app for %s: %v", row.Ticket, err)
		}
	}
	if row.App == (hubstore.WorktreeApp{State: hubstore.AppStopped}) || row.App == (hubstore.WorktreeApp{}) {
		return
	}
	if _, err := s.stores.Worktrees().SetApp(root, row.ID, hubstore.WorktreeApp{
		State: hubstore.AppStopped,
	}); err != nil {
		logger.Verbosef("record the stopped app for %s: %v", row.Ticket, err)
	}
}

// stopWorktreeAppFor stops the app of the repo's row for a ticket, for the settle
// paths that hold a ticket rather than a row. A ticket with no row — the whole
// worktree-less world — costs one absent lookup.
func (s *Server) stopWorktreeAppFor(root, ticket string) {
	row, exists, err := s.stores.Worktrees().ByTicket(root, ticket)
	if err != nil || !exists {
		return
	}
	s.stopWorktreeApp(root, row)
}

// reconcileWorktreeApp re-adopts or clears one row's app at boot. A recorded pid
// still alive is the app this hub's predecessor started and the tree still needs,
// so it is kept exactly as it stands; a pid that is gone leaves a row promising a
// URL nothing answers, so it is returned to stopped.
func (s *Server) reconcileWorktreeApp(root string, row hubstore.Worktree) {
	if row.App.State != hubstore.AppStarting && row.App.State != hubstore.AppRunning {
		return
	}
	if row.App.PID > 0 && s.alive(row.App.PID) {
		return
	}
	if _, err := s.stores.Worktrees().SetApp(root, row.ID, hubstore.WorktreeApp{
		State: hubstore.AppStopped,
	}); err != nil {
		logger.Verbosef("reconcile the app for %s: %v", row.Ticket, err)
	}
}

// allocateAppPort returns the lowest port at or above base that no live worktree
// app holds and that nothing on the machine is already bound to, reserved against
// every other lane until the returned release is called. The bind probe is what
// makes the answer true rather than merely unclaimed: the ports around a
// developer's base are exactly where their own servers sit.
//
// The reservation is the other half of the answer, and it is why allocation is
// serialized rather than merely per-tree: starts for two different trees run at
// once, and a port only reaches HeldPorts once its start has written it to the
// row. Without the reservation both lanes read the same free port, both spawn on
// it, and the loser dies on bind while its row claims it is running.
func (s *Server) allocateAppPort(base int) (int, func(), error) {
	s.appPortMu.Lock()
	defer s.appPortMu.Unlock()
	held, err := s.stores.Worktrees().HeldPorts()
	if err != nil {
		return 0, nil, fmt.Errorf("read the ports worktree apps hold: %w", err)
	}
	for port := base; port < base+appPortScan; port++ {
		if held[port] || s.appPortHeld[port] || !portFree(port) {
			continue
		}
		if s.appPortHeld == nil {
			s.appPortHeld = map[int]bool{}
		}
		s.appPortHeld[port] = true
		return port, func() { s.releaseAppPort(port) }, nil
	}
	return 0, nil, fmt.Errorf("no free port for a worktree app between %d and %d", base, base+appPortScan-1)
}

// releaseAppPort drops a start's reservation, leaving the row the start wrote as
// the only claim on the port.
func (s *Server) releaseAppPort(port int) {
	s.appPortMu.Lock()
	defer s.appPortMu.Unlock()
	delete(s.appPortHeld, port)
}

// portFree reports whether port can be bound on loopback right now.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// waitForPort blocks until the app accepts on port, ctx is done, or the readiness
// window closes, reporting whether the app came up. The log is trimmed as it goes:
// this is the one stretch where the hub is watching, and an app that floods its
// output while failing to serve is precisely the one worth bounding.
func waitForPort(ctx context.Context, port int, logPath string, window time.Duration) bool {
	deadline := time.Now().Add(window)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for {
		conn, err := net.DialTimeout("tcp", addr, appDialTimeout)
		if err == nil {
			_ = conn.Close()
			return true
		}
		capAppLog(logPath)
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(appReadyPoll):
		}
	}
}

// appEnv is the environment a worktree app starts with: the hub's own, plus the
// allocated port under both the name trau owns and the one nearly every framework
// already reads.
func appEnv(port int) []string {
	p := strconv.Itoa(port)
	return append(os.Environ(), "TRAU_APP_PORT="+p, "PORT="+p)
}

// appLogPath is where a tree's app output is captured, one file per repo and
// ticket under the hub's home.
func (s *Server) appLogPath(root, ticket string) string {
	return filepath.Join(s.home, "apps", filepath.Base(root), appLogName(ticket)+".log")
}

// appLogName makes a ticket safe to use as a filename — a tracker id is normally
// already tame, but nothing guarantees it holds no separator.
func appLogName(ticket string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, ticket)
	if safe = strings.Trim(safe, "-."); safe == "" {
		return "app"
	}
	return safe
}

// appLogTail returns the last appLogTailBytes of a captured app log, empty when
// there is nothing to read.
func appLogTail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if size == 0 {
		return ""
	}
	if size > appLogTailBytes {
		if _, err := f.Seek(size-appLogTailBytes, 0); err != nil {
			return ""
		}
	}
	buf := make([]byte, min(size, appLogTailBytes))
	n, _ := f.Read(buf)
	return strings.TrimSpace(string(buf[:n]))
}

// capAppLog trims a log file back to its tail once it clears appLogMaxBytes, so a
// chatty app cannot grow it without bound.
func capAppLog(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= appLogMaxBytes {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if err := os.WriteFile(path, data[int64(len(data))-appLogMaxBytes:], 0o644); err != nil {
		logger.Verbosef("trim the app log %s: %v", path, err)
	}
}

// worktreeAppView renders a row's app for the API. serving carries whether the
// repo serves an app at all, which no column records: it is APP_START_CMD's
// presence, and a stopped app in a repo that serves one means something quite
// different from a repo that serves none.
func worktreeAppView(row hubstore.Worktree, serving bool, output string) WorktreeAppView {
	return WorktreeAppView{
		Ticket:    row.Ticket,
		Port:      row.App.Port,
		URL:       appURLForPort(row.App),
		State:     appStateOr(row.App.State),
		PID:       row.App.PID,
		StartedAt: row.App.StartedAt,
		Output:    output,
		Serving:   serving,
	}
}

// appURLForPort is the address a running app answers on, empty unless it is
// actually up: a port recorded against a failed or stopped app is history, not a
// link anyone should follow.
func appURLForPort(app hubstore.WorktreeApp) string {
	if app.Port <= 0 || app.State != hubstore.AppRunning {
		return ""
	}
	return "http://localhost:" + strconv.Itoa(app.Port)
}

// appStateOr reads a row written before the app columns existed as stopped.
func appStateOr(state string) string {
	if state == "" {
		return hubstore.AppStopped
	}
	return state
}
