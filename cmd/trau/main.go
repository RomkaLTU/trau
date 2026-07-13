// Command trau is the Go reimplementation of the Trau loop (v2): an
// autonomous harness that pulls the next ready Linear ticket and drives it
// through the fixed pipeline — build → handoff → verify → commit → PR →
// CI → merge → Done — one ticket per iteration, running each phase as a
// fresh, isolated headless-agent process.
//
// The CLI dispatches: --version/--status/--reset early exits, --dry-run, and
// otherwise the resumable main loop (runLoop) over a fully-wired pipeline.Pipeline
// — resume-first, else clean-base + Linear pick, one ticket per
// iteration. The target app repo is resolved per ADR 0001 §2 (--repo / TRAU_REPO_ROOT
// / cwd git top-level). docs/adr/0001 records the layout.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/budget"
	"github.com/RomkaLTU/trau/internal/checks"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/doctor"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubartifact"
	"github.com/RomkaLTU/trau/internal/hubcheckpoint"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/hubevent"
	"github.com/RomkaLTU/trau/internal/hublesson"
	"github.com/RomkaLTU/trau/internal/hubphaselog"
	"github.com/RomkaLTU/trau/internal/hubpresence"
	"github.com/RomkaLTU/trau/internal/hubtokens"
	"github.com/RomkaLTU/trau/internal/hubtranscript"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/pipeline"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tui"
	"github.com/RomkaLTU/trau/internal/usage/probe"
)

var version = "dev"

// usagePoller builds the HUD's provider usage-window poller from config, or nil
// when the feature is off or the provider exposes no window. The caller runs it on
// a context bounded by the loop's lifetime; every probe is metadata-only.
func usagePoller(cfg config.Config, log *event.Log) *probe.Poller {
	return probe.NewPoller(probe.Options{
		Provider:   cfg.Provider,
		Enabled:    cfg.UsageWindow,
		PTY:        cfg.UsageWindowPTY,
		ClaudeBin:  cfg.ClaudeBin,
		CodexBin:   cfg.CodexBin,
		KimiBin:    cfg.KimiBin,
		KimiAPIKey: os.Getenv("KIMI_API_KEY"),
	}, log)
}

const usage = `trau — autonomous Linear-ticket dev loop

Usage:
  trau [flags]               run the loop: resume any in-flight ticket, else pick the next ready one
  trau <ID>                  run a single ticket (e.g. ENG-123), or its sub-issues if it is an epic
  trau doctor                preflight check: git/gh/provider/config/labels/write perms
  trau watch                 tail a running loop's live agent activity (headless counterpart to the TUI 'w' key)
  trau forensics <cmd>       read-only incident queries over the run history: runs, events, spend (see 'trau forensics --help')
  trau serve                 start the local web hub — HTTP API + embedded UI on 127.0.0.1:8728 (--bind, --port)
  trau --status [--json]     show saved ticket checkpoints with token/cost totals
  trau --dry-run             print the next eligible ticket without doing any work
  trau --list-eligible [--json]  list the repo's eligible ready tickets (ID, title, labels)
  trau --list-epic <ID> [--json]  list an epic's sub-issues and their states (ID, title, state)
  trau --reset <ID>          drop the branch + state and re-queue the ticket (refuses if already merged; --force overrides)
  trau --clear <ID>          drop only the local checkpoint (no git, no re-queue) — for tickets finished out-of-band

Flags:
  --parent <ID>     treat <ID> as an epic and process its sub-issues (a bare <PREFIX>-<n> arg is equivalent)
  --once            stop after one ticket
  --max <N>         cap iterations for this run (overrides MAX_ITERATIONS)
  --no-resume       skip the resume scan; always pick a fresh ticket
  --provider <name> override the configured provider (claude | codex | kimi)
  --repo <path>     target app repo (else TRAU_REPO_ROOT, else the cwd git top-level)
  --dry-run         print the next eligible ticket and exit
  --list-eligible   list the eligible ready tickets and exit (--json for machine-readable output)
  --list-epic <ID>  list an epic's sub-issues and their states and exit (--json for machine-readable output)
  --reset <ID>      reset a ticket and exit
  --clear <ID>      drop a ticket's local checkpoint without touching git or the tracker (a.k.a. --forget)
  --force           with --reset, reset even a ticket whose code is already merged
  --status          print saved checkpoints (auto-reconciles stale in-flight/quarantined rows against the tracker) and exit
  --json            emit --status, --list-eligible, or --list-epic as machine-readable JSON
  --no-tui          force plain console output (disable the Bubble Tea TUI)
  --verbose         extra stderr diagnostics (what the loop is doing)
  --debug           very verbose stderr diagnostics, incl. git/gh commands invoked
  --yes             no-op, kept for backward compatibility
  -v, --version     print version and exit
  -h, --help        show this help and exit

Configuration is layered (lowest to highest precedence):
  defaults < ./trau.ini < <repo>/.trau.ini < ~/.trau.ini < environment < flags
See trau.ini.example for every documented knob.
`

type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// silentExit carries an exit code for a command that has already written its own
// diagnostics (e.g. `trau doctor`), so main exits non-zero without printing an
// extra wrapper line.
type silentExit struct{ code int }

