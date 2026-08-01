package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/launchd"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/proc"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/webserver"
)

// hubStopDeadline bounds the wait for a stopped hub to let go of the port,
// matching the grace a restart gives its successor to claim it.
const hubStopDeadline = 15 * time.Second

// loopStopGrace bounds the wait for a loop that was asked to stop: its handler
// checkpoints and unwinds in seconds, so anything past this is a run that is not
// coming down on its own.
const loopStopGrace = 30 * time.Second

// runStop ends the configured hub and leaves it stopped, which is what nothing
// else does — `trau hub restart` always starts a successor. It blocks until the
// port is actually free, so the `trau serve` the operator runs next binds it.
// --force is the escape hatch for a machine with live loops: they are stopped
// first, and only once every one is confirmed gone does the hub follow, since a
// loop with no hub can neither checkpoint nor reach its tracker.
func runStop(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var force, verbose, debug bool
	for _, a := range args {
		switch a {
		case "--force":
			force = true
		case "--verbose":
			verbose = true
		case "--debug":
			debug = true
		default:
			return usageError{fmt.Errorf("stop: unknown arg: %s", a)}
		}
	}
	logger.Init(stderr, verbose, debug)

	cfg, err := loadServeConfig("")
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	if err := checkStopGuards(); err != nil {
		return err
	}

	api := hubAPI{base: hubBaseURL(cfg), token: cfg.ServeToken}
	addr := net.JoinHostPort(webserver.DialHost(cfg.ServeBind), strconv.Itoa(cfg.ServePort))
	healthURL := api.base + webserver.APIPrefix + "/health"
	probe := probeHub(ctx, healthURL, cfg.ServeToken)

	live, err := runningLoops(ctx, api, probe.isHub)
	if err != nil {
		return console.Actionable(err, "list the live runs",
			"stopping the hub needs to see the runs it would cut off; fix the named file or stop the hub yourself")
	}
	if len(live) > 0 {
		if !force {
			return console.Actionable(fmt.Errorf("%d live run(s) would lose their data channel: %s", len(live), describeLoops(live)),
				"stop the hub", "let them finish, or `trau stop --force` to stop them first and the hub after")
		}
		if err := stopLiveLoops(ctx, api, live, probe.isHub, stdout); err != nil {
			return console.Actionable(err, "stop the live runs",
				"end them from their own terminal, then run `trau stop` again")
		}
		// A run that has to be escalated costs its full grace, so the hub found
		// before them is not necessarily the one still there afterwards.
		probe = probeHub(ctx, healthURL, cfg.ServeToken)
	}

	switch {
	case probe.isHub:
		if err := api.post(ctx, "/hub/stop"); err != nil {
			return console.Actionable(err, "ask the hub to stop", "see "+hubLogPath())
		}
		if !awaitPortFree(cfg, hubStopDeadline) {
			return console.Actionable(fmt.Errorf("the hub did not release the port within %s", hubStopDeadline),
				"stop the hub", "see what holds the port with "+proc.PortInspectHint(cfg.ServePort))
		}
		_, _ = fmt.Fprintf(stdout, "hub stopped (%s) — %s is free\n", probe.version, addr)
	case probe.reachable || portOccupied(cfg):
		if err := stopWedgedHub(ctx, cfg, stdout); err != nil {
			return console.Actionable(err, "stop the process holding the port",
				"see what holds the port with "+proc.PortInspectHint(cfg.ServePort))
		}
	default:
		_, _ = fmt.Fprintf(stdout, "no hub is running on %s\n", addr)
	}
	return nil
}

// checkStopGuards refuses the two stops that cannot leave the hub stopped: one
// asked for from inside a trau-managed run, whose data channel is the hub it
// would end, and one on a launchd-supervised machine, where KeepAlive brings
// back whatever this command stops. Neither yields to --force.
func checkStopGuards() error {
	if err := checkNotInsideARun("stop the hub"); err != nil {
		return err
	}
	st, err := launchd.Read()
	if err != nil {
		return console.Actionable(err, "read the LaunchAgent", "check ~/Library/LaunchAgents")
	}
	if st.Installed {
		return console.Actionable(errors.New("launchd supervises this hub, and KeepAlive restarts whatever this command stops"),
			"stop the hub", "run `trau hub unsupervise`, which stops the hub as it releases the agent")
	}
	return nil
}

