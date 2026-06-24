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
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/budget"
	"github.com/RomkaLTU/trau/internal/checks"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/doctor"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/pipeline"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tokens"
	"github.com/RomkaLTU/trau/internal/tracker"
	"github.com/RomkaLTU/trau/internal/tui"
)

var version = "dev"

const usage = `trau — autonomous Linear-ticket dev loop

Usage:
  trau [flags]               run the loop: resume any in-flight ticket, else pick the next ready one
  trau <ID>                  run a single ticket (e.g. ENG-123), or its sub-issues if it is an epic
  trau doctor                preflight check: git/gh/provider/config/labels/write perms
  trau --status [--json]     show saved ticket checkpoints with token/cost totals
  trau --dry-run             print the next eligible ticket without doing any work
  trau --reset <ID>          drop the branch + state and re-queue the ticket (refuses if already merged; --force overrides)

Flags:
  --parent <ID>     treat <ID> as an epic and process its sub-issues (a bare <PREFIX>-<n> arg is equivalent)
  --once            stop after one ticket
  --max <N>         cap iterations for this run (overrides MAX_ITERATIONS)
  --no-resume       skip the resume scan; always pick a fresh ticket
  --provider <name> override the configured provider (claude | codex | kimi)
  --repo <path>     target app repo (else TRAU_REPO_ROOT, else the cwd git top-level)
  --dry-run         print the next eligible ticket and exit
  --reset <ID>      reset a ticket and exit
  --force           with --reset, reset even a ticket whose code is already merged
  --status          print saved checkpoints and exit
  --json            emit --status as machine-readable JSON
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

	for _, id := range []string{opts.Parent, opts.ResetID} {
		if err := config.ValidatePrefix(id, cfg.IssuePrefix); err != nil {
			return console.Actionable(err, "validate ticket id",
				fmt.Sprintf("set ISSUE_PREFIX (or LINEAR_TEAM) to this tracker's key, or pass a %s-<n> ticket", cfg.IssuePrefix))
		}
	}

	if len(args) == 0 && cfg.TUI && !opts.NoTUI && console.IsTerminal(stdout) && os.Getenv("TRAU_LOG_JSON") != "1" {
		return runSession(ctx, cfg, opts, stdout, stderr)
	}

	con := newRenderer(stdout, stderr, cfg, opts, cancelLoop)

	var log *event.Log
	if os.Getenv("TRAU_LOG_JSON") == "1" {
		log = event.New(stderr)
	} else {
		log = event.New(newEventFile(cfg.RunsDir)).WithHuman(con.Event)
	}

	sink := tokens.New(cfg.RunsDir)

	if opts.Status {
		store := state.NewStore(cfg.RunsDir)
		var report any
		if lim := budgetLimits(cfg); lim.Enabled() {
			today := time.Now().Format("2006-01-02")
			dt, dc, dm := sink.DayTotal(today)
			report = budget.Report{Date: today, Limits: lim, Today: budget.Spend{Tokens: dt, Cost: dc, Metered: dm}}
		}
		if opts.JSON {
			return store.StatusJSON(stdout, sink.Total, report)
		}
		con.Logf("Saved ticket checkpoints:")
		store.Status(stdout, sink.Total)
		if r, ok := report.(budget.Report); ok {
			_, _ = fmt.Fprintf(stdout, "  %s\n", r.Summary())
		}
		return nil
	}

	runner, err := buildRouter(cfg, log, sink, stderr)
	if err != nil {
		return usageError{err}
	}

	pm, err := buildTracker(cfg, runner)
	if err != nil {
		return usageError{err}
	}

	if opts.ResetID != "" {
		repoRoot, err := config.ResolveRepoRoot(opts.Repo, cfg.RepoRoot, config.GitToplevel)
		if err != nil {
			return console.Actionable(err, "resolve target repo", "pass --repo <path>, set TRAU_REPO_ROOT, or run inside a git repository")
		}
		pipe, err := buildPipeline(cfg, runner, repoRoot, pm, sink, log, con)
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
			if len(subs) == 0 {
				con.Logf("  %s has no sub-issues — processing as standalone ticket", epicID)
				forcedID = epicID
				epicID = ""
				opts.Once = true
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
	p, err := buildPipeline(cfg, runner, repoRoot, pm, sink, log, con)
	if err != nil {
		return err
	}
	p.EpicID = epicID

	maxIter := cfg.MaxIterations
	if opts.Max >= 0 {
		maxIter = opts.Max
	}
	budgetSuffix := ""
	if lim := budgetLimits(cfg); lim.Enabled() {
		budgetSuffix = " · " + lim.Summary()
	}
	con.Logf("provider=%s · AUTO_MERGE=%v · max=%d%s%s", cfg.Provider, cfg.AutoMerge, maxIter, parentSuffix, budgetSuffix)

	eng := &realEngine{pipe: p, tracker: pm, scope: scope}

	total := func(ids []string) (int, float64, bool) {
		t, c := 0, 0.0
		metered := true
		for _, id := range ids {
			tk, cs, m := sink.Total(id)
			t += tk
			c += cs
			metered = metered && m
		}
		return t, math.Round(c*100) / 100, metered
	}

	result := func(id string, elapsed time.Duration) console.TicketResult {
		tk, cs, metered := sink.Total(id)
		return console.TicketResult{
			ID:          id,
			Title:       p.State.Get(id, "TITLE"),
			Phase:       p.State.Get(id, "PHASE"),
			PRURL:       p.State.Get(id, "PR_URL"),
			Tokens:      tk,
			Cost:        math.Round(cs*100) / 100,
			CostMetered: metered,
			Elapsed:     elapsed,
		}
	}
	start := time.Now()
	processed, lerr := runLoop(ctx, eng, loopParams{
		Once:         opts.Once,
		Max:          maxIter,
		NoResume:     opts.NoResume,
		ParentSuffix: parentSuffix,
		ForcedID:     forcedID,
	}, con, result)

	tk, cost, metered := total(processed)
	con.LoopDone(console.SessionSummary{
		Tickets:     len(processed),
		TotalTokens: tk,
		TotalCost:   cost,
		CostMetered: metered,
		Elapsed:     time.Since(start),
		Err:         lerr,
		Paused:      pipeline.IsPaused(lerr),
	})
	con.Wait()
	return lerr
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
	return tracker.New(cfg.TrackerProvider, runner, tracker.Config{
		Team:            cfg.LinearTeam,
		Project:         cfg.Project,
		ReadyLabel:      cfg.ReadyLabel,
		QuarantineLabel: cfg.QuarantineLabel,
		APIKey:          cfg.LinearAPIKey,
	})
}

// scopeFor builds a picker scope carrying the configured issue prefix so whole-team
// picks (which have no parent id to derive a prefix from) match the right tracker.
func scopeFor(cfg config.Config, parent string) tracker.Scope {
	return tracker.Scope{Parent: parent, Team: cfg.LinearTeam, Prefix: cfg.IssuePrefix}
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
	return tui.New(stdout, stderr, onInterrupt)
}

func buildPipeline(cfg config.Config, runner agent.Runner, repoRoot string, pm tracker.Tracker, sink *tokens.Sink, log *event.Log, con console.Renderer) (*pipeline.Pipeline, error) {
	var verifyChecks []checks.Check
	if cfg.VerifyChecks {
		loaded, _, err := checks.Load(repoRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v — falling back to default verify checks\n", err)
			loaded = checks.Defaults()
		}
		verifyChecks = loaded
	}
	panel, err := buildPanel(cfg, log, sink)
	if err != nil {
		return nil, err
	}
	return &pipeline.Pipeline{
		Runner:         runner,
		State:          state.NewStore(cfg.RunsDir),
		Git:            pipeline.ExecGit{Repo: repoRoot},
		GitHub:         pipeline.ExecGitHub{Repo: repoRoot},
		Tracker:        pm,
		Tokens:         sink,
		Budget:         budgetLimits(cfg),
		RunsDir:        cfg.RunsDir,
		Base:           cfg.BaseBranch,
		Remote:         cfg.Remote,
		Prefix:         cfg.IssuePrefix,
		MaxRepairs:     cfg.MaxRepairs,
		MaxBugfixes:    cfg.MaxBugfixes,
		Checks:         verifyChecks,
		VerifyPanel:    panel,
		PanelPolicy:    cfg.VerifyPanelPolicy,
		BrowserVerify:  cfg.BrowserVerify,
		AppURL:         cfg.AppURL,
		AutoMerge:      cfg.AutoMerge,
		MergeMethod:    cfg.MergeMethod,
		ExpectedChecks: cfg.ExpectedChecks,
		CITimeout:      cfg.CITimeout,
		CIPoll:         cfg.CIPoll,
		Lessons:        cfg.Lessons,
		LessonsDistill: cfg.LessonsDistill,
		Renderer:       con,
	}, nil
}

// buildPanel constructs the cross-vendor verify panel from VERIFY_PANEL — one
// fresh backend per provider:model:effort spec, reusing the same route parsing and
// backend construction as phase routes so each member can be a different provider.
// Returns nil when no panel is configured (the single-verifier default). A spec
// naming an unknown provider or whose binary is missing from PATH is a startup
// error. Repeated providers get a numeric suffix (claude, claude2) so their
// verdict files and ledger labels stay distinct.
func buildPanel(cfg config.Config, log *event.Log, sink agent.TokenSink) ([]pipeline.Verifier, error) {
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
		runner, err := buildBackend(reg, cfg, provider, model, effort, log, sink)
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

type engine interface {
	ResumeTarget() (id, phase string)

	InferredResume(ctx context.Context) (id, phase string)

	EnsureCleanBase(ctx context.Context) error

	Pick(ctx context.Context) (string, error)

	Process(ctx context.Context, id, from string) error

	BudgetExhausted() (reason string, stop bool)
}

type realEngine struct {
	pipe    *pipeline.Pipeline
	tracker tracker.Tracker
	scope   tracker.Scope
}

func (e *realEngine) ResumeTarget() (string, string) { return e.pipe.State.ResumeTarget() }
func (e *realEngine) InferredResume(ctx context.Context) (string, string) {
	return e.pipe.InferredResume(ctx)
}
func (e *realEngine) EnsureCleanBase(ctx context.Context) error { return e.pipe.EnsureCleanBase(ctx) }
func (e *realEngine) Pick(ctx context.Context) (string, error)  { return e.tracker.Pick(ctx, e.scope) }
func (e *realEngine) Process(ctx context.Context, id, from string) error {
	return e.pipe.Resume(ctx, id, from)
}
func (e *realEngine) BudgetExhausted() (string, bool) { return e.pipe.BudgetExhausted() }

type loopParams struct {
	Once         bool
	Max          int
	NoResume     bool
	ParentSuffix string
	ForcedID     string
}

func runLoop(ctx context.Context, eng engine, p loopParams, con console.Renderer, result func(id string, elapsed time.Duration) console.TicketResult) ([]string, error) {
	var processed []string
	for {
		select {
		case <-ctx.Done():
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
			t0 := time.Now()
			err := eng.Process(ctx, rid, rphase)
			if pipeline.IsPaused(err) {
				return processed, err
			}
			if errors.Is(err, pipeline.ErrAlreadyDone) {
				con.Logf("  %s already done — skipping", rid)
				continue
			}
			processed = append(processed, rid)
			con.TicketDone(result(rid, time.Since(t0)))
		} else if p.ForcedID != "" {
			con.Logf("▶ [%d] %s", len(processed)+1, p.ForcedID)
			t0 := time.Now()
			if err := eng.Process(ctx, p.ForcedID, ""); pipeline.IsPaused(err) {
				return processed, err
			}
			processed = append(processed, p.ForcedID)
			con.TicketDone(result(p.ForcedID, time.Since(t0)))
			if p.Once {
				con.Logf("--once: stopping")
				break
			}

			p.ForcedID = ""
		} else {
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
			t0 := time.Now()
			if err := eng.Process(ctx, id, ""); pipeline.IsPaused(err) {
				return processed, err
			}
			processed = append(processed, id)
			con.TicketDone(result(id, time.Since(t0)))
		}

		if p.Once {
			con.Logf("--once: stopping")
			break
		}
	}
	return processed, nil
}

func runSession(ctx context.Context, cfg config.Config, opts config.Options, stdout, stderr io.Writer) error {
	holder := tui.NewRenderer()

	var log *event.Log
	if os.Getenv("TRAU_LOG_JSON") == "1" {
		log = event.New(stderr)
	} else {
		log = event.New(newEventFile(cfg.RunsDir)).WithHuman(holder.Event)
	}

	maxIter := cfg.MaxIterations
	if opts.Max >= 0 {
		maxIter = opts.Max
	}
	acts := &appActions{
		cfg:     cfg,
		opts:    opts,
		stderr:  io.Discard,
		log:     log,
		sink:    tokens.New(cfg.RunsDir),
		store:   state.NewStore(cfg.RunsDir),
		scope:   scopeFor(cfg, ""),
		maxIter: maxIter,
	}
	return tui.RunSession(ctx, stdout, holder, acts)
}

type appActions struct {
	cfg     config.Config
	opts    config.Options
	stderr  io.Writer
	log     *event.Log
	sink    *tokens.Sink
	store   *state.Store
	scope   tracker.Scope
	maxIter int

	built    bool
	buildErr error
	pipe     *pipeline.Pipeline
	tracker  tracker.Tracker
	eng      *realEngine
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
		LinearAPIKey:    a.cfg.LinearAPIKey,
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
		"EPIC_FLOW":        epicFlowValue(setup.EpicFlow),
	}
	if err := config.WriteProjectEnv(path, values); err != nil {
		return tui.SetupResult{}, fmt.Errorf("write project env: %w", err)
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

	runner, err := buildRouter(a.cfg, a.log, a.sink, a.stderr)
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
func (a *appActions) DetectTeams(ctx context.Context, trackerProvider, aiProvider string) (tui.TeamDetection, error) {
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
		runner, err := buildRouter(cfg, a.log, a.sink, a.stderr)
		if err != nil {
			return tui.TeamDetection{Label: label}, err
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
			return tui.TeamDetection{Label: label}, err
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
	return tui.MenuInfo{
		Version:       version,
		Provider:      a.cfg.Provider,
		Model:         modelEffortTag(model, effort),
		Base:          a.cfg.BaseBranch,
		Prefix:        a.cfg.IssuePrefix,
		MaxIterations: a.maxIter,
		AutoMerge:     a.cfg.AutoMerge,
		InFlight:      inFlight,
		Done:          done,
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
		rows = append(rows, tui.StatusRow{
			ID:          id,
			Title:       a.store.Get(id, "TITLE"),
			Phase:       a.store.Get(id, "PHASE"),
			PRURL:       a.store.Get(id, "PR_URL"),
			Tokens:      tok,
			Cost:        cost,
			CostMetered: metered,
		})
	}
	return rows
}

func (a *appActions) ensure() error {
	if a.built {
		return a.buildErr
	}
	a.built = true
	runner, err := buildRouter(a.cfg, a.log, a.sink, a.stderr)
	if err != nil {
		a.buildErr = err
		return err
	}
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
	pipe, err := buildPipeline(a.cfg, runner, repoRoot, a.tracker, a.sink, a.log, nil)
	if err != nil {
		a.buildErr = err
		return err
	}
	a.pipe = pipe
	a.eng = &realEngine{pipe: a.pipe, tracker: a.tracker, scope: a.scope}
	return nil
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
		out = append(out, tui.SubIssue{ID: s.ID, Title: s.Title})
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
	total := func(ids []string) (int, float64, bool) {
		t, c := 0, 0.0
		metered := true
		for _, id := range ids {
			tk, cs, m := a.sink.Total(id)
			t += tk
			c += cs
			metered = metered && m
		}
		return t, math.Round(c*100) / 100, metered
	}
	result := func(id string, elapsed time.Duration) console.TicketResult {
		tk, cs, metered := a.sink.Total(id)
		return console.TicketResult{
			ID:          id,
			Title:       a.store.Get(id, "TITLE"),
			Phase:       a.store.Get(id, "PHASE"),
			PRURL:       a.store.Get(id, "PR_URL"),
			Tokens:      tk,
			Cost:        math.Round(cs*100) / 100,
			CostMetered: metered,
			Elapsed:     elapsed,
		}
	}
	start := time.Now()
	processed, lerr := runLoop(ctx, a.eng, loopParams{Max: max}, r, result)
	tk, cost, metered := total(processed)
	r.LoopDone(console.SessionSummary{
		Tickets:     len(processed),
		TotalTokens: tk,
		TotalCost:   cost,
		CostMetered: metered,
		Elapsed:     time.Since(start),
		Err:         lerr,
		Paused:      pipeline.IsPaused(lerr),
	})
}

// RunTicket runs exactly the ticket the user chose in the run-once picker,
// resuming its saved checkpoint when it has one and otherwise starting fresh from
// a clean base. Progress routes to the dashboard renderer r, always closing with
// r.LoopDone so the shell flips to the summary.
func (a *appActions) RunTicket(ctx context.Context, id string, r console.Renderer) {
	if err := a.ensure(); err != nil {
		r.LoopDone(console.SessionSummary{Err: err})
		return
	}
	a.pipe.Renderer = r

	// Epic guard: a parent issue is a container, not a buildable leaf. If the chosen
	// ticket has sub-issues, descend into the epic flow — pick the next eligible child
	// and build it on the epic branch — instead of building the epic directly. Capped
	// at one ticket so "Run once" still means one. Mirrors the CLI `trau <epic>` descent
	// so every entry point agrees.
	if subs, err := a.tracker.SubIssues(ctx, id); err == nil && len(subs) > 0 {
		r.Logf("%s is an epic → running its next eligible sub-issue", id)
		a.runEpicLoop(ctx, id, r, 1)
		return
	}

	start := time.Now()
	phase := a.store.Get(id, "PHASE")
	var lerr error
	if phase == "" {
		lerr = a.pipe.EnsureCleanBase(ctx)
	}
	if lerr == nil {
		r.Logf("▶ %s", id)
		if err := a.pipe.Resume(ctx, id, phase); err != nil && !errors.Is(err, pipeline.ErrAlreadyDone) {
			lerr = err
		}
		tk, cs, metered := a.sink.Total(id)
		r.TicketDone(console.TicketResult{
			ID:          id,
			Title:       a.store.Get(id, "TITLE"),
			Phase:       a.store.Get(id, "PHASE"),
			PRURL:       a.store.Get(id, "PR_URL"),
			Tokens:      tk,
			Cost:        math.Round(cs*100) / 100,
			CostMetered: metered,
			Elapsed:     time.Since(start),
		})
	}

	tk, cs, metered := a.sink.Total(id)
	r.LoopDone(console.SessionSummary{
		Tickets:     1,
		TotalTokens: tk,
		TotalCost:   math.Round(cs*100) / 100,
		CostMetered: metered,
		Elapsed:     time.Since(start),
		Err:         lerr,
		Paused:      pipeline.IsPaused(lerr),
	})
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
			extra:  map[string]string{"profile": cfg.CodexProfile},
		}
	case "kimi":
		return providerConfig{
			bin:    cfg.KimiBin,
			flags:  cfg.KimiFlags,
			model:  cfg.KimiModel,
			effort: "",
			extra:  map[string]string{},
		}
	}
	return providerConfig{extra: map[string]string{}}
}

func buildRouter(cfg config.Config, log *event.Log, sink agent.TokenSink, stderr io.Writer) (agent.Runner, error) {
	reg := agent.DefaultRegistry()
	used := map[string]bool{cfg.Provider: true}

	defPC := providerConfigFor(cfg, cfg.Provider)
	def, err := buildBackend(reg, cfg, cfg.Provider, defPC.model, defPC.effort, log, sink)
	if err != nil {
		return nil, err
	}

	routes := map[string]agent.Runner{}
	for phase, spec := range cfg.Routes {
		provider, model, effort, err := parseRoute(reg, spec, cfg)
		if err != nil {
			return nil, fmt.Errorf("%s phase route: %w", phase, err)
		}
		b, err := buildBackend(reg, cfg, provider, model, effort, log, sink)
		if err != nil {
			return nil, fmt.Errorf("%s phase route %q: %w", phase, spec, err)
		}
		routes[phase] = b
		used[provider] = true
	}

	emitProviderNotes(reg, used, cfg.RepoRoot, stderr)
	if len(routes) == 0 {
		return def, nil
	}
	return agent.NewRouter(def, routes), nil
}

func emitProviderNotes(reg agent.Registry, used map[string]bool, repoRoot string, stderr io.Writer) {
	if stderr == nil {
		return
	}
	con := console.New(stderr, stderr)
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

func buildBackend(reg agent.Registry, cfg config.Config, provider, model, effort string, log *event.Log, sink agent.TokenSink) (agent.Runner, error) {
	spec, ok := reg.Lookup(provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (expected: %s)", provider, strings.Join(reg.Names(), " | "))
	}
	pc := providerConfigFor(cfg, provider)
	if _, err := exec.LookPath(pc.bin); err != nil {
		return nil, fmt.Errorf("provider %q: %q not found on PATH", provider, pc.bin)
	}
	return spec.New(agent.BackendParams{
		Bin:      pc.bin,
		Flags:    strings.Fields(pc.flags),
		Model:    model,
		Effort:   effort,
		Dir:      cfg.RepoRoot,
		Preamble: config.Preamble,
		Timeout:  time.Duration(cfg.AgentTimeout) * time.Second,
		Log:      log,
		Tokens:   sink,
		Extra:    pc.extra,
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

func epicFlowValue(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

func newEventFile(runsDir string) io.Writer {
	return &eventFile{path: filepath.Join(runsDir, "events.jsonl")}
}

type eventFile struct {
	path string
	mu   sync.Mutex
	f    *os.File
	bad  bool
}

func (e *eventFile) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.f == nil && !e.bad {
		if err := os.MkdirAll(filepath.Dir(e.path), 0o755); err != nil {
			e.bad = true
			return len(p), nil
		}
		f, err := os.OpenFile(e.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			e.bad = true
			return len(p), nil
		}
		e.f = f
	}
	if e.f != nil {
		_, _ = e.f.Write(p)
	}
	return len(p), nil
}