func (silentExit) Error() string { return "" }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	// A real OS signal (Ctrl-C / SIGTERM) exits 130 (128+SIGINT) per Unix
	// convention, whether run returned nil after noticing the cancellation or
	// surfaced it as an error from an agent call aborted mid-phase (e.g. the
	// picker or EnsureCleanBase). Only a real signal cancels this parent
	// context; the TUI quit key cancels a separate child context and exits 0.
	if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
		os.Exit(130)
	}
	if err == nil {
		return
	}
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var se silentExit
	if errors.As(err, &se) {
		os.Exit(se.code)
	}
	fmt.Fprintln(os.Stderr, "trau:", console.FormatActionable(err))
	fmt.Fprintln(os.Stderr, "  → run `trau doctor` to check your setup, or add `--verbose` / `--debug` for more detail")
	os.Exit(1)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	// forensics owns its subcommand args (including --help), so it is dispatched
	// before the loop's global --version/--help scan claims them.
	if len(args) > 0 && args[0] == "forensics" {
		return runForensics(ctx, args[1:], stdout, stderr)
	}

	for _, a := range args {
		switch a {
		case "--version", "-v":
			_, _ = fmt.Fprintf(stdout, "trau %s\n", version)
			return nil
		case "--help", "-h":
			_, _ = fmt.Fprint(stdout, usage)
			return nil
		}
	}

	if len(args) > 0 && args[0] == "doctor" {
		return runDoctor(ctx, args[1:], stderr)
	}

	if len(args) > 0 && args[0] == "watch" {
		return runWatch(ctx, args[1:], stdout, stderr)
	}

	if len(args) > 0 && args[0] == "serve" {
		return runServe(ctx, args[1:], stderr)
	}

	opts, err := config.ParseArgs(args)
	if err != nil {
		return usageError{err}
	}

	logger.Init(stderr, opts.Verbose, opts.Debug)
	logger.Verbosef("trau %s starting (verbose=%v debug=%v)", version, opts.Verbose, opts.Debug)

	if os.Getenv("TRAU_ACTIVE") == "1" && os.Getenv("TRAU_ALLOW_NESTED") != "1" {
		return fmt.Errorf("refusing to start a nested Trau loop (a controller is already active; set TRAU_ALLOW_NESTED=1 to override)")
	}
	_ = os.Setenv("TRAU_ACTIVE", "1")

	ctx, cancelLoop := context.WithCancel(ctx)
	defer cancelLoop()

	repoRoot, rrErr := config.ResolveRepoRoot(opts.Repo, os.Getenv("TRAU_REPO_ROOT"), config.GitToplevel)
	if rrErr != nil {
		logger.Verbosef("repo root resolution: %v", rrErr)
	} else {
		logger.Verbosef("resolved repo root: %s", repoRoot)
	}

	projectEnv := config.ProjectConfigPath(repoRoot)
	userEnv := ""
	if home, herr := os.UserHomeDir(); herr == nil {
		userEnv = config.ProjectConfigPath(home)
	}

	cfg, err := config.LoadLayered(projectEnv, userEnv, config.LocalConfigPath(), opts.Provider)
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	logger.Verbosef("loaded config: provider=%s tracker=%s team=%q prefix=%s base=%s", cfg.Provider, cfg.TrackerProvider, cfg.LinearTeam, cfg.IssuePrefix, cfg.BaseBranch)
	if opts.Repo != "" || os.Getenv("TRAU_REPO_ROOT") != "" {
		cfg.RepoRoot = repoRoot
	} else if cfg.RepoRoot == "" {
		cfg.RepoRoot = repoRoot
	}

	if cfg.EffectiveTrackerProvider() == "internal" {
		// Internal issue IDs are minted with the repo-derived InternalPrefix, not the
		// COD fallback ResolvePrefix lands on with no team; align id parsing, branch
		// inference, and scope so the CLI and resume paths recognize them.
		cfg.IssuePrefix = config.InternalPrefix(cfg.IssuePrefixConfigured, repoName(cfg.RepoRoot))
	}

	if note := tui.SetTheme(cfg.Theme, cfg.ThemeColors); note != "" {
		_, _ = fmt.Fprintln(stderr, note)
	}

	if cfg.RunsDir == ".trau/runs" {
		if _, err := os.Stat("runs"); err == nil {
			if legacyRunsTracked() {
				logger.Verbosef("legacy runs/ is git-tracked — skipping auto-migration; move it to .trau/runs manually")
			} else if moved, merr := state.MigrateLegacyRunsDir("runs", cfg.RunsDir); merr != nil {
				logger.Verbosef("migrate legacy runs/: %v", merr)
			} else if moved {
				logger.Verbosef("migrated legacy runs/ → %s", cfg.RunsDir)
			}
		}
	}

	if err := state.EnsureGitignore(cfg.RepoRoot, cfg.RunsDir); err != nil {
		logger.Verbosef("ensure runs .gitignore: %v", err)
	}

	for _, id := range []string{opts.Parent, opts.ResetID, opts.ClearID} {
		if err := config.ValidatePrefix(id, cfg.IssuePrefix); err != nil {
			return console.Actionable(err, "validate ticket id",
				fmt.Sprintf("set ISSUE_PREFIX (or LINEAR_TEAM) to this tracker's key, or pass a %s-<n> ticket", cfg.IssuePrefix))
		}
	}

	cfg.LiveView = !opts.Status &&
		os.Getenv("TRAU_LOG_JSON") != "1" &&
		cfg.TUI && !opts.NoTUI &&
		console.IsTerminal(stdout)

	if menuOnlyArgs(args) && cfg.TUI && !opts.NoTUI && console.IsTerminal(stdout) && os.Getenv("TRAU_LOG_JSON") != "1" {
		return runSession(ctx, cfg, opts, stdout, stderr)
	}

	con := newRenderer(stdout, stderr, cfg, opts, cancelLoop)

	log, flushEvents := newEventLog(cfg, stderr, con.Event)
	defer flushEvents()

	sink := newTokenSink(cfg)
	defer sink.Close()

	transcripts := newTranscriptSink(cfg)
	defer transcripts.Close()
	wireTUITranscripts(cfg)

	if opts.ClearID != "" {
		ensureHubForStore(ctx, cfg, stderr)
		store := newCheckpointStore(cfg, cfg.RepoRoot)
		was := store.Get(opts.ClearID, "PHASE")
		if err := store.RemoveState(opts.ClearID); err != nil {
			return console.Actionable(err, "clear "+opts.ClearID, "check the web hub is reachable")
		}
		if err := newArtifactStore(cfg, cfg.RepoRoot).Remove(opts.ClearID); err != nil {
			return console.Actionable(err, "clear "+opts.ClearID, "check the web hub is reachable")
		}
		if err := newPhaseLogStore(cfg, cfg.RepoRoot).Remove(opts.ClearID); err != nil {
			return console.Actionable(err, "clear "+opts.ClearID, "check the web hub is reachable")
		}
		if was == "" {
			con.Logf("No saved checkpoint for %s — nothing to clear.", opts.ClearID)
		} else {
			con.Logf("Cleared %s local checkpoint (was %s). Branch and tracker were left untouched.", opts.ClearID, was)
		}
		return nil
	}

	if opts.Status {
		ensureHubForStore(ctx, cfg, stderr)
		store := newCheckpointStore(cfg, cfg.RepoRoot)
		reconciled := reconcileCheckpoints(ctx, cfg, log, sink, store)
		var report any
		if lim := budgetLimits(cfg); lim.Enabled() {
			today := time.Now().Format("2006-01-02")
			dt, dc, dm := sink.DayTotal(today)
			report = budget.Report{Date: today, Limits: lim, Today: budget.Spend{Tokens: dt, Cost: dc, Metered: dm}}
		}
		if opts.JSON {
			return state.WriteStatusJSON(stdout, store, sink.Total, report, reconciledIDs(reconciled))
		}
		for _, rt := range reconciled {
			con.Logf("↳ %s is %s on the tracker — cleared stale %s checkpoint", rt.ID, rt.Status, rt.Phase)
		}
		con.Logf("Saved ticket checkpoints:")
		state.WriteStatus(stdout, store, cfg.RunsDir, sink.Total)
		if r, ok := report.(budget.Report); ok {
			_, _ = fmt.Fprintf(stdout, "  %s\n", r.Summary())
		}
		return nil
	}

	runner, err := buildRouter(cfg, log, sink, transcripts, stderr)
	if err != nil {
		return usageError{err}
	}

	pm, err := buildTracker(cfg, runner)
	if err != nil {
		return usageError{err}
	}
	var reg *hubpresence.Handle
	if usesHubStore(cfg) {
		ensureHubForStore(ctx, cfg, stderr)
		// Register before the standalone hub-backed commands (--list-eligible,
		// --dry-run, --reset) so the hub can resolve this repo, the same way the loop
		// path does before Pick. The main loop reuses this handle below.
		reg = newPresence(cfg, cfg.RepoRoot)
		defer reg.Deregister()
	}

	if opts.ListEligible {
		lister, ok := pm.(tracker.TicketLister)
		if !ok {
			return console.Actionable(
				fmt.Errorf("tracker %q cannot list eligible tickets", cfg.TrackerProvider),
				"list eligible tickets",
				"use Linear (with LINEAR_API_KEY) or Jira with REST credentials")
		}
		listCtx, cancel := context.WithTimeout(ctx, listEligibleTimeout)
		defer cancel()
		tickets, err := lister.ListEligible(listCtx, scopeFor(cfg, opts.Parent))
		if err != nil {
			return fmt.Errorf("list eligible tickets: %w", err)
		}
		if opts.JSON {
			return writeEligibleJSON(stdout, tickets)
		}
		if len(tickets) == 0 {
			con.Logf("Nothing eligible right now.")
			return nil
		}
		for _, line := range groupEligibleLines(tickets) {
			con.Logf("%s", line)
		}
		return nil
	}

	if opts.ListEpicID != "" {
		listCtx, cancel := context.WithTimeout(ctx, listEpicTimeout)
		defer cancel()
		subs, err := pm.SubIssues(listCtx, opts.ListEpicID)
		if err != nil {
			return fmt.Errorf("list sub-issues for %s: %w", opts.ListEpicID, err)
		}
		if opts.JSON {
			return writeEpicSubIssuesJSON(stdout, subs)
		}
		if len(subs) == 0 {
			con.Logf("%s has no sub-issues.", opts.ListEpicID)
			return nil
		}
		for _, s := range subs {
			con.Logf("%s  %s  [%s]", s.ID, s.Title, subIssueState(s))
		}
		return nil
	}

	if opts.ResetID != "" {
		repoRoot, err := config.ResolveRepoRoot(opts.Repo, cfg.RepoRoot, config.GitToplevel)
		if err != nil {
			return console.Actionable(err, "resolve target repo", "pass --repo <path>, set TRAU_REPO_ROOT, or run inside a git repository")
		}
		ensureHubForStore(ctx, cfg, stderr)
		pipe, err := buildPipeline(cfg, runner, repoRoot, pm, sink, transcripts, log, con)
		if err != nil {
			return err
		}
		if phase := pipe.State.Get(opts.ResetID, "PHASE"); phase == state.Merged && !opts.Force {
			return console.Actionable(
				fmt.Errorf("%s is already shipped (phase: %s)", opts.ResetID, phase),
				"reset "+opts.ResetID,
				"its code is already merged — pass --force to reset it anyway")
		}
		return pipe.Reset(ctx, opts.ResetID)
	}

	epicID := opts.Parent
	forcedID := ""
	if epicID != "" {
		if cfg.EpicFlow {
			subs, err := pm.SubIssues(ctx, epicID)
			if err != nil {
				return fmt.Errorf("check sub-issues for %s: %w", epicID, err)
			}
			if len(leafSubs(subs)) == 0 {
				// epicID is itself a leaf, not an epic. Under epic flow a leaf that
				// belongs to an epic is forced onto that epic's branch (stacking with
				// its siblings); a leaf with no parent is built standalone off the base.
				forcedID = epicID
				opts.Once = true
				if parent := parentEpic(ctx, pm, epicID); parent != "" {
					con.Logf("  %s is a sub-issue of epic %s — building on the epic branch", epicID, parent)
					epicID = parent
				} else {
					con.Logf("  %s has no buildable leaf sub-issues — processing as standalone ticket", epicID)
					epicID = ""
				}
			}
		} else {
			con.Logf("  epic flow disabled — processing %s as standalone ticket", epicID)
			forcedID = epicID
			epicID = ""
			opts.Once = true
		}
	}

	dryRun := opts.DryRun

	scope := scopeFor(cfg, epicID)
	parentSuffix := ""
	if epicID != "" {
		parentSuffix = " under " + epicID
	}

	if dryRun {
		if forcedID != "" {
			con.Logf("Next up: %s", forcedID)
			return nil
		}
		con.Logf("[%s] Asking %s for the next eligible ticket%s…", cfg.Provider, cfg.TrackerProvider, parentSuffix)
		id, err := pm.Pick(ctx, scope)
		if err != nil {
			return fmt.Errorf("pick: %w", err)
		}
		if id != "" {
			con.Logf("Next up: %s", id)
		} else {
			con.Logf("Nothing eligible right now.")
		}
		return nil
	}

	repoRoot, err = config.ResolveRepoRoot(opts.Repo, cfg.RepoRoot, config.GitToplevel)
	if err != nil {
		return console.Actionable(err, "resolve target repo", "pass --repo <path>, set TRAU_REPO_ROOT, or run inside a git repository")
	}
	logger.Verbosef("final repo root for pipeline: %s", repoRoot)
	// Checkpoints write through the hub now (ADR 0008), so the loop needs one up
	// for every tracker — not only the hub-store providers ensured above. Idempotent.
	ensureHubForStore(ctx, cfg, stderr)
	p, err := buildPipeline(cfg, runner, repoRoot, pm, sink, transcripts, log, con)
	if err != nil {
		return err
	}
	p.EpicID = epicID
	if epicID != "" {
		if subs, serr := pm.SubIssues(ctx, epicID); serr == nil && len(subs) > 0 {
			con.Event(event.Event{Kind: "tickets", Fields: map[string]any{"total": len(subs)}})
		}
	}

	maxIter := cfg.MaxIterations
	if opts.Max >= 0 {
		maxIter = opts.Max
	}
	budgetSuffix := ""
	if lim := budgetLimits(cfg); lim.Enabled() {
		budgetSuffix = " · " + lim.Summary()
	}
	con.Logf("provider=%s · AUTO_MERGE=%v · max=%d%s%s", cfg.Provider, cfg.AutoMerge, maxIter, parentSuffix, budgetSuffix)

	if reg == nil {
		reg = newPresence(cfg, repoRoot)
		defer reg.Deregister()
	}
	p.OnPhase = func(id, phase string) { reg.SetState(registry.StateWorking, id, phase) }

	eng := &realEngine{pipe: p, tracker: pm, scope: scope, sink: sink, log: log}

	total := func(ids []string) (int, float64, bool) {
		t, c := 0, 0.0
		metered := true
		for _, id := range ids {
			tk, cs, m := sink.SessionTotal(id)
			t += tk
			c += cs
			metered = metered && m
		}
		return t, math.Round(c*100) / 100, metered
	}

	result := func(id string, elapsed time.Duration) console.TicketResult {
		tk, cs, metered := sink.SessionTotal(id)
		return console.TicketResult{
			ID:            id,
			Title:         p.State.Get(id, "TITLE"),
			Phase:         p.State.Get(id, "PHASE"),
			Branch:        p.State.Get(id, "BRANCH"),
			PRURL:         p.State.Get(id, "PR_URL"),
			Tokens:        tk,
			Cost:          math.Round(cs*100) / 100,
			CostMetered:   metered,
			Elapsed:       elapsed,
			FailureReason: p.State.Get(id, "FAILURE_REASON"),
		}
	}
	start := time.Now()
	processed, lerr := runLoop(ctx, eng, loopParams{
		Once:         opts.Once,
		Max:          maxIter,
		NoResume:     opts.NoResume,
		ParentSuffix: parentSuffix,
		ForcedID:     forcedID,
		Poller:       usagePoller(cfg, log),
		Report:       reg.SetState,
	}, con, result)

	if opts.DrainReport != "" {
		var class, reason string
		switch {
		case pipeline.IsPaused(lerr):
			class, reason = state.FailPaused, lerr.Error()
		case pipeline.IsFault(lerr):
			class, reason = state.FailFaulted, lerr.Error()
		}
		hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
		if werr := hub.PutDrainOutcome(context.Background(), repoName(cfg.RepoRoot), opts.DrainReport, class, reason); werr != nil {
			logger.Verbosef("drain report post failed: %v", werr)
			log.Emit("drain_report_error", "", fmt.Sprintf("drain report post failed: %v", werr),
				map[string]any{"ticket": opts.DrainReport, "error": werr.Error()})
		}
	}

	tk, cost, metered := total(processed)
	con.LoopDone(applyFault(console.SessionSummary{
		Tickets:     len(processed),
		TotalTokens: tk,
		TotalCost:   cost,
		CostMetered: metered,
		Elapsed:     time.Since(start),
		Err:         lerr,
		Paused:      pipeline.IsPaused(lerr),
	}, lerr))
	con.Wait()
	return lerr
}

// applyFault fills a SessionSummary's fault fields from err when the loop stopped
// on a *FaultError, so the summary can show an actionable "incomplete — work
// saved, rerun to resume" line instead of a generic "aborted".
func applyFault(s console.SessionSummary, err error) console.SessionSummary {
	if f := pipeline.AsFault(err); f != nil {
		s.Fault = true
		s.FaultID = f.ID
		s.FaultPhase = pipeline.NextPhaseLabel(f.Phase)
	}
	return s
}

// runDoctor runs the preflight health check and exits non-zero if any required
// check failed. It loads config best-effort (defaults still apply) so the
// report can flag what is missing rather than aborting on the first problem.
func runDoctor(ctx context.Context, args []string, stderr io.Writer) error {
	opts, err := config.ParseArgs(args)
	if err != nil {
		return usageError{err}
	}
	logger.Init(stderr, opts.Verbose, opts.Debug)

	repoRoot, rrErr := config.ResolveRepoRoot(opts.Repo, os.Getenv("TRAU_REPO_ROOT"), config.GitToplevel)
	if rrErr != nil {
		logger.Verbosef("repo root resolution failed: %v", rrErr)
		repoRoot = ""
	}

	projectEnv := config.ProjectConfigPath(repoRoot)
	userEnv := ""
	if home, herr := os.UserHomeDir(); herr == nil {
		userEnv = config.ProjectConfigPath(home)
	}

	cfg, sources, err := config.LoadLayeredWithSources(projectEnv, userEnv, config.LocalConfigPath(), opts.Provider)
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	if opts.Repo != "" || os.Getenv("TRAU_REPO_ROOT") != "" || cfg.RepoRoot == "" {
		cfg.RepoRoot = repoRoot
	}

	if _, err = doctor.Run(ctx, cfg, sources, cfg.RepoRoot, stderr); err != nil {
		return silentExit{1}
	}
	return nil
}

func buildTracker(cfg config.Config, runner agent.Runner) (tracker.Tracker, error) {
	provider := cfg.EffectiveTrackerProvider()
	tc := tracker.Config{
		Team:            cfg.LinearTeam,
		Project:         cfg.Project,
		ReadyLabel:      cfg.ReadyLabel,
		QuarantineLabel: cfg.QuarantineLabel,
		SplitLabel:      cfg.SplitLabel,
		APIKey:          cfg.LinearAPIKey,
	}
	switch provider {
	case "jira":
		tc.APIKey = cfg.JiraAPIToken
		tc.BaseURL = cfg.JiraBaseURL
		tc.Email = cfg.JiraEmail
		// A full set of per-repo REST credentials makes the Jira tracker act solely
		// as that Atlassian identity: drop the agent runner so no operation can fall
		// back to the shared Rovo MCP, which authenticates as a different account.
		// The pipeline keeps its own runner, so agent work is unaffected. This mirrors
		// onboarding detection — the loop must not silently switch identity either.
		if jiraRESTComplete(cfg) {
			runner = nil
		}
	case "internal":
		// The internal provider drives issues through the hub over HTTP, never the
		// database — so it needs the hub's origin, an optional bearer token, and the
		// hub-registered repo name, but no agent runner.
		tc.Repo = repoName(cfg.RepoRoot)
		tc.HubBaseURL = hubBaseURL(cfg)
		tc.HubToken = cfg.ServeToken
	}
	pm, err := tracker.New(provider, runner, tc)
	if err != nil {
		return nil, err
	}
	// A synced tracker with direct read credentials reads every issue from the hub's
	// store, not the tracker: the loop's status/label writes still land on the tracker
	// (and, in the same motion, the store row), but pick, prompts, and status all read
	// local (ADR 0007). Without direct credentials the provider keeps its agent/MCP path.
	if storeBackedProvider(cfg) {
		hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
		pm = tracker.NewStoreBacked(pm, hub, repoName(cfg.RepoRoot), cfg.ReadyLabel, cfg.QuarantineLabel)
	}
	return pm, nil
}

// storeBackedProvider reports whether the repo's synced tracker (Linear or Jira)
// carries the direct read credentials the hub needs to sync its Project into the
// store, so the pipeline reads every ticket from the store instead of the tracker
// (ADR 0007). Without them the loop keeps reading through the provider's agent/MCP path.
func storeBackedProvider(cfg config.Config) bool {
	switch cfg.EffectiveTrackerProvider() {
	case "linear":
		return strings.TrimSpace(cfg.LinearAPIKey) != ""
	case "jira":
		return jiraRESTComplete(cfg)
	default:
		return false
	}
}

// usesHubStore reports whether the repo's tracker reads through the hub's issue
// store — the internal provider always, and a synced provider with direct read
// credentials — so the loop must bring the hub up and register the repo first.
func usesHubStore(cfg config.Config) bool {
	return cfg.EffectiveTrackerProvider() == "internal" || storeBackedProvider(cfg)
}

