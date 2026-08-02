package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/launchd"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/proc"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/transcriptdb"
	"github.com/RomkaLTU/trau/internal/update"
	"github.com/RomkaLTU/trau/internal/webserver"
)

// hubRestartDeadline bounds the wait for the successor hub to answer health.
// It outlasts a cold boot with database migrations without leaving the user
// staring at a hung command.
const hubRestartDeadline = 15 * time.Second

// hubStopGrace bounds how long a forced restart waits for a wedged hub to exit
// on its own graceful stop before escalating to a kill.
const hubStopGrace = 5 * time.Second

func runHub(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return usageError{errors.New("hub: missing subcommand (try `trau hub restart`)")}
	}
	switch args[0] {
	case "restart":
		return runHubRestart(ctx, args[1:], stdout)
	case "supervise":
		return runHubSupervise(ctx, args[1:], stdout)
	case "unsupervise":
		return runHubUnsupervise(args[1:], stdout)
	case "preflight":
		return runHubPreflight(args[1:], stdout)
	default:
		return usageError{fmt.Errorf("hub: unknown subcommand: %s", args[0])}
	}
}

// runHubSupervise hands the hub to launchd with KeepAlive, so a crashed or killed
// one comes back in seconds instead of waiting for the next interactive session
// (ADR 0004) — nothing else brings it back, since loop children deliberately
// never resurrect it (ADR 0008). Whatever already runs the hub — an agent from an
// earlier run, or a bare process holding the port — is stopped first, behind the
// same guards a forced restart uses, so launchd's copy is the one that binds it.
// Re-running adopts a moved binary or a changed PATH: the agent in place is
// released before the port is looked at, so only a process launchd does not own is
// ever displaced.
func runHubSupervise(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return usageError{fmt.Errorf("hub supervise: unknown arg: %s", args[0])}
	}
	if !launchd.Supported() {
		return console.Actionable(launchd.ErrUnsupported, "supervise the hub",
			"keep `trau serve` up with this machine's init system (a systemd user unit, say), or rely on SERVE_AUTOSTART")
	}
	cfg, err := loadServeConfig("")
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	if err := webserver.CheckExposure(cfg.ServeBind, cfg.ServeToken); err != nil {
		return console.Actionable(err, "supervise the hub", "set SERVE_TOKEN to a secret, or keep SERVE_BIND on loopback (127.0.0.1)")
	}
	exe, err := update.ResolveBinary()
	if err != nil {
		return console.Actionable(err, "resolve the trau binary", "install trau so `trau` resolves on PATH")
	}
	st, err := launchd.Read()
	if err != nil {
		return console.Actionable(err, "read the LaunchAgent", "check ~/Library/LaunchAgents")
	}
	// Releasing the agent stops the hub under it just as surely as killing the
	// process holding the port does, so both go behind the guards; a first install
	// on a machine with nothing running passes them by.
	if st.Installed || portOccupied(cfg) {
		if err := checkForcedRestart(); err != nil {
			return err
		}
	}
	// A machine already supervised gives its agent up first: launchd's KeepAlive
	// would re-bind the port under any hub stopped beneath it, and the plist this
	// re-run exists to rewrite cannot be replaced while the old job holds it.
	if st.Installed {
		if err := launchd.Uninstall(); err != nil {
			return console.Actionable(err, "release the installed LaunchAgent", "check ~/Library/LaunchAgents")
		}
		awaitPortFree(cfg, hubStopGrace)
	}
	if portOccupied(cfg) {
		if err := stopWedgedHub(ctx, cfg, stdout); err != nil {
			return console.Actionable(err, "stop the unsupervised hub",
				"see what holds the port with "+proc.PortInspectHint(cfg.ServePort))
		}
	}
	if err := launchd.Install(exe, []string{"serve"}, superviseEnv(), hubLogPath()); err != nil {
		return console.Actionable(err, "supervise the hub", "see "+hubLogPath())
	}
	healthURL := hubBaseURL(cfg) + webserver.APIPrefix + "/health"
	after, ok := awaitHub(ctx, healthURL, cfg.ServeToken, hubRestartDeadline, anyHub)
	if !ok {
		return console.Actionable(fmt.Errorf("the supervised hub did not come up within %s", hubRestartDeadline),
			"supervise the hub", "see "+hubLogPath())
	}
	_, _ = fmt.Fprintf(stdout, "hub supervised by launchd (%s), running %s — it now restarts on its own\n", launchd.Label, after.version)
	return nil
}