// runningLoops enumerates the loops a stop would cut off: the hub's own presence
// list when it still answers, and the read-only hub database when it does not —
// a wedged hub has stopped answering, not stopped owning runs.
func runningLoops(ctx context.Context, api hubAPI, healthy bool) ([]registry.Entry, error) {
	if !healthy {
		return liveLoops()
	}
	instances, err := hubclient.New(api.base, api.token).Instances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]registry.Entry, 0, len(instances))
	for _, in := range instances {
		out = append(out, registry.Entry{PID: in.PID, RepoRoot: in.RepoRoot, Ticket: in.Ticket})
	}
	return out, nil
}

// stopLiveLoops ends every live loop before the hub goes down, each one confirmed
// gone before the next is asked.
func stopLiveLoops(ctx context.Context, api hubAPI, live []registry.Entry, healthy bool, stdout io.Writer) error {
	for _, e := range live {
		askLoopToStop(ctx, api, e, healthy)
		if err := awaitLoopGone(e.PID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "stopped loop %s (pid %d) in %s\n", e.Ticket, e.PID, repoName(e.RepoRoot))
	}
	return nil
}

// askLoopToStop asks one run to checkpoint and exit. A hub that still answers is
// asked through the repo's queue, the path the web UI's Stop uses: that disarms
// the drain synchronously, so no drainer tick spawns a replacement child while
// the hub is on its way down, and parks the running item at its checkpoint with
// everything else left queued and resumable. A second loop in the same repo
// re-POSTs it, which the hub answers as the no-op it is. A repo with no queue
// child of its own answers 200, and an observe-only one is not on the workspace
// allowlist and answers 403 — either way the run is asked by pid instead, and it
// parks the same way with no queue to disarm. A wedged hub can route neither, so
// the ask goes straight to the process, whose own handler checkpoints on it
// exactly as it does on a hub-routed stop.
//
// Every ask is best-effort, because awaitLoopGone is what actually ends the run:
// a graceful stop the hub took and could not deliver — a Windows loop in a
// console no other process can signal (ADR 0023 §7) — is left to that escalation
// instead of failing the command with both the loop and the hub still up.
func askLoopToStop(ctx context.Context, api hubAPI, e registry.Entry, healthy bool) {
	if !healthy {
		if err := proc.StopGracefully(e.PID); err != nil {
			logger.Verbosef("graceful stop of pid %d failed, leaving it to the escalation: %v", e.PID, err)
		}
		return
	}
	repo := repoName(e.RepoRoot)
	err := api.post(ctx, "/repos/"+repo+"/queue/stop")
	if err == nil {
		return
	}
	if !hubDeclined(err) {
		logger.Verbosef("queue stop for %s failed, leaving pid %d to the escalation: %v", repo, e.PID, err)
		return
	}
	logger.Verbosef("no queue stop routed for %s, falling back to a per-loop stop: %v", repo, err)
	if err := api.post(ctx, "/instances/"+strconv.Itoa(e.PID)+"/stop"); err != nil {
		logger.Verbosef("per-loop stop of pid %d failed, leaving it to the escalation: %v", e.PID, err)
	}
}

// awaitLoopGone confirms pid is gone, escalating once the grace passes: a loop
// that outlives its own signal handler gets a group kill, and one that outlives
// that fails the command rather than letting the hub go down over it.
func awaitLoopGone(pid int) error {
	if awaitProcessGone(pid, loopStopGrace) {
		return nil
	}
	logger.Verbosef("pid %d outlived the %s stop grace, escalating", pid, loopStopGrace)
	if err := proc.KillGroup(pid); err != nil {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	if !awaitProcessGone(pid, hubStopGrace) {
		return fmt.Errorf("pid %d is still alive after a group kill", pid)
	}
	return nil
}