// jiraRESTComplete reports whether cfg carries a full set of Jira REST credentials
// (base URL, email, API token) — enough to drive Jira's API directly as a single
// Atlassian identity without the Rovo MCP.
func jiraRESTComplete(cfg config.Config) bool {
	return cfg.JiraBaseURL != "" && cfg.JiraEmail != "" && cfg.JiraAPIToken != ""
}

// reconciledTicket records a stale local checkpoint that --status cleared because
// its tracker issue is already terminal (Done/Canceled).
type reconciledTicket struct {
	ID     string
	Status tracker.IssueStatus
	Phase  string
}

func reconciledIDs(ts []reconciledTicket) []string {
	ids := make([]string, 0, len(ts))
	for _, t := range ts {
		ids = append(ids, t.ID)
	}
	return ids
}

// reconcileCheckpoints cross-checks each in-flight/quarantined local checkpoint
// against the tracker and drops any whose issue is already terminal (Done/Canceled)
// — the out-of-band-finished case (COD-585). The tracker is built best-effort: a
// missing provider binary or a tracker that can't report issue status simply skips
// reconciliation (the status report still prints). Each query is time-bounded so a
// hung MCP call can't stall --status, and a query error or StatusUnknown leaves the
// checkpoint intact rather than risk clearing live work.
func reconcileCheckpoints(ctx context.Context, cfg config.Config, log *event.Log, sink *hubtokens.Sink, store state.Checkpoints) []reconciledTicket {
	if !hasReconcileCandidate(store) {
		return nil
	}
	runner, err := buildRouter(cfg, log, sink, nil, io.Discard)
	if err != nil {
		logger.Verbosef("reconcile: provider unavailable, skipping (%v)", err)
		return nil
	}
	pm, err := buildTracker(cfg, runner)
	if err != nil {
		logger.Verbosef("reconcile: tracker unavailable, skipping (%v)", err)
		return nil
	}
	statuser, ok := pm.(tracker.IssueStatuser)
	if !ok {
		logger.Verbosef("reconcile: tracker %q cannot report issue status, skipping", cfg.TrackerProvider)
		return nil
	}
	return reconcileWith(ctx, store, statuser)
}

// hasReconcileCandidate reports whether any saved checkpoint is in a reconcilable
// (in-flight/quarantined) phase, so callers can skip building a provider/tracker
// when there is nothing to cross-check.
func hasReconcileCandidate(store state.Checkpoints) bool {
	for _, id := range store.Tickets() {
		if state.Reconcilable(store.Get(id, "PHASE")) {
			return true
		}
	}
	return false
}

// reconcileWith cross-checks each in-flight/quarantined local checkpoint against
// the tracker via statuser and drops any whose issue is already terminal
// (Done/Canceled). Each query is time-bounded so a hung MCP call can't stall the
// caller; a query error or StatusUnknown leaves the checkpoint intact rather than
// risk clearing live work. Shared by the --status CLI path and the TUI status
// screen's on-demand reconcile.
func reconcileWith(ctx context.Context, store state.Checkpoints, statuser tracker.IssueStatuser) []reconciledTicket {
	var cleared []reconciledTicket
	for _, id := range store.Tickets() {
		phase := store.Get(id, "PHASE")
		if !state.Reconcilable(phase) {
			continue
		}
		qctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		st, err := statuser.IssueStatus(qctx, id)
		cancel()
		if err != nil {
			logger.Verbosef("reconcile %s: status query failed: %v", id, err)
			continue
		}
		if !state.StaleCheckpoint(phase, st.Terminal()) {
			continue
		}
		if err := store.RemoveState(id); err != nil {
			logger.Verbosef("reconcile %s: clear failed: %v", id, err)
			continue
		}
		cleared = append(cleared, reconciledTicket{ID: id, Status: st, Phase: phase})
	}
	return cleared
}

// scopeFor builds a picker scope carrying the configured issue prefix so whole-team
// picks (which have no parent id to derive a prefix from) match the right tracker.
func scopeFor(cfg config.Config, parent string) tracker.Scope {
	return tracker.Scope{Parent: parent, Team: cfg.LinearTeam, Project: cfg.Project, Prefix: cfg.IssuePrefix}
}

// listEligibleTimeout bounds --list-eligible so a hung tracker query surfaces as
// a clean error instead of stalling the caller (the serve hub captures this).
const listEligibleTimeout = 90 * time.Second

// eligibleTicket is the machine-readable shape of one eligible ticket under
// --list-eligible --json: the fields a picker needs to offer a ticket without a
// blind ID. It is the stable contract the serve hub parses. Parent carries the
// immediate epic's identifier (empty for a top-level ticket) and HasChildren
// marks a listed epic, so a consumer can group sub-issues under their parent.
type eligibleTicket struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent"`
	HasChildren bool     `json:"has_children"`
}

// writeEligibleJSON emits the eligible tickets as a JSON array on stdout, keeping
// the stream byte-stable: labels is always an array (never null) and parent /
// has_children are always present, so the shape does not vary with whether a
// ticket carries extra labels or sits under an epic.
func writeEligibleJSON(w io.Writer, tickets []tracker.ListedTicket) error {
	out := make([]eligibleTicket, 0, len(tickets))
	for _, t := range tickets {
		labels := t.Labels
		if labels == nil {
			labels = []string{}
		}
		out = append(out, eligibleTicket{
			ID:          t.ID,
			Title:       t.Title,
			Labels:      labels,
			Parent:      t.Parent,
			HasChildren: t.HasChildren,
		})
	}
	return json.NewEncoder(w).Encode(out)
}

// eligibleLine renders one eligible ticket as the "ID  Title  [labels]" form the
// human --list-eligible output has always printed, dropping the bracket when the
// ticket carries no labels.
func eligibleLine(t tracker.ListedTicket) string {
	if len(t.Labels) == 0 {
		return fmt.Sprintf("%s  %s", t.ID, t.Title)
	}
	return fmt.Sprintf("%s  %s  [%s]", t.ID, t.Title, strings.Join(t.Labels, ", "))
}

// groupEligibleLines formats the eligible queue for human --list-eligible output.
// With no ticket under an epic the result is the flat list, byte-for-byte as
// before. Once any ticket carries a parent, sub-issues are grouped and indented
// beneath their epic heading — the epic's own line when it is itself eligible,
// otherwise a bare epic id — while top-level tickets stay flat.
func groupEligibleLines(tickets []tracker.ListedTicket) []string {
	childrenByParent := map[string][]tracker.ListedTicket{}
	for _, t := range tickets {
		if t.Parent != "" {
			childrenByParent[t.Parent] = append(childrenByParent[t.Parent], t)
		}
	}

	lines := make([]string, 0, len(tickets))
	if len(childrenByParent) == 0 {
		for _, t := range tickets {
			lines = append(lines, eligibleLine(t))
		}
		return lines
	}

	byID := make(map[string]tracker.ListedTicket, len(tickets))
	for _, t := range tickets {
		byID[t.ID] = t
	}
	epicBlock := func(parent string) []string {
		block := make([]string, 0, len(childrenByParent[parent])+1)
		if epic, ok := byID[parent]; ok {
			block = append(block, eligibleLine(epic))
		} else {
			block = append(block, parent)
		}
		for _, c := range childrenByParent[parent] {
			block = append(block, "  "+eligibleLine(c))
		}
		return block
	}

	emitted := map[string]bool{}
	for _, t := range tickets {
		switch {
		case t.Parent != "":
			if !emitted[t.Parent] {
				emitted[t.Parent] = true
				lines = append(lines, epicBlock(t.Parent)...)
			}
		case len(childrenByParent[t.ID]) > 0:
			if !emitted[t.ID] {
				emitted[t.ID] = true
				lines = append(lines, epicBlock(t.ID)...)
			}
		default:
			lines = append(lines, eligibleLine(t))
		}
	}
	return lines
}

// listEpicTimeout bounds --list-epic so a hung sub-issue listing surfaces as a
// clean error instead of stalling the caller (the serve hub captures this).
const listEpicTimeout = 90 * time.Second

// epicSubIssue is the machine-readable shape of one epic sub-issue under
// --list-epic --json. It is the stable contract the serve hub parses to preview
// an epic before a loop is launched against it.
type epicSubIssue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// subIssueState collapses a sub-issue's tracker facts into the preview's state
// vocabulary: done for finished work, epic for a nested parent the loop never
// runs as a leaf, and todo for a buildable open child.
func subIssueState(s tracker.SubIssue) string {
	switch {
	case s.Done:
		return "done"
	case s.HasChildren:
		return "epic"
	default:
		return "todo"
	}
}

// writeEpicSubIssuesJSON emits an epic's sub-issues as a JSON array on stdout,
// keeping the stream byte-stable so the serve hub can parse it.
func writeEpicSubIssuesJSON(w io.Writer, subs []tracker.SubIssue) error {
	out := make([]epicSubIssue, 0, len(subs))
	for _, s := range subs {
		out = append(out, epicSubIssue{ID: s.ID, Title: s.Title, State: subIssueState(s)})
	}
	return json.NewEncoder(w).Encode(out)
}

// leafSubs returns the sub-issues that are themselves leaves (they have no
// children of their own). Nested epics are filtered out so the loop never
// accidentally treats them as buildable tickets.
func leafSubs(subs []tracker.SubIssue) []tracker.SubIssue {
	out := make([]tracker.SubIssue, 0, len(subs))
	for _, s := range subs {
		if !s.HasChildren {
			out = append(out, s)
		}
	}
	return out
}

// parentEpic returns the identifier of id's parent epic, or "" when the tracker
// cannot report a parent or id is top-level. A parent issue is the epic that owns
// the leaf, so under epic flow a directly-run child stacks on that epic's branch
// instead of branching off the base. Any tracker error degrades to "" (standalone)
// so an unreachable tracker never blocks a build.
func parentEpic(ctx context.Context, tr tracker.Tracker, id string) string {
	pr, ok := tr.(tracker.IssueParenter)
	if !ok {
		return ""
	}
	parent, err := pr.ParentIssue(ctx, id)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parent)
}

// budgetLimits projects the resolved config's spend ceilings into the budget
// package's Limits (zero fields = no cap).
func budgetLimits(cfg config.Config) budget.Limits {
	return budget.Limits{
		TicketUSD:    cfg.MaxTicketUSD,
		TicketTokens: cfg.MaxTicketTokens,
		DailyUSD:     cfg.MaxDailyUSD,
		DailyTokens:  cfg.MaxDailyTokens,
	}
}

func newRenderer(stdout, stderr io.Writer, cfg config.Config, opts config.Options, onInterrupt func()) console.Renderer {
	if opts.Status {
		return console.New(stdout, stderr)
	}
	if os.Getenv("TRAU_LOG_JSON") == "1" {
		return console.New(stdout, stderr)
	}
	if !cfg.TUI || opts.NoTUI {
		return console.New(stdout, stderr)
	}
	if !console.IsTerminal(stdout) {
		return console.New(stdout, stderr)
	}
	return tui.New(stdout, stderr, onInterrupt, cfg.Notify, repoName(cfg.RepoRoot))
}

// repoName is the repo folder name the TUI marks itself with — the basename of the
// resolved repo root, or "" when no repo was resolved.
func repoName(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Base(root)
}

// newCheckpointStore is the hub-backed client that writes every phase transition
// to the serve hub over HTTP (ADR 0008); a hub unreachable past
// HUB_WRITE_RETRY_WINDOW pauses the run.
func newCheckpointStore(cfg config.Config, repoRoot string) state.Checkpoints {
	hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
	window := time.Duration(cfg.HubWriteRetryWindow) * time.Second
	return hubcheckpoint.New(hub, repoName(repoRoot), window)
}

// newArtifactStore is the hub-backed client for the durable per-run phase
// artifacts — handoff brief, verify rubric, verify verdict, build notes (ADR
// 0008); the child posts each to the serve hub over HTTP and restores it on
// resume, writing no run files.
func newArtifactStore(cfg config.Config, repoRoot string) pipeline.ArtifactStore {
	hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
	window := time.Duration(cfg.HubWriteRetryWindow) * time.Second
	return hubartifact.New(hub, repoName(repoRoot), window, hubclient.IsUnreachable)
}

// newPhaseLogStore is the hub-backed client for the per-phase agent logs the TUI
// log inspector browses (ADR 0008); the child posts each phase's output to the
// serve hub over HTTP and the inspector reads them back, writing no run files.
func newPhaseLogStore(cfg config.Config, repoRoot string) *hubphaselog.Store {
	hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
	window := time.Duration(cfg.HubWriteRetryWindow) * time.Second
	return hubphaselog.New(hub, repoName(repoRoot), window, hubclient.IsUnreachable)
}

// newLessonStore is the hub-backed client for the per-repo lessons ledger — the
// distilled repair-experiment takeaways a failed or repaired run leaves for later
// runs (COD-529, ADR 0008); the child posts each lesson to the serve hub over HTTP
// and recalls the relevant ones for prompt injection, writing no ledger file.
func newLessonStore(cfg config.Config, repoRoot string) pipeline.LessonStore {
	hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
	window := time.Duration(cfg.HubWriteRetryWindow) * time.Second
	return hublesson.New(hub, repoName(repoRoot), window, hubclient.IsUnreachable)
}