// runHubUnsupervise gives the hub back to the user. launchd stops the job as it
// releases it, so the hub goes down with the agent rather than lingering under
// nobody's supervision.
func runHubUnsupervise(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return usageError{fmt.Errorf("hub unsupervise: unknown arg: %s", args[0])}
	}
	st, err := launchd.Read()
	if err != nil {
		return console.Actionable(err, "read the LaunchAgent", "check ~/Library/LaunchAgents")
	}
	if !st.Installed {
		_, _ = fmt.Fprintln(stdout, "the hub is not supervised; nothing to remove")
		return nil
	}
	if err := launchd.Uninstall(); err != nil {
		return console.Actionable(err, "remove the hub's LaunchAgent", "check ~/Library/LaunchAgents")
	}
	_, _ = fmt.Fprintf(stdout, "hub supervision removed (%s); the hub stopped with it — `trau serve` or the next interactive session brings it back\n", launchd.Label)
	return nil
}

// runHubPreflight does the startup work `trau serve` can still fail at once its
// arguments parse — opening and migrating both hub databases — and then exits
// without binding a port or starting anything. That makes it what a hub asks a
// candidate build before it restarts onto it (ADR 0024 §2): a binary whose
// migrations collide, or whose schema will not apply, says so here while the hub
// that asked is still serving, instead of dying alone as the successor.
//
// The databases are the machine's real ones, opened against the same trau home
// the successor would use. Migrations are forward-only and additive, so applying
// them from the candidate is the same work adopting it would do a moment later.
func runHubPreflight(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return usageError{fmt.Errorf("hub preflight: unknown arg: %s", args[0])}
	}
	home := registry.Home()
	db, err := hubdb.Open(home)
	if err != nil {
		return console.Actionable(err, "open hub database",
			"try a build that can open it before moving "+hubdb.Path(home)+" aside — it holds every run recorded here")
	}

	tdb, err := transcriptdb.Open(home)
	if err != nil {
		_ = db.Close()
		return console.Actionable(err, "open transcript database",
			fmt.Sprintf("delete %s and restart to recreate it empty (it holds only transcripts)", transcriptdb.Path(home)))
	}

	// The close carries the WAL checkpoint, so a preflight that reported success
	// through a failed one would have proved nothing about the write path.
	hubVersion, transcriptVersion := db.Version(), tdb.Version()
	if err := errors.Join(db.Close(), tdb.Close()); err != nil {
		return console.Actionable(err, "close the hub databases",
			"check free space and write permissions under "+home)
	}

	_, _ = fmt.Fprintf(stdout, "trau %s can serve: hub schema v%d, transcripts v%d\n", version, hubVersion, transcriptVersion)
	return nil
}

// superviseEnv is the environment launchd starts the hub with. An agent inherits
// almost nothing, so the PATH that resolves git, gh, and the provider CLIs for
// every loop the hub spawns is captured at install time. TRAU_SUPERVISED tells
// the hub that launchd owns it, so a self-restart exits and lets KeepAlive bring
// the successor up instead of racing it for the port.
func superviseEnv() map[string]string {
	env := map[string]string{"TRAU_SUPERVISED": "1"}
	for _, key := range []string{"PATH", "TRAU_HOME"} {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}
	return env
}

