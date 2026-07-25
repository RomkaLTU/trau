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
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/webserver"
)

// hubRestartDeadline bounds the wait for the successor hub to answer health.
// It outlasts a cold boot with database migrations without leaving the user
// staring at a hung command.
const hubRestartDeadline = 15 * time.Second

// hubStopGrace bounds how long a forced restart waits for a wedged hub to exit
// on its own SIGTERM before escalating to SIGKILL.
const hubStopGrace = 5 * time.Second

func runHub(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return usageError{errors.New("hub: missing subcommand (try `trau hub restart`)")}
	}
	switch args[0] {
	case "restart":
		return runHubRestart(ctx, args[1:], stdout)
	default:
		return usageError{fmt.Errorf("hub: unknown subcommand: %s", args[0])}
	}
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
	base := hubBaseURL(cfg)
	healthURL := base + webserver.APIPrefix + "/health"

	before := probeHub(ctx, healthURL, cfg.ServeToken)
	switch {
	case before.isHub:
		if err := postHubRestart(ctx, base, cfg.ServeToken); err != nil {
			return console.Actionable(err, "ask the hub to restart", "see "+hubLogPath())
		}
	case before.reachable || portOccupied(cfg):
		if !force {
			return console.Actionable(portBusyError(cfg.ServePort), "restart the hub",
				"if that is a hub whose API has wedged, `trau hub restart --force` stops it and starts a fresh one")
		}
		if err := stopWedgedHub(ctx, cfg, stdout); err != nil {
			return console.Actionable(err, "stop the wedged hub",
				fmt.Sprintf("see what holds the port with `lsof -i tcp:%d`", cfg.ServePort))
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

func startHub(cfg config.Config) error {
	if err := webserver.CheckExposure(cfg.ServeBind, cfg.ServeToken); err != nil {
		return console.Actionable(err, "start the hub", "set SERVE_TOKEN to a secret, or keep SERVE_BIND on loopback (127.0.0.1)")
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
	if os.Getenv("TRAU_ACTIVE") == "1" {
		return console.Actionable(errors.New("this process runs inside a trau-managed run that the hub owns"),
			"force-restart the hub",
			"let the run finish; to QA hub changes now start an isolated hub: iso=$(mktemp -d) && TRAU_HOME=$iso/.trau HOME=$iso trau serve --port 8799")
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

// stopWedgedHub ends whatever holds the hub's port — SIGTERM, then SIGKILL once
// the grace passes. Only ever reached behind checkForcedRestart.
func stopWedgedHub(ctx context.Context, cfg config.Config, stdout io.Writer) error {
	pids, err := portListeners(ctx, cfg.ServePort)
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

func stopProcess(pid int) error {
	if !registry.Alive(pid) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal pid %d: %w", pid, err)
	}
	if awaitProcessGone(pid, hubStopGrace) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	if !awaitProcessGone(pid, hubStopGrace) {
		return fmt.Errorf("pid %d is still alive after a SIGKILL", pid)
	}
	return nil
}

func awaitProcessGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for registry.Alive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(hubHealthPoll)
	}
	return true
}

// portListeners reports the pids listening on port. Nothing in the standard
// library maps a port to a process, so this shells out to lsof, whose non-zero
// exit means "no match" rather than a failure worth reporting.
func portListeners(ctx context.Context, port int) ([]int, error) {
	out, err := exec.CommandContext(ctx, "lsof", "-t", "-i", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("lsof is not installed, so the process holding the port cannot be identified")
	}
	pids := make([]int, 0, 1)
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
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

func postHubRestart(ctx context.Context, base, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+webserver.APIPrefix+"/hub/restart", nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hubHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("hub answered %s", resp.Status)
	}
	return nil
}