// newPresence registers this loop with the serve hub over HTTP and heartbeats its
// reported session state (ADR 0005, ADR 0008 §7); the hub holds presence and
// reaps a dead PID via signal 0, so no per-PID instance file is written.
// Best-effort — a hub that never answers only leaves the loop unlisted, never
// blocks it.
func newPresence(cfg config.Config, repoRoot string) *hubpresence.Handle {
	hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
	return hubpresence.Register(hub, repoRoot, cfg.RunsDir)
}

// newTokenSink is the hub-backed token/cost sink: the child posts every provider
// call's usage to the serve hub over HTTP (ADR 0008) and reads ticket/day totals
// back from it, writing no per-run token files. Close it to flush the tail before
// the process exits.
func newTokenSink(cfg config.Config) *hubtokens.Sink {
	hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
	window := time.Duration(cfg.HubWriteRetryWindow) * time.Second
	return hubtokens.New(hub, repoName(cfg.RepoRoot), cfg.HubWriteBufferBytes, window)
}

// newTranscriptSink is the hub-backed transcript sink: the agent tees each phase's
// PTY output to it and the child posts the chunks to the hub, writing no .pty.log
// file (ADR 0008 §4). Close it to flush the tail before the process exits.
func newTranscriptSink(cfg config.Config) *hubtranscript.Sink {
	hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
	window := time.Duration(cfg.HubWriteRetryWindow) * time.Second
	return hubtranscript.New(hub, repoName(cfg.RepoRoot), cfg.HubWriteBufferBytes, window)
}

// tuiTranscriptSource adapts the hub client to the TUI live agent view: the view
// polls the hub's chunk store for the running phase instead of tailing a file.
type tuiTranscriptSource struct{ hub *hubclient.Client }

func (s tuiTranscriptSource) Chunks(ctx context.Context, repo, id string, after int64) (tui.TranscriptDelta, error) {
	p, err := s.hub.TranscriptChunks(ctx, repo, id, after, false, 0)
	if err != nil {
		return tui.TranscriptDelta{}, err
	}
	return tui.TranscriptDelta{ID: p.ID, Cols: p.Cols, Rows: p.Rows, Data: p.Data, Seq: p.Seq}, nil
}

// wireTUITranscripts points the TUI live view at the hub's transcript chunk store,
// scoped to this run's repo. Called once before the program runs.
func wireTUITranscripts(cfg config.Config) {
	tui.SetTranscriptSource(tuiTranscriptSource{hub: hubclient.New(hubBaseURL(cfg), cfg.ServeToken)}, repoName(cfg.RepoRoot))
}

func buildPipeline(cfg config.Config, runner agent.Runner, repoRoot string, pm tracker.Tracker, sink *hubtokens.Sink, transcripts agent.TranscriptSink, log *event.Log, con console.Renderer) (*pipeline.Pipeline, error) {
	wireCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if added, err := pipeline.EnsureRepoConfigInclude(wireCtx, repoRoot); err != nil {
		return nil, fmt.Errorf("wire %s into the repo's local git config: %w", pipeline.RepoConfigFile, err)
	} else if added {
		fmt.Fprintf(os.Stderr, "wired %s into the repo's local git config (include.path)\n", pipeline.RepoConfigFile)
	}
	var verifyChecks []checks.Check
	if cfg.VerifyChecks {
		loaded, _, err := checks.Load(repoRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v — falling back to default verify checks\n", err)
			loaded = checks.Defaults()
		}
		verifyChecks = loaded
	}
	panel, err := buildPanel(cfg, log, sink, transcripts)
	if err != nil {
		return nil, err
	}
	fallback, err := buildFallback(cfg, log, sink, transcripts)
	if err != nil {
		return nil, err
	}
	return &pipeline.Pipeline{
		Runner:              runner,
		State:               newCheckpointStore(cfg, repoRoot),
		Artifacts:           newArtifactStore(cfg, repoRoot),
		PhaseLogs:           newPhaseLogStore(cfg, repoRoot),
		LessonLedger:        newLessonStore(cfg, repoRoot),
		Git:                 pipeline.ExecGit{Repo: repoRoot},
		GitHub:              pipeline.ExecGitHub{Repo: repoRoot},
		Tracker:             pm,
		Tokens:              sink,
		Budget:              budgetLimits(cfg),
		RunsDir:             cfg.RunsDir,
		Base:                cfg.BaseBranch,
		Remote:              cfg.Remote,
		Prefix:              cfg.IssuePrefix,
		MaxRepairs:          cfg.MaxRepairs,
		MaxBugfixes:         cfg.MaxBugfixes,
		AgentRetries:        cfg.AgentRetries,
		AgentBackoff:        cfg.AgentBackoff,
		Fallback:            fallback,
		Checks:              verifyChecks,
		VerifyPanel:         panel,
		PanelPolicy:         cfg.VerifyPanelPolicy,
		PanelParallel:       cfg.PanelParallel,
		BrowserVerify:       cfg.BrowserVerify,
		AppURL:              cfg.AppURL,
		AutoMerge:           cfg.AutoMerge,
		MergeMethod:         cfg.MergeMethod,
		DeterministicCommit: cfg.DeterministicCommit,
		ExpectedChecks:      cfg.ExpectedChecks,
		RequireCI:           cfg.RequireCI,
		RequireRepoChanges:  cfg.RequireRepoChanges,
		AutoStash:           cfg.AutoStash,
		LintFix:             cfg.LintFix,
		LintFixCmd:          cfg.LintFixCmd,
		Cleanup:             cfg.Cleanup,
		SkillsExpected:      skillsExpected(repoRoot),
		RequiredSkills:      cfg.RequiredSkills,
		CITimeout:           cfg.CITimeout,
		CIPoll:              cfg.CIPoll,
		Lessons:             cfg.Lessons,
		LessonsDistill:      cfg.LessonsDistill,
		Renderer:            con,
		Events:              log,
		OwnedProject:        cfg.Project,

		RepoRoot:            repoRoot,
		TimelogEnabled:      cfg.TimelogEnabled,
		TimelogStorage:      cfg.TimelogStorage,
		TimelogOutputFormat: cfg.TimelogOutputFormat,
		TimelogEstimator:    cfg.TimelogEstimator,
	}, nil
}

// skillsExpected gates the pipeline's post-build no-skills warning: true only
// for a provider that reports skill usage, in a repo that has skills installed.
// Skills are re-checked per call so an install after startup (onboarding,
// AUTO_INSTALL_SKILLS) is seen without restarting.
func skillsExpected(repoRoot string) func(string) bool {
	reg := agent.DefaultRegistry()
	return func(provider string) bool {
		spec, ok := reg.Lookup(provider)
		if !ok || !spec.ReportsSkills {
			return false
		}
		has, _ := agent.CheckSkills(repoRoot)
		return has
	}
}

// buildPanel constructs the cross-vendor verify panel from VERIFY_PANEL — one
// fresh backend per provider:model:effort spec, reusing the same route parsing and
// backend construction as phase routes so each member can be a different provider.
// Returns nil when no panel is configured (the single-verifier default). A spec
// naming an unknown provider or whose binary is missing from PATH is a startup
// error. Repeated providers get a numeric suffix (claude, claude2) so their
// verdict files and ledger labels stay distinct.
func buildPanel(cfg config.Config, log *event.Log, sink agent.TokenSink, transcripts agent.TranscriptSink) ([]pipeline.Verifier, error) {
	if len(cfg.VerifyPanel) == 0 {
		return nil, nil
	}
	reg := agent.DefaultRegistry()
	counts := map[string]int{}
	var panel []pipeline.Verifier
	for _, spec := range cfg.VerifyPanel {
		provider, model, effort, err := parseRoute(reg, spec, cfg)
		if err != nil {
			return nil, fmt.Errorf("verify panel %q: %w", spec, err)
		}
		runner, err := buildBackend(reg, cfg, provider, model, effort, agent.PhaseVerify, log, sink, transcripts)
		if err != nil {
			return nil, fmt.Errorf("verify panel %q: %w", spec, err)
		}
		counts[provider]++
		name := provider
		if counts[provider] > 1 {
			name = fmt.Sprintf("%s%d", provider, counts[provider])
		}
		panel = append(panel, pipeline.Verifier{Name: name, Provider: provider, Runner: runner})
	}
	return panel, nil
}

// buildFallback builds the transient-recovery fallback chain from FALLBACK_PROVIDERS
// — one fresh backend per provider[:model[:effort]] spec, reusing the same route
// parsing and backend construction as phase routes (COD-547) so a fallback can be a
// different provider, model, or effort. It returns a phase-keyed resolver; the
// chain is global, so every phase gets the same ordered providers. Returns nil when
// no fallback is configured (retry-only). A spec naming an unknown provider or whose
// binary is missing from PATH is a startup error, surfaced before any run begins.
func buildFallback(cfg config.Config, log *event.Log, sink agent.TokenSink, transcripts agent.TranscriptSink) (func(string) []agent.Runner, error) {
	if len(cfg.FallbackProviders) == 0 {
		return nil, nil
	}
	reg := agent.DefaultRegistry()
	var chain []agent.Runner
	for _, spec := range cfg.FallbackProviders {
		provider, model, effort, err := parseRoute(reg, spec, cfg)
		if err != nil {
			return nil, fmt.Errorf("fallback provider %q: %w", spec, err)
		}
		b, err := buildBackend(reg, cfg, provider, model, effort, "", log, sink, transcripts)
		if err != nil {
			return nil, fmt.Errorf("fallback provider %q: %w", spec, err)
		}
		chain = append(chain, b)
	}
	return func(string) []agent.Runner { return chain }, nil
}

type engine interface {
	ResumeTarget() (id, phase string)

	InferredResume(ctx context.Context) (id, phase string)

	EnsureCleanBase(ctx context.Context) error

	// RestoreWIP undoes any EnsureCleanBase auto-stash at session end, popping the
	// user's WIP back onto its original branch. No-op when nothing was stashed.
	RestoreWIP(ctx context.Context)

	Pick(ctx context.Context) (string, error)

	Process(ctx context.Context, id, from string) error

	Finalize(ctx context.Context) error

	BudgetExhausted() (reason string, stop bool)
}

type realEngine struct {
	pipe    *pipeline.Pipeline
	tracker tracker.Tracker
	scope   tracker.Scope
	sink    *hubtokens.Sink
	log     *event.Log
	// resumeKeep, when set, restricts the resume scan to ids it accepts — the epic
	// flow sets it to the epic's child set so a stale checkpoint for an unrelated
	// ticket in the same runs/ dir is skipped rather than resumed. Nil scans all.
	resumeKeep func(id string) bool
}

func (e *realEngine) ResumeTarget() (string, string) {
	return e.pipe.State.ResumeTargetFunc(e.resumeKeep)
}
func (e *realEngine) InferredResume(ctx context.Context) (string, string) {
	return e.pipe.InferredResume(ctx)
}
func (e *realEngine) EnsureCleanBase(ctx context.Context) error { return e.pipe.EnsureCleanBase(ctx) }
func (e *realEngine) RestoreWIP(ctx context.Context)            { e.pipe.RestoreWIP(ctx) }
func (e *realEngine) Pick(ctx context.Context) (string, error)  { return e.tracker.Pick(ctx, e.scope) }
func (e *realEngine) Process(ctx context.Context, id, from string) error {
	err := e.pipe.Resume(ctx, id, from)
	e.flagCostAnomalies(id)
	return err
}

// flagCostAnomalies runs the post-run cost-anomaly check for a ticket the size
// judge called one-window: it records any tripped phases through the hub (ADR 0008),
// stamps the count onto the checkpoint (so --status can surface it), and emits one
// summary event. A no-op when nothing tripped or the sink/log are unset.
func (e *realEngine) flagCostAnomalies(id string) {
	if e.sink == nil {
		return
	}
	anomalies := e.sink.Flag(id)
	if len(anomalies) == 0 {
		return
	}
	// Stamp only tickets that still have a checkpoint — a refusal reset just
	// removed the state file, and recreating it here would leave a ghost row.
	if e.pipe.State.Get(id, "PHASE") != "" {
		_ = e.pipe.State.Set(id, "ANOMALIES", strconv.Itoa(len(anomalies)))
	}
	phases := make([]string, len(anomalies))
	for i, a := range anomalies {
		phases[i] = a.Phase
	}
	if e.log != nil {
		e.log.Emit("cost_anomaly", "", fmt.Sprintf("%s: cost anomaly in %s", id, strings.Join(phases, ", ")),
			map[string]any{"id": id, "phases": phases})
	}
}
func (e *realEngine) Finalize(ctx context.Context) error { return e.pipe.FinalizeEpic(ctx) }
func (e *realEngine) BudgetExhausted() (string, bool)    { return e.pipe.BudgetExhausted() }

type loopParams struct {
	Once         bool
	Max          int
	NoResume     bool
	ParentSuffix string
	ForcedID     string
	Poller       *probe.Poller
	// Report, when set, records the loop's session-state transitions to the
	// instance registry; nil disables reporting.
	Report func(state, ticket, phase string)
}