// runHubRestart makes the configured hub current: a running one is asked to
// respawn itself from the on-disk binary, and a missing one is simply started,
// since a fresh hub already runs that binary. --force is the escape hatch for a
// hub whose API no longer answers — it stops the process holding the port and
// starts a fresh one, which only the guards in checkForcedRestart make safe.
func runHubRestart(ctx context.Context, args []string, stdout io.Writer) error {
	force := false
	for _, a := range args {
		if a != "--force" {
			return usageError{fmt.Errorf("hub restart: unknown arg: %s", a)}
		}
		force = true
	}
	cfg, err := loadServeConfig("")
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	if force {
		if err := checkForcedRestart(); err != nil {
			return err
		}
	}
	api := hubAPI{base: hubBaseURL(cfg), token: cfg.ServeToken}
	healthURL := api.base + webserver.APIPrefix + "/health"

	before := probeHub(ctx, healthURL, cfg.ServeToken)
	switch {
	case before.isHub:
		if err := api.post(ctx, "/hub/restart"); err != nil {
			return console.Actionable(err, "ask the hub to restart", "see "+hubLogPath())
		}
	case before.reachable || portOccupied(cfg):
		if !force {
			return console.Actionable(portBusyError(cfg.ServePort), "restart the hub",
				"if that is a hub whose API has wedged, `trau hub restart --force` stops it and starts a fresh one")
		}
		if err := stopWedgedHub(ctx, cfg, stdout); err != nil {
			return console.Actionable(err, "stop the wedged hub",
				"see what holds the port with "+proc.PortInspectHint(cfg.ServePort))
		}
		if err := startHub(cfg); err != nil {
			return err
		}
	default:
		if err := startHub(cfg); err != nil {
			return err
		}
	}

	// The outgoing hub keeps answering health while it drains, so a successor is
	// recognised by its shorter uptime rather than by merely being healthy.
	fresh := func(p hubStatus) bool { return p.uptime < before.uptime }
	if !before.isHub {
		fresh = anyHub
	}
	after, ok := awaitHub(ctx, healthURL, cfg.ServeToken, hubRestartDeadline, fresh)
	if !ok {
		return console.Actionable(fmt.Errorf("the hub did not come back within %s", hubRestartDeadline),
			"restart the hub", "see "+hubLogPath())
	}

	switch {
	case !before.isHub:
		_, _ = fmt.Fprintf(stdout, "hub started (%s)\n", after.version)
	case after.version == before.version:
		_, _ = fmt.Fprintf(stdout, "hub restarted (%s)\n", after.version)
	default:
		_, _ = fmt.Fprintf(stdout, "hub restarted: %s -> %s\n", before.version, after.version)
	}
	return nil
}

// startHub brings a stopped hub up: through launchd on a supervised machine, so
// the process that binds the port is the one KeepAlive owns, and as a detached
// child otherwise (ADR 0004).
func startHub(cfg config.Config) error {
	if err := webserver.CheckExposure(cfg.ServeBind, cfg.ServeToken); err != nil {
		return console.Actionable(err, "start the hub", "set SERVE_TOKEN to a secret, or keep SERVE_BIND on loopback (127.0.0.1)")
	}
	if st, err := launchd.Read(); err == nil && st.Installed {
		if err := launchd.Kickstart(); err != nil {
			return console.Actionable(err, "start the supervised hub",
				"inspect the agent with `launchctl print gui/$(id -u)/"+launchd.Label+"`, or run `trau hub unsupervise`")
		}
		return nil
	}
	if err := spawnDetachedServe(); err != nil {
		return console.Actionable(err, "start the hub", "install trau so `trau` resolves on PATH")
	}
	return nil
}

// checkForcedRestart refuses the two situations where killing the hub destroys
// work instead of recovering it: a caller running inside a trau-managed run,
// whose data channel is the hub it would kill, and a machine with live loops.
func checkForcedRestart() error {
	if err := checkNotInsideARun("force-restart the hub"); err != nil {
		return err
	}
	live, err := liveLoops()
	if err != nil {
		return console.Actionable(err, "read the hub database",
			"a forced restart needs to see the live runs it would cut off; fix the named file or stop the hub yourself")
	}
	if len(live) > 0 {
		return console.Actionable(fmt.Errorf("%d live run(s) would lose their data channel: %s", len(live), describeLoops(live)),
			"force-restart the hub", "stop them first (Ctrl-C in their terminal, or Stop in the web UI)")
	}
	return nil
}

// checkNotInsideARun refuses action when the caller is itself a trau-managed
// run, whose data channel is the hub the action would take down. It is the one
// refusal that never yields to --force, shared by the stop and the forced
// restart so the isolated-hub escape hatch it points at cannot drift between
// them.
func checkNotInsideARun(action string) error {
	if os.Getenv("TRAU_ACTIVE") != "1" {
		return nil
	}
	return console.Actionable(errors.New("this process runs inside a trau-managed run that the hub owns"),
		action,
		"let the run finish; to QA hub changes now start an isolated hub: iso=$(mktemp -d) && TRAU_HOME=$iso/.trau HOME=$iso trau serve --port 8799")
}

// liveLoops reads presence straight from the hub database, the only way to see
// it once the API a forced restart is about to kill has stopped answering.
func liveLoops() ([]registry.Entry, error) {
	db, err := hubdb.OpenReadOnly(registry.Home())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return hubstore.NewInstances(db).Live()
}

// registeredRoots reads the registered set straight from the hub database, the
// tie-breaker cwd resolution needs between a Child repo and the Folder repo that
// holds it. A machine whose hub has never run has none.
func registeredRoots() ([]string, error) {
	db, err := hubdb.OpenReadOnly(registry.Home())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return hubstore.NewRegistrations(db).Registered()
}