func runLoop(ctx context.Context, eng engine, p loopParams, con console.Renderer, result func(id string, elapsed time.Duration) console.TicketResult) ([]string, error) {
	var processed []string
	report := p.Report
	if report == nil {
		report = func(string, string, string) {}
	}
	// Put any WIP that EnsureCleanBase auto-stashed on a fresh pick back where it
	// came from once the loop ends (no-op when nothing was stashed).
	defer eng.RestoreWIP(ctx)
	// Poll the provider usage window for the run's lifetime, stopping when the loop
	// returns. Windows reach the renderer over the event log; nil when disabled.
	if p.Poller != nil {
		pctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go p.Poller.Run(pctx)
	}
	// crossStop surfaces an ownership refusal — the config-level guard (the ticket
	// belongs to another Linear project) or the build agent's REFUSED backstop —
	// and signals the loop to stop cleanly rather than re-pick the same foreign
	// ticket. Either way the ticket is left runnable from the repo that owns it.
	crossStop := func(id string, err error) bool {
		switch {
		case pipeline.IsCrossProject(err):
			con.Logf("✗ %v", err)
			con.Logf("  ↳ run it from the repo that owns that project, or `trau --clear %s` to drop a stray checkpoint here", id)
			return true
		case pipeline.AsRefused(err) != nil:
			r := pipeline.AsRefused(err)
			con.Logf("✗ %s: build agent refused — %s", id, r.Reason)
			con.Logf("  ↳ ticket reset (branch dropped, tracker restored) — run it from the repo it belongs to, and set PROJECT in this repo's .trau.ini so foreign tickets are never picked here")
			return true
		default:
			return false
		}
	}
	// doneSkipped remembers picks that turned out to be already merged. Pick
	// offering such an id a second time means the tracker is not converging —
	// stop cleanly instead of spending a pick agent per spin.
	doneSkipped := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			report(registry.StateStopping, "", "")
			con.Logf("⏹ interrupted — stopping")
			return processed, nil
		default:
		}

		if len(processed) >= p.Max {
			con.Logf("hit MAX_ITERATIONS=%d", p.Max)
			break
		}

		if reason, stop := eng.BudgetExhausted(); stop {
			con.Logf("■ daily budget reached — stopping the run (%s)", reason)
			break
		}

		var rid, rphase string
		if !p.NoResume {
			if rid, rphase = eng.ResumeTarget(); rid == "" {
				rid, rphase = eng.InferredResume(ctx)
			}
		}

		if rid != "" {
			con.Logf("↻ [%d] resuming %s", len(processed)+1, rid)
			report(registry.StateWorking, rid, rphase)
			t0 := time.Now()
			err := eng.Process(ctx, rid, rphase)
			if pipeline.IsPaused(err) {
				return processed, err
			}
			if crossStop(rid, err) {
				return processed, err
			}
			if errors.Is(err, pipeline.ErrAlreadyDone) {
				con.Logf("  %s already done — skipping", rid)
				continue
			}
			processed = append(processed, rid)
			con.TicketDone(result(rid, time.Since(t0)))
			if pipeline.IsFault(err) {
				return processed, err
			}
		} else if p.ForcedID != "" {
			con.Logf("▶ [%d] %s", len(processed)+1, p.ForcedID)
			report(registry.StateWorking, p.ForcedID, "")
			t0 := time.Now()
			err := eng.Process(ctx, p.ForcedID, "")
			if pipeline.IsPaused(err) {
				return processed, err
			}
			if crossStop(p.ForcedID, err) {
				return processed, err
			}
			if errors.Is(err, pipeline.ErrAlreadyDone) {
				con.Logf("  %s already done — nothing to do (`trau --clear %s` to run it again)", p.ForcedID, p.ForcedID)
				if p.Once {
					break
				}
				p.ForcedID = ""
				continue
			}
			processed = append(processed, p.ForcedID)
			con.TicketDone(result(p.ForcedID, time.Since(t0)))
			if pipeline.IsFault(err) {
				return processed, err
			}
			if p.Once {
				con.Logf("--once: stopping")
				break
			}

			p.ForcedID = ""
		} else {
			report(registry.StateGrazing, "", "")
			if err := eng.EnsureCleanBase(ctx); err != nil {
				return processed, err
			}
			id, err := eng.Pick(ctx)
			if err != nil {
				return processed, fmt.Errorf("pick: %w", err)
			}
			if id == "" {
				con.Logf("no eligible tickets left%s — done", p.ParentSuffix)
				break
			}
			con.Logf("▶ [%d] %s", len(processed)+1, id)
			report(registry.StateWorking, id, "")
			t0 := time.Now()
			err = eng.Process(ctx, id, "")
			if pipeline.IsPaused(err) {
				return processed, err
			}
			if crossStop(id, err) {
				return processed, err
			}
			if errors.Is(err, pipeline.ErrAlreadyDone) {
				if doneSkipped[id] {
					con.Logf("  %s already done — picked again after being skipped; stopping so the pick loop can't spin", id)
					break
				}
				doneSkipped[id] = true
				con.Logf("  %s already done — skipping", id)
				continue
			}
			processed = append(processed, id)
			con.TicketDone(result(id, time.Since(t0)))
			if pipeline.IsFault(err) {
				return processed, err
			}
		}

		if p.Once {
			con.Logf("--once: stopping")
			break
		}
	}
	if err := eng.Finalize(ctx); err != nil {
		return processed, err
	}
	return processed, nil
}

// menuOnlyArgs reports whether the argv requests the interactive menu rather
// than a headless action: a bare invocation, or a lone --no-serve.
func menuOnlyArgs(args []string) bool {
	for _, a := range args {
		if a != "--no-serve" {
			return false
		}
	}
	return true
}

func runSession(ctx context.Context, cfg config.Config, opts config.Options, stdout, stderr io.Writer) error {
	holder := tui.NewRenderer()

	log, flushEvents := newEventLog(cfg, stderr, holder.Event)
	defer flushEvents()

	maxIter := cfg.MaxIterations
	if opts.Max >= 0 {
		maxIter = opts.Max
	}
	acts := &appActions{
		cfg:         cfg,
		opts:        opts,
		stderr:      io.Discard,
		log:         log,
		sink:        newTokenSink(cfg),
		transcripts: newTranscriptSink(cfg),
		store:       newCheckpointStore(cfg, cfg.RepoRoot),
		logs:        newPhaseLogStore(cfg, cfg.RepoRoot),
		scope:       scopeFor(cfg, ""),
		maxIter:     maxIter,
	}
	defer acts.sink.Close()
	defer acts.transcripts.Close()
	wireTUITranscripts(cfg)
	if tui.AccessibleOnboardingRequested() && acts.OnboardingNeeded() {
		res, err := tui.RunAccessibleOnboarding(ctx, acts)
		if err != nil {
			return err
		}
		if res.ConfigPath != "" {
			_, _ = fmt.Fprintf(stdout, "Wrote %s\n", res.ConfigPath)
		}
	}

	reg := newPresence(cfg, cfg.RepoRoot)
	defer reg.Deregister()
	acts.reg = reg

	maybeAutostartHub(ctx, cfg, opts.NoServe, stderr)

	return tui.RunSession(ctx, stdout, holder, acts)
}

type appActions struct {
	cfg         config.Config
	opts        config.Options
	stderr      io.Writer
	log         *event.Log
	sink        *hubtokens.Sink
	transcripts *hubtranscript.Sink
	store       state.Checkpoints
	logs        *hubphaselog.Store
	scope       tracker.Scope
	maxIter     int

	built    bool
	buildErr error
	runner   agent.Runner
	pipe     *pipeline.Pipeline
	tracker  tracker.Tracker
	eng      *realEngine
	reg      *hubpresence.Handle
}

// RepoRoot returns the resolved target repo root, or "" when none was found.
func (a *appActions) RepoRoot() string { return a.cfg.RepoRoot }

// OnboardingPrefill returns the current configuration so the onboarding wizard
// can default to existing values when it is re-run.
func (a *appActions) OnboardingPrefill() tui.OnboardingPrefill {
	return tui.OnboardingPrefill{
		Provider:        a.cfg.Provider,
		TrackerProvider: a.cfg.TrackerProvider,
		BaseBranch:      a.cfg.BaseBranch,
		Team:            a.cfg.LinearTeam,
		ReadyLabel:      a.cfg.ReadyLabel,
		QuarantineLabel: a.cfg.QuarantineLabel,
		EpicFlow:        a.cfg.EpicFlow,
		Timelog:         a.cfg.TimelogEnabled,
		LinearAPIKey:    a.cfg.LinearAPIKey,
		JiraBaseURL:     a.cfg.JiraBaseURL,
		JiraEmail:       a.cfg.JiraEmail,
		JiraAPIToken:    a.cfg.JiraAPIToken,
	}
}

// LinearAPIKeyConfigured reports whether a Linear API key is already present
// in the layered config or environment.
func (a *appActions) LinearAPIKeyConfigured() bool {
	return strings.TrimSpace(a.cfg.LinearAPIKey) != ""
}

// ConfigLayers returns the writable layers exposed by the in-TUI settings
// editor, ordered from lowest to highest precedence.
func (a *appActions) ConfigLayers() []string {
	return []string{"project", "user"}
}

// ConfigItems returns every known config key with its effective value and the
// layer that supplied it, so the settings editor can show precedence.
func (a *appActions) ConfigItems() []tui.ConfigItem {
	projectEnv, userEnv, localEnv := a.configPaths()
	items, err := config.ResolveConfigItems(a.cfg, localEnv, projectEnv, userEnv, a.opts.Provider, a.opts)
	if err != nil {
		return nil
	}
	out := make([]tui.ConfigItem, 0, len(items))
	for _, it := range items {
		out = append(out, tui.ConfigItem{
			Key:         it.Key,
			Value:       it.Value,
			Layer:       string(it.Layer),
			Advanced:    it.Advanced,
			Options:     it.Options,
			Bool:        it.Bool,
			Description: it.Description,
			Default:     it.Default,
		})
	}
	return out
}

// SaveConfigItem writes a config key to the selected layer's file and reloads
// the in-memory configuration so the editor reflects the change immediately.
func (a *appActions) SaveConfigItem(key, value, layer string) error {
	projectEnv, userEnv, localEnv := a.configPaths()
	if err := config.WriteConfigLayer(layer, localEnv, projectEnv, userEnv, key, value); err != nil {
		return err
	}
	cfg, err := config.LoadLayered(projectEnv, userEnv, localEnv, a.opts.Provider)
	if err != nil {
		return fmt.Errorf("reload config after save: %w", err)
	}
	if a.opts.Repo != "" || os.Getenv("TRAU_REPO_ROOT") != "" {
		cfg.RepoRoot = a.cfg.RepoRoot
	} else if cfg.RepoRoot == "" {
		cfg.RepoRoot = a.cfg.RepoRoot
	}
	a.cfg = cfg
	a.scope.Team = cfg.LinearTeam
	return nil
}

// ProviderTunings returns per-provider execution tuning (model/effort defaults
// and per-phase overrides) for the in-TUI provider settings panel.
func (a *appActions) ProviderTunings() []tui.ProviderTuning {
	projectEnv, userEnv, localEnv := a.configPaths()
	tunings := config.ResolveProviderTunings(localEnv, projectEnv, userEnv, a.cfg.Provider)
	out := make([]tui.ProviderTuning, 0, len(tunings))
	for _, t := range tunings {
		pt := tui.ProviderTuning{
			Name:    t.Name,
			Active:  t.Active,
			Models:  t.Models,
			Efforts: t.Efforts,
			Model:   tui.ProviderTuningField{Value: t.Model.Value, Layer: string(t.Model.Layer)},
			Effort:  tui.ProviderTuningField{Value: t.Effort.Value, Layer: string(t.Effort.Layer)},
		}
		for _, ph := range t.Phases {
			pt.Phases = append(pt.Phases, tui.ProviderPhaseTuning{
				Phase:     ph.Phase,
				Model:     tui.ProviderTuningField{Value: ph.Model.Value, Layer: string(ph.Model.Layer)},
				Effort:    tui.ProviderTuningField{Value: ph.Effort.Value, Layer: string(ph.Effort.Layer)},
				EffModel:  ph.EffModel,
				EffEffort: ph.EffEffort,
			})
		}
		out = append(out, pt)
	}
	return out
}

func (a *appActions) configPaths() (projectEnv, userEnv, localEnv string) {
	localEnv = config.LocalConfigPath()
	projectEnv = config.ProjectConfigPath(a.cfg.RepoRoot)
	if home, err := os.UserHomeDir(); err == nil {
		userEnv = config.ProjectConfigPath(home)
	}
	return projectEnv, userEnv, localEnv
}

// OnboardingNeeded reports whether the project is missing the setup required to
// run the loop. It is true when no repo root was resolved or when the resolved
// configuration lacks a Linear team (the minimum signal that the loop is
// configured for this project).
func (a *appActions) OnboardingNeeded() bool {
	if a.cfg.RepoRoot == "" {
		return true
	}
	return strings.TrimSpace(a.cfg.LinearTeam) == ""
}

// SetupProject writes the project-level env file from the wizard's collected
// values, saves the Linear API key to the user-level env file, reloads
// configuration in-memory, and optionally creates the PM labels. It returns the
// project config path that was written.
func (a *appActions) SetupProject(ctx context.Context, setup tui.ProjectSetup) (tui.SetupResult, error) {
	path := filepath.Join(a.cfg.RepoRoot, config.ProjectConfigName)
	values := map[string]string{
		"TRACKER_PROVIDER": setup.TrackerProvider,
		"LINEAR_TEAM":      setup.Team,
		"READY_LABEL":      setup.ReadyLabel,
		"QUARANTINE_LABEL": setup.QuarantineLabel,
		"BASE_BRANCH":      setup.BaseBranch,
		"PROVIDER":         setup.Provider,
		"EPIC_FLOW":        boolEnvValue(setup.EpicFlow),
		"REQUIRE_CI":       boolEnvValue(setup.RequireCI),
	}
	if len(setup.ExpectedChecks) > 0 {
		// Detection found the exact required checks — pin the gate to them so it
		// waits for those specific checks instead of any-green.
		values["EXPECTED_CHECKS"] = strings.Join(setup.ExpectedChecks, ",")
	}
	if setup.Timelog {
		// Opting in writes the master toggle plus sensible defaults for the rest, so
		// the feature is fully configured. Leaving it off writes nothing — the keys
		// default to off and trau behaves exactly as before.
		values["TIMELOG_ENABLED"] = "1"
		values["TIMELOG_STORAGE"] = "repo"
		values["TIMELOG_OUTPUT_FORMAT"] = "default"
		values["TIMELOG_ESTIMATOR"] = "heuristic"
	}
	// Jira REST credentials are per-repo — writing them to the project .trau.ini
	// (not the shared user file) is what lets two repos target two separate Jira
	// accounts, the whole point of the direct adapter.
	if setup.TrackerProvider == "jira" {
		if v := strings.TrimSpace(setup.JiraBaseURL); v != "" {
			values["JIRA_BASE_URL"] = v
		}
		if v := strings.TrimSpace(setup.JiraEmail); v != "" {
			values["JIRA_EMAIL"] = v
		}
		if v := strings.TrimSpace(setup.JiraAPIToken); v != "" {
			values["JIRA_API_TOKEN"] = v
		}
	}
	if err := config.WriteProjectEnv(path, values); err != nil {
		return tui.SetupResult{}, fmt.Errorf("write project env: %w", err)
	}
	// The project .trau.ini may now hold a Jira token; keep it out of git.
	if err := state.EnsureGitignore(a.cfg.RepoRoot, a.cfg.RunsDir); err != nil {
		logger.Verbosef("ensure .trau.ini gitignored: %v", err)
	}

	projectEnv := path
	userEnv := ""
	if home, herr := os.UserHomeDir(); herr == nil {
		userEnv = config.ProjectConfigPath(home)
		if setup.LinearAPIKey != "" {
			if err := config.WriteEnvFile(userEnv, map[string]string{"LINEAR_API_KEY": setup.LinearAPIKey}); err != nil {
				return tui.SetupResult{ConfigPath: path}, fmt.Errorf("write user env: %w", err)
			}
		}
	}
	cfg, err := config.LoadLayered(projectEnv, userEnv, config.LocalConfigPath(), a.opts.Provider)
	if err != nil {
		return tui.SetupResult{ConfigPath: path}, fmt.Errorf("reload config after setup: %w", err)
	}
	if a.opts.Repo != "" || os.Getenv("TRAU_REPO_ROOT") != "" {
		cfg.RepoRoot = a.cfg.RepoRoot
	} else if cfg.RepoRoot == "" {
		cfg.RepoRoot = a.cfg.RepoRoot
	}
	a.cfg = cfg
	a.scope.Team = cfg.LinearTeam

	res := tui.SetupResult{ConfigPath: path}
	if !setup.CreateLabels {
		return res, nil
	}

	runner, err := buildRouter(a.cfg, a.log, a.sink, a.transcripts, a.stderr)
	if err != nil {
		res.LabelErr = err
		return res, nil
	}
	pm, err := buildTracker(a.cfg, runner)
	if err != nil {
		res.LabelErr = err
		return res, nil
	}
	if err := pm.EnsureLabels(ctx); err != nil {
		res.LabelErr = err
	}
	return res, nil
}

// DetectTeams enumerates the selectable containers for the chosen tracker so the
// onboarding wizard can offer a picker instead of free-text entry. GitHub's repo
// is read locally from the git remote (no agent call); Linear and Jira are
// listed by driving their MCP through the chosen AI provider, bounded by a
// timeout so a hung agent cannot stall onboarding. Any error tells the wizard to
// fall back to manual entry.
func (a *appActions) DetectTeams(ctx context.Context, trackerProvider, aiProvider string, jira tui.JiraCreds) (tui.TeamDetection, error) {
	switch trackerProvider {
	case "github":
		slug, err := detectGitHubRepo(a.cfg.RepoRoot)
		if err != nil {
			return tui.TeamDetection{Label: "repository"}, err
		}
		return tui.TeamDetection{
			Label:    "repository",
			AutoFill: true,
			Teams:    []tui.DetectedTeam{{Key: slug, Name: slug}},
		}, nil
	case "linear", "jira":
		label := "team"
		if trackerProvider == "jira" {
			label = "project"
		}
		cfg := a.cfg
		cfg.Provider = aiProvider
		cfg.TrackerProvider = trackerProvider
		if trackerProvider == "jira" {
			cfg.JiraBaseURL = jira.BaseURL
			cfg.JiraEmail = jira.Email
			cfg.JiraAPIToken = jira.APIToken
		}
		// When the wizard supplied a full set of Jira REST credentials, detection
		// must enumerate projects as THAT identity only — never silently fall back
		// to the shared Rovo MCP, which authenticates as a different Atlassian
		// account. Building the tracker without an agent runner makes the REST path
		// the sole source and surfaces an auth failure instead of masking it.
		restOnly := trackerProvider == "jira" && jiraRESTComplete(cfg)
		var runner agent.Runner
		if !restOnly {
			r, err := buildRouter(cfg, a.log, a.sink, a.transcripts, a.stderr)
			if err != nil {
				return tui.TeamDetection{Label: label}, err
			}
			runner = r
		}
		pm, err := buildTracker(cfg, runner)
		if err != nil {
			return tui.TeamDetection{Label: label}, err
		}
		lister, ok := pm.(tracker.TeamLister)
		if !ok {
			return tui.TeamDetection{Label: label}, fmt.Errorf("%s does not support listing", trackerProvider)
		}
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		teams, err := lister.ListTeams(ctx)
		if err != nil {
			return tui.TeamDetection{Label: label}, detectListErr(trackerProvider, err)
		}
		out := make([]tui.DetectedTeam, 0, len(teams))
		for _, t := range teams {
			out = append(out, tui.DetectedTeam{Key: t.Key, Name: t.Name})
		}
		return tui.TeamDetection{Label: label, Teams: out}, nil
	default:
		return tui.TeamDetection{Label: "team"}, fmt.Errorf("unknown tracker provider %q", trackerProvider)
	}
}

// detectListErr rewrites a team/project detection failure into an actionable
// message. For Jira it maps an auth failure (the wizard token was rejected) to a
// clear "regenerate your token" hint instead of the raw sentinel, so a user who
// mistyped a token sees why detection failed rather than a bare error.
func detectListErr(trackerProvider string, err error) error {
	if trackerProvider == "jira" {
		if msg := jiraapi.AuthErrorMessage(err); msg != "" {
			return errors.New(msg)
		}
	}
	return err
}

// detectGitHubRepo resolves the current repository slug ("owner/repo") for the
// GitHub tracker. It prefers the gh CLI (already required by the readiness
// check) and falls back to parsing the origin remote URL.
func detectGitHubRepo(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", fmt.Errorf("no repository root resolved")
	}
	if _, err := exec.LookPath("gh"); err == nil {
		cmd := exec.Command("gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
		cmd.Dir = repoRoot
		if out, err := cmd.Output(); err == nil {
			if slug := strings.TrimSpace(string(out)); slug != "" {
				return slug, nil
			}
		}
	}
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("detect repo from git remote: %w", err)
	}
	slug := parseRepoSlug(strings.TrimSpace(string(out)))
	if slug == "" {
		return "", fmt.Errorf("could not derive owner/repo from origin remote")
	}
	return slug, nil
}