// resolveRepoRoot is the production binding of the repo resolver: the current
// directory's git top-level, the Folder repo above it, and the registered set
// that decides between them.
func resolveRepoRoot(flagRepo, envRoot string) (string, error) {
	return config.ResolveRepoRoot(flagRepo, envRoot, config.GitToplevel, registeredRoots)
}

func describeLoops(live []registry.Entry) string {
	out := make([]string, 0, len(live))
	for _, e := range live {
		desc := fmt.Sprintf("pid %d in %s", e.PID, e.RepoRoot)
		if e.Ticket != "" {
			desc += " (" + e.Ticket + ")"
		}
		out = append(out, desc)
	}
	return strings.Join(out, ", ")
}

// stopWedgedHub ends whatever holds the hub's port — a graceful stop, then a
// kill once the grace passes. Only ever reached behind checkForcedRestart.
func stopWedgedHub(ctx context.Context, cfg config.Config, stdout io.Writer) error {
	pids, err := proc.PortListeners(ctx, cfg.ServePort)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if err := stopProcess(pid); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "stopped the process holding :%d (pid %d)\n", cfg.ServePort, pid)
	}
	if portOccupied(cfg) {
		return fmt.Errorf("port %d is still held after stopping %d process(es)", cfg.ServePort, len(pids))
	}
	return nil
}

// stopProcess ends pid with the same escalation the hub's own Stop routes use:
// a graceful stop, then a group kill once the grace passes. A graceful stop that
// cannot even be delivered escalates straight to the kill rather than failing —
// a detached hub owns no console to take a Ctrl-Break on Windows, and displacing
// a hub that will not step aside is the whole point of a forced restart.
func stopProcess(pid int) error {
	if !registry.Alive(pid) {
		return nil
	}
	if err := proc.StopGracefully(pid); err != nil {
		logger.Verbosef("graceful stop of pid %d failed, escalating: %v", pid, err)
	} else if awaitProcessGone(pid, hubStopGrace) {
		return nil
	}
	if err := proc.KillGroup(pid); err != nil {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	if !awaitProcessGone(pid, hubStopGrace) {
		return fmt.Errorf("pid %d is still alive after a group kill", pid)
	}
	return nil
}

// awaitPortFree gives a hub that was just stopped the grace to release the port,
// so the process launchd is letting go of is not mistaken for a foreign one, and
// reports whether it let go within it.
func awaitPortFree(cfg config.Config, within time.Duration) bool {
	return awaitFalse(func() bool { return portOccupied(cfg) }, within)
}

func awaitProcessGone(pid int, within time.Duration) bool {
	return awaitFalse(func() bool { return registry.Alive(pid) }, within)
}

// awaitFalse polls pred until it stops reporting true, and reports whether it
// did so within the grace.
func awaitFalse(pred func() bool, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for pred() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(hubHealthPoll)
	}
	return true
}

// portOccupied reports whether something already holds the hub's port. The
// health probe alone cannot tell a silent listener — one that accepts and then
// never answers — apart from nothing listening at all, and spawning into that
// port only produces a bind failure the caller never sees.
func portOccupied(cfg config.Config) bool {
	addr := net.JoinHostPort(webserver.DialHost(cfg.ServeBind), strconv.Itoa(cfg.ServePort))
	conn, err := net.DialTimeout("tcp", addr, hubProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// hubAPI reaches the hub's own control routes — restart, stop, the queue and
// per-loop stops — over raw HTTP: hubclient is the loop's client, and no loop
// ever calls these.
type hubAPI struct {
	base  string
	token string
}

// post asks the hub for one control action. Only an accepted request counts:
// every one of these routes answers 202 when it took the action, and anything
// else comes back as a hubStatusError carrying the code the caller branches on.
func (h hubAPI) post(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+webserver.APIPrefix+path, nil)
	if err != nil {
		return err
	}
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := hubHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return hubStatusError{code: resp.StatusCode, status: resp.Status}
	}
	return nil
}

// hubStatusError is a control route's answer to a request it did not accept.
type hubStatusError struct {
	code   int
	status string
}

func (e hubStatusError) Error() string { return "hub answered " + e.status }

// hubDeclined reports whether err is a hub answer that never attempted the
// action — a queue stop with no child of its own (200), an observe-only repo
// (403) — which is what makes a second route worth trying. A 5xx or a dead
// connection means the ask was spent on the attempt, and re-asking repeats it.
func hubDeclined(err error) bool {
	var answer hubStatusError
	return errors.As(err, &answer) && answer.code < http.StatusInternalServerError
}