// parseRepoSlug extracts "owner/repo" from an origin remote URL in either SSH
// (git@github.com:owner/repo.git) or HTTPS (https://github.com/owner/repo.git)
// form. It returns "" for hosts other than github.com.
func parseRepoSlug(remote string) string {
	remote = strings.TrimSuffix(remote, ".git")
	i := strings.Index(remote, "github.com")
	if i < 0 {
		return ""
	}
	rest := strings.TrimLeft(remote[i+len("github.com"):], ":/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// DetectCI probes whether this repo gates PRs on CI for the onboarding wizard,
// reading GitHub's required checks off the repo resolved from the git remote and
// falling back to the local .github/workflows scan. It never errors.
func (a *appActions) DetectCI(ctx context.Context, baseBranch string) tui.CIDetection {
	return detectCIGate(ctx, a.cfg.RepoRoot, baseBranch)
}

// detectCIGate layers CI-gate signals from most to least authoritative:
// branch-protection / ruleset required checks (literally the merge gate GitHub
// enforces, which also names the checks) then the local pull_request workflow
// scan. It deliberately does NOT trust check runs on the base-branch tip: a
// push-only workflow produces runs on main while PRs receive zero checks — the
// exact false positive REQUIRE_CI exists to avoid. When nothing authoritative is
// found the local guess stays a question rather than an auto-decision.
func detectCIGate(ctx context.Context, repoRoot, baseBranch string) tui.CIDetection {
	fallback := tui.CIDetection{Gate: config.HasPullRequestCI(repoRoot), Source: "none"}
	if fallback.Gate {
		fallback.Source = "workflows"
	}
	if repoRoot == "" {
		return fallback
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return fallback
	}
	slug, err := detectGitHubRepo(repoRoot)
	if err != nil || slug == "" {
		return fallback
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	branch := strings.TrimSpace(baseBranch)
	if branch == "" {
		branch = ghDefaultBranch(ctx, repoRoot, slug)
	}
	if checks, found := ghRequiredChecks(ctx, repoRoot, slug, branch); found && len(checks) > 0 {
		return tui.CIDetection{Gate: true, Confident: true, ExpectedChecks: checks, Source: "branch-protection"}
	}
	return fallback
}

// ghDefaultBranch resolves the repo's default branch so protection lookups target
// the branch PRs actually merge into; it defaults to "main" on any failure.
func ghDefaultBranch(ctx context.Context, repoRoot, slug string) string {
	out, err := ghAPI(ctx, repoRoot, "repos/"+slug, "-q", ".default_branch")
	if err != nil {
		return "main"
	}
	if b := strings.TrimSpace(string(out)); b != "" {
		return b
	}
	return "main"
}

// ghRequiredChecks unions the required status-check names from classic branch
// protection and repository rulesets for branch. found reports whether either
// source declared required checks, so an empty result reads as "protected but no
// checks required" rather than "unknown".
func ghRequiredChecks(ctx context.Context, repoRoot, slug, branch string) ([]string, bool) {
	esc := strings.ReplaceAll(branch, "/", "%2F")
	var names []string
	found := false
	if out, err := ghAPI(ctx, repoRoot, fmt.Sprintf("repos/%s/branches/%s/protection/required_status_checks", slug, esc)); err == nil {
		found = true
		names = append(names, parseProtectionChecks(out)...)
	}
	if out, err := ghAPI(ctx, repoRoot, fmt.Sprintf("repos/%s/rules/branches/%s", slug, esc)); err == nil {
		if rc := parseRulesetChecks(out); len(rc) > 0 {
			found = true
			names = append(names, rc...)
		}
	}
	return dedupeChecks(names), found
}

// parseProtectionChecks extracts required check names from the classic
// branch-protection required_status_checks payload, tolerating both the modern
// checks[].context and the deprecated contexts[] shapes.
func parseProtectionChecks(data []byte) []string {
	var resp struct {
		Contexts []string `json:"contexts"`
		Checks   []struct {
			Context string `json:"context"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil
	}
	names := append([]string{}, resp.Contexts...)
	for _, c := range resp.Checks {
		names = append(names, c.Context)
	}
	return names
}

// parseRulesetChecks extracts required check names from the active branch rules
// payload (the rulesets feature), reading required_status_checks rules.
func parseRulesetChecks(data []byte) []string {
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil
	}
	var names []string
	for _, r := range rules {
		if r.Type != "required_status_checks" {
			continue
		}
		for _, c := range r.Parameters.RequiredStatusChecks {
			names = append(names, c.Context)
		}
	}
	return names
}

func dedupeChecks(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// ghAPI runs `gh api` in repoRoot and returns stdout; a non-2xx response (404
// when a branch is unprotected, 403 without admin) surfaces as an error so
// callers fall back rather than read it as "no checks".
func ghAPI(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", append([]string{"api"}, args...)...)
	cmd.Dir = repoRoot
	return cmd.Output()
}

// MenuInfo builds the landing-screen context from config + saved state (no heavy
// deps), so it stays a cheap local read called on every return to the menu.
func (a *appActions) MenuInfo() tui.MenuInfo {
	model, effort := "", ""
	switch a.cfg.Provider {
	case "claude":
		model, effort = a.cfg.ClaudeModel, a.cfg.ClaudeEffort
	case "codex":
		model, effort = a.cfg.CodexModel, a.cfg.CodexEffort
	case "kimi":
		model = a.cfg.KimiModel
	}
	inFlight, done := 0, 0
	for _, id := range a.store.Tickets() {
		switch ph := a.store.Get(id, "PHASE"); {
		case ph == "":

		case ph == state.Merged:
			done++
		case !state.Terminal(ph):
			inFlight++
		}
	}
	resume := tui.ResumeTarget{}
	if id, phase := a.store.ResumeTargetFunc(nil); id != "" {
		resume = tui.ResumeTarget{
			ID:    id,
			Phase: pipeline.NextPhaseLabel(phase),
			Title: a.store.Get(id, "TITLE"),
		}
	}
	return tui.MenuInfo{
		Version:       version,
		Repo:          repoName(a.cfg.RepoRoot),
		RepoPath:      a.cfg.RepoRoot,
		Provider:      a.cfg.Provider,
		Model:         modelEffortTag(model, effort),
		Base:          a.cfg.BaseBranch,
		Prefix:        a.cfg.IssuePrefix,
		MaxIterations: a.maxIter,
		AutoMerge:     a.cfg.AutoMerge,
		Notify:        a.cfg.Notify,
		InFlight:      inFlight,
		Done:          done,
		Resume:        resume,
		Providers: []tui.ProviderChoice{
			{Name: "claude", Model: modelEffortTag(a.cfg.ClaudeModel, a.cfg.ClaudeEffort)},
			{Name: "codex", Model: modelEffortTag(a.cfg.CodexModel, a.cfg.CodexEffort)},
			{Name: "kimi", Model: modelEffortTag(a.cfg.KimiModel, "")},
		},
	}
}

func modelEffortTag(model, effort string) string {
	name := strings.TrimPrefix(model, "claude-")
	switch {
	case name != "" && effort != "":
		return name + " @" + effort
	case name != "":
		return name
	case effort != "":
		return "@" + effort
	default:
		return ""
	}
}

// StatusRows reads the saved checkpoints (+ token totals) for the status table.
func (a *appActions) StatusRows() []tui.StatusRow {
	ids := a.store.Tickets()
	rows := make([]tui.StatusRow, 0, len(ids))
	for _, id := range ids {
		tok, cost, metered := a.sink.Total(id)
		updated, _ := time.Parse("2006-01-02 15:04:05", a.store.Get(id, "UPDATED"))
		rows = append(rows, tui.StatusRow{
			ID:            id,
			Title:         a.store.Get(id, "TITLE"),
			Phase:         a.store.Get(id, "PHASE"),
			PRURL:         a.store.Get(id, "PR_URL"),
			Branch:        a.store.Get(id, "BRANCH"),
			FailureReason: a.store.Get(id, "FAILURE_REASON"),
			FailureClass:  state.FailureClass(a.store.Get(id, "PHASE"), a.store.Get(id, "FAILURE_CLASS"), a.store.Get(id, "FAILURE_REASON")),
			Tokens:        tok,
			Cost:          cost,
			CostMetered:   metered,
			Updated:       updated,
		})
	}
	return rows
}

// LogRuns returns every saved ticket run for the in-TUI log inspector, ordered
// by most recent update first.
func (a *appActions) LogRuns() []tui.LogRun {
	ids := a.store.Tickets()
	runs := make([]tui.LogRun, 0, len(ids))
	for _, id := range ids {
		updated, _ := time.Parse("2006-01-02 15:04:05", a.store.Get(id, "UPDATED"))
		runs = append(runs, tui.LogRun{
			ID:            id,
			Title:         a.store.Get(id, "TITLE"),
			Phase:         a.store.Get(id, "PHASE"),
			Updated:       updated,
			FailureReason: a.store.Get(id, "FAILURE_REASON"),
			Path:          filepath.Join(a.cfg.RunsDir, id),
		})
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Updated.After(runs[j].Updated)
	})
	return runs
}

// LogContent returns the logs for id formatted for the TUI inspector. The
// output starts with a run header (phase + failure reason), followed by the
// tail of the most recent phase log so the latest output/error is immediately
// visible, then the full concatenated phase logs.
func (a *appActions) LogContent(id string) string {
	logs, err := a.logs.List(id)
	if err != nil {
		return ""
	}

	phase := a.store.Get(id, "PHASE")
	if phase == "" {
		phase = "?"
	}
	failureReason := a.store.Get(id, "FAILURE_REASON")

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "══ %s ══\nphase: %s\n", id, phase)
	if failureReason != "" {
		_, _ = fmt.Fprintf(&b, "failure: %s\n", failureReason)
	}

	if len(logs) == 0 {
		b.WriteString("\n(no phase logs)\n")
		return b.String()
	}

	// The store returns logs most-recently-written first. Show the tail of the
	// newest up front so the latest output/error is immediately visible.
	latest := []byte(logs[0].Content)
	b.WriteString("\n── latest output ──\n")
	if len(latest) == 0 {
		b.WriteString("(empty log)\n")
	} else {
		b.Write(tailLines(latest, 80))
	}

	// Then the full logs for deeper inspection.
	b.WriteString("\n── full logs ──\n")
	for _, l := range logs {
		if l.Content == "" {
			continue
		}
		_, _ = fmt.Fprintf(&b, "\n── %s ──\n", l.Phase)
		b.WriteString(l.Content)
	}
	return b.String()
}

// tailLines returns the last n lines of buf, preserving the trailing newline
// when buf ends with one.
func tailLines(buf []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	lines := strings.Split(string(buf), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := strings.Join(lines, "\n")
	if len(buf) > 0 && buf[len(buf)-1] == '\n' && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out)
}

func (a *appActions) ensure() error {
	if a.built {
		return a.buildErr
	}
	a.built = true
	runner, err := buildRouter(a.cfg, a.log, a.sink, a.transcripts, a.stderr)
	if err != nil {
		a.buildErr = err
		return err
	}
	a.runner = runner
	a.tracker, err = buildTracker(a.cfg, runner)
	if err != nil {
		a.buildErr = err
		return err
	}
	repoRoot, err := config.ResolveRepoRoot(a.opts.Repo, a.cfg.RepoRoot, config.GitToplevel)
	if err != nil {
		a.buildErr = err
		return err
	}
	pipe, err := buildPipeline(a.cfg, runner, repoRoot, a.tracker, a.sink, a.transcripts, a.log, nil)
	if err != nil {
		a.buildErr = err
		return err
	}
	a.pipe = pipe
	a.pipe.OnPhase = func(id, phase string) { a.reg.SetState(registry.StateWorking, id, phase) }
	a.eng = &realEngine{pipe: a.pipe, tracker: a.tracker, scope: a.scope, sink: a.sink, log: a.log}
	return nil
}

// ReportIdle marks the session idle — the TUI is back on the menu with nothing
// live. It is the tui.sessionReporter hook the menu shell calls on every return
// to a browse screen.
func (a *appActions) ReportIdle() { a.reg.SetState(registry.StateIdle, "", "") }

// ReportStopping marks a graceful stop in flight, reported the moment the user
// interrupts a run — before the loop unwinds — so the hub stops counting the
// session as actively working.
func (a *appActions) ReportStopping() { a.reg.SetState(registry.StateStopping, "", "") }

// reportAfterRun settles the session state once a run returns: parked, naming
// the ticket a fault, pause, or give-up left on the recap for a human, or idle
// when the run finished cleanly. The failure class and reason stay on the
// checkpoint.
func (a *appActions) reportAfterRun(err error) {
	if f := pipeline.AsFault(err); f != nil {
		a.reg.SetState(registry.StateParked, f.ID, "")
		return
	}
	if p := pipeline.AsPaused(err); p != nil {
		a.reg.SetState(registry.StateParked, p.ID, "")
		return
	}
	if g := pipeline.AsGiveUp(err); g != nil {
		a.reg.SetState(registry.StateParked, g.ID, "")
		return
	}
	a.reg.SetState(registry.StateIdle, "", "")
}

func (a *appActions) DryRun(ctx context.Context) (string, error) {
	if err := a.ensure(); err != nil {
		return "", err
	}
	return a.tracker.Pick(ctx, a.scope)
}

// SubIssues lists an epic's direct children for the run-loop preview. The agent
// call is bounded so a hung listing cannot stall the screen.
func (a *appActions) SubIssues(ctx context.Context, id string) ([]tui.SubIssue, error) {
	if err := a.ensure(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	raw, err := a.tracker.SubIssues(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]tui.SubIssue, 0, len(raw))
	for _, s := range raw {
		out = append(out, tui.SubIssue{ID: s.ID, Title: s.Title, Done: s.Done, HasChildren: s.HasChildren})
	}
	return out, nil
}

// ListEligible returns the ready queue using the tracker's fast lister, if
// available. It is bounded so a hung call cannot stall the TUI.
func (a *appActions) ListEligible(ctx context.Context) ([]tui.ListedTicket, error) {
	if err := a.ensure(); err != nil {
		return nil, err
	}
	lister, ok := a.tracker.(tracker.TicketLister)
	if !ok {
		return nil, fmt.Errorf("tracker %q does not support listing eligible tickets", a.cfg.TrackerProvider)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	raw, err := lister.ListEligible(ctx, a.scope)
	if err != nil {
		return nil, err
	}
	out := make([]tui.ListedTicket, 0, len(raw))
	for _, t := range raw {
		out = append(out, tui.ListedTicket{ID: t.ID, Title: t.Title, State: t.State})
	}
	return out, nil
}

func (a *appActions) Reset(ctx context.Context, id string) error {
	if err := a.ensure(); err != nil {
		return err
	}
	return a.pipe.Reset(ctx, id)
}

// Reconcile cross-checks in-flight/quarantined checkpoints against the tracker and
// clears any whose issue is already Done/Canceled, returning the cleared ids. It
// backs the TUI status screen's on-demand reconcile; the CLI --status path uses
// reconcileCheckpoints, which builds its own tracker.
func (a *appActions) Reconcile(ctx context.Context) ([]string, error) {
	if err := a.ensure(); err != nil {
		return nil, err
	}
	statuser, ok := a.tracker.(tracker.IssueStatuser)
	if !ok {
		return nil, fmt.Errorf("tracker %q cannot report issue status", a.cfg.TrackerProvider)
	}
	return reconciledIDs(reconcileWith(ctx, a.store, statuser)), nil
}

func (a *appActions) CheckoutBranch(ctx context.Context, id string) (string, error) {
	if err := a.ensure(); err != nil {
		return "", err
	}
	return a.pipe.CheckoutBranch(ctx, id)
}

// RunLoop runs the autonomous loop with the configured defaults (MAX_ITERATIONS,
// resume in-flight work first), routing its progress to the dashboard renderer r,
// and always closes with r.LoopDone so the shell flips to the summary. A non-empty
// epic scopes the loop to that epic's sub-issues (and stacks work on its branch);
// otherwise it works the team's ready queue.
func (a *appActions) RunLoop(ctx context.Context, epic string, r console.Renderer) {
	max := a.maxIter
	if max <= 0 {
		max = math.MaxInt
	}
	a.runEpicLoop(ctx, epic, r, max)
}

// epicChildFilter returns a predicate accepting the epic's own id and the ids of
// its direct leaf sub-issues — the only checkpoints the epic flow may resume.
// Nested epics (sub-issues that themselves have children) are excluded because
// they are not buildable leaves. A lookup failure yields nil (no filter) so a
// transient tracker error never silently narrows the resume scan to nothing;
// the project guard still backstops a cross-project checkpoint. Matching is
// case-insensitive on the trimmed id.
func epicChildFilter(ctx context.Context, tr tracker.Tracker, epic string) func(string) bool {
	subs, err := tr.SubIssues(ctx, epic)
	if err != nil {
		return nil
	}
	subs = leafSubs(subs)
	allow := map[string]bool{strings.ToUpper(strings.TrimSpace(epic)): true}
	for _, s := range subs {
		if id := strings.ToUpper(strings.TrimSpace(s.ID)); id != "" {
			allow[id] = true
		}
	}
	return func(id string) bool { return allow[strings.ToUpper(strings.TrimSpace(id))] }
}

// runEpicLoop runs the loop scoped to epic (or the team ready-queue when epic is
// empty), processing at most max tickets. Run once on an epic uses max=1.
func (a *appActions) runEpicLoop(ctx context.Context, epic string, r console.Renderer, max int) {
	if err := a.ensure(); err != nil {
		r.LoopDone(console.SessionSummary{Err: err})
		return
	}
	a.pipe.Renderer = r
	a.pipe.EpicID = epic
	a.eng.scope = scopeFor(a.cfg, epic)
	a.eng.resumeKeep = nil
	if epic != "" {
		if err := a.pipe.EnsureOwnedProject(ctx, epic); err != nil {
			r.Logf("✗ %v", err)
			r.LoopDone(console.SessionSummary{Err: err})
			return
		}
		// Restrict the resume scan to this epic and its children so a stale
		// checkpoint for an unrelated ticket in the same runs/ dir is skipped
		// rather than resumed before the epic's real next child.
		a.eng.resumeKeep = epicChildFilter(ctx, a.tracker, epic)
	}
	total := func(ids []string) (int, float64, bool) {
		t, c := 0, 0.0
		metered := true
		for _, id := range ids {
			tk, cs, m := a.sink.SessionTotal(id)
			t += tk
			c += cs
			metered = metered && m
		}
		return t, math.Round(c*100) / 100, metered
	}
	result := func(id string, elapsed time.Duration) console.TicketResult {
		tk, cs, metered := a.sink.SessionTotal(id)
		return console.TicketResult{
			ID:            id,
			Title:         a.store.Get(id, "TITLE"),
			Phase:         a.store.Get(id, "PHASE"),
			Branch:        a.store.Get(id, "BRANCH"),
			PRURL:         a.store.Get(id, "PR_URL"),
			Tokens:        tk,
			Cost:          math.Round(cs*100) / 100,
			CostMetered:   metered,
			Elapsed:       elapsed,
			FailureReason: a.store.Get(id, "FAILURE_REASON"),
		}
	}
	start := time.Now()
	processed, lerr := runLoop(ctx, a.eng, loopParams{Max: max, Poller: usagePoller(a.cfg, a.log), Report: a.reg.SetState}, r, result)
	a.reportAfterRun(lerr)
	tk, cost, metered := total(processed)
	r.LoopDone(applyFault(console.SessionSummary{
		Tickets:     len(processed),
		TotalTokens: tk,
		TotalCost:   cost,
		CostMetered: metered,
		Elapsed:     time.Since(start),
		Err:         lerr,
		Paused:      pipeline.IsPaused(lerr),
	}, lerr))
}

// RunTicket runs exactly the ticket the user chose in the run-once picker,
// resuming its saved checkpoint when it has one and otherwise starting fresh from
// a clean base. Progress routes to the dashboard renderer r, always closing with
// r.LoopDone so the shell flips to the summary.
//
// A non-empty provider is an ephemeral single-run override of the default: it is
// snapshotted, applied, the built deps invalidated so ensure() rebuilds the
// router/tracker/pipeline for it, and restored on return — so the menu and later
// runs fall back to the config default. Per-phase Routes and FALLBACK_PROVIDERS
// are untouched (they layer on top of whichever default is active).
func (a *appActions) RunTicket(ctx context.Context, id, provider string, r console.Renderer) {
	if provider != "" && provider != a.cfg.Provider {
		orig := a.cfg.Provider
		a.cfg.Provider = provider
		a.built = false
		defer func() {
			a.cfg.Provider = orig
			a.built = false
		}()
	}
	if err := a.ensure(); err != nil {
		r.LoopDone(console.SessionSummary{Err: err})
		return
	}
	a.pipe.Renderer = r

	// Epic guard: a parent issue is a container, not a buildable leaf. If the chosen
	// ticket has leaf sub-issues, descend into the epic flow — pick the next eligible
	// child and build it on the epic branch — instead of building the epic directly.
	// Nested epics (sub-issues that themselves have children) are ignored here because
	// they are not buildable leaves. Capped at one ticket so "Run once" still means one.
	// Mirrors the CLI `trau <epic>` descent so every entry point agrees.
	if subs, err := a.tracker.SubIssues(ctx, id); err == nil && len(leafSubs(subs)) > 0 {
		r.Logf("%s is an epic → running its next eligible sub-issue", id)
		a.runEpicLoop(ctx, id, r, 1)
		return
	}

	// A fresh leaf run auto-stashes any uncommitted WIP in EnsureCleanBase below;
	// restore it once this ticket is done (no-op when nothing was stashed, and when
	// resuming an existing checkpoint).
	defer a.pipe.RestoreWIP(ctx)

	// Leaf ticket: under epic flow, if it belongs to an epic, build it ON the epic
	// branch (and have its PR target that branch) instead of branching off the base.
	// Resolve the parent fresh each run — and clear any stale value from a prior epic
	// run on this shared pipeline — so the decision tracks this ticket alone.
	a.pipe.EpicID = ""
	if a.cfg.EpicFlow {
		if parent := parentEpic(ctx, a.tracker, id); parent != "" {
			r.Logf("%s is a sub-issue of epic %s → building on the epic branch", id, parent)
			a.pipe.EpicID = parent
		}
	}

	if pl := usagePoller(a.cfg, a.log); pl != nil {
		pctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go pl.Run(pctx)
	}

	start := time.Now()
	phase := a.store.Get(id, "PHASE")
	var lerr error
	if phase == "" {
		lerr = a.pipe.EnsureCleanBase(ctx)
	}
	if lerr == nil {
		r.Logf("▶ %s", id)
		a.reg.SetState(registry.StateWorking, id, phase)
		if err := a.pipe.Resume(ctx, id, phase); err != nil && !errors.Is(err, pipeline.ErrAlreadyDone) {
			lerr = err
		}
		tk, cs, metered := a.sink.SessionTotal(id)
		r.TicketDone(console.TicketResult{
			ID:            id,
			Title:         a.store.Get(id, "TITLE"),
			Phase:         a.store.Get(id, "PHASE"),
			Branch:        a.store.Get(id, "BRANCH"),
			PRURL:         a.store.Get(id, "PR_URL"),
			Tokens:        tk,
			Cost:          math.Round(cs*100) / 100,
			CostMetered:   metered,
			Elapsed:       time.Since(start),
			FailureReason: a.store.Get(id, "FAILURE_REASON"),
		})
	}

	a.reportAfterRun(lerr)
	tk, cs, metered := a.sink.SessionTotal(id)
	r.LoopDone(applyFault(console.SessionSummary{
		Tickets:     1,
		TotalTokens: tk,
		TotalCost:   math.Round(cs*100) / 100,
		CostMetered: metered,
		Elapsed:     time.Since(start),
		Err:         lerr,
		Paused:      pipeline.IsPaused(lerr),
	}, lerr))
}

type providerConfig struct {
	bin    string
	flags  string
	model  string
	effort string
	extra  map[string]string
}

func providerConfigFor(cfg config.Config, provider string) providerConfig {
	switch provider {
	case "claude":
		return providerConfig{
			bin:    cfg.ClaudeBin,
			flags:  cfg.ClaudeFlags,
			model:  cfg.ClaudeModel,
			effort: cfg.ClaudeEffort,
			extra: map[string]string{
				"disallowed_tools": cfg.ClaudeDisallowedTools,
				"result_dir":       cfg.RunsDir,
			},
		}
	case "codex":
		return providerConfig{
			bin:    cfg.CodexBin,
			flags:  cfg.CodexFlags,
			model:  cfg.CodexModel,
			effort: cfg.CodexEffort,
			extra: map[string]string{
				"profile":    cfg.CodexProfile,
				"result_dir": cfg.RunsDir,
			},
		}
	case "kimi":
		return providerConfig{
			bin:    cfg.KimiBin,
			flags:  cfg.KimiFlags,
			model:  cfg.KimiModel,
			effort: "",
			extra:  map[string]string{"result_dir": cfg.RunsDir},
		}
	}
	return providerConfig{extra: map[string]string{}}
}

func buildRouter(cfg config.Config, log *event.Log, sink agent.TokenSink, transcripts agent.TranscriptSink, stderr io.Writer) (agent.Runner, error) {
	reg := agent.DefaultRegistry()
	used := map[string]bool{cfg.Provider: true}

	defPC := providerConfigFor(cfg, cfg.Provider)
	def, err := buildBackend(reg, cfg, cfg.Provider, defPC.model, defPC.effort, "", log, sink, transcripts)
	if err != nil {
		return nil, err
	}

	specs := map[string]string{}
	for phase, spec := range cfg.Routes {
		specs[phase] = spec
	}
	// A Claude phase whose per-phase disallowed-tools resolve differently from the
	// provider default needs its own backend so its tool policy — and the preamble
	// derived from it — diverge from the default backend that serves the unrouted
	// phases. This is what routes build/verify through the Explore opt-in.
	if cfg.Provider == "claude" {
		for _, phase := range agent.Phases {
			if _, ok := specs[phase]; ok {
				continue
			}
			if cfg.PhaseDisallowedTools(phase) != cfg.ClaudeDisallowedTools {
				specs[phase] = cfg.Provider
			}
		}
	}

	routes := map[string]agent.Runner{}
	for phase, spec := range specs {
		provider, model, effort, err := parseRoute(reg, spec, cfg)
		if err != nil {
			return nil, fmt.Errorf("%s phase route: %w", phase, err)
		}
		b, err := buildBackend(reg, cfg, provider, model, effort, phase, log, sink, transcripts)
		if err != nil {
			return nil, fmt.Errorf("%s phase route %q: %w", phase, spec, err)
		}
		routes[phase] = b
		used[provider] = true
	}

	emitProviderNotes(reg, used, cfg, stderr)
	if len(routes) == 0 {
		return def, nil
	}
	return agent.NewRouter(def, routes), nil
}

func emitProviderNotes(reg agent.Registry, used map[string]bool, cfg config.Config, stderr io.Writer) {
	if stderr == nil {
		return
	}
	repoRoot := cfg.RepoRoot
	con := console.New(stderr, stderr)
	autoInstallSkills(cfg, con)
	warnMissingRequiredSkills(cfg, con)
	for _, name := range reg.Names() {
		if !used[name] {
			continue
		}
		spec, ok := reg.Lookup(name)
		if !ok {
			continue
		}
		if spec.NeedsSkills {
			r := agent.CheckSkillReadiness(repoRoot)
			if !r.HasSkills {
				msg := agent.MissingSkillsMessage(r)
				if msg == "" {
					msg = "no repo skills found — phase prompts assume skills/slash-commands are available"
				}
				con.Logf("⚠ %s: %s", name, msg)
			} else {
				con.Logf("↳ %s: found skills in %s", name, strings.Join(r.FoundDirs, ", "))
			}
		}
		if name == "kimi" {
			con.Logf("↳ %s: token usage is recovered from the session log; per-call dollar cost is not metered (shown as n/a)", name)
		}
	}
}

// warnMissingRequiredSkills flags REQUIRED_SKILLS names that are not installed
// in the repo, so a mistyped or uninstalled skill surfaces at loop start rather
// than silently dropping out of the build prompt. Advisory only — the run
// proceeds and the build prompt names whichever required skills are present.
func warnMissingRequiredSkills(cfg config.Config, con *console.Console) {
	missing := agent.MissingRequiredSkills(cfg.RepoRoot, cfg.RequiredSkills)
	if len(missing) == 0 {
		return
	}
	con.Logf("⚠ REQUIRED_SKILLS not installed in this repo: %s — the build prompt can only name installed skills; install them or fix REQUIRED_SKILLS", strings.Join(missing, ", "))
}

// autoInstallSkills installs the curated recommended skill set at loop start
// when AUTO_INSTALL_SKILLS is on and the repo has none. Only the pinned
// recommendations for the detected project type are installed — never skill
// search results. Failures warn and continue; the loop runs either way.
func autoInstallSkills(cfg config.Config, con *console.Console) {
	if !cfg.AutoInstallSkills {
		return
	}
	r := agent.CheckSkillReadiness(cfg.RepoRoot)
	if r.HasSkills || len(r.Missing) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, rec := range r.Missing {
		con.Logf("↳ installing skill %s (%s)", rec.Name, rec.Package)
		if err := agent.InstallSkill(ctx, cfg.RepoRoot, rec); err != nil {
			con.Logf("⚠ %v", err)
		}
	}
	if has, dirs := agent.CheckSkills(cfg.RepoRoot); has {
		con.Logf("↳ skills installed into %s — review and commit them", strings.Join(dirs, ", "))
	}
}

func buildBackend(reg agent.Registry, cfg config.Config, provider, model, effort, phase string, log *event.Log, sink agent.TokenSink, transcripts agent.TranscriptSink) (agent.Runner, error) {
	spec, ok := reg.Lookup(provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (expected: %s)", provider, strings.Join(reg.Names(), " | "))
	}
	pc := providerConfigFor(cfg, provider)
	if provider == "claude" {
		pc.extra["disallowed_tools"] = cfg.PhaseDisallowedTools(phase)
	}
	if _, err := exec.LookPath(pc.bin); err != nil {
		return nil, fmt.Errorf("provider %q: %q not found on PATH", provider, pc.bin)
	}
	var sizeFn func() (int, int)
	if cfg.LiveView {
		sizeFn = func() (int, int) {
			w, h, ok := console.TerminalSize(os.Stdout)
			if !ok {
				return 0, 0
			}
			return tui.LiveAgentSize(w, h)
		}
	}
	return spec.New(agent.BackendParams{
		Bin:                pc.bin,
		Flags:              strings.Fields(pc.flags),
		Model:              model,
		Effort:             effort,
		Dir:                cfg.RepoRoot,
		Preamble:           cfg.PhasePreamble(provider, phase),
		Cols:               cfg.AgentCols,
		Rows:               cfg.AgentRows,
		SizeFn:             sizeFn,
		Timeout:            time.Duration(cfg.AgentTimeout) * time.Second,
		StallWindow:        time.Duration(cfg.AgentStallWindow) * time.Second,
		StripMechanicalMCP: cfg.StripMechanicalMCP,
		Log:                log,
		Tokens:             sink,
		Transcripts:        transcripts,
		Extra:              pc.extra,
	})
}

func parseRoute(reg agent.Registry, spec string, cfg config.Config) (provider, model, effort string, err error) {
	parts := strings.SplitN(spec, ":", 3)
	provider = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		model = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		effort = strings.TrimSpace(parts[2])
	}
	if _, ok := reg.Lookup(provider); !ok {
		return "", "", "", fmt.Errorf("unknown provider %q (expected: %s)", provider, strings.Join(reg.Names(), " | "))
	}
	pc := providerConfigFor(cfg, provider)
	if model == "" {
		model = pc.model
	}
	if effort == "" {
		effort = pc.effort
	}
	return provider, model, effort, nil
}

func boolEnvValue(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// legacyRunsTracked reports whether the cwd-relative legacy runs/ dir has any
// git-tracked files. Auto-migration skips a tracked dir: os.Rename would surface its
// files as tracked deletions and trip EnsureCleanBase. Errors (no git, not a repo)
// read as untracked — there is nothing to dirty.
func legacyRunsTracked() bool {
	return exec.Command("git", "ls-files", "--error-unmatch", "runs").Run() == nil
}

// newEventLog builds the loop's event log and a close that flushes its tail before
// the process exits. Normally events flow to the hub over HTTP (ADR 0008) — the
// child writes no event-log file — and to human for the in-process feed. Under
// TRAU_LOG_JSON the diagnostic stderr stream stands in and writes no run data.
func newEventLog(cfg config.Config, stderr io.Writer, human func(event.Event)) (*event.Log, func()) {
	if os.Getenv("TRAU_LOG_JSON") == "1" {
		return event.New(stderr), func() {}
	}
	hub := hubclient.New(hubBaseURL(cfg), cfg.ServeToken)
	sink := hubevent.New(hub, repoName(cfg.RepoRoot), cfg.HubWriteBufferBytes, time.Duration(cfg.HubWriteRetryWindow)*time.Second)
	return event.NewSink(sink).WithHuman(human), sink.Close
}
