// Package pipeline runs a ticket through the fixed phases — build → handoff →
// verify → commit → PR → CI → merge — one phase per fresh, isolated agent
// process. Each phase records a durable checkpoint via internal/state the moment
// it completes, so a crash only loses the phase in flight; resumption skips
// checkpointed phases (Resume) or adopts a parked feature branch
// (InferredResume). Every collaborator is an injected seam (agent runner, git,
// state store, token bucket).
package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sync/errgroup"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/budget"
	"github.com/RomkaLTU/trau/internal/checks"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/proofs"
	"github.com/RomkaLTU/trau/internal/sanitize"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// Git is the subset of git operations the pipeline needs, scoped to the target
// repo. Stubbed in tests; ExecGit is the real `git -C <repo>` implementation.
// Later phases (commit/PR/merge, resume) widen this interface further.
type Git interface {
	CurrentBranch(ctx context.Context) (string, error)

	AddAll(ctx context.Context) error

	Commit(ctx context.Context, message string, noVerify bool) error

	// Push pushes ref to remote; noVerify adds --no-verify to bypass local hooks.
	// Real deliverable pushes run hooks (noVerify=false); WIP-preservation pushes
	// bypass them (noVerify=true) so saving work is never gated by the repo's checks.
	Push(ctx context.Context, remote, ref string, noVerify bool) error

	// PushDryRun performs git push --dry-run --no-verify: it contacts the remote and
	// reports what the push WOULD do without transferring anything and without running
	// local hooks. A nil result means the remote itself would accept the ref — so if
	// the real push failed, a LOCAL pre-push hook was the only blocker. A non-nil error
	// carries git's own ref-level reason (non-fast-forward, remote rejected) or an
	// auth/network failure — the behavior-based, hook-manager-agnostic push classifier.
	PushDryRun(ctx context.Context, remote, ref string) error

	Checkout(ctx context.Context, ref string, force bool) error

	// CheckoutDetached puts HEAD on ref without claiming a branch (git checkout
	// --detach); force discards local changes as Checkout's does. It is the only way
	// onto a branch another worktree already holds, since git allows exactly one
	// checkout of a branch across a repo's worktrees.
	CheckoutDetached(ctx context.Context, ref string, force bool) error

	// WorktreeHolding returns the path of another linked worktree that has branch
	// checked out, or "" when none does — the structural reason git refuses to
	// check that branch out here.
	WorktreeHolding(ctx context.Context, branch string) (string, error)

	CreateBranch(ctx context.Context, branch, base string) error

	Clean(ctx context.Context) error

	BranchExists(ctx context.Context, branch string) (bool, error)

	FindFeatureBranch(ctx context.Context, id string) (string, error)

	// FindEpicBranch returns the existing local epic/<id>-* branch (or exact
	// epic/<id>), matched by epic ID so a renamed epic never spawns a second branch.
	FindEpicBranch(ctx context.Context, id string) (string, error)

	// FindRemoteEpicBranch returns the existing remote epic/<id>-* branch (or exact
	// epic/<id>) on remote, the cross-machine/fresh-clone counterpart to
	// FindEpicBranch. An error means the remote could not be consulted (indeterminate).
	FindRemoteEpicBranch(ctx context.Context, remote, id string) (string, error)

	DeleteBranch(ctx context.Context, branch string) error

	DeletePushedBranch(ctx context.Context, remote, branch string) error

	StatusPorcelain(ctx context.Context) (string, error)

	// WorktreeDirty reports whether the working tree has any uncommitted change,
	// including untracked files. Unlike StatusPorcelain (tracked-only, for
	// clean-base detection) it counts new files an agent created but has not yet
	// committed, so a build that only adds files is not mistaken for a no-op.
	WorktreeDirty(ctx context.Context) (bool, error)

	// Stash saves uncommitted TRACKED changes under a label (git stash push),
	// leaving untracked files in place to match StatusPorcelain's clean-base
	// semantics, so a fresh run can reset to base without discarding the user's WIP.
	Stash(ctx context.Context, msg string) error

	// StashPop restores the most recent stash onto the working tree (git stash pop).
	// A non-nil error covers the pop-stopped-on-conflict case, where git keeps the
	// stash so it can be resolved by hand.
	StashPop(ctx context.Context) error

	// Commits returns the short SHAs on branch but not base (base..branch).
	Commits(ctx context.Context, base, branch string) ([]string, error)

	// CommitSubject returns the subject line of ref's commit (git log -1 --format=%s).
	CommitSubject(ctx context.Context, ref string) (string, error)

	Pull(ctx context.Context, remote, branch string) error

	// Fetch updates the remote-tracking ref for branch without touching the
	// working tree or the local branch, so a detached checkout can land on an
	// up-to-date tip even when the local branch is unwritable.
	Fetch(ctx context.Context, remote, branch string) error

	// MergeRemote fetches remote/base and merges it into the current branch,
	// reporting conflicted=true when the merge stopped on conflicts (the tree is
	// left with conflict markers for an agent to resolve). A clean merge or an
	// already-up-to-date branch returns (false, nil); a non-conflict failure
	// aborts the merge and returns the error.
	MergeRemote(ctx context.Context, remote, base string) (conflicted bool, err error)

	// MergeAbort aborts an in-progress conflicted merge (git merge --abort).
	MergeAbort(ctx context.Context) error

	// Unmerged returns the still-conflicted paths after a merge, empty when none
	// remain (git diff --name-only --diff-filter=U).
	Unmerged(ctx context.Context) (string, error)

	// ContinueMerge completes a resolved merge by staging all changes and
	// committing with the default merge message; a no-op when the tree is not
	// mid-merge (the resolving agent may already have committed).
	ContinueMerge(ctx context.Context) error

	// RemoteBranchExists reports whether remote/branch exists on the remote
	// (git ls-remote). A missing branch is an expected (false, nil), not an
	// error; only an unreachable remote returns a non-nil error.
	RemoteBranchExists(ctx context.Context, remote, branch string) (bool, error)

	// CheckoutRemoteBranch creates a local branch from remote/branch and checks
	// it out, adopting existing remote work instead of starting fresh.
	CheckoutRemoteBranch(ctx context.Context, remote, branch string) error
}

// Check is one PR status check (gh pr checks --json name,bucket). bucket is gh's
// rollup of the check's conclusion: pass | fail | pending | skipping | cancel.
type Check struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
}

// GitHub is the subset of `gh` operations the closing phases need, scoped to the
// target repo. Stubbed in tests; ExecGitHub is the real `gh` implementation. The
// read methods follow a swallow-and-default convention (a missing PR or a gh
// hiccup reads as "" / no checks) so a transient failure re-polls rather than
// aborting the ticket.
type GitHub interface {
	PRURL(ctx context.Context, branch string) (string, error)

	CreatePR(ctx context.Context, base, head, title, body string) (string, error)

	PRState(ctx context.Context, pr string) (string, error)

	Checks(ctx context.Context, pr string) ([]Check, error)

	Merge(ctx context.Context, pr, method string, deleteBranch bool) error
}

// ErrCIFailed and ErrCITimeout are the two non-green outcomes of pollCI; both map
// to the same "CI not green" give-up reason but are distinguished in the log line.
// ErrAlreadyDone is returned by CIAndMerge when it reconciles a ticket whose PR
// was already merged, and by Resume when the ticket's checkpoint is already
// merged, so the outer loop can skip counting it and keep picking.
var (
	ErrCIFailed    = errors.New("a required CI check failed")
	ErrCITimeout   = errors.New("CI timed out waiting for required checks")
	ErrAlreadyDone = errors.New("ticket already done")
)

// Ledger is the pipeline's view of the token/cost sink: it points the bucket at
// the current ticket (SetTicket) and reports accumulated spend for budget
// enforcement (Total per ticket, DayTotal for the per-day window). The hub-backed
// sink (internal/hubtokens) satisfies it; kept as a narrow interface so pipeline
// doesn't depend on the tokens package.
type Ledger interface {
	SetTicket(id string)
	Total(id string) (tokens int, cost float64, metered bool)
	DayTotal(date string) (tokens int, cost float64, metered bool)
}

// GiveUpError signals a ticket cannot proceed and must be abandoned; the caller
// runs the quarantine/finalize path.
type GiveUpError struct {
	ID     string
	Reason string
}

func (e *GiveUpError) Error() string {
	return fmt.Sprintf("give up on %s: %s", e.ID, e.Reason)
}

// PausedError signals the loop hit an external, blameless stop — a provider
// rate/usage limit — partway through a ticket. Unlike a give-up it does NOT
// quarantine the ticket or file a bug: the work stays on its branch at the last
// checkpoint, and a later run resumes it once the limit clears. The loop driver
// stops picking new tickets when it sees one.
type PausedError struct {
	ID       string
	Phase    string
	Provider string
	Reason   string
}

func (e *PausedError) Error() string {
	return fmt.Sprintf("paused on %s (%s): %s", e.ID, e.Phase, e.Reason)
}

// IsPaused reports whether err is (or wraps) a *PausedError.
func IsPaused(err error) bool {
	var p *PausedError
	return errors.As(err, &p)
}

// AsPaused extracts the *PausedError from err (traversing wraps), or nil when err
// is not a pause. Callers use it to name the ticket a blameless pause left parked.
func AsPaused(err error) *PausedError {
	var p *PausedError
	if errors.As(err, &p) {
		return p
	}
	return nil
}

// FaultError signals a ticket hit an UNEXPECTED error mid-phase — an agent crash
// or timeout, a failed git push, an infra hiccup — that is neither a blameless
// rate-limit pause nor a verified give-up. Unlike a give-up it does NOT file a
// bug or quarantine the ticket: the partial work is committed to the feature
// branch and the ticket is left at its last checkpoint, resumable on a rerun.
// Unlike a swallowed error, the loop stops on it instead of dragging a dirty
// working tree (or an infinitely re-faulting ticket) across the rest of the run.
type FaultError struct {
	ID    string
	Phase string
	Err   error
}

func (e *FaultError) Error() string {
	return fmt.Sprintf("ticket %s could not finish during %s: %v", e.ID, e.Phase, e.Err)
}

func (e *FaultError) Unwrap() error { return e.Err }

// IsFault reports whether err is (or wraps) a *FaultError.
func IsFault(err error) bool {
	var f *FaultError
	return errors.As(err, &f)
}

// StoppedError signals a ticket was cut short by a deliberate stop — web Stop's
// SIGTERM, a takeover, Ctrl-C — rather than by anything going wrong. Like a fault
// it preserves the partial work on the feature branch and leaves PHASE at its last
// checkpoint, but nothing is blamed: it unwraps to context.Canceled so the loop
// driver and the CLI take the ordinary signal path (exit 130) with no fault recap.
type StoppedError struct {
	ID    string
	Phase string
}

func (e *StoppedError) Error() string {
	return fmt.Sprintf("ticket %s stopped during %s", e.ID, NextPhaseLabel(e.Phase))
}

func (e *StoppedError) Unwrap() error { return context.Canceled }

// IsStopped reports whether err is (or wraps) a *StoppedError.
func IsStopped(err error) bool {
	var s *StoppedError
	return errors.As(err, &s)
}

// AsStopped extracts the *StoppedError from err (traversing wraps), or nil when
// err is not a stop. Callers use it to name the ticket the stop left parked.
func AsStopped(err error) *StoppedError {
	var s *StoppedError
	if errors.As(err, &s) {
		return s
	}
	return nil
}

// CrossProjectError signals a refusal: the ticket belongs to a different Linear
// project than the one this repo owns (config PROJECT). It is raised before any
// branch/checkpoint/build work, so nothing is left to clean up — the run simply
// stops with guidance. It is neither a give-up (no quarantine, no filed bug) nor a
// fault; the ticket is untouched and stays runnable from the repo that owns it.
type CrossProjectError struct {
	ID      string
	Owned   string
	Project string
}

func (e *CrossProjectError) Error() string {
	return fmt.Sprintf("%s belongs to Linear project %q, but this repo owns %q — refusing to run it here", e.ID, e.Project, e.Owned)
}

// IsCrossProject reports whether err is (or wraps) a *CrossProjectError.
func IsCrossProject(err error) bool {
	var c *CrossProjectError
	return errors.As(err, &c)
}

// RefusedError signals that the build agent declined to implement a ticket in
// this repository (its final output carried the REFUSED sentinel): the ticket
// targets a different codebase. It is the agent-level backstop behind
// EnsureOwnedProject for setups without a configured PROJECT. The handler resets
// the ticket — empty branch dropped, checkpoint cleared, tracker restored — so
// nothing half-started lingers here, and the loop stops with guidance instead of
// re-picking the same ticket forever.
type RefusedError struct {
	ID     string
	Reason string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("build agent refused %s: %s", e.ID, e.Reason)
}

// AsRefused extracts the *RefusedError from err (traversing wraps), or nil when
// err is not a refusal.
func AsRefused(err error) *RefusedError {
	var r *RefusedError
	if errors.As(err, &r) {
		return r
	}
	return nil
}

// EpicUnfinalizedError signals FinalizeEpic declined to ship the epic because
// children still read open in the tracker. Nothing is blamed — no quarantine, no
// filed bug, the epic branch and every child are left exactly as they were — but
// the epic is NOT delivered, so a caller whose whole job was to ship it must park
// it for another attempt instead of recording a clean finish. Open names the
// children still blocking it.
type EpicUnfinalizedError struct {
	EpicID string
	Open   []string
}

func (e *EpicUnfinalizedError) Error() string {
	return fmt.Sprintf("epic %s unfinalized — waiting on %s", e.EpicID, strings.Join(e.Open, ", "))
}

// IsEpicUnfinalized reports whether err is (or wraps) an *EpicUnfinalizedError.
func IsEpicUnfinalized(err error) bool {
	var e *EpicUnfinalizedError
	return errors.As(err, &e)
}

// parseRefusal recovers the build agent's refusal from its final output: the
// last line starting with the REFUSED: sentinel, with the reason after the
// colon. Line-anchored and case-sensitive so prose mentioning the word can't
// trip it.
func parseRefusal(out string) (reason string, ok bool) {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if rest, found := strings.CutPrefix(line, "REFUSED:"); found {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// Pipeline holds the collaborators a ticket run needs. One Pipeline is
// constructed per process and reused across tickets.
type Pipeline struct {
	Runner       agent.Runner
	State        state.Checkpoints
	Artifacts    ArtifactStore
	PhaseLogs    PhaseLogStore
	LessonLedger LessonStore
	Git          Git
	GitHub       GitHub
	Tracker      tracker.Tracker
	Tokens       Ledger
	Budget       budget.Limits
	RunsDir      string
	Base         string
	Remote       string
	Prefix       string
	// TrackerProvider is the effective tracker backend (config
	// EffectiveTrackerProvider) that names the PR body's ticket trailer;
	// InternalPrefix is the repo's internal issue-id prefix, marking ids no
	// external tracker knows.
	TrackerProvider string
	InternalPrefix  string
	MaxRepairs      int
	MaxBugfixes     int

	// AgentRetries is how many times a TRANSIENT agent-step failure (timeout,
	// output stall, non-rate-limit crash) is retried on a fresh process per
	// provider before recovery moves on; AgentBackoff is the base seconds slept
	// between those retries (growing with the attempt). Zero retries reproduces the
	// old single-shot behavior.
	AgentRetries int
	AgentBackoff int
	// Fallback returns the ordered alternate runners to try for a phase once the
	// primary provider's retries are exhausted (config FALLBACK_PROVIDERS). Nil or
	// an empty result means retry-only — no provider fallback. Built at the
	// composition root so the pipeline stays provider-agnostic.
	Fallback func(phase string) []agent.Runner

	// SelectRunner, when set, resolves the default backend one ticket runs on and
	// names the Provider behind it. Resume calls it once before any phase spawns and
	// installs the result as Runner; Routes and Fallback layer on top of that default.
	// Nil keeps Runner as built.
	SelectRunner func(ctx context.Context, id string) (agent.Runner, string, error)

	Checks        []checks.Check
	VerifyPanel   []Verifier
	PanelPolicy   string
	PanelParallel bool
	BrowserVerify string
	// VerifyProofs gates whether a browser-driving verify records a trace and
	// screenshots and harvests them to the hub (config VERIFY_PROOFS): "on" or
	// "off". Empty reads as on, the default.
	VerifyProofs string
	AppURL       string
	// AppURLs maps a workspace to its app URL (config APP_URLS) so browser verify
	// drives the app the slice actually changed in a multi-app monorepo; AppURL
	// covers slices that match no workspace.
	AppURLs     map[string]string
	AutoMerge   bool
	MergeMethod string
	// DeterministicCommit routes a squash repo's commit phase through a templated
	// Conventional Commit instead of a commit agent (config DETERMINISTIC_COMMIT).
	// Non-squash merge methods always use the agent commit.
	DeterministicCommit bool
	ExpectedChecks      string
	RequireCI           bool
	// RequireRepoChanges gates the post-build empty-diff guard (config
	// REQUIRE_REPO_CHANGES, default on). When set, a build that left the managed
	// repo unchanged faults instead of advancing to a hollow handoff or empty PR.
	RequireRepoChanges bool
	// LintFix gates the pre-verify lint-fix step (config LINT_FIX). LintFixCmd, when
	// set, is run deterministically in RepoRoot; empty falls back to a cheap agent.
	LintFix    bool
	LintFixCmd string
	// AutoStash gates the fresh-pick WIP guard (config AUTO_STASH, default on). When
	// set, EnsureCleanBase stashes the user's uncommitted tracked changes (recording
	// the branch they were on) instead of aborting, and RestoreWIP pops them back at
	// session end. When off, a dirty tracked tree aborts the run as before. It does
	// not gate the reconcile of an interrupted run's leftovers, which is a commit.
	AutoStash bool
	// SkillsExpected reports whether a build or verify run by the named provider
	// is expected to load repo skills (the provider reports skill usage and the
	// repo has skills installed). Nil disables the no-skills warnings.
	SkillsExpected func(provider string) bool
	// RequiredSkills names the skills the build prompt tells the agent to load
	// before implementing (config REQUIRED_SKILLS). It is the first step of the
	// build resolution chain; empty falls through to the project type's
	// recommended skills, then to every installed skill.
	RequiredSkills []string
	// RequiredSkillsVerify names the skills the verify prompt tells the agent to
	// load (config REQUIRED_SKILLS_VERIFY), alongside the project's test skills
	// and browser-harness on a browser-verify slice.
	RequiredSkillsVerify []string
	// SkillsMode selects skill delivery (config SKILLS_MODE): "instruct" (default)
	// names the resolved set for the agent to load with the Skill tool; "inject"
	// delivers each skill's SKILL.md content inline in the build/verify/repair/
	// bugfix prompt, so delivery is guaranteed and provider-agnostic.
	SkillsMode string
	// Cleanup gates the pre-verify slop-cleanup step (config CLEANUP).
	Cleanup        bool
	CITimeout      int
	CIPoll         int
	Lessons        bool
	LessonsDistill bool
	Sleep          func(time.Duration)
	Renderer       console.Renderer
	// Events is the durable event log. Lifecycle transitions the dashboard recap
	// and browser notifications key off — merge, quarantine, fault, pause — are
	// emitted here as state_change events; nil in modes that keep no durable log.
	Events *event.Log

	// FetchPrompts returns the repo's stored prompt-override map (the hub's
	// resolved prompts read at the composition root). It is called once at
	// ticket-run start; a failure logs one warning and the run proceeds on
	// built-in defaults — prompt resolution never blocks a run (ADR 0008).
	// Nil disables overrides.
	FetchPrompts func(ctx context.Context) (map[string]string, error)

	// FetchQAAccounts returns the repo's QA credentials roster (accounts with
	// full secrets plus free-text notes) from the hub. It is called at verify
	// time only for a browser-verify UI slice; a failure logs one warning and
	// verify proceeds without stored credentials. Nil disables QA injection.
	FetchQAAccounts func(ctx context.Context) (hubclient.QARoster, error)

	// SaveQAAccount stores on the hub a QA credential the verifier discovered
	// inside the repo under test, so the next run's roster is prefilled. A
	// failure logs one warning and the run proceeds — capture never blocks a
	// run. Nil disables capture.
	SaveQAAccount func(ctx context.Context, in hubclient.QAAccountInput) error

	// UploadProofs ships a browser-verify run's harvested proofs — the screenshots
	// and the recorder's trace directory path — to the hub. It is called after a
	// verify attempt resolves; a failure logs one warning and the run proceeds,
	// since missing proofs never fail or pause a run. Nil disables harvest.
	UploadProofs func(ctx context.Context, ticket, traceDir string, shots []hubclient.ProofScreenshot) error

	// Steer is the hub-backed queue of operator steer notes typed at a running
	// ticket. Every substantive phase drains it into its prompt and lends it to
	// the agent layer for mid-session delivery. Nil disables steering.
	Steer SteerQueue

	// OnPhase, when set, is called each time a ticket enters a checkpoint phase,
	// carrying the ticket and the phase just written (state.Building, …). The
	// composition root wires it to the instance registry so the hub sees a
	// reported working state whose state_since is the phase transition, not a file
	// mtime. Nil disables reporting.
	OnPhase func(id, phase string)

	// OnActivity, when set, is called at the start of each present-tense pipeline
	// activity (ADR 0009) — build, verify, repair, ci-wait, merge, … — carrying the
	// ticket, the activity, and a free-text detail (the raw call label, e.g.
	// repair2). The composition root wires it to the instance registry so the
	// heartbeat reports what the Working session is doing right now, ahead of the
	// past-tense checkpoint. Nil disables reporting.
	OnActivity func(id, activity, detail string)

	// Now supplies the current time for the per-day budget window; nil defaults
	// to time.Now (overridable in tests).
	Now func() time.Time

	EpicID     string
	epicBranch string

	// stashedBranch records the branch the user's WIP was on when EnsureCleanBase
	// auto-stashed it, so RestoreWIP can check that branch back out and pop the stash
	// at session end. Empty means nothing was stashed this run.
	stashedBranch string

	// detachedBase records the ref checkoutBase parked HEAD on when another worktree
	// held the base branch, so baseRef cuts from those commits and not from the local
	// base branch that stayed behind. Empty means the base branch itself is checked out.
	detachedBase string

	// buildProvider/buildSkills capture, from the last build agent call, which
	// provider ran and which skills its session loaded — the inputs to the
	// post-build no-skills warning. buildSkillsKnown is false in the Unknown state,
	// which suppresses the warning so a lost transcript never reads as a skill-less build.
	buildProvider    string
	buildSkills      []string
	buildSkillsKnown bool

	// verifyProvider/verifySkills mirror the build capture for the primary
	// verify call — the inputs to the post-verify no-skills warning.
	verifyProvider    string
	verifySkills      []string
	verifySkillsKnown bool

	// qaRoster is the roster the verify prompt was built from, held so the
	// capture ingest can tell a newly discovered credential from one the
	// verifier was handed. Each captured account joins it, so a later attempt
	// of the same verify offering that credential again is a duplicate.
	// qaCaptured counts what the current verify has already stored, which is
	// what qaCaptureMax bounds.
	qaRoster   []hubclient.QAAccount
	qaCaptured int

	// prompts is the ticket run's prompt renderer: the override snapshot
	// fetched at run start layered over the built-in defaults. Edits made
	// mid-run apply from the next run, never mid-ticket. The zero value
	// renders defaults, so entry points that never fetched still work.
	prompts prompts.Renderer

	// OwnedProject is the Linear project this repo is bound to (config PROJECT).
	// When set, Resume refuses any ticket whose project differs — before any
	// branch/checkpoint/build — so a wrong-project ticket can't pollute this repo.
	// Empty disables the guard (back-compat).
	OwnedProject string

	// Opt-in, per-ticket time tracking (off by default). When
	// TimelogEnabled is false none of the time-log code runs. RepoRoot is the
	// resolved target-repo filesystem root, where repo-mode logs and the
	// .gitignore live. See internal/timelog and recordTimelog (COD-622).
	RepoRoot            string
	TimelogEnabled      bool
	TimelogStorage      string
	TimelogOutputFormat string
	TimelogEstimator    string
}

// Process runs a ticket end-to-end through the fresh full chain: build → handoff →
// verify → commit/PR → CI/merge. It is the from="" entry to Resume, kept as a named
// method so callers that always start clean (and the existing tests) read plainly.
func (p *Pipeline) Process(ctx context.Context, id string) error {
	return p.Resume(ctx, id, "")
}

// EnsureOwnedProject refuses a ticket that belongs to a different Linear project
// than this repo owns (OwnedProject, from config PROJECT). It is a no-op when no
// owned project is configured, when the tracker can't report a ticket's project,
// or when the project can't be determined — the guard never blocks on uncertainty,
// only on a confirmed mismatch. Callers run it before any hard-to-reverse work so a
// wrong-project ticket can't cut a branch or write a checkpoint in this repo.
func (p *Pipeline) EnsureOwnedProject(ctx context.Context, id string) error {
	owned := strings.TrimSpace(p.OwnedProject)
	if owned == "" {
		return nil
	}
	projecter, ok := p.Tracker.(tracker.IssueProjecter)
	if !ok {
		return nil
	}
	proj, err := projecter.IssueProject(ctx, id)
	if err != nil {
		return nil
	}
	if proj = strings.TrimSpace(proj); proj == "" {
		return nil
	}
	if !strings.EqualFold(proj, owned) {
		return &CrossProjectError{ID: id, Owned: owned, Project: proj}
	}
	return nil
}

// Resume runs a ticket through the phases not yet checkpointed. A checkpoint
// already at merged short-circuits to ErrAlreadyDone — a stale tracker or a
// bad pick must never rebuild delivered work (`trau --clear` is the explicit
// override) — unless the tracker affirmatively shows the ticket reopened after
// trau marked it Done, in which case the delivered checkpoint is cleared and the
// ticket rebuilds fresh. Otherwise it buckets token logs to the ticket, restores
// the recorded feature branch (auto-resetting the ticket when that branch is
// gone), then runs each phase whose rank exceeds the resume point
// (fi = Idx(from)); from="" runs everything fresh. A *GiveUpError
// from build (no feature branch) is funneled into giveUp here; verify and the CI
// gate run giveUp themselves and return the resulting *GiveUpError, which passes
// straight through.
func (p *Pipeline) Resume(ctx context.Context, id, from string) error {
	if err := p.EnsureOwnedProject(ctx, id); err != nil {
		return err
	}
	if p.State.Get(id, "PHASE") == state.Merged {
		if !p.reopenedInTracker(ctx, id) {
			pr := p.State.Get(id, "PR")
			if pr != "" {
				pr = " (PR #" + pr + ")"
			}
			p.logf("  ✓ %s is already merged%s — skipping; `trau --clear %s` to run it again", id, pr, id)
			p.clearFailure(id)
			return ErrAlreadyDone
		}
		p.logf("  ↻ %s was delivered but reopened in the tracker — clearing the merged checkpoint to rebuild", id)
		p.resetLocal(ctx, id)
		from = ""
	}
	p.clearFailureMarks(id)
	if err := p.selectRunner(ctx, id); err != nil {
		return err
	}
	if p.Tokens != nil {
		p.Tokens.SetTicket(id)
	}
	if p.Renderer != nil {
		p.Renderer.SetTicket(id)

		p.setTitle(p.State.Get(id, "TITLE"))
	}
	p.loadPrompts(ctx, id)
	fi := state.Idx(from)

	if from != "" {
		p.logf("  ↳ resuming from checkpoint: %s", from)
	}

	branch := p.State.Get(id, "BRANCH")
	exists := false
	if branch != "" {
		exists, _ = p.Git.BranchExists(ctx, branch)
	}
	switch {
	case branch != "" && exists:
		_ = p.Git.Checkout(ctx, branch, false)
	case fi >= 2:
		shown := branch
		if shown == "" {
			shown = "?"
		}
		p.logf("  ⚠ resume: recorded branch '%s' for %s is missing — resetting it to start fresh", shown, id)
		return p.Reset(ctx, id)
	}

	return p.classifyPhaseErr(ctx, id, p.runPhases(ctx, id, fi))
}

// selectRunner installs the default backend this ticket runs on, so a Provider
// pinned on the ticket applies wherever the run was started from.
func (p *Pipeline) selectRunner(ctx context.Context, id string) error {
	if p.SelectRunner == nil {
		return nil
	}
	runner, provider, err := p.SelectRunner(ctx, id)
	if err != nil {
		return p.pauseProviderUnavailable(id, provider, err)
	}
	p.Runner = runner
	return nil
}

// loadPrompts snapshots the repo's stored prompt overrides for this ticket run.
// A fetch failure logs one warning and leaves the built-in defaults in place —
// the hub being down never blocks prompt resolution. An override that later
// fails to render falls back to its default and is flagged like the skills
// warning: on the console and as a durable event naming the prompt.
func (p *Pipeline) loadPrompts(ctx context.Context, id string) {
	p.prompts = prompts.Renderer{OnOverrideError: func(name string, err error) {
		msg := fmt.Sprintf("prompt override %q failed to render — using the built-in default: %v", name, err)
		p.logf("  ⚠ %s", msg)
		if p.Events != nil {
			p.Events.Emit(event.KindPromptOverrideSkipped, "", msg, map[string]any{"ticket": id, "prompt": name})
		}
	}}
	if p.FetchPrompts == nil {
		return
	}
	overrides, err := p.FetchPrompts(ctx)
	if err != nil {
		p.logf("  ⚠ prompt overrides unavailable — using built-in defaults: %v", err)
		return
	}
	p.prompts.Overrides = overrides
}

// reopenedInTracker reports whether a merged ticket should rebuild: trau saw the
// tracker reach Done (TRACKER_DONE) and the tracker now affirmatively reports the
// issue back in an unstarted state, the shape of a deliberate reopen. A started
// state is not a reopen: that is what an external automation's status flip on an
// already-delivered ticket looks like. Anything uncertain — no marker, no status
// capability, a lookup error, an unknown status — reads as not-reopened, so
// delivered work is never rebuilt on doubt.
func (p *Pipeline) reopenedInTracker(ctx context.Context, id string) bool {
	if p.State.Get(id, "TRACKER_DONE") != "1" {
		return false
	}
	statuser, ok := p.Tracker.(tracker.IssueStatuser)
	if !ok {
		return false
	}
	st, err := statuser.IssueStatus(ctx, id)
	return err == nil && st == tracker.StatusOpen
}

// runPhases runs each phase whose rank exceeds the resume point fi, returning the
// first phase error verbatim (build's *GiveUpError, verify/CI's already-finalized
// *GiveUpError, a *PausedError, ErrAlreadyDone, or a raw unexpected error). The
// classification of what that error MEANS for the ticket is centralized in
// classifyPhaseErr, so every phase is handled the same way.
func (p *Pipeline) runPhases(ctx context.Context, id string, fi int) error {
	if fi < 2 {
		if err := p.build(ctx, id, fi == 1); err != nil {
			return err
		}
	}
	if fi < 4 {
		if err := p.handoffAndCleanup(ctx, id, fi < 3); err != nil {
			return err
		}
		if err := p.Verify(ctx, id); err != nil {
			return err
		}
	}
	if fi < 5 {
		if err := p.CommitAndPR(ctx, id); err != nil {
			return err
		}
	}
	return p.CIAndMerge(ctx, id)
}

// classifyPhaseErr decides what a phase error means for the ticket and the loop:
//   - nil / ErrAlreadyDone: nothing went wrong — pass through.
//   - paused: a blameless provider rate/usage limit — pass through; the work
//     stays on its branch and the loop driver stops picking new tickets.
//   - give-up: a verified dead end — finalize+quarantine (idempotent, so the
//     give-ups verify/CI already finalized are not double-handled).
//   - stopped: the run's own context was canceled, so a human stopped the run —
//     preserve the WIP and report a blameless stop. The test is the loop context,
//     never the error: AGENT_TIMEOUT expires a child context of it, so a timeout
//     surfaces DeadlineExceeded while this context stays live and still faults.
//   - anything else: an UNEXPECTED error — funnel into the blameless fault path,
//     which preserves the WIP on the branch without quarantining or filing a bug.
func (p *Pipeline) classifyPhaseErr(ctx context.Context, id string, err error) error {
	switch {
	case err == nil, errors.Is(err, ErrAlreadyDone):
		p.clearFailure(id)
		return err
	case IsPaused(err):
		return err
	case errors.Is(err, state.ErrHubUnreachable):
		return p.pauseHubUnreachable(id)
	case isGiveUp(err):
		return p.handleGiveUp(ctx, id, err)
	case AsRefused(err) != nil:
		return p.handleRefusal(ctx, id, err)
	case errors.Is(ctx.Err(), context.Canceled):
		return p.stop(ctx, id)
	default:
		return p.fault(ctx, id, err)
	}
}

// handleRefusal undoes a refused ticket's scaffolding — the pre-cut empty branch,
// the checkpoint, the tracker's In Progress — via Reset, so the ticket is left
// exactly as runnable from its owning repo as before the pick. The refusal
// passes through for the loop driver to stop on with guidance.
func (p *Pipeline) handleRefusal(ctx context.Context, id string, err error) error {
	r := AsRefused(err)
	p.logf("  ✗ build refused %s: %s", id, r.Reason)
	if rerr := p.Reset(ctx, id); rerr != nil {
		p.logf("  reset after refusal error (continuing): %v", rerr)
	}
	return err
}

func isGiveUp(err error) bool {
	var g *GiveUpError
	return errors.As(err, &g)
}

func (p *Pipeline) handleGiveUp(ctx context.Context, id string, err error) error {
	var g *GiveUpError
	if errors.As(err, &g) {
		return p.giveUp(ctx, id, g.Reason)
	}
	return err
}

// clearFailure drops a stale FAILURE_REASON once a run ends successfully — the
// recorded reason describes why the ticket is stuck, so it must not outlive the
// attempt that resolved it (e.g. a merge fault cleared by a manual merge).
func (p *Pipeline) clearFailure(id string) {
	_ = p.State.Unset(id, "FAILURE_REASON")
}

// fault preserves the partial work of a ticket aborted by an unexpected error and
// returns a *FaultError tagged with the phase it died in. The ticket is left at
// its last checkpoint so a rerun resumes it; the loop driver stops the session on
// the *FaultError rather than dragging a dirty tree or a re-faulting ticket on.
func (p *Pipeline) fault(ctx context.Context, id string, err error) error {
	phase := p.State.Get(id, "PHASE")
	p.finalizeFault(ctx, id)
	p.expireSteer(ctx, id)
	reason := fmt.Sprintf("unexpected error during %s: %v", NextPhaseLabel(phase), err)
	_ = p.State.Set(id, "FAILURE_REASON", reason)
	_ = p.State.Set(id, "FAILURE_CLASS", state.FailFaulted)
	p.logf("  ⚠ %s could not finish during %s — work saved, ticket left resumable", id, NextPhaseLabel(phase))
	p.emitState(id, phase, "faulted", NextPhaseLabel(phase))
	return &FaultError{ID: id, Phase: phase, Err: err}
}

// stop preserves the partial work of a ticket cut short by a deliberate stop and
// returns a *StoppedError. It mirrors fault's preserve-and-clean and leaves PHASE
// at its checkpoint so a rerun resumes the ticket, but records the blameless
// stopped class instead.
func (p *Pipeline) stop(ctx context.Context, id string) error {
	phase := p.State.Get(id, "PHASE")
	label := NextPhaseLabel(phase)
	p.preserveAndClean(ctx, fmt.Sprintf("wip(%s): stopped mid-run — rerun trau to resume", id))
	_ = p.State.Set(id, "FAILURE_REASON", fmt.Sprintf("stopped during %s — work saved at the last checkpoint", label))
	_ = p.State.Set(id, "FAILURE_CLASS", state.FailStopped)
	p.logf("  ⏹ %s stopped during %s — work saved, ticket left resumable", id, label)
	p.emitState(id, phase, "stopped", label)
	return &StoppedError{ID: id, Phase: phase}
}

// Both budgets stay under the hub's SIGKILL escalation grace, so a stopped run's
// WIP commit lands before the escalation. The push gets its own sub-deadline so an
// unreachable remote still leaves time for the checkout and clean that follow.
const (
	cleanupBudget     = 60 * time.Second
	cleanupPushBudget = 20 * time.Second
)

// detachedCleanup derives the context every stop-time cleanup runs on. A stop
// (web Stop's SIGTERM, Ctrl-C) cancels the run's context, and the cleanup is
// exactly the work that must still happen afterwards, so it is detached from that
// cancellation and bounded by its own deadline instead.
func detachedCleanup(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupBudget)
}

// preserveAndClean saves whatever an aborted attempt left on the feature branch —
// commit it under msg, push it best-effort — and returns the working tree to a
// clean base.
func (p *Pipeline) preserveAndClean(ctx context.Context, msg string) {
	ctx, cancel := detachedCleanup(ctx)
	defer cancel()

	branch, _ := p.Git.CurrentBranch(ctx)
	if !p.onBase(branch) {
		_ = p.Git.AddAll(ctx)
		_ = p.Git.Commit(ctx, msg, true)
		if p.pushPreserved(ctx) {
			p.logf("  saved attempt to %s/%s", p.Remote, branch)
		} else {
			p.logf("  saved attempt to local branch %s", branch)
		}
	}
	_, _ = p.checkoutBase(ctx, true)
	_ = p.Git.Clean(ctx)
}

func (p *Pipeline) pushPreserved(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, cleanupPushBudget)
	defer cancel()
	return p.Git.Push(ctx, p.Remote, "HEAD", true) == nil
}

// finalizeFault mirrors finalizeFailed's preserve-and-clean — commit the WIP to
// the feature branch, push it best-effort, then return the working tree to a clean
// base — but it does NOT quarantine the ticket or file a bug, and it leaves PHASE
// untouched so the ticket stays resumable.
func (p *Pipeline) finalizeFault(ctx context.Context, id string) {
	p.preserveAndClean(ctx, fmt.Sprintf("wip(%s): incomplete attempt — rerun trau to resume", id))
}

// AsFault extracts the *FaultError from err (traversing wraps), or nil when err
// is not a fault. Callers use it to read the faulted ticket's ID and phase for
// the summary.
func AsFault(err error) *FaultError {
	var f *FaultError
	if errors.As(err, &f) {
		return f
	}
	return nil
}

// AsGiveUp extracts the *GiveUpError from err (traversing wraps), or nil when err
// is not a give-up. Callers use it to name the ticket a quarantine left parked.
func AsGiveUp(err error) *GiveUpError {
	var g *GiveUpError
	if errors.As(err, &g) {
		return g
	}
	return nil
}

// NextPhaseLabel maps a checkpoint phase to the human name of the phase that runs
// next from it ("built" → "handoff", "" → "startup"). It is the phase a fault
// died in and the phase a resume continues into — the same mapping serves both
// the fault recap and the resume callout.
func NextPhaseLabel(phase string) string {
	switch phase {
	case state.Building:
		return "build"
	case state.Built:
		return "handoff"
	case state.HandedOff:
		return "verify"
	case state.Verified:
		return "commit/PR"
	case state.PROpen:
		return "CI/merge"
	case "":
		return "startup"
	default:
		return phase
	}
}

// prefix returns the configured issue-identifier prefix, falling back to COD when
// the pipeline was constructed without one (e.g. in tests).
func (p *Pipeline) prefix() string {
	if p.Prefix != "" {
		return p.Prefix
	}
	return "COD"
}

// InferredResume is the bridge for work started BEFORE state tracking (or whose
// state file was lost): if HEAD is parked on a feature/<PREFIX>-… branch with no
// tracked checkpoint, it infers how far the work got from the artifacts on disk
// (branch → built; handoff file → handed_off; passing verdict → verified; open PR →
// pr_open), seeds the state file, and returns (id, phase) for the resume path.
// Conservative on purpose — only the currently checked-out branch, never a scan. It
// returns ("", "") when HEAD is not a parked feature branch or the ticket is already
// tracked.
func (p *Pipeline) InferredResume(ctx context.Context) (id, phase string) {
	pfx := p.prefix()
	head, err := p.Git.CurrentBranch(ctx)
	if err != nil || !strings.HasPrefix(head, "feature/"+pfx+"-") {
		return "", ""
	}
	id = regexp.MustCompile(regexp.QuoteMeta(pfx) + `-[0-9]+`).FindString(head)
	if id == "" {
		return "", ""
	}
	if p.State.Get(id, "PHASE") != "" {
		return "", ""
	}

	phase = state.Built
	if fi, err := os.Stat(handoffPath(id)); err == nil && fi.Size() > 0 {
		phase = state.HandedOff
	}
	if v, ok := readVerdict(verifyPath(id)); ok && v.Pass {
		phase = state.Verified
	}
	if pr, _ := p.GitHub.PRURL(ctx, head); pr != "" {
		phase = state.PROpen
		_ = p.State.Set(id, "PR", prNumber(pr))
		_ = p.State.Set(id, "PR_URL", pr)
	}
	_ = p.State.Set(id, "BRANCH", head)
	_ = p.State.Set(id, "PHASE", phase)
	p.logf("  ↻ adopted in-progress branch %s (inferred checkpoint: %s)", head, phase)
	return id, phase
}

// autoStashMsg labels the stash EnsureCleanBase creates so it is recognizable in
// `git stash list` if the run dies before RestoreWIP pops it.
const autoStashMsg = "trau autostash: uncommitted WIP set aside for a fresh run"

// EnsureCleanBase guards the loop's fresh-pick path: TRACKED files with uncommitted
// changes must not ride into a fresh build (untracked tooling rides along safely).
// Leftovers on a branch trau cut belong to a run that died without cleaning up and
// are committed back to that branch; anything else is the user's WIP, which AutoStash
// (default on) sets aside for RestoreWIP to put back at session end and which aborts
// the run when AutoStash is off. Then it checks out the base branch and fast-forwards
// it from the remote (best-effort); a base another worktree holds is ridden detached
// at its tip instead, already up to date, so the pull is redundant there. The resume
// path deliberately skips this — the feature branch's WIP IS the work.
func (p *Pipeline) EnsureCleanBase(ctx context.Context) error {
	dirty, err := p.Git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("ensure clean base: git status: %w", err)
	}
	if strings.TrimSpace(dirty) != "" {
		if err := p.setAsideWIP(ctx); err != nil {
			return err
		}
	}
	detached, err := p.checkoutBase(ctx, false)
	if err != nil {
		return fmt.Errorf("ensure clean base: checkout %s: %w", p.Base, err)
	}
	if !detached {
		_ = p.Git.Pull(ctx, p.Remote, p.Base)
	}
	return nil
}

// detachedHead is what CurrentBranch reports once HEAD is detached.
const detachedHead = "HEAD"

// onBase reports whether branch is the base branch itself or the detached HEAD at
// its tip. Neither carries a run's work, so neither is preserved or adopted as one.
func (p *Pipeline) onBase(branch string) bool {
	return branch == p.Base || branch == detachedHead
}

// checkoutBase puts the repo on the base branch, reporting whether it had to settle
// for a detached HEAD at the base tip. An operator's sibling worktree with the base
// checked out makes git refuse ours, and that worktree is theirs — never freed by
// trau — so the run rides the same commits detached rather than stopping the queue.
// It records the ref HEAD ended up on for baseRef to cut later branches from.
func (p *Pipeline) checkoutBase(ctx context.Context, force bool) (detached bool, err error) {
	if err = p.Git.Checkout(ctx, p.Base, force); err == nil {
		p.detachedBase = ""
		return false, nil
	}
	where, held := p.baseHeldElsewhere(ctx, err)
	if !held {
		return false, err
	}
	tip := p.Base
	if p.fetchBaseTip(ctx) {
		tip = p.Remote + "/" + p.Base
	}
	if derr := p.Git.CheckoutDetached(ctx, tip, force); derr != nil {
		return false, derr
	}
	p.detachedBase = tip
	p.logf("  ↻ base %s is checked out in %s — running detached at %s", p.Base, where, tip)
	return true, nil
}

// baseRef names the ref a run's branch is cut from. It is the base branch, except
// while the run rides that base detached: git can refuse to move the local branch
// but never the fetched remote tip HEAD is parked at, so cutting from the branch
// there would start the run behind the commits it just fetched.
func (p *Pipeline) baseRef() string {
	if p.detachedBase != "" {
		return p.detachedBase
	}
	return p.Base
}

// fetchBaseTip refreshes the base's remote-tracking ref under its own sub-deadline,
// reporting whether that tip is now current. The budget keeps an unreachable remote
// from eating the sweep-back's whole cleanup budget before the checkout lands.
func (p *Pipeline) fetchBaseTip(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, baseFetchBudget)
	defer cancel()
	return p.Git.Fetch(ctx, p.Remote, p.Base) == nil
}

const baseFetchBudget = 20 * time.Second

// baseHeldElsewhere reports whether refusal is git declining to check out a base
// another worktree already holds, and names that holder for the log. git's worktree
// list answers structurally; its refusal message is the fallback when that lookup
// itself fails, and then the holder goes unnamed.
func (p *Pipeline) baseHeldElsewhere(ctx context.Context, refusal error) (where string, held bool) {
	path, err := p.Git.WorktreeHolding(ctx, p.Base)
	if err != nil {
		return "another worktree", strings.Contains(refusal.Error(), "already used by worktree")
	}
	if path == "" {
		return "", false
	}
	return "worktree " + path, true
}

// setAsideWIP clears the dirty tree EnsureCleanBase found by the route that suits
// whoever left it there. An interrupted run's work is committed back to its own
// branch rather than stashed — a stash would pop a dead run's leftovers onto a dead
// branch at session end, which reads to the user as files disappearing — so that
// path runs whether or not AutoStash is on: it is a reconcile, not a stash.
func (p *Pipeline) setAsideWIP(ctx context.Context) error {
	branch, err := p.Git.CurrentBranch(ctx)
	if err == nil {
		if id := p.interruptedRunID(branch); id != "" {
			p.logf("  ↻ %s left uncommitted work on %s when its run died — committing it there", id, branch)
			p.preserveAndClean(ctx, fmt.Sprintf("wip(%s): preserved after interrupted run", id))
			return nil
		}
	}
	if !p.AutoStash {
		return fmt.Errorf("tracked files have uncommitted changes — aborting so I don't touch your WIP (set AUTO_STASH=1 to stash and restore them automatically)")
	}
	if err != nil {
		return fmt.Errorf("tracked files have uncommitted changes and I couldn't read the current branch to stash them safely: %w — commit or stash manually", err)
	}
	if serr := p.Git.Stash(ctx, autoStashMsg); serr != nil {
		return fmt.Errorf("tracked files have uncommitted changes and auto-stash failed: %w — commit or stash manually", serr)
	}
	p.stashedBranch = branch
	p.logf("  ↩ stashed your WIP on %s — I'll restore it when the run ends", branch)
	return nil
}

// interruptedRunID names the ticket whose run left branch checked out: the branch a
// saved checkpoint recorded, or one trau would have cut for a ticket that has a
// checkpoint. It returns "" for the base branch and for branches trau never cut,
// a personal feature/… branch included.
func (p *Pipeline) interruptedRunID(branch string) string {
	if branch == "" || p.onBase(branch) {
		return ""
	}
	for _, id := range p.State.Tickets() {
		cut := "feature/" + id
		if branch == cut || strings.HasPrefix(branch, cut+"-") || p.State.Get(id, "BRANCH") == branch {
			return id
		}
	}
	return ""
}

// RestoreWIP undoes an EnsureCleanBase auto-stash at session end: it checks the
// original branch back out and pops the stash. It is a no-op when nothing was
// stashed, and idempotent — it consumes the recorded branch so a second deferred
// call does nothing. Every step is best-effort: on failure the WIP stays safe in
// `git stash`, and the log tells the user how to recover it by hand.
func (p *Pipeline) RestoreWIP(ctx context.Context) {
	branch := p.stashedBranch
	if branch == "" {
		return
	}
	p.stashedBranch = ""
	// Detach from the loop's context and give the restore its own deadline so a
	// Ctrl-C (which cancels ctx) still puts the user's WIP back rather than leaving
	// it stranded in the stash.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := p.Git.Checkout(ctx, branch, false); err != nil {
		p.logf("  ⚠ couldn't switch back to %s (%v) — your WIP is safe: run `git stash pop` to restore it", branch, err)
		return
	}
	if err := p.Git.StashPop(ctx); err != nil {
		p.logf("  ⚠ back on %s but couldn't pop your WIP (%v) — it's in `git stash list`; run `git stash pop` to restore it", branch, err)
		return
	}
	p.logf("  ↪ restored your WIP on %s", branch)
}

// Reset discards a ticket's attempt: drop its feature branch (local + remote) and
// saved state + /tmp artifacts, then send Linear back to an unstarted/ready state so
// the picker re-selects it. Every git step is best-effort — a stale ref or a remote
// that already pruned the branch must not stop the reset. The recorded BRANCH is
// preferred; with none, the first matching feature/<id>-* branch is used.
func (p *Pipeline) Reset(ctx context.Context, id string) error {
	p.resetLocal(ctx, id)
	return p.Tracker.Reset(ctx, id)
}

// resetLocal is Reset without the tracker step — used when the tracker already
// reflects the desired status (a user restore) and must be left untouched. It is
// also reached from the refusal path, where the run's context may already be
// cancelled, so it runs detached rather than half-undoing the scaffolding.
func (p *Pipeline) resetLocal(ctx context.Context, id string) {
	ctx, cancel := detachedCleanup(ctx)
	defer cancel()

	branch := p.featureBranch(ctx, id)
	_, _ = p.checkoutBase(ctx, true)
	if branch != "" && branch != p.Base {
		_ = p.Git.DeleteBranch(ctx, branch)
		_ = p.Git.DeletePushedBranch(ctx, p.Remote, branch)
	}
	_ = os.Remove(handoffPath(id))
	_ = os.Remove(verifyPath(id))
	_ = os.Remove(rubricPath(id))
	_ = os.Remove(buildNotesPath(id))
	attachfile.Remove(id)
	p.clearArtifacts(id)
	p.clearPhaseLogs(id)
	_ = p.State.RemoveState(id)
	if branch != "" {
		p.logf("  reset %s: cleared saved state + branch %s", id, branch)
	} else {
		p.logf("  reset %s: cleared saved state", id)
	}
}

// PurgeLocal drops what a hard-deleted ticket left on this machine: its feature
// branch, local and remote, and its run directory. It is deliberately narrower
// than a reset — the hub's run history (checkpoint, phase logs, artifacts) stays,
// so what ran is still browsable once the ticket it ran for is gone, and the
// tracker is never touched because a tombstoned ticket's upstream issue is not
// trau's to reset. It returns the cleanup steps that failed so the hub that
// ordered it can log them; the purge itself has already happened either way.
func (p *Pipeline) PurgeLocal(ctx context.Context, id string) error {
	ctx, cancel := detachedCleanup(ctx)
	defer cancel()

	branch := p.featureBranch(ctx, id)
	_, _ = p.checkoutBase(ctx, true)

	var errs []error
	if branch != "" && branch != p.Base {
		if err := p.Git.DeleteBranch(ctx, branch); err != nil {
			errs = append(errs, fmt.Errorf("delete branch %s: %w", branch, err))
		}
		if err := p.dropPushedBranch(ctx, branch); err != nil {
			errs = append(errs, err)
		}
	}
	if dir := p.runDir(id); dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("remove run dir %s: %w", dir, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if branch != "" {
		p.logf("  purged %s: dropped branch %s + its run directory", id, branch)
	} else {
		p.logf("  purged %s: dropped its run directory", id)
	}
	return nil
}

// dropPushedBranch deletes branch from the remote, when the remote still has it.
// A remote that pruned it already is nothing to report; one that cannot be reached
// is, since the branch then outlives the ticket unseen.
func (p *Pipeline) dropPushedBranch(ctx context.Context, branch string) error {
	exists, err := p.Git.RemoteBranchExists(ctx, p.Remote, branch)
	if err != nil {
		return fmt.Errorf("look up %s/%s: %w", p.Remote, branch, err)
	}
	if !exists {
		return nil
	}
	if err := p.Git.DeletePushedBranch(ctx, p.Remote, branch); err != nil {
		return fmt.Errorf("delete %s/%s: %w", p.Remote, branch, err)
	}
	return nil
}

// featureBranch resolves ticket id's feature branch: the recorded BRANCH, else the
// first matching feature/<id>-* branch, empty when the ticket left neither.
func (p *Pipeline) featureBranch(ctx context.Context, id string) string {
	if branch := p.State.Get(id, "BRANCH"); branch != "" {
		return branch
	}
	branch, _ := p.Git.FindFeatureBranch(ctx, id)
	return branch
}

// runDir is ticket id's run directory, resolving a relative RUNS_DIR against the
// repo root the way every other resolver does. It is empty when no runs dir is
// configured, so a caller never mistakes the repo root for one.
func (p *Pipeline) runDir(id string) string {
	if p.RunsDir == "" {
		return ""
	}
	if filepath.IsAbs(p.RunsDir) {
		return filepath.Join(p.RunsDir, id)
	}
	return filepath.Join(p.RepoRoot, p.RunsDir, id)
}

// CheckoutBranch checks out ticket id's recorded feature branch in the target repo
// so a user inspecting an incomplete or quarantined result lands directly on its
// preserved WIP. It resolves the branch from saved state, falling back to the
// first matching feature/<id>-* branch, and returns the branch it switched to.
func (p *Pipeline) CheckoutBranch(ctx context.Context, id string) (string, error) {
	branch := p.featureBranch(ctx, id)
	if branch == "" {
		return "", fmt.Errorf("no feature branch recorded for %s", id)
	}
	if err := p.Git.Checkout(ctx, branch, false); err != nil {
		return "", fmt.Errorf("checkout %s: %w", branch, err)
	}
	return branch, nil
}

// Build runs the implementation phase fresh (no resume note). It is the
// from="" entry to build; the resumable path uses build directly.
func (p *Pipeline) Build(ctx context.Context, id string) error {
	return p.build(ctx, id, false)
}

func (p *Pipeline) build(ctx context.Context, id string, withNote bool) error {
	p.setActivity(id, activity.Build, "")

	_ = os.Remove(handoffPath(id))
	_ = os.Remove(verifyPath(id))
	_ = os.Remove(rubricPath(id))
	_ = os.Remove(buildNotesPath(id))
	attachfile.Remove(id)
	p.clearArtifacts(id)
	p.clearPhaseLogs(id)

	if err := p.setPhase(id, state.Building); err != nil {
		return fmt.Errorf("build %s: checkpoint building: %w", id, err)
	}

	branch, err := p.resolveBuildBranch(ctx, id)
	if err != nil {
		return err
	}
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		return fmt.Errorf("build %s: record branch: %w", id, err)
	}

	note := ""
	if withNote {
		note = resumeNote
	}
	note += buildLessonsNote(p.recallLessons(p.lessonQuery(id)))
	ticketCtx, labels := p.ticketContextWithLabels(ctx, id)
	resolver := p.skillResolver()
	buildSkills := resolver.Build(agent.SkillContext{Text: skillMatchText(ticketCtx, labels)})
	buildDelivery := p.resolveSkills(buildSkills, resolver.Installed(), false)
	p.recordPhaseSkills(id, "build", buildDelivery)
	out, err := p.agentStep(ctx, id, "build", injectInto(buildDelivery.injection, buildInstruction(p.prompts, id, branch, buildDelivery.note, note, ticketCtx)))
	if err != nil {
		return err
	}
	if rerr := p.checkRefusal(ctx, out, id); rerr != nil {
		return rerr
	}
	if err := p.assertRepoChanged(ctx, id); err != nil {
		return err
	}
	p.warnBuildWithoutSkills(id, buildSkills.Names)
	p.persistBuildNotes(id)
	_ = p.State.Set(id, "BUILD_SUMMARY", summarizeBuildOutput(out))
	if fi, err := os.Stat(buildNotesPath(id)); err == nil && fi.Size() > 0 {
		p.logf("  ↳ build notes: %s captured for cleanup/repair", fmtBytes(fi.Size()))
	}

	if err := p.setPhase(id, state.Built); err != nil {
		return fmt.Errorf("build %s: checkpoint built: %w", id, err)
	}
	return nil
}

const noSkillsWarning = "build loaded no skills — the repo has skills installed but the agent used none"

// warnBuildWithoutSkills flags a build that loaded none of the skills its prompt
// named. Advisory only — the run proceeds. It prints to the console/TUI and, in
// serve mode, records a durable event so the web UI surfaces the same warning a
// headless run would otherwise bury. It fires only on a confirmed empty set; the
// Unknown state (buildSkillsKnown false) stays silent.
func (p *Pipeline) warnBuildWithoutSkills(id string, named []string) {
	if p.injectSkills() {
		return
	}
	if p.SkillsExpected == nil || len(named) == 0 || len(p.buildSkills) > 0 || !p.buildSkillsKnown || !p.SkillsExpected(p.buildProvider) {
		return
	}
	p.logf("  ⚠ %s", noSkillsWarning)
	if p.Events != nil {
		p.Events.Emit(event.KindBuildNoSkills, "build", noSkillsWarning, map[string]any{"ticket": id})
	}
}

// checkRefusal honors the build agent's REFUSED sentinel — its declaration that
// the ticket targets a different repository/codebase — but only when the agent
// backed it up by leaving the working tree untouched. A refusal accompanied by
// changes is a contradiction; the changes win and the run proceeds normally.
func (p *Pipeline) checkRefusal(ctx context.Context, out, id string) error {
	reason, ok := parseRefusal(out)
	if !ok {
		return nil
	}
	if dirty, err := p.Git.WorktreeDirty(ctx); err != nil || dirty {
		p.logf("  ⚠ build replied REFUSED but left changes — keeping them and continuing")
		return nil
	}
	return &RefusedError{ID: id, Reason: reason}
}

func (p *Pipeline) resolveBuildBranch(ctx context.Context, id string) (string, error) {
	branch := p.State.Get(id, "BRANCH")
	if branch == "" {
		branch, _ = p.Git.FindFeatureBranch(ctx, id)
	}
	if branch != "" {
		if exists, _ := p.Git.BranchExists(ctx, branch); exists {
			if err := p.Git.Checkout(ctx, branch, false); err != nil {
				return "", fmt.Errorf("build %s: checkout %s: %w", id, branch, err)
			}
			return branch, nil
		}
		if remote, _ := p.Git.RemoteBranchExists(ctx, p.Remote, branch); remote {
			if err := p.Git.CheckoutRemoteBranch(ctx, p.Remote, branch); err == nil {
				p.logf("  ↳ recorded branch %s was missing locally — adopted %s/%s", branch, p.Remote, branch)
				return branch, nil
			}
		}
		p.logf("  ⚠ recorded branch %s is gone (likely deleted by a completed merge) — starting fresh", branch)
	}

	title, err := p.Tracker.Title(ctx, id)
	switch {
	case err != nil:
		p.logf("  title lookup error (using id-only branch): %v", err)
	case slugify(title) == "":

		p.logf("  title yielded no usable slug (using id-only branch)")
	}

	if title != "" {
		_ = p.State.Set(id, "TITLE", title)
		p.setTitle(title)
	}
	branch = featureBranch(id, title)
	base := p.baseRef()
	if p.EpicID != "" {
		epic, err := p.epicBranchName(ctx)
		if err != nil {
			return "", err
		}
		p.syncEpicBest(ctx, epic)
		base = epic
	}
	if err := p.Git.CreateBranch(ctx, branch, base); err != nil {
		return "", &GiveUpError{ID: id, Reason: "could not create feature branch for " + id}
	}
	p.logf("  branch %s ← %s", branch, base)

	if err := p.Tracker.SetStatus(ctx, id, "In Progress", ""); err != nil {
		p.logf("  set In Progress error (continuing): %v", err)
	}
	return branch, nil
}

func featureBranch(id, title string) string {
	if slug := slugify(title); slug != "" {
		return "feature/" + id + "-" + slug
	}
	return "feature/" + id
}

// assertRepoChanged catches a build that produced nothing in the managed repo —
// the agent escaped its working directory or built in the wrong repository — and
// faults (resumable, WIP preserved) instead of advancing to a hollow handoff or
// empty PR. It runs inside build BEFORE the built checkpoint, so a tripped guard
// leaves the ticket at building and a resume re-runs build (and the guard) rather
// than marching an empty branch into handoff. Build leaves its work uncommitted
// (the commit phase runs later), so "nothing here" means BOTH a clean working tree
// (untracked files included) AND no commits on the branch beyond base.
// REQUIRE_REPO_CHANGES=0 disables it for the rare legitimately no-op ticket.
func (p *Pipeline) assertRepoChanged(ctx context.Context, id string) error {
	if !p.RequireRepoChanges {
		return nil
	}
	dirty, err := p.Git.WorktreeDirty(ctx)
	if err != nil {
		return fmt.Errorf("repo-change guard %s: status: %w", id, err)
	}
	if dirty {
		return nil
	}
	base, err := p.buildBase(ctx)
	if err != nil {
		return err
	}
	if branch := p.State.Get(id, "BRANCH"); branch != "" {
		commits, err := p.Git.Commits(ctx, base, branch)
		if err != nil {
			return fmt.Errorf("repo-change guard %s: commits: %w", id, err)
		}
		if len(commits) > 0 {
			return nil
		}
	}
	return fmt.Errorf("build produced no changes in %s — the agent may have built in the wrong repository or escaped its working directory", p.repoLabel())
}

// buildBase resolves the branch the feature work diverges from: the epic branch
// for an epic sub-ticket, otherwise the configured base.
func (p *Pipeline) buildBase(ctx context.Context) (string, error) {
	if p.EpicID != "" {
		return p.epicBranchName(ctx)
	}
	return p.Base, nil
}

// repoLabel names the managed repo for guard messages — its directory basename,
// or a generic phrase when no repo root was resolved.
func (p *Pipeline) repoLabel() string {
	if p.RepoRoot == "" {
		return "the managed repo"
	}
	return filepath.Base(p.RepoRoot)
}

// handoffAndCleanup overlaps the handoff brief with the lintfix→cleanup chain.
// Handoff only reads the tree to write the QA brief while lintfix and cleanup make
// behavior-preserving edits, so the two sides run concurrently after build and
// verify waits for both — saving roughly one agent-phase of wall clock per ticket.
// A fatal outcome (pause, give-up, fault) from either side cancels the other through
// the shared errgroup context and propagates unchanged, classified exactly as the
// sequential pipeline classified it; cleanup still fails open on a non-fatal agent
// error. The handed_off checkpoint is written only once BOTH sides finish, so a crash
// mid-overlap resumes from build and re-runs both. runHandoff is false when the
// ticket resumes from an existing handed_off checkpoint — handoff is already durable,
// so only the lintfix→cleanup chain runs.
//
// For a tiny working-tree diff the standalone handoff agent is skipped entirely: only
// the lintfix→cleanup chain runs (cleanup skips itself on the same gate) and verify
// later derives its checklist from the ticket and the diff. The gate fails open, so a
// diff that cannot be sized still runs the full handoff + verify chain.
func (p *Pipeline) handoffAndCleanup(ctx context.Context, id string, runHandoff bool) error {
	if !runHandoff {
		return p.lintFixAndCleanup(ctx, id)
	}
	if p.skipHandoff(ctx, id) {
		p.logf("  ↳ handoff: skipped for tiny diff — verify derives its checklist from the ticket + diff")
		if err := p.lintFixAndCleanup(ctx, id); err != nil {
			return err
		}
		return p.checkpointHandoff(id)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return p.handoffWork(gctx, id) })
	g.Go(func() error { return p.lintFixAndCleanup(gctx, id) })
	if err := g.Wait(); err != nil {
		return err
	}
	return p.checkpointHandoff(id)
}

// lintFixAndCleanup runs the pre-verify style chain: the project's autofixers, then
// the slop-cleanup pass unless the diff is tiny enough to skip it. Both steps make
// only behavior-preserving edits and fail open on a non-fatal agent error; only a
// provider pause or budget give-up propagates.
func (p *Pipeline) lintFixAndCleanup(ctx context.Context, id string) error {
	if err := p.lintFix(ctx, id); err != nil {
		return err
	}
	if p.Cleanup && p.skipCleanup(ctx, id) {
		p.logf("  ↳ cleanup: skipped for tiny diff — build's inline style note already covers slop")
		return nil
	}
	return p.cleanup(ctx, id)
}

// Handoff runs the handoff skill to write the QA brief to exactly
// /tmp/handoff-<ID>.md, then checkpoints handed_off. The normal pipeline overlaps the
// brief-writing work with the lintfix→cleanup chain (handoffAndCleanup); this
// sequential form is the direct entry point.
func (p *Pipeline) Handoff(ctx context.Context, id string) error {
	if err := p.handoffWork(ctx, id); err != nil {
		return err
	}
	return p.checkpointHandoff(id)
}

// handoffWork runs the handoff agent and persists the brief + rubric, but does NOT
// checkpoint — the overlap orchestrator writes handed_off only after the concurrent
// lintfix→cleanup chain has also finished.
func (p *Pipeline) handoffWork(ctx context.Context, id string) error {
	p.setActivity(id, activity.Handoff, "")
	if _, err := p.agentStep(ctx, id, "handoff", handoffTail(p.prompts, id, p.ticketContext(ctx, id))); err != nil {
		return err
	}
	if fi, err := os.Stat(handoffPath(id)); err != nil || fi.Size() == 0 {
		return fmt.Errorf("handoff %s: agent did not write handoff brief", id)
	}
	p.persistHandoff(id)
	p.persistRubric(id)
	if _, ok := p.activeRubric(id); !ok {
		p.logf("  ⚠ handoff wrote no usable rubric — verify will grade from the brief alone")
	}
	return nil
}

func (p *Pipeline) checkpointHandoff(id string) error {
	if err := p.setPhase(id, state.HandedOff); err != nil {
		return fmt.Errorf("handoff %s: checkpoint handed_off: %w", id, err)
	}
	return nil
}

func (p *Pipeline) persistHandoff(id string) {
	data, err := os.ReadFile(handoffPath(id))
	if err != nil || len(data) == 0 {
		return
	}
	p.putArtifact(id, artifactHandoff, string(data))
}

// persistVerdict stores the graded verify verdict through the hub so the last QA
// outcome survives a reboot and is readable out of band (the web hub renders it on
// the run detail page). Best-effort and silent.
func (p *Pipeline) persistVerdict(id string, v verdict) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	p.putArtifact(id, artifactVerdict, string(data))
}

// restoreHandoff copies the durable handoff brief back to /tmp when /tmp lost it
// (wiped on reboot), so a resumed verify reuses the exact brief the handoff
// produced — and the matching rubric — instead of regenerating a fresh pair.
// Best-effort: it leaves /tmp untouched when a non-empty copy is already there or
// the hub holds none.
func (p *Pipeline) restoreHandoff(id string) {
	if fi, err := os.Stat(handoffPath(id)); err == nil && fi.Size() > 0 {
		return
	}
	content, ok := p.getArtifact(id, artifactHandoff)
	if !ok || content == "" {
		return
	}
	_ = os.WriteFile(handoffPath(id), []byte(content), 0o644)
}

// Verify is the real test gate (EXPECTED_CHECKS is empty), run in a fresh,
// adversarial process that can only inherit the durable handoff file + the code on
// disk. It regenerates the handoff if /tmp lost it, runs the verify skill, parses
// the JSON verdict, and self-heals up to MaxRepairs times — each repair and each
// re-verify is its own cold process. If quick repairs are exhausted, it delegates
// to a comprehensive bugfix agent up to MaxBugfixes times. On pass it checkpoints
// verified; on exhaustion it files a last-resort HITL blocker issue and
// quarantines the original ticket.
func (p *Pipeline) Verify(ctx context.Context, id string) error {
	p.restoreHandoff(id)
	p.restoreRubric(id)

	handoff := handoffPath(id)
	fi, err := os.Stat(handoff)
	briefPresent := err == nil && fi.Size() > 0
	if !briefPresent && !p.skipHandoff(ctx, id) {
		if _, err := p.agentStep(ctx, id, "handoff", handoffTail(p.prompts, id, p.ticketContext(ctx, id))); err != nil {
			return err
		}
		p.persistHandoff(id)
		p.persistRubric(id)
		fi, err = os.Stat(handoff)
		briefPresent = err == nil && fi.Size() > 0
	}

	var ticketCtx string
	if briefPresent {
		p.logf("  ↳ handoff: %s → verify", fmtBytes(fi.Size()))
	} else {
		handoff = ""
		ticketCtx = p.ticketContext(ctx, id)
		p.logf("  ↳ handoff: none — verify derives its checklist from the ticket + diff")
	}

	verdictPath := verifyPath(id)
	appURL := p.sliceAppURL(ctx)
	note := browserNote(p.BrowserVerify, appURL)
	if note != "" && appURL != p.AppURL {
		p.logf("  ↳ browser verify targets %s (workspace match)", appURL)
	}
	qaNote := p.qaVerifyNote(ctx, id, note)
	branch := p.State.Get(id, "BRANCH")

	rubricRef, rubricOK := p.activeRubric(id)
	switch {
	case rubricOK:
		p.logf("  ↳ rubric → verify")
	case briefPresent:
		p.logf("  ⚠ no usable rubric — verify grades from the brief alone")
	}
	rubricVerify := verifyRubricNote(rubricRef)
	rubricRepair := repairRubricNote(rubricRef)
	notesRef, _ := p.activeBuildNotes(id)
	notesRepair := buildNotesNote(notesRef)
	lessonsVerify := verifyLessonsNote(p.recallLessons(p.lessonQuery(id)))
	resolver := p.skillResolver()
	changed, _ := p.sliceChangedFiles(ctx)
	skillCtx := agent.SkillContext{Changed: changed}
	verifySkills := resolver.Verify(skillCtx, note != "")
	repairSkills := resolver.Repair(skillCtx)
	verifyDelivery := p.resolveSkills(verifySkills, resolver.Installed(), true)
	repairDelivery := p.resolveSkills(repairSkills, resolver.Installed(), false)
	p.recordPhaseSkills(id, "verify", verifyDelivery)

	checksFragment := checks.Render(p.Checks)
	if n := len(p.Checks); n > 0 {
		p.logf("  ↳ verify checks: %d active (severity gates the merge)", n)
	}
	if n := len(p.VerifyPanel); n > 0 {
		p.logf("  ↳ verify panel: %d cross-vendor verifiers, %s policy", n, normalizePolicy(p.PanelPolicy))
	}

	repairAttempt := 0
	bugfixAttempt := 0
	passed := false
	label := "verify"
	var lastFail, passVerdict verdict
	for {
		p.setActivity(id, activity.Verify, "")
		v, err := p.verifyAttempt(ctx, id, label, handoff, note, qaNote, checksFragment, rubricVerify, lessonsVerify, verifyDelivery.note, verifyDelivery.injection, ticketCtx)
		if err != nil {
			return err
		}
		if label == "verify" {
			p.warnVerifyWithoutSkills(id, verifySkills.Names)
		}
		p.persistVerdict(id, v)
		if v.Pass {
			passed = true
			passVerdict = v
			break
		}
		lastFail = v
		lessonsRepair := repairLessonsNote(p.recallLessons(v.failureLines()))
		if repairAttempt < p.MaxRepairs {
			repairAttempt++
			label = fmt.Sprintf("verify-retry%d", repairAttempt)
			p.logf("  ⚠ verify failed — self-heal attempt %d/%d", repairAttempt, p.MaxRepairs)
			for _, fl := range topFailures(v) {
				p.logf("  ↳ %s", fl)
			}
			p.setActivity(id, activity.Repair, fmt.Sprintf("repair%d", repairAttempt))
			p.recordPhaseSkills(id, "repair", repairDelivery)
			if _, err := p.agentStep(ctx, id, fmt.Sprintf("repair%d", repairAttempt), injectInto(repairDelivery.injection, repairInstruction(p.prompts, id, verdictPath, handoff, branch, v.failureLines(), rubricRepair, lessonsRepair, notesRepair, repairDelivery.note, ticketCtx))); err != nil {
				return err
			}
			continue
		}
		if bugfixAttempt < p.MaxBugfixes {
			bugfixAttempt++
			label = fmt.Sprintf("verify-retry%d", repairAttempt+bugfixAttempt)
			p.logf("  ⚠ repairs exhausted — comprehensive bugfix attempt %d/%d", bugfixAttempt, p.MaxBugfixes)
			for _, fl := range topFailures(v) {
				p.logf("  ↳ %s", fl)
			}
			p.setActivity(id, activity.Bugfix, fmt.Sprintf("bugfix%d", bugfixAttempt))
			p.recordPhaseSkills(id, "bugfix", repairDelivery)
			if _, err := p.agentStep(ctx, id, fmt.Sprintf("bugfix%d", bugfixAttempt), injectInto(repairDelivery.injection, bugfixInstruction(p.prompts, id, verdictPath, handoff, branch, v.failureLines(), rubricRepair, lessonsRepair, notesRepair, repairDelivery.note, ticketCtx))); err != nil {
				return err
			}
			continue
		}
		break
	}

	if !passed {
		p.recordLesson(ctx, id, lastFail, attemptLabel(repairAttempt, bugfixAttempt), lessonResultQuarantined)
		reason := fmt.Sprintf("verify failed after %d repair attempt(s) and %d bugfix attempt(s)", repairAttempt, bugfixAttempt)
		bug, _ := p.Tracker.FileBug(ctx, id, verdictPath)
		if bug != "" {
			reason += "; filed HITL blocker " + bug
		}
		return p.giveUp(ctx, id, reason)
	}
	if err := p.gateBrowserVerify(ctx, id, passVerdict, handoff, qaNote, checksFragment, rubricVerify, lessonsVerify, verifyDelivery.note, verifyDelivery.injection, ticketCtx); err != nil {
		return err
	}
	p.harvestProofs(ctx, id)
	if repairAttempt > 0 || bugfixAttempt > 0 {
		p.recordLesson(ctx, id, lastFail, attemptLabel(repairAttempt, bugfixAttempt), lessonResultRepaired)
		p.logf("  ✓ verify passed (after %d repair attempt(s) and %d bugfix attempt(s))", repairAttempt, bugfixAttempt)
	} else {
		p.logf("  ✓ verify passed")
	}
	if err := p.setPhase(id, state.Verified); err != nil {
		return fmt.Errorf("verify %s: checkpoint verified: %w", id, err)
	}
	return nil
}

const verifyNoSkillsWarning = "verify loaded no skills — the repo has skills installed but the agent used none"

// warnVerifyWithoutSkills flags a primary verify attempt that loaded none of the
// skills its prompt named, mirroring warnBuildWithoutSkills: a console/TUI line
// plus, in serve mode, a durable verify_no_skills event. Called once per Verify,
// after the first attempt only, so a run emits the event at most once.
func (p *Pipeline) warnVerifyWithoutSkills(id string, named []string) {
	if p.injectSkills() {
		return
	}
	if p.SkillsExpected == nil || len(named) == 0 || len(p.verifySkills) > 0 || !p.verifySkillsKnown || !p.SkillsExpected(p.verifyProvider) {
		return
	}
	p.logf("  ⚠ %s", verifyNoSkillsWarning)
	if p.Events != nil {
		p.Events.Emit(event.KindVerifyNoSkills, "verify", verifyNoSkillsWarning, map[string]any{"ticket": id})
	}
}

const noBrowserWarning = "browser verify skipped on a UI slice — the verifier did not drive the app in a browser"

// gateBrowserVerify enforces browser-verify accounting on a slice that already
// passed functional verify. The verdict's self-reported browser value is recorded
// but never trusted to decide the gate: the slice is classified as UI
// deterministically from its own diff, so a UI slice whose verify did not DRIVE
// the browser is a violation regardless of what the verdict claimed (including a
// "not-applicable" on a front-end diff).
//
// Under BROWSER_VERIFY=auto — or whenever no APP_URL is configured, where there is
// no reachable target to demand — the violation is advisory: a console warning
// plus a durable verify_no_browser event, and the run proceeds. Under always with
// a real APP_URL it re-runs verify once with an explicit must-drive instruction;
// a still-undriven UI slice pauses blamelessly, carrying the verdict's
// browser_notes as the reason. A browser skip is an environment/config gap, never
// a code defect, so it never routes into the repair loop.
func (p *Pipeline) gateBrowserVerify(ctx context.Context, id string, v verdict, handoff, qaNote, checksFragment, rubricNote, lessonsNote, skillsNote, skillsInject, ticketCtx string) error {
	if p.BrowserVerify == "never" || p.BrowserVerify == "" {
		return nil
	}
	if !p.sliceIsUI(ctx) {
		return nil
	}
	if browserOutcome(v) == "driven" {
		return nil
	}
	appURL := p.sliceAppURL(ctx)
	if appURL == "" {
		p.logf("  ⚠ browser verify: no APP_URL configured — treating the browser gate as advisory")
		p.warnNoBrowser(id, v)
		return nil
	}
	if p.BrowserVerify != "always" {
		p.warnNoBrowser(id, v)
		return nil
	}

	p.logf("  ⚠ browser verify required but the UI slice was not driven — re-verifying once (must drive %s)", appURL)
	p.setActivity(id, activity.Verify, "browser")
	v2, err := p.verifyAttempt(ctx, id, "verify-browser", handoff, browserDriveNote(appURL), qaNote, checksFragment, rubricNote, lessonsNote, skillsNote, skillsInject, ticketCtx)
	if err != nil {
		return err
	}
	p.persistVerdict(id, v2)
	if browserOutcome(v2) == "driven" {
		p.logf("  ✓ browser verify driven on re-verify")
		return nil
	}
	return p.pauseNoBrowser(id, browserPauseReason(v2))
}

// warnNoBrowser flags a UI slice whose verify did not drive the browser when the
// gate is advisory (BROWSER_VERIFY=auto, or no APP_URL configured). Advisory only
// — the run proceeds; the warning makes an unverified UI surface visible instead
// of trusting the verdict's self-reported browser value. Mirrors
// warnBuildWithoutSkills: a console/TUI line plus, in serve mode, a durable
// verify_no_browser event the web UI surfaces.
func (p *Pipeline) warnNoBrowser(id string, v verdict) {
	msg := noBrowserWarning
	notes := strings.TrimSpace(v.BrowserNotes)
	if notes != "" {
		msg += " (" + notes + ")"
	}
	p.logf("  ⚠ %s", msg)
	if p.Events != nil {
		fields := map[string]any{"ticket": id, "browser": browserOutcome(v)}
		if notes != "" {
			fields["browser_notes"] = notes
		}
		p.Events.Emit(event.KindVerifyNoBrowser, "verify", msg, fields)
	}
}

// proofsEnabled reports whether this verify run should record a trace and
// screenshots and harvest them to the hub: the browser gate is active for the
// slice (note != "") and VERIFY_PROOFS is not off (empty reads as on, the default).
func (p *Pipeline) proofsEnabled(note string) bool {
	return note != "" && !strings.EqualFold(strings.TrimSpace(p.VerifyProofs), "off")
}

// harvestProofs ships the proofs the verify agent saved under /tmp for the run to
// the hub, then clears the directory. Best-effort: a harvest failure logs one
// warning and never fails or pauses the run. When the last verdict reported
// driving the browser yet no proofs turned up, it emits an advisory warning event.
func (p *Pipeline) harvestProofs(ctx context.Context, id string) {
	if strings.EqualFold(strings.TrimSpace(p.VerifyProofs), "off") {
		return
	}
	defer proofs.Remove(id)
	man, shots, err := proofs.Read(id)
	if err != nil || len(shots) == 0 {
		if p.lastBrowserDriven(id) {
			p.warnNoProofs(id)
		}
		return
	}
	if p.UploadProofs == nil {
		return
	}
	payload := make([]hubclient.ProofScreenshot, 0, len(shots))
	for _, s := range shots {
		payload = append(payload, hubclient.ProofScreenshot{
			Filename: s.Filename,
			Mime:     s.MimeType,
			Caption:  s.Caption,
			Data:     base64.StdEncoding.EncodeToString(s.Bytes),
		})
	}
	if err := p.UploadProofs(ctx, id, man.TraceDir, payload); err != nil {
		p.logf("  ⚠ harvest verify proofs: %v", err)
		return
	}
	p.logf("  ✓ harvested %d verify proof screenshot(s)", len(payload))
}

// lastBrowserDriven reports whether the run's most recent verdict claimed a
// browser run, read from the verdict file the last verify attempt wrote.
func (p *Pipeline) lastBrowserDriven(id string) bool {
	v, ok := readVerdict(verifyPath(id))
	return ok && browserOutcome(v) == "driven"
}

// warnNoProofs records that a browser-driven verify left nothing to harvest. It is
// advisory: a run is never failed or paused over missing proofs.
func (p *Pipeline) warnNoProofs(id string) {
	msg := "browser verify reported a driven UI run but saved no proofs to harvest"
	p.logf("  ⚠ %s", msg)
	if p.Events != nil {
		p.Events.Emit(event.KindVerifyNoProofs, "verify", msg, map[string]any{"ticket": id})
	}
}

// browserPauseReason builds the blameless-pause reason for a BROWSER_VERIFY=always
// UI slice that stayed undriven, folding in the verdict's browser_notes so the
// parked ticket names the concrete blocker (e.g. "cannot reach APP_URL").
func browserPauseReason(v verdict) string {
	reason := "browser verify required but not run"
	if notes := strings.TrimSpace(v.BrowserNotes); notes != "" {
		reason += ": " + notes
	}
	return reason
}

// pauseNoBrowser blamelessly parks a UI slice that BROWSER_VERIFY=always required
// to be driven but the verifier did not. Like the other pauses the WIP stays on
// its branch at the last checkpoint and the ticket is neither quarantined nor
// bug-filed — a browser skip is a config/environment gap, not a code defect. A
// rerun continues once a reachable APP_URL and automation browser are in place.
func (p *Pipeline) pauseNoBrowser(id, reason string) error {
	p.markPaused(id, reason)
	p.logf("  ⏸ paused — %s", reason)
	p.logf("  ↳ %s left resumable on its branch; give browser verify a reachable APP_URL and automation browser, then rerun trau", id)
	p.emitState(id, "verify", "paused", "browser_verify")
	return &PausedError{ID: id, Phase: "verify", Provider: "browser", Reason: reason}
}

func (p *Pipeline) giveUp(ctx context.Context, id, reason string) error {
	// Idempotent: a ticket already quarantined this run (e.g. a budget guard that
	// fired inside build, whose *GiveUpError then flows through handleGiveUp) must
	// not be finalized or quarantined twice.
	if p.State.Get(id, "PHASE") == state.Quarantined {
		return &GiveUpError{ID: id, Reason: reason}
	}
	p.finalizeFailed(ctx, id)
	p.expireSteer(ctx, id)
	if err := p.State.Set(id, "PHASE", state.Quarantined); err != nil {
		return fmt.Errorf("give up %s: checkpoint quarantined: %w", id, err)
	}
	_ = p.State.Set(id, "FAILURE_REASON", reason)
	p.emitState(id, state.Quarantined, "quarantined", reason)
	p.logf("  ✗ quarantining %s: %s", id, reason)
	if err := p.Tracker.Quarantine(ctx, id, reason); err != nil {
		p.logf("  quarantine MCP error (continuing): %v", err)
	}
	return &GiveUpError{ID: id, Reason: reason}
}

func (p *Pipeline) finalizeFailed(ctx context.Context, id string) {
	p.preserveAndClean(ctx, fmt.Sprintf("wip(%s): quarantined attempt — needs human", id))
}

// CommitAndPR ships the verified slice: the commit phase stages and commits ONLY
// this ticket's files, then the branch is pushed and a PR opened against Base — or
// an existing PR reused when a prior run already created one. It checkpoints
// pr_open with PR/PR_URL and moves the ticket to In Review with the PR link.
// A push/PR failure aborts this ticket (returned to the caller) without
// quarantining — the WIP stays on the branch for a later resume.
func (p *Pipeline) CommitAndPR(ctx context.Context, id string) error {
	p.setActivity(id, activity.Commit, "")
	if err := p.commitSlice(ctx, id); err != nil {
		return err
	}
	if err := p.pushDeliverable(ctx, id, "HEAD"); err != nil {
		return err
	}

	p.setActivity(id, activity.PR, "")
	branch := p.State.Get(id, "BRANCH")
	if branch == "" {
		if b, err := p.Git.CurrentBranch(ctx); err == nil {
			branch = b
		}
	}
	prURL, err := p.GitHub.PRURL(ctx, branch)
	if err != nil {
		return fmt.Errorf("commit %s: pr view: %w", id, err)
	}
	if prURL == "" {
		prBase := p.Base
		if p.EpicID != "" {
			prBase, err = p.epicBranchName(ctx)
			if err != nil {
				return fmt.Errorf("commit %s: resolve epic branch: %w", id, err)
			}
		}
		prURL, err = p.createOrAdoptPR(ctx, prBase, branch, p.slicePRTitle(ctx, id, prBase, branch), p.prBody(ctx, id))
		if err != nil {
			return fmt.Errorf("commit %s: pr create: %w", id, err)
		}
	}
	p.logf("  PR %s", prURL)
	if err := p.State.Set(id, "PR", prNumber(prURL)); err != nil {
		return fmt.Errorf("commit %s: record PR: %w", id, err)
	}
	p.emitEvent("pr_open", map[string]any{"number": prNumberInt(prURL), "url": prURL})
	if err := p.State.Set(id, "PR_URL", prURL); err != nil {
		return fmt.Errorf("commit %s: record PR_URL: %w", id, err)
	}
	if err := p.setPhase(id, state.PROpen); err != nil {
		return fmt.Errorf("commit %s: checkpoint pr_open: %w", id, err)
	}
	if err := p.Tracker.SetStatus(ctx, id, "In Review", "Attach this PR link to the issue: "+prURL+"."); err != nil {
		p.logf("  status (In Review) error: %v", err)
	}
	return nil
}

// commitSlice records the verified slice. A squash repo takes the deterministic
// path — the squash collapses the message anyway, so a full commit agent is pure
// overhead — while other merge methods keep the agent commit, which may split the
// work into atomic commits. DeterministicCommit=false restores the agent commit for
// squash repos whose commit conventions need judgment. A deterministic commit the
// repo's own hooks reject falls back to the agent commit with the rejection quoted:
// the repo stays the authority on message format, and the agent satisfies
// conventions no template can know.
func (p *Pipeline) commitSlice(ctx context.Context, id string) error {
	hookNote := ""
	if p.MergeMethod == "squash" && p.DeterministicCommit {
		err := p.deterministicCommit(ctx, id)
		var rejected *commitRejectedError
		if !errors.As(err, &rejected) {
			return err
		}
		p.logf("  deterministic commit rejected by the repo's hooks — retrying via the commit agent")
		hookNote = " A templated commit was just rejected by this repository's hooks (" + sanitize.FeedLine(rejected.Error()) + "). The changes are still staged: read the repo's commit conventions (commit hooks, lint config, recent git log) and write a message they accept."
	}
	rubricRef, _ := p.activeRubric(id)
	_, err := p.agentStep(ctx, id, "commit", commitInstruction(p.prompts, id, commitRubricNote(rubricRef), p.MergeMethod == "squash")+hookNote)
	return err
}

// commitRejectedError marks a failure of the deterministic git-commit step itself —
// most commonly the repo's commit-msg/pre-commit hook rejecting the templated
// message — as opposed to a staging failure. commitSlice catches it and retries via
// the commit agent.
type commitRejectedError struct{ err error }

func (e *commitRejectedError) Error() string { return e.err.Error() }
func (e *commitRejectedError) Unwrap() error { return e.err }

// deterministicCommit stages the slice and commits it with a templated Conventional
// Commit, no agent. Under the clean-base invariant the whole dirty tree is this
// slice's work (user WIP was autostashed), so AddAll (git add -A) stages every
// tracked change plus untracked non-ignored files. Hooks run (noVerify=false): a
// staging failure keeps the plain fault path, while a rejected commit returns a
// *commitRejectedError so commitSlice can retry via the commit agent.
func (p *Pipeline) deterministicCommit(ctx context.Context, id string) error {
	if err := p.Git.AddAll(ctx); err != nil {
		return fmt.Errorf("commit %s: stage: %w", id, err)
	}
	msg := deterministicCommitMessage(id, p.commitTitle(ctx, id))
	if err := p.Git.Commit(ctx, msg, false); err != nil {
		return &commitRejectedError{err: fmt.Errorf("commit %s: %w", id, err)}
	}
	p.logf("  committed %s", strings.SplitN(msg, "\n", 2)[0])
	return nil
}

// commitTitle resolves the slice's title for the commit subject: the checkpointed
// TITLE (set when the branch was cut) first, then a fresh tracker lookup as a
// fallback. Empty is tolerated — deterministicCommitMessage falls back to the id.
func (p *Pipeline) commitTitle(ctx context.Context, id string) string {
	if t := strings.TrimSpace(p.State.Get(id, "TITLE")); t != "" {
		return t
	}
	t, err := p.Tracker.Title(ctx, id)
	if err != nil {
		p.logf("  title lookup for commit subject failed (using id): %v", err)
		return ""
	}
	return strings.TrimSpace(t)
}

// CIAndMerge is the CI gate + merge. It reconciles first: a PR a prior run
// already merged is marked Done without re-merging. Otherwise it polls
// CI; on green it squash-merges and deletes the branch when AutoMerge is set (else
// it stops at the open PR), moves the ticket to Done, and checkpoints merged. A CI
// failure or timeout gives up — preserving the branch and quarantining without
// aborting the loop. A merge GitHub refuses as "not mergeable" (the base moved
// under the PR) goes through recoverUnmergeablePR — sync, agent-resolved
// conflicts, one more CI gate — before it too becomes a give-up, never a fault.
func (p *Pipeline) CIAndMerge(ctx context.Context, id string) error {
	pr := p.State.Get(id, "PR")
	if prState, _ := p.GitHub.PRState(ctx, pr); prState == "MERGED" {
		if err := p.markDone(ctx, id, "  ✓ %s already merged — marked Done"); err != nil {
			return err
		}
		return ErrAlreadyDone
	}

	p.setActivity(id, activity.CIWait, "")
	if err := p.pollCI(ctx, pr); err != nil {
		p.logf("  ✗ CI: %v", err)
		return p.giveUp(ctx, id, "CI not green")
	}
	if !p.AutoMerge {
		return p.awaitManualMerge(ctx, id, pr)
	}
	p.setActivity(id, activity.Merge, "")
	err := p.mergePR(ctx, pr)
	if unmergeablePR(err) {
		err = p.recoverUnmergeablePR(ctx, id, pr, err)
	}
	if err != nil {
		if isGiveUp(err) {
			return err
		}
		return fmt.Errorf("merge %s: %w", id, err)
	}
	return p.markDone(ctx, id, "  ✓ merged %s, marked Done")
}

const (
	prStatusAwaitingMerge = "awaiting-merge"
	prStatusMerged        = "merged"
	prStatusClosed        = "closed"
)

// setPRStatus is display-only, so a write failure logs and continues rather
// than aborting the run.
func (p *Pipeline) setPRStatus(id, status string) {
	if err := p.State.Set(id, "PR_STATUS", status); err != nil {
		p.logf("  pr status (%s) error (continuing): %v", status, err)
	}
}

// awaitManualMerge is the AUTO_MERGE=0 ticket path: CI is green, so it waits for the
// operator to merge the PR by hand. A merge marks the ticket Done; a close without
// merge is a human rejection (give-up); a canceled context stops blamelessly. The
// awaiting-merge status is stamped here rather than inside the shared wait so the
// epic finalize path never opens a checkpoint under an epic id that has none.
func (p *Pipeline) awaitManualMerge(ctx context.Context, id, pr string) error {
	p.setPRStatus(id, prStatusAwaitingMerge)
	merged, err := p.waitForManualMerge(ctx, id, pr, p.State.Get(id, "PR_URL"))
	if err != nil {
		return err
	}
	if merged {
		return p.markDone(ctx, id, "  ✓ merged %s, marked Done")
	}
	p.setPRStatus(id, prStatusClosed)
	return p.giveUp(ctx, id, fmt.Sprintf("PR #%s closed without merge", pr))
}

// waitForManualMerge is the shared AUTO_MERGE=0 wait for a human to merge a green
// PR. It enters the "awaiting manual merge" activity, fires the one-time notification
// carrying the PR number and URL, then polls PRState at the CI cadence with no
// timeout: true once the PR merges, false on a close without merge. A canceled
// context returns its error (blameless stop); a transient lookup error never ends the
// wait. Both the ticket and epic finalize paths drive their own terminal handling off
// the returned outcome.
func (p *Pipeline) waitForManualMerge(ctx context.Context, id, pr, url string) (bool, error) {
	p.setActivity(id, activity.MergeWait, "")
	p.logf("  ⏳ green CI — awaiting manual merge of PR #%s (AUTO_MERGE=0)", pr)
	p.emitAwaitingMerge(id, pr, url)
	warnedLookup := false
	for {
		switch st, err := p.GitHub.PRState(ctx, pr); {
		case st == "MERGED":
			return true, nil
		case st == "CLOSED":
			return false, nil
		case err != nil && !warnedLookup:
			p.logf("  PR #%s state lookup failing (still awaiting merge): %v", pr, err)
			warnedLookup = true
		case err == nil:
			warnedLookup = false
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		p.sleep(p.CIPoll)
	}
}

// emitAwaitingMerge records the one-time state_change that tells the operator a
// green PR is theirs to merge, riding the same pathway as the pause/fault/quarantine
// notifications and carrying the PR number and URL so the hub notification links to it.
func (p *Pipeline) emitAwaitingMerge(id, pr, url string) {
	if p.Events == nil {
		return
	}
	fields := map[string]any{"ticket": id, "state": "awaiting_merge", "pr": pr}
	if url != "" {
		fields["url"] = url
	}
	p.Events.Emit("state_change", state.PROpen, "PR #"+pr+" awaiting your merge", fields)
}

// mergePR merges pr with the transient-retry guard, adopting a merge a prior
// attempt (or a racing actor) already completed.
func (p *Pipeline) mergePR(ctx context.Context, pr string) error {
	return p.retryGH(ctx, "gh pr merge", func() error {
		if st, _ := p.GitHub.PRState(ctx, pr); st == "MERGED" {
			return nil
		}
		return p.GitHub.Merge(ctx, pr, p.MergeMethod, true)
	})
}

// recoverUnmergeablePR handles GitHub's deterministic "not mergeable" refusal:
// the PR's base moved after it opened — in the epic flow, typically a sibling
// squash-merging into the epic branch — and now conflicts with it. The recovery
// mirrors the epic finalize sync: merge the remote base INTO the feature branch
// (an agent resolves real conflicts, bounded by MaxRepairs), push the merge
// commit, re-gate CI, and retry the merge. A PR that stays unmergeable is a
// verified dead end → give-up (quarantine + needs-human, session keeps going),
// NOT an "unexpected error" fault that stops the whole session.
func (p *Pipeline) recoverUnmergeablePR(ctx context.Context, id, pr string, mergeErr error) error {
	base, err := p.buildBase(ctx)
	if err != nil {
		return err
	}
	branch := p.State.Get(id, "BRANCH")
	if branch == "" {
		branch, _ = p.Git.FindFeatureBranch(ctx, id)
	}
	if branch == "" {
		return p.giveUp(ctx, id, fmt.Sprintf("PR %s conflicts with %s and no feature branch was found to sync — resolve manually (%v)", pr, base, mergeErr))
	}
	p.logf("  ⚠ PR %s is not mergeable — syncing %s with %s to resolve", pr, branch, base)
	if err := p.checkoutExisting(ctx, branch); err != nil {
		return p.giveUp(ctx, id, fmt.Sprintf("PR %s conflicts with %s and branch %s could not be checked out to sync — resolve manually", pr, base, branch))
	}
	synced, err := p.syncBranchWithBase(ctx, id, branch, base, "merge-sync")
	if err != nil {
		return err
	}
	if !synced {
		return p.giveUp(ctx, id, fmt.Sprintf("PR %s conflicts with %s and the conflicts could not be auto-resolved — resolve manually", pr, base))
	}
	p.setActivity(id, activity.CIWait, "")
	if err := p.pollCI(ctx, pr); err != nil {
		p.logf("  ✗ CI after conflict sync: %v", err)
		return p.giveUp(ctx, id, "CI not green after syncing the PR with "+base)
	}
	// The sync just pushed a new PR head and GitHub recomputes mergeability
	// asynchronously, so a stale "not mergeable" right after the push gets a few
	// paced retries before it is believed.
	p.setActivity(id, activity.Merge, "")
	for attempt := 0; ; attempt++ {
		err := p.mergePR(ctx, pr)
		switch {
		case err == nil:
			return nil
		case !unmergeablePR(err):
			return err
		case attempt >= 2:
			return p.giveUp(ctx, id, fmt.Sprintf("PR %s is still not mergeable after syncing with %s: %v", pr, base, err))
		}
		p.logf("  ⟳ PR %s still reports not mergeable — waiting for GitHub to recompute (%d/2)", pr, attempt+1)
		p.sleep(5)
	}
}

// checkoutExisting checks out branch, adopting it from the remote when it is
// missing locally (e.g. a resume in a fresh clone).
func (p *Pipeline) checkoutExisting(ctx context.Context, branch string) error {
	if exists, _ := p.Git.BranchExists(ctx, branch); exists {
		return p.Git.Checkout(ctx, branch, false)
	}
	return p.Git.CheckoutRemoteBranch(ctx, p.Remote, branch)
}

// syncBranchWithBase merges the remote base into the checked-out branch so its PR
// becomes mergeable again. A clean merge is pushed; a conflict is resolved by a
// bounded repair-agent loop (labeled label<N>), then the merge is completed and
// pushed. Returns false (with the merge aborted) when the conflicts could not be
// resolved, so the caller leaves the PR to a human instead of shipping a broken
// merge.
func (p *Pipeline) syncBranchWithBase(ctx context.Context, id, branch, base, label string) (bool, error) {
	conflicted, err := p.Git.MergeRemote(ctx, p.Remote, base)
	if err != nil {
		return false, fmt.Errorf("merge %s into %s: %w", base, branch, err)
	}
	if !conflicted {
		if err := p.Git.Push(ctx, p.Remote, branch, false); err != nil {
			p.logf("  push synced branch %s error (continuing): %v", branch, err)
		}
		return true, nil
	}

	p.setActivity(id, activity.Merge, label)
	p.logf("  ⚠ %s conflicts with %s — resolving merge conflicts", branch, base)
	maxAttempts := p.MaxRepairs
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if _, err := p.agentStep(ctx, id, fmt.Sprintf("%s%d", label, attempt), resolveConflictsInstruction(p.prompts, id, base, branch)); err != nil {
			return false, err
		}
		if unmerged, _ := p.Git.Unmerged(ctx); strings.TrimSpace(unmerged) == "" {
			if err := p.Git.ContinueMerge(ctx); err != nil {
				return false, fmt.Errorf("complete merge: %w", err)
			}
			if err := p.Git.Push(ctx, p.Remote, branch, false); err != nil {
				p.logf("  push synced branch %s error (continuing): %v", branch, err)
			}
			return true, nil
		}
		p.logf("  ⚠ conflicts remain after attempt %d/%d", attempt, maxAttempts)
	}
	_ = p.Git.MergeAbort(ctx)
	return false, nil
}

func (p *Pipeline) markDone(ctx context.Context, id, logFmt string) error {
	if err := p.Tracker.SetStatus(ctx, id, "Done", ""); err != nil {
		p.logf("  status (Done) error: %v", err)
	} else if err := p.State.Set(id, "TRACKER_DONE", "1"); err != nil {
		p.logf("  checkpoint TRACKER_DONE error (continuing): %v", err)
	}
	if err := p.State.Set(id, "PHASE", state.Merged); err != nil {
		return fmt.Errorf("merge %s: checkpoint merged: %w", id, err)
	}
	p.setPRStatus(id, prStatusMerged)
	p.expireSteer(ctx, id)
	p.emitEvent("ci", map[string]any{"state": "merged"})
	p.emitState(id, state.Merged, "merged", "")
	p.recordTimelog(ctx, id)
	p.logf(logFmt, id)
	return nil
}

func (p *Pipeline) pollCI(ctx context.Context, pr string) error {
	if !p.RequireCI {
		p.logf("  CI gate off (REQUIRE_CI=0) — not waiting for checks")
		return nil
	}
	expected := splitChecks(p.ExpectedChecks)
	sawCheck := false
	for waited := 0; ; waited += p.CIPoll {
		checks, _ := p.GitHub.Checks(ctx, pr)
		if len(checks) > 0 {
			sawCheck = true
		}
		switch evalChecks(checks, expected) {
		case ciFailed:
			p.emitEvent("ci", map[string]any{"state": "failing"})
			return ErrCIFailed
		case ciGreen:
			p.emitEvent("ci", map[string]any{"state": "green"})
			return nil
		}
		if waited >= p.CITimeout {
			if !sawCheck && len(expected) == 0 {
				if config.ScanPullRequestCI(p.RepoRoot).AllPathFiltered {
					p.logf("  ⓘ no checks appeared and every PR workflow is path-filtered — this change matches none of them; skipping the CI gate")
					p.emitEvent("ci", map[string]any{"state": "skipped"})
					return nil
				}
				p.logf("  ⓘ no checks ever appeared — if this repo has no PR CI, set REQUIRE_CI=0 to skip the gate")
			}
			p.emitEvent("ci", map[string]any{"state": "failing"})
			return ErrCITimeout
		}
		p.emitEvent("ci", map[string]any{"state": "pending", "poll_secs": p.CIPoll})
		p.sleep(p.CIPoll)
	}
}

func (p *Pipeline) sleep(seconds int) {
	d := time.Duration(seconds) * time.Second
	if p.Sleep != nil {
		p.Sleep(d)
		return
	}
	time.Sleep(d)
}

// retryGH runs an idempotent gh/git operation, retrying transient failures with
// exponential backoff (1s, 2s) before giving up. Deterministic failures
// (retryableGH == false) return at once. op must re-check remote state so a
// partially-applied first attempt is adopted, not duplicated.
func (p *Pipeline) retryGH(ctx context.Context, what string, op func() error) error {
	const attempts = 3
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = op(); err == nil {
			return nil
		}
		if attempt == attempts || !retryableGH(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		backoff := 1 << (attempt - 1)
		p.logf("  ⟳ %s failed (%v) — retrying in %ds (%d/%d)", what, err, backoff, attempt, attempts-1)
		p.sleep(backoff)
	}
	return err
}

// retryableGH reports whether a failed gh/git command is worth retrying. It is
// optimistic: anything that is not a recognized deterministic failure (one a
// retry cannot fix — bad input, auth, a missing or duplicate resource) is treated
// as a transient hiccup. It reads the error text, which now carries the command's
// stderr (see withStderr).
func retryableGH(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, deterministic := range []string{
		"no commits between",
		"already exists",
		"not found",
		"could not resolve to",
		"unauthorized", "authentication", "permission", "forbidden",
		"not mergeable", "merge conflict",
		"http 401", "http 403", "http 404", "http 422",
	} {
		if strings.Contains(s, deterministic) {
			return false
		}
	}
	return true
}

// unmergeablePR reports whether a gh pr merge failure means GitHub refused the
// PR in its current state ("not mergeable": conflicting with its base, or still
// recomputing mergeability after a push) — the one class of deterministic merge
// failure the pipeline can fix itself by syncing the branch with its base. A
// policy block ("the base branch policy prohibits the merge") also matches: the
// sync is then a no-op and the bounded retries funnel it into a clear give-up
// instead of an "unexpected error" fault.
func unmergeablePR(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{"not mergeable", "merge conflict", "cannot be cleanly created"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// pushOutcome classifies why a deliverable push failed, so the commit phase can
// treat a hook rejection as repairable feedback rather than a transient hiccup.
type pushOutcome int

const (
	pushOK             pushOutcome = iota
	pushTransient                  // auth-less network hiccup — retry with backoff
	pushLocalHook                  // a local pre-push hook rejected the diff — repair
	pushRemoteRejected             // remote-side hook/policy declined it — repair
	pushNonFastForward             // remote moved on — deterministic, needs a sync
	pushDeterministic              // auth / other non-retryable, non-repairable failure
)

// pushDeliverable pushes the committed slice with the repo's pre-push hooks live.
// A local hook rejection (or a remote-side decline) is deterministic feedback about
// the committed code — the same class as a red verify — so it is routed into the
// bounded repair loop (REPAIRS) instead of being blind-retried or faulted: each
// retry would otherwise re-run the repo's entire check suite. Auth/network hiccups
// keep the transient retry path; repairs exhausted → normal give-up (WIP preserved,
// session continues); a non-fast-forward or other deterministic failure returns to
// the caller (fault, resumable). A green push returns nil.
func (p *Pipeline) pushDeliverable(ctx context.Context, id, ref string) error {
	repairs := 0
	for {
		outcome, err := p.retryPush(ctx, ref)
		if outcome == pushOK {
			return nil
		}
		if outcome != pushLocalHook && outcome != pushRemoteRejected {
			return fmt.Errorf("commit %s: push: %w", id, err)
		}
		if repairs >= p.MaxRepairs {
			return p.giveUp(ctx, id, fmt.Sprintf("push rejected by a pre-push gate after %d repair attempt(s)", repairs))
		}
		repairs++
		p.logf("  ⚠ push rejected by a pre-push gate — repair attempt %d/%d", repairs, p.MaxRepairs)
		notesRef, _ := p.activeBuildNotes(id)
		if _, err := p.agentStep(ctx, id, fmt.Sprintf("push-repair%d", repairs), pushRepairInstruction(p.prompts, id, err.Error(), buildNotesNote(notesRef))); err != nil {
			return err
		}
	}
}

// retryPush pushes ref (hooks live), retrying only genuinely transient failures
// (auth-less network hiccups) with the same backoff as retryGH. It classifies each
// failure with classifyPush so a local pre-push hook rejection is never blind-retried
// — every retry would re-run the repo's whole check suite for zero chance of success.
// It returns the final classified outcome and the last error.
func (p *Pipeline) retryPush(ctx context.Context, ref string) (pushOutcome, error) {
	const attempts = 3
	var (
		err     error
		outcome pushOutcome
	)
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = p.Git.Push(ctx, p.Remote, ref, false); err == nil {
			return pushOK, nil
		}
		outcome = p.classifyPush(ctx, ref)
		if outcome != pushTransient || attempt == attempts {
			return outcome, err
		}
		if ctx.Err() != nil {
			return outcome, ctx.Err()
		}
		backoff := 1 << (attempt - 1)
		p.logf("  ⟳ git push failed (%v) — retrying in %ds (%d/%d)", err, backoff, attempt, attempts-1)
		p.sleep(backoff)
	}
	return outcome, err
}

// classifyPush decides why a real push of ref failed, using git's own behavior
// rather than any hook tool's output. It probes the remote with hooks bypassed
// (git push --dry-run --no-verify): if the remote WOULD accept the ref, the only
// thing that blocked the real push was a local pre-push hook; otherwise the probe's
// error carries git's ref-level reason, which classifyRemotePushErr maps.
func (p *Pipeline) classifyPush(ctx context.Context, ref string) pushOutcome {
	return classifyRemotePushErr(p.Git.PushDryRun(ctx, p.Remote, ref))
}

// classifyRemotePushErr maps a hook-bypassed push failure to an outcome using only
// git's stable ref-level markers — never hook-tool output, whose format differs per
// language and manager. A nil error means the remote would accept the ref.
func classifyRemotePushErr(probeErr error) pushOutcome {
	if probeErr == nil {
		return pushLocalHook
	}
	s := strings.ToLower(probeErr.Error())
	switch {
	case strings.Contains(s, "[remote rejected]"):
		return pushRemoteRejected
	case strings.Contains(s, "[rejected]"), strings.Contains(s, "fetch first"), strings.Contains(s, "non-fast-forward"):
		return pushNonFastForward
	case retryableGH(probeErr):
		return pushTransient
	default:
		return pushDeterministic
	}
}

// createOrAdoptPR opens a PR, retrying transient failures. If a create attempt
// fails but a PR for the branch already exists (a prior attempt, or a concurrent
// run), it adopts that PR rather than failing or opening a duplicate.
func (p *Pipeline) createOrAdoptPR(ctx context.Context, base, branch, title, body string) (string, error) {
	var url string
	err := p.retryGH(ctx, "gh pr create", func() error {
		created, e := p.GitHub.CreatePR(ctx, base, branch, title, body)
		if e == nil {
			url = created
			return nil
		}
		if existing, e2 := p.GitHub.PRURL(ctx, branch); e2 == nil && existing != "" {
			url = existing
			return nil
		}
		return e
	})
	return url, err
}

type ciStatus int

const (
	ciWaiting ciStatus = iota
	ciGreen
	ciFailed
)

func evalChecks(checks []Check, expected []string) ciStatus {
	for _, c := range checks {
		if c.Bucket == "fail" || c.Bucket == "cancel" {
			return ciFailed
		}
	}
	if len(expected) > 0 {
		for _, name := range expected {
			if !hasGreenNamed(checks, name) {
				return ciWaiting
			}
		}
		return ciGreen
	}
	if len(checks) == 0 {
		return ciWaiting
	}
	for _, c := range checks {
		if c.Bucket == "pending" {
			return ciWaiting
		}
	}
	return ciGreen
}

func hasGreenNamed(checks []Check, pattern string) bool {
	re, err := regexp.Compile("(?i)" + pattern)
	for _, c := range checks {
		if c.Bucket != "pass" && c.Bucket != "skipping" {
			continue
		}
		if err == nil {
			if re.MatchString(c.Name) {
				return true
			}
		} else if strings.Contains(strings.ToLower(c.Name), strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

func splitChecks(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func prNumber(url string) string { return url[strings.LastIndex(url, "/")+1:] }

// prNumberInt is prNumber parsed to an int, or 0.
func prNumberInt(url string) int {
	n, _ := strconv.Atoi(prNumber(url))
	return n
}

var (
	reBranchType = regexp.MustCompile(`^[a-z]+/`)
	reBranchID   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+-`)
)

func prDesc(branch string) string {
	slug := branch
	if i := strings.LastIndex(branch, "/"); i >= 0 {
		slug = branch[i+1:]
	}
	slug = reBranchType.ReplaceAllString(slug, "")
	slug = reBranchID.ReplaceAllString(slug, "")
	return strings.ReplaceAll(slug, "-", " ")
}

// slicePRTitle titles the slice PR with the branch's sole commit subject when
// exactly one commit sits above the base: that message already passed the repo's
// own commit hooks, so a squash-merge title (and any PR-title lint the repo runs)
// conforms wherever commits do. Multi-commit branches and lookup failures fall
// back to the '<id>: <branch words>' template.
func (p *Pipeline) slicePRTitle(ctx context.Context, id, base, branch string) string {
	if shas, err := p.Git.Commits(ctx, base, branch); err == nil && len(shas) == 1 {
		if subject, err := p.Git.CommitSubject(ctx, branch); err == nil && strings.TrimSpace(subject) != "" {
			return strings.TrimSpace(subject)
		}
	}
	return id + ": " + prDesc(branch)
}

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(title string) string {
	s := reSlug.ReplaceAllString(strings.ToLower(title), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return ""
	}
	words := strings.Split(s, "-")
	if len(words) > 6 {
		words = words[:6]
	}
	return strings.Join(words, "-")
}

// agentPhaseOn runs one phase on a specific runner (the primary loop runner, or a
// single panel verifier's backend). The label and transcript are keyed off phase,
// so panel members must pass distinct phase tags to avoid clobbering each other.
func (p *Pipeline) agentPhaseOn(ctx context.Context, id, phase, prompt string, runner agent.Runner) (string, error) {
	label := runnerLabel(phase, runner)
	p.logf("  ▸ %s", label)
	stop := p.spin(label)
	res, err := runner.Run(ctx, prompt, phase)
	stop()
	switch phase {
	case "build":
		p.buildSkills, p.buildProvider, p.buildSkillsKnown = res.Skills, phaseProvider(runner, phase), res.SkillsKnown
	case "verify":
		p.verifySkills, p.verifyProvider, p.verifySkillsKnown = res.Skills, phaseProvider(runner, phase), res.SkillsKnown
	}
	p.putPhaseLog(id, phase, res.Final)
	return res.Final, err
}

func phaseProvider(runner agent.Runner, phase string) string {
	pr, ok := runner.(agent.PhaseRoute)
	if !ok {
		return ""
	}
	provider, _, _ := pr.Route(phase)
	return provider
}

func runnerLabel(phase string, runner agent.Runner) string {
	pr, ok := runner.(agent.PhaseRoute)
	if !ok {
		return phase
	}
	if tag := routeTag(pr.Route(phase)); tag != "" {
		return phase + " · " + tag
	}
	return phase
}

func routeTag(provider, model, effort string) string {
	name := strings.TrimPrefix(model, "claude-")
	if name == "" {
		name = provider
	}
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

func (p *Pipeline) spin(phase string) func() {
	if p.Renderer == nil {
		return func() {}
	}
	return p.Renderer.Spin(phase)
}

func (p *Pipeline) logf(format string, a ...any) {
	if p.Renderer != nil {
		p.Renderer.Logf(format, a...)
	}
}

// emitEvent forwards a display-only event to the renderer (live TUI header);
// no-op without one. It does not touch the durable event log.
func (p *Pipeline) emitEvent(kind string, fields map[string]any) {
	if p.Renderer != nil {
		p.Renderer.Event(event.Event{Kind: kind, Fields: fields})
	}
}

// emitState records a durable state_change for id — the signal the dashboard
// recap and browser notifications consume. state ∈ {merged, quarantined, faulted,
// paused, stopped}; reason distinguishes a blameless pause (usage_window vs
// reauth), names the phase a fault or stop cut short, and carries the give-up text
// for a quarantine. No-op without a durable log.
func (p *Pipeline) emitState(id, phase, st, reason string) {
	if p.Events == nil {
		return
	}
	fields := map[string]any{"ticket": id, "state": st}
	if reason != "" {
		fields["reason"] = reason
	}
	p.Events.Emit("state_change", phase, "", fields)
}

// logAgentErr surfaces an agent failure as a single clean line: a paused glyph for
// provider rate/usage limits, an error glyph otherwise.
func (p *Pipeline) logAgentErr(phase string, err error) {
	msg, _ := agentErrSummary(err)
	p.logf("  ✗ %s error — %s", phase, msg)
}

// agentStep runs a phase agent through transient-failure recovery: the primary
// runner first, retried on a fresh process, then each configured fallback
// provider. A provider rate/usage limit short-circuits to a blameless *PausedError
// (never retried); a verified give-up never reaches here. Only when the whole
// chain is exhausted is the error returned, for the caller to funnel into the
// WIP-preserving fault path.
func (p *Pipeline) agentStep(ctx context.Context, id, phase, prompt string) (string, error) {
	return p.recoverStep(ctx, id, phase, prompt, p.recoveryChain(phase, p.Runner))
}

// agentStepOn runs a phase against ONE specific runner — a cross-vendor
// verify-panel member — through the same budget guard, transient-retry, and
// rate-limit classification as the primary phases, but with no provider fallback:
// the panel deliberately pins each member to its provider.
func (p *Pipeline) agentStepOn(ctx context.Context, id, phase, prompt string, runner agent.Runner) (string, error) {
	return p.recoverStep(ctx, id, phase, prompt, []agent.Runner{runner})
}

// recoveryChain is the ordered list of runners a phase tries: the primary first,
// then the configured fallback-provider backends. A nil/empty Fallback yields just
// the primary (retry-only).
func (p *Pipeline) recoveryChain(phase string, primary agent.Runner) []agent.Runner {
	chain := []agent.Runner{primary}
	if p.Fallback != nil {
		for _, r := range p.Fallback(phase) {
			if r != nil {
				chain = append(chain, r)
			}
		}
	}
	return chain
}

// recoverStep drives a phase agent through bounded transient-failure recovery. It
// tries each runner in the chain in order; each is retried up to AgentRetries
// times on a TRANSIENT failure (timeout, output stall, non-rate-limit crash), on a
// fresh process, with a backoff that grows by attempt. A provider rate/usage limit
// short-circuits to a blameless pause and is never retried; an outer-context
// cancellation (user interrupt) stops immediately without burning retries. When
// every runner and retry is exhausted the last error is returned — wrapped with
// the attempt/provider count when recovery was actually attempted — so the caller
// funnels it into the WIP-preserving fault path. A single-entry chain with
// AgentRetries==0 is exactly the old single-shot behavior.
func (p *Pipeline) recoverStep(ctx context.Context, id, phase, prompt string, chain []agent.Runner) (string, error) {
	// Drained once for the whole chain, so a retry or a fallback provider still
	// carries the notes the first attempt was handed.
	if agent.SteerablePhase(phase) {
		prompt += p.steerSection(ctx, id, phase)
		ctx = agent.WithSteer(ctx, p.steerSource(id))
	}
	retries := p.AgentRetries
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	runs := 0
	for ci, runner := range chain {
		for attempt := 0; ; attempt++ {
			if err := p.guardBudget(ctx, id); err != nil {
				return "", err
			}
			runs++
			out, err := p.agentPhaseOn(ctx, id, phase, prompt, runner)
			if err == nil {
				return out, nil
			}
			if isRateLimited(err) {
				return out, p.pause(id, phase, err)
			}
			if isAuthFailure(err) {
				return out, p.pauseAuth(id, phase, err)
			}
			lastErr = err
			if ctx.Err() != nil {
				p.logAgentErr(phase, err)
				return out, err
			}
			if attempt >= retries {
				break
			}
			msg, _ := agentErrSummary(err)
			p.logf("  ↻ %s failed (%s) — retrying %d/%d", phase, msg, attempt+1, retries)
			p.backoff(attempt)
		}
		if ci < len(chain)-1 {
			p.logf("  ⤳ %s: %s exhausted — falling back to %s", phase, providerLabel(chain[ci]), providerLabel(chain[ci+1]))
			p.backoff(0)
		}
	}
	p.logAgentErr(phase, lastErr)
	if runs <= 1 {
		return "", lastErr
	}
	return "", fmt.Errorf("agent step %q exhausted recovery after %d attempt(s) across %d provider(s): %w", phase, runs, len(chain), lastErr)
}

// backoff sleeps a growing delay before a transient retry: AgentBackoff*(n+1)
// seconds via the injected Sleep (a no-op in tests). Zero AgentBackoff is instant.
func (p *Pipeline) backoff(n int) {
	if p.AgentBackoff <= 0 {
		return
	}
	p.sleep(p.AgentBackoff * (n + 1))
}

// providerLabel names the backend a runner dispatches to, for the fallback log
// line; backends and the Router implement Provider(). Defaults to "provider".
func providerLabel(r agent.Runner) string {
	if pv, ok := r.(interface{ Provider() string }); ok {
		if name := pv.Provider(); name != "" {
			return name
		}
	}
	return "provider"
}

// pause logs the blameless stop and builds the *PausedError. The ticket keeps its
// last checkpoint, so a later run resumes it from there once the limit clears.
func (p *Pipeline) pause(id, phase string, err error) error {
	prov := providerOf(err)
	reason := prov + " rate/usage limit reached"
	p.markPaused(id, reason)
	p.logf("  ⏸ paused — %s usage/rate limit reached during %s", prov, phase)
	p.logf("  ↳ %s left resumable on its branch; rerun trau when the limit resets", id)
	p.emitState(id, phase, "paused", "usage_window")
	return &PausedError{ID: id, Phase: phase, Provider: prov, Reason: reason}
}

// pauseProviderUnavailable stops a ticket whose resolved Provider cannot be built
// on this machine. Nothing has run yet, so a rerun picks it up once the provider is
// installed or the pin is cleared; falling back to the repo default would silently
// run the ticket on a Provider nobody asked for.
func (p *Pipeline) pauseProviderUnavailable(id, provider string, err error) error {
	phase := p.State.Get(id, "PHASE")
	reason := "provider unavailable: " + err.Error()
	p.markPaused(id, reason)
	p.logf("  ⏸ paused — %s is pinned to %s, which is not available: %v", id, provider, err)
	p.logf("  ↳ install %s or clear the pin on %s, then rerun trau", provider, id)
	p.emitState(id, phase, "paused", "provider_unavailable")
	return &PausedError{ID: id, Phase: phase, Provider: provider, Reason: reason}
}

func isRateLimited(err error) bool {
	_, rl := agentErrSummary(err)
	return rl
}

// pauseAuth logs the blameless stop for a provider auth/login wall and builds the
// *PausedError. Unlike a rate limit it won't clear on its own — the human must
// re-authenticate the provider — so the message says so. The ticket keeps its last
// checkpoint and resumes from there once the provider is logged back in.
func (p *Pipeline) pauseAuth(id, phase string, err error) error {
	prov := providerOf(err)
	reason := prov + " authentication required — re-login"
	p.markPaused(id, reason)
	p.logf("  ⏸ paused — %s needs re-authentication during %s (run the provider's /login)", prov, phase)
	p.logf("  ↳ %s left resumable on its branch; rerun trau after re-authenticating %s", id, prov)
	p.emitState(id, phase, "paused", "reauth")
	return &PausedError{ID: id, Phase: phase, Provider: prov, Reason: reason}
}

// pauseHubUnreachable logs the blameless stop for a hub that stayed unreachable
// past the write-retry window (ADR 0008 §3) and builds the *PausedError. The WIP
// lives on the pushed feature branch and the last hub-persisted checkpoint is the
// resume point; the pause itself cannot be recorded in the database — the
// checkpoint writer is the hub — so it is surfaced in-process. A rerun once the
// hub is back reconnects and continues from the last persisted checkpoint.
func (p *Pipeline) pauseHubUnreachable(id string) error {
	phase := p.State.Get(id, "PHASE")
	reason := "hub unreachable — run data could not be saved"
	p.markPaused(id, reason)
	p.logf("  ⏸ paused — hub unreachable during %s; run data could not be saved", NextPhaseLabel(phase))
	p.logf("  ↳ %s left resumable on its branch; rerun trau once the hub is back", id)
	p.emitState(id, phase, "paused", "hub_unreachable")
	return &PausedError{ID: id, Phase: phase, Provider: "hub", Reason: reason}
}

// markPaused records the blameless pause on the ticket's checkpoint so a
// file-first reader (trau serve) can tell a pause apart from a fault while the
// loop is stopped. The next attempt clears it in Resume once the ticket runs
// again. Best-effort — a failed write never blocks the pause.
func (p *Pipeline) markPaused(id, reason string) {
	_ = p.State.Set(id, "FAILURE_CLASS", state.FailPaused)
	_ = p.State.Set(id, "FAILURE_REASON", reason)
}

// clearFailureMarks drops a prior attempt's failure marker — whatever its class —
// as the ticket is retried, so a resumed run that progresses no longer reads as
// failed. It only writes when a marker is actually present, so a fresh ticket
// keeps its first checkpoint being the build phase rather than an empty state file.
func (p *Pipeline) clearFailureMarks(id string) {
	if p.State.Get(id, "FAILURE_CLASS") == "" && p.State.Get(id, "FAILURE_REASON") == "" {
		return
	}
	_ = p.State.Set(id, "FAILURE_CLASS", "")
	_ = p.State.Set(id, "FAILURE_REASON", "")
}

// isAuthFailure reports whether err is (or wraps) the agent's auth/login-wall
// sentinel — a provider state that retrying can't fix and that isn't the ticket's
// fault, so the loop pauses blamelessly rather than burning retries.
func isAuthFailure(err error) bool {
	return agent.IsAuthRequired(err)
}

// guardBudget enforces the configured spend ceilings before an agent call. It
// reads the LIVE ledger totals from the hub (this ticket's total and the day's spend
// across all buckets) and, on the first cap reached, quarantines the ticket via
// giveUp with a cost-overrun reason — halting before the next call adds to the bill.
// A nil ledger or no configured cap is a no-op (back-compat).
func (p *Pipeline) guardBudget(ctx context.Context, id string) error {
	if p.Tokens == nil || !p.Budget.Enabled() {
		return nil
	}
	tt, tc, tm := p.Tokens.Total(id)
	b, ok := p.Budget.Check(budget.Spend{Tokens: tt, Cost: tc, Metered: tm}, p.dailySpend())
	if !ok {
		return nil
	}
	return p.giveUp(ctx, id, "budget cap reached — "+b.Reason())
}

// dailySpend reads the day's accumulated spend across every ticket bucket from the
// hub, keyed on the local date from p.Now (defaulting to time.Now).
func (p *Pipeline) dailySpend() budget.Spend {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	dt, dc, dm := p.Tokens.DayTotal(now().Format("2006-01-02"))
	return budget.Spend{Tokens: dt, Cost: dc, Metered: dm}
}

// BudgetExhausted reports whether today's spend has already reached a configured
// DAILY cap, with a human reason. The loop calls it before picking or resuming a
// ticket so a day already over budget stops the run cleanly — rather than
// quarantining every remaining ticket against the same exhausted ceiling. Per-ticket
// caps are not consulted here; those are enforced inline by guardBudget.
func (p *Pipeline) BudgetExhausted() (string, bool) {
	if p.Tokens == nil || !p.Budget.Enabled() {
		return "", false
	}
	b, ok := p.Budget.CheckDaily(p.dailySpend())
	if !ok {
		return "", false
	}
	return b.Reason(), true
}

var reProvider = regexp.MustCompile(`^(\w+)(?: \w+)? run \(`)

// providerOf best-effort extracts the backend name from a wrapped agent error
// like "kimi run (verify): …" or "claude interactive run (build): …"; defaults to
// "provider".
func providerOf(err error) string {
	if m := reProvider.FindStringSubmatch(err.Error()); m != nil {
		return m[1]
	}
	return "provider"
}

// setActivity reports the present-tense pipeline work the session is doing now
// (ADR 0009): it drives the live stepper through the renderer, advances the
// presence heartbeat through OnActivity, and emits an activity_change event on the
// durable log, so per-activity wall-clock — including non-agent waits like CI,
// invisible to agent_call durations — derives from event timestamp deltas. One
// writer, two displays: the TUI stepper and the web read the same signal. detail
// carries the raw call label (e.g. repair2), empty when there is none. Checkpoint
// phases are untouched; Activity is its own signal.
func (p *Pipeline) setActivity(id string, act activity.Activity, detail string) {
	if p.Renderer != nil {
		p.Renderer.Activity(act, detail)
	}
	if p.OnActivity != nil {
		p.OnActivity(id, string(act), detail)
	}
	if p.Events != nil {
		fields := map[string]any{"ticket": id, "activity": string(act)}
		if detail != "" {
			fields["detail"] = detail
		}
		p.Events.Emit("activity_change", "", "", fields)
	}
}

// setPhase writes the checkpoint phase and, on success, reports it through
// OnPhase so the instance registry can advance the session's working state. It
// carries only the working phases; the terminal merged/quarantined writes stay
// direct, since parked/idle is decided once the run ends.
func (p *Pipeline) setPhase(id, phase string) error {
	if err := p.State.Set(id, "PHASE", phase); err != nil {
		return err
	}
	if p.OnPhase != nil {
		p.OnPhase(id, phase)
	}
	return nil
}

func (p *Pipeline) setTitle(title string) {
	if title != "" && p.Renderer != nil {
		p.Renderer.SetTitle(title)
	}
}

const resumeNote = " A previous attempt may have left partial work on this branch; continue from it rather than starting over."

func (p *Pipeline) skillResolver() agent.SkillResolver {
	return agent.NewSkillResolver(p.RepoRoot, p.RequiredSkills, p.RequiredSkillsVerify)
}

// skillMatchText is what a build's routing rules match against: the ticket block
// the prompt already carries, plus the ticket's labels.
func skillMatchText(ticketCtx string, labels []string) string {
	if len(labels) == 0 {
		return ticketCtx
	}
	return ticketCtx + "\n" + strings.Join(labels, " ")
}

const (
	skillsModeInstruct = "instruct"
	skillsModeInject   = "inject"
)

// injectSkills reports whether resolved skill sets are delivered by injecting
// their SKILL.md content into the phase prompt (SKILLS_MODE=inject) rather than
// by naming them for the agent to load with the Skill tool (the default).
func (p *Pipeline) injectSkills() bool { return p.SkillsMode == skillsModeInject }

// phaseSkills is a phase's resolved set turned into what its prompt carries. In
// instruct mode note is the Skill-tool sentence and injection is empty; in inject
// mode note is empty, injection is the inline SKILL.md block, and activated names
// the skills whose content that block actually delivered — the run receipt.
type phaseSkills struct {
	set       agent.SkillSet
	note      string
	injection string
	activated []string
}

// resolveSkills turns a resolved set into its delivery for the current mode. The
// injection rides the prompt outside the phase template (see agentStep call
// sites), so a prompt-catalog override cannot drop it; the Skill-tool sentence is
// dropped entirely in inject mode.
func (p *Pipeline) resolveSkills(set agent.SkillSet, installed []string, verify bool) phaseSkills {
	if !p.injectSkills() {
		render := skillsPrompt
		if verify {
			render = verifySkillsPrompt
		}
		return phaseSkills{set: set, note: render(p.prompts, installed, set.Names)}
	}
	injected := agent.LoadInjectableSkills(p.RepoRoot, set.Names)
	return phaseSkills{
		set:       set,
		injection: skillInjectionBlock(injected),
		activated: injectedSkillNames(injected),
	}
}

// recordPhaseSkills files a phase attempt's skill set: the planned set in
// instruct mode, or the deterministically injected (Activated) set plus its byte
// size in inject mode. A phase that delivers nothing files nothing.
func (p *Pipeline) recordPhaseSkills(id, phase string, ps phaseSkills) {
	if p.Events == nil {
		return
	}
	if p.injectSkills() {
		if len(ps.activated) == 0 {
			return
		}
		p.logf("  ↳ injected %d skill(s), %s into %s", len(ps.activated), fmtBytes(int64(len(ps.injection))), phase)
		p.Events.Emit(event.KindSkillsPlanned, phase, "activated skills: "+strings.Join(ps.activated, ", "), map[string]any{
			"ticket": id,
			"skills": ps.activated,
			"source": ps.set.Source,
			"mode":   skillsModeInject,
			"bytes":  len(ps.injection),
		})
		return
	}
	if len(ps.set.Names) == 0 {
		return
	}
	p.Events.Emit(event.KindSkillsPlanned, phase, "planned skills: "+strings.Join(ps.set.Names, ", "), map[string]any{
		"ticket": id,
		"skills": ps.set.Names,
		"source": ps.set.Source,
		"mode":   skillsModeInstruct,
	})
}

// skillInjectionBlock renders the resolved set's SKILL.md contents as a
// self-contained prompt block: each skill under its own heading, preceded by the
// repo-relative path to its SKILL.md so the agent can open the skill's
// references/ and asset files itself. Empty when nothing injectable resolves.
func skillInjectionBlock(skills []agent.InjectedSkill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("The skills below are activated for this phase — their full SKILL.md contents are included inline, so you do not need the Skill tool to load them. Each is preceded by the repo-relative path to its SKILL.md; read that directory's referenced files yourself when a skill points to them.")
	for _, s := range skills {
		b.WriteString("\n\n===== SKILL: " + s.Name + " (" + s.Path + ") =====\n")
		b.WriteString(strings.TrimRight(s.Body, "\n"))
	}
	return b.String()
}

func injectedSkillNames(skills []agent.InjectedSkill) []string {
	if len(skills) == 0 {
		return nil
	}
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return names
}

// injectInto prepends a skill-injection block to a rendered phase prompt. The
// block lands outside the phase template, so it survives a prompt-catalog
// override that drops the template's skills note. A no-op on an empty block.
func injectInto(injection, prompt string) string {
	if injection == "" {
		return prompt
	}
	return injection + "\n\n" + prompt
}

// skillsPrompt renders the build-prompt sentence naming the skills to load. Only
// a repo with no installed skills renders an empty set, where the template falls
// back to generic self-selection wording (Claude Code stopped honoring that in
// 2.1.202, which is why every other repo names its skills explicitly).
func skillsPrompt(r prompts.Renderer, installed, resolved []string) string {
	return r.Render("skills", prompts.SkillsData{Installed: installed, Required: resolved})
}

// verifySkillsPrompt renders the verify-prompt sentence naming the skills to load.
func verifySkillsPrompt(r prompts.Renderer, installed, resolved []string) string {
	return r.Render("verify_skills", prompts.SkillsData{Installed: installed, Required: resolved})
}

func buildInstruction(r prompts.Renderer, id, branch, skillsNote, note, ticketCtx string) string {
	return r.Render("build", prompts.BuildData{
		ID:            id,
		Branch:        branch,
		SkillsNote:    skillsNote,
		Note:          note,
		CodeStyle:     r.Render("code_style", nil),
		BuildNotes:    buildNotesInstruction(r, id),
		TicketContext: ticketCtx,
	})
}

// ticketContext returns a prompt block carrying the ticket's title and full
// description, fetched via the tracker's REST API (IssueDetailer). Injecting it
// lets the build/handoff agent work from the content directly instead of reading
// the ticket through the account-level Atlassian/Linear MCP — a shared OAuth
// identity independent of the per-repo API credentials. It returns "" (and the
// agent falls back to the MCP, as before) when the tracker exposes no REST detail
// capability or the API read fails — i.e. only when the per-repo credentials are
// configured and working does the content get injected.
func (p *Pipeline) ticketContext(ctx context.Context, id string) string {
	note, _ := p.ticketContextWithLabels(ctx, id)
	return note
}

// ticketContextWithLabels returns the injected ticket block alongside the
// ticket's label names, from a single tracker read: the block goes to the
// prompt, the labels only to skill routing, which matches rule keywords against
// them as well as the title and description.
func (p *Pipeline) ticketContextWithLabels(ctx context.Context, id string) (string, []string) {
	detailer, ok := p.Tracker.(tracker.IssueDetailer)
	if !ok {
		return "", nil
	}
	detail, err := detailer.IssueDetail(ctx, id)
	if err != nil {
		p.logf("  ticket %s content not injected (agent will read it via MCP): %v", id, err)
		return "", nil
	}
	return ticketContextNote(id, detail, p.materializeAttachments(ctx, id, detail.Attachments)), detail.Labels
}

// ticketContextNote renders the injected ticket block — title, description,
// comments, and the ticket's files — or "" when there is no content to inject.
// Every reference to an image the run materialized is repointed at its local copy,
// so an agent following one opens a file rather than an unreachable URL.
func ticketContextNote(id string, detail tracker.IssueDetail, files []attachfile.File) string {
	title := strings.TrimSpace(detail.Title)
	desc := attachfile.Rewrite(strings.TrimSpace(detail.Description), files)
	comments := attachfile.Rewrite(ticketComments(detail.Comments), files)
	attachments := attachfile.Section(files)
	if title == "" && desc == "" && comments == "" && attachments == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThe ticket content is provided below (read from the issue store) — work from it directly and do NOT call the Jira/Atlassian or Linear MCP to read " + id + ".\n\n=== " + id)
	if title != "" {
		b.WriteString(": " + title)
	}
	b.WriteString(" ===\n")
	if desc != "" {
		b.WriteString(desc + "\n")
	}
	if comments != "" {
		b.WriteString("\n--- Comments ---\n" + comments)
	}
	b.WriteString(attachments)
	b.WriteString("=== end " + id + " ===")
	return b.String()
}

// ticketComments renders an issue's comments as a prompt block, each attributed to
// its author, or "" when there are none.
func ticketComments(comments []tracker.IssueComment) string {
	var b strings.Builder
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if body == "" {
			continue
		}
		author := strings.TrimSpace(c.Author)
		if author == "" {
			author = "unknown"
		}
		fmt.Fprintf(&b, "%s: %s\n", author, body)
	}
	return b.String()
}

func fmtBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}

func handoffPath(id string) string { return "/tmp/handoff-" + id + ".md" }

func verifyPath(id string) string { return "/tmp/verify-" + id + ".json" }

func handoffTail(r prompts.Renderer, id, ticketCtx string) string {
	return r.Render("handoff", prompts.HandoffData{
		ID:            id,
		Handoff:       handoffPath(id),
		Rubric:        rubricInstruction(r, id),
		TicketContext: ticketCtx,
	})
}

// worktreeLister lists the working-tree changed files against a base branch.
// ExecGit implements it; a Git that does not (test stubs) keeps the per-workspace
// app URL resolution on the AppURL fallback.
type worktreeLister interface {
	WorktreeChangedFiles(ctx context.Context, base string) ([]string, error)
}

// sliceAppURL picks the browser-verify URL for the slice: the APP_URLS entry
// whose workspace holds the slice's changed files, AppURL otherwise. Fails open
// to AppURL — an unsizable tree or unmatched slice never blocks verify.
func (p *Pipeline) sliceAppURL(ctx context.Context) string {
	if len(p.AppURLs) == 0 || p.RepoRoot == "" {
		return p.AppURL
	}
	changed, ok := p.sliceChangedFiles(ctx)
	if !ok {
		return p.AppURL
	}
	if url := agent.WorkspaceAppURL(p.RepoRoot, p.AppURLs, changed); url != "" {
		return url
	}
	return p.AppURL
}

// sliceLintFixCmd picks the lint-fix command for the slice: the owning
// workspace's own .trau.ini LINT_FIX_CMD (ADR 0019) if set, LintFixCmd
// otherwise. Fails open to LintFixCmd — an unsizable tree or unmatched slice
// never blocks lint-fix.
func (p *Pipeline) sliceLintFixCmd(ctx context.Context) string {
	if p.RepoRoot == "" {
		return p.LintFixCmd
	}
	changed, ok := p.sliceChangedFiles(ctx)
	if !ok {
		return p.LintFixCmd
	}
	dir := agent.OwningWorkspaceDir(p.RepoRoot, changed)
	if v, ok := config.WorkspaceOverride(dir, "LINT_FIX_CMD"); ok {
		return v
	}
	return p.LintFixCmd
}

// sliceChangedFiles lists the slice's working-tree changes against the base
// branch through the same worktreeLister sliceAppURL relies on. The bool is false
// when the diff can't be listed (a Git stub without the interface, an unresolved
// base, a diff error) so callers can fail open rather than fabricate a result.
func (p *Pipeline) sliceChangedFiles(ctx context.Context) ([]string, bool) {
	lister, ok := p.Git.(worktreeLister)
	if !ok {
		return nil, false
	}
	base, err := p.buildBase(ctx)
	if err != nil {
		return nil, false
	}
	changed, err := lister.WorktreeChangedFiles(ctx, base)
	if err != nil {
		return nil, false
	}
	return changed, true
}

var frontendExts = map[string]bool{
	".tsx":    true,
	".jsx":    true,
	".vue":    true,
	".svelte": true,
	".css":    true,
	".scss":   true,
}

var templateDirs = map[string]bool{
	"templates": true,
	"template":  true,
	"views":     true,
	"view":      true,
}

// isUIFile reports whether a repo-relative changed path is a front-end surface: a
// known front-end file type, a Blade template, or any file under a template/view
// directory. Deterministic and framework-agnostic — it classifies from the path
// alone, never the agent's say-so.
func isUIFile(path string) bool {
	p := strings.ToLower(filepath.ToSlash(path))
	if strings.HasSuffix(p, ".blade.php") {
		return true
	}
	if frontendExts[filepath.Ext(p)] {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		if templateDirs[seg] {
			return true
		}
	}
	return false
}

// sliceIsUI reports whether the slice's diff touches any front-end surface,
// classified deterministically from the changed files. It fails open to false —
// an unlistable diff never fabricates a UI gate — so the browser gate can only
// fire on a diff it could actually read.
func (p *Pipeline) sliceIsUI(ctx context.Context) bool {
	changed, ok := p.sliceChangedFiles(ctx)
	if !ok {
		return false
	}
	for _, f := range changed {
		if isUIFile(f) {
			return true
		}
	}
	return false
}

// browserNote is the verify prompt's browser-driving instruction. It is empty
// when no APP_URL is configured (no reachable target to drive) or when browser
// verify is off. The wording is deliberately hardened: the automation browser is
// sanctioned and dedicated, so a genuine connection failure is reported in
// browser_notes rather than skipped out of concern for a user's session. Kept
// framework-agnostic.
func browserNote(mode, appURL string) string {
	if appURL == "" {
		return ""
	}
	switch mode {
	case "always":
		return "Drive the running app at " + appURL + ` via the browser-harness skill and exercise this slice's UI, then set the verdict's "browser" to "driven" and record what you exercised in "browser_notes". This is an unattended run against a sanctioned, dedicated automation browser — do not skip out of concern for a user's session; if the browser cannot connect, set "browser" to "skipped" and give the concrete reason in "browser_notes" instead of silently skipping.`
	case "auto":
		return "If this slice has a UI surface, drive the running app at " + appURL + ` via the browser-harness skill and record what you exercised in "browser_notes" (set "browser" to "driven"); for a backend-only slice set "browser" to "not-applicable". This is an unattended run against a sanctioned, dedicated automation browser — do not skip a UI surface out of concern for a user's session; if the browser cannot connect, set "browser" to "skipped" and give the concrete reason in "browser_notes".`
	default:
		return ""
	}
}

// browserDriveNote is the must-drive instruction for the single
// BROWSER_VERIFY=always re-verify after a UI slice came back undriven: the
// browser is no longer optional, and a genuine connection failure must be
// reported rather than skipped.
func browserDriveNote(appURL string) string {
	return "This slice changes a UI surface and browser verification is REQUIRED. Drive the running app at " + appURL + ` through the browser-harness skill, exercise the changed UI, then set the verdict's "browser" to "driven" and record what you exercised in "browser_notes". This is an unattended run against a sanctioned, dedicated automation browser — do NOT skip out of concern for a user's session. If the automation browser genuinely cannot connect, set "browser" to "skipped" and report the concrete reason in "browser_notes" instead of skipping silently.`
}

const (
	qaNoRosterWarning          = "no QA accounts stored — verify runs without stored credentials"
	qaRosterUnavailableWarning = "QA accounts unavailable — verify runs without stored credentials"
)

// qaVerifyNote fetches the repo's QA credentials and renders the verify-prompt
// QA fragment, but only for a slice where browser verify is actually active:
// there is a browser-driving note (a configured APP_URL under auto/always) and
// the slice's own diff touches a UI surface. A backend slice, a disabled browser
// gate, or a missing APP_URL injects nothing. Once the gate is active the
// fragment always goes out, even with an empty or unreachable roster — the
// discovery and capture instructions are exactly what an unprovisioned repo
// needs. The note is built once per verify and threaded through every attempt,
// so this is also where the outcome is reported.
func (p *Pipeline) qaVerifyNote(ctx context.Context, id, browserNote string) string {
	if p.FetchQAAccounts == nil || browserNote == "" || !p.sliceIsUI(ctx) {
		return ""
	}
	roster, err := p.FetchQAAccounts(ctx)
	p.reportQARoster(id, roster, err)
	if err != nil {
		roster = hubclient.QARoster{}
	}
	p.qaRoster = roster.Accounts
	p.qaCaptured = 0
	return qaRosterNote(id, roster.Accounts, roster.Notes)
}

// reportQARoster records what the roster contributed to an active verify gate —
// injected, none stored, or unreachable. Counts and flags only: a label,
// username, or secret must never reach the log or the event fields.
func (p *Pipeline) reportQARoster(id string, roster hubclient.QARoster, err error) {
	accounts := len(usableQAAccounts(roster.Accounts))
	notes := strings.TrimSpace(roster.Notes) != ""
	fields := map[string]any{"ticket": id, "accounts": accounts, "notes": notes}

	var msg string
	switch {
	case err != nil:
		msg = qaRosterUnavailableWarning
		fields["error"] = err.Error()
		p.logf("  ⚠ %s: %v", msg, err)
	case accounts == 0 && !notes:
		msg = qaNoRosterWarning
		p.logf("  ⚠ %s", msg)
	default:
		msg = fmt.Sprintf("QA roster injected: %d account(s)", accounts)
		if notes {
			msg += " + QA notes"
		}
		p.logf("  ↳ %s", msg)
	}

	if p.Events != nil {
		p.Events.Emit(event.KindQARoster, "verify", msg, fields)
	}
}

// usableQAAccounts keeps the roster entries the verifier can be pointed at by
// name: an unlabeled account cannot be referred to without naming its credentials.
func usableQAAccounts(accounts []hubclient.QAAccount) []hubclient.QAAccount {
	usable := make([]hubclient.QAAccount, 0, len(accounts))
	for _, a := range accounts {
		if strings.TrimSpace(a.Label) != "" {
			usable = append(usable, a)
		}
	}
	return usable
}

// qaRosterNote renders the QA credentials fragment appended to a browser-verify
// prompt: the accounts the verifier may sign in with, each by label with its
// username, secret, and coverage, the free-text notes, a standing order never to
// copy any credential into a durable artifact, and — for the login wall no stored
// account opens — how to discover a working credential inside the repo under test
// and where to hand it back. Kept framework-agnostic — it names no stack and no
// login mechanism.
func qaRosterNote(id string, accounts []hubclient.QAAccount, notes string) string {
	notes = strings.TrimSpace(notes)
	usable := usableQAAccounts(accounts)

	var b strings.Builder
	b.WriteString(" QA test credentials for the app under test — treat every credential value as a write-only secret: NEVER copy a username, secret, or any of it into the QA brief, the verdict JSON, the PR, a comment, or the tracker; refer to an account only by its label.")
	if len(usable) > 0 {
		b.WriteString(" Available accounts:")
		for _, a := range usable {
			fmt.Fprintf(&b, " label %q — username %q, secret %q", a.Label, a.Username, a.Secret)
			if d := strings.TrimSpace(a.Description); d != "" {
				b.WriteString(", covers " + d)
			}
			b.WriteString(";")
		}
		b.WriteString(" pick the account(s) whose coverage matches the flows this slice exercises and use them to sign in past any login wall.")
	}
	if notes != "" {
		b.WriteString(" QA notes: " + notes)
	}
	b.WriteString(" If a login wall blocks verification and no account above signs you in (including when none is listed), search the repo under test for credentials that do work — seed data, fixtures, test documentation, environment-variable examples — and sign in with those. Only the repo under test is a permitted source: never reach for credentials in your own configuration files, the machine environment, or another project.")
	b.WriteString(" Every repo-discovered credential that successfully signed in and is not already listed above must be written to " + qaCapturePath(id) + ` as {"accounts": [{"label": "...", "username": "...", "secret": "...", "description": "..."}]}, the description saying what the account covers and where in the repo you found it, and the label a short human-readable name for the account — never the username, which is itself a credential value. That file is the ONLY place a discovered credential value may be written; the never-copy order above applies everywhere else.`)
	return b.String()
}

// verifyTail builds the cold-verifier prompt. When handoff names a QA brief the
// verifier grades against it; when handoff is "" (a tiny slice ran no standalone
// handoff agent) it derives the checkable behaviors itself from the injected ticket
// content and the slice's diff. The verdict shape and pass/fail gating are identical
// either way.
func verifyTail(r prompts.Renderer, id, handoff, verdict, note, qaNote, checksFragment, rubricNote, lessonsNote, skillsNote, ticketCtx string, proofsContract bool) string {
	return r.Render("verify", prompts.VerifyData{
		ID:             id,
		Handoff:        handoff,
		Verdict:        verdict,
		Note:           note,
		QANote:         qaNote,
		ChecksFragment: checksFragment,
		RubricNote:     rubricNote,
		LessonsNote:    lessonsNote,
		SkillsNote:     skillsNote,
		TicketContext:  ticketCtx,
		ProofsContract: proofsContract,
	})
}

func commitInstruction(r prompts.Renderer, id, rubricNote string, squash bool) string {
	return r.Render("commit", prompts.CommitData{ID: id, RubricNote: rubricNote, Squash: squash})
}

// deterministicCommitMessage builds the templated Conventional Commit for a squash
// slice: '<type>: <subject>' with a 'Refs: <id>' trailer. The type is inferred from
// the title, the subject is the title truncated to 72 chars and case-conformed to
// the spec's lowercase description style (tracker titles arrive sentence-cased,
// which commitlint's default subject-case rule rejects), and an empty title falls
// back to the id. No scope and no AI/authorship trailers, matching the commit rule
// the agent path enforces.
func deterministicCommitMessage(id, title string) string {
	subject := strings.TrimRight(conformSubjectCase(commitSubject(title)), ".")
	if subject == "" {
		subject = id
	}
	return commitType(title) + ": " + subject + "\n\nRefs: " + id
}

// conformSubjectCase lowercases the subject's leading rune so a sentence-cased
// tracker title reads as a spec-style lowercase description. The first word is
// left untouched when it carries uppercase beyond its first rune — an acronym,
// identifier, or CamelCase name, not sentence casing.
func conformSubjectCase(s string) string {
	word := s
	if i := strings.IndexByte(s, ' '); i >= 0 {
		word = s[:i]
	}
	runes := []rune(word)
	if len(runes) == 0 || !unicode.IsUpper(runes[0]) {
		return s
	}
	for _, r := range runes[1:] {
		if unicode.IsUpper(r) {
			return s
		}
	}
	runes[0] = unicode.ToLower(runes[0])
	return string(runes) + s[len(word):]
}

// commitType maps a ticket title to a Conventional Commit type. The loop always cuts
// feature/ branches, so the signal is the title's leading verb; unknown verbs are a
// feature by default.
func commitType(title string) string {
	first := ""
	if fields := strings.Fields(strings.ToLower(title)); len(fields) > 0 {
		first = strings.TrimFunc(fields[0], func(r rune) bool { return !unicode.IsLetter(r) })
	}
	switch first {
	case "fix", "fixes", "fixed", "bug", "bugfix", "hotfix":
		return "fix"
	case "refactor", "refactors":
		return "refactor"
	case "docs", "document", "documentation":
		return "docs"
	case "test", "tests":
		return "test"
	case "chore":
		return "chore"
	default:
		return "feat"
	}
}

// commitSubject normalizes the title's whitespace and truncates it to 72 runes,
// backing off to the last word boundary rather than cutting a word in half.
func commitSubject(title string) string {
	const max = 72
	s := strings.Join(strings.Fields(title), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	cut := string(runes[:max])
	if !unicode.IsSpace(runes[max]) {
		if i := strings.LastIndexByte(cut, ' '); i > 0 {
			cut = cut[:i]
		}
	}
	return strings.TrimRight(cut, " ")
}

func repairInstruction(r prompts.Renderer, id, verdict, handoff, branch, fails, rubricNote, lessonsNote, notesNote, skillsNote, ticketCtx string) string {
	return r.Render("repair", repairData(r, id, verdict, handoff, branch, fails, rubricNote, lessonsNote, notesNote, skillsNote, ticketCtx))
}

func bugfixInstruction(r prompts.Renderer, id, verdict, handoff, branch, fails, rubricNote, lessonsNote, notesNote, skillsNote, ticketCtx string) string {
	return r.Render("bugfix", repairData(r, id, verdict, handoff, branch, fails, rubricNote, lessonsNote, notesNote, skillsNote, ticketCtx))
}

func repairData(r prompts.Renderer, id, verdict, handoff, branch, fails, rubricNote, lessonsNote, notesNote, skillsNote, ticketCtx string) prompts.RepairData {
	return prompts.RepairData{
		ID:            id,
		Verdict:       verdict,
		Handoff:       handoff,
		Branch:        branch,
		Fails:         fails,
		RubricNote:    rubricNote,
		LessonsNote:   lessonsNote,
		NotesNote:     notesNote,
		SkillsNote:    skillsNote,
		CodeStyle:     r.Render("code_style", nil),
		TicketContext: ticketCtx,
	}
}

// pushRepairInstruction hands the verbatim pre-push rejection to a repair agent.
// The slice is already committed on the branch, so the agent must fix the flagged
// problem AND fold the fix into what gets pushed (amend or a follow-up commit); the
// loop re-pushes after it finishes. The output is passed raw and unparsed — the
// agent reads the hook's own report rather than trau guessing at its format.
func pushRepairInstruction(r prompts.Renderer, id, hookOutput, notesNote string) string {
	return r.Render("push_repair", prompts.PushRepairData{
		ID:         id,
		HookOutput: hookOutput,
		NotesNote:  notesNote,
		CodeStyle:  r.Render("code_style", nil),
	})
}

type verdict struct {
	Pass     bool          `json:"pass"`
	Summary  string        `json:"summary"`
	Failures []string      `json:"failures"`
	Checks   []checkResult `json:"checks,omitempty"`
	// Browser is the verifier's self-reported browser-QA outcome: "driven",
	// "skipped", or "not-applicable". Recorded for accounting only — the pipeline
	// classifies the slice as UI deterministically and never lets this value
	// decide the gate. An absent field (old verdicts, or an agent that omitted it)
	// reads as "skipped" via browserOutcome.
	Browser      string `json:"browser,omitempty"`
	BrowserNotes string `json:"browser_notes,omitempty"`
}

// browserOutcome normalizes a verdict's self-reported browser field for
// accounting. Only the two affirmative values are trusted verbatim; anything
// else — including an empty field from a pre-field verdict — reads as "skipped",
// so a missing value can never masquerade as a browser run.
func browserOutcome(v verdict) string {
	switch v.Browser {
	case "driven", "not-applicable":
		return v.Browser
	default:
		return "skipped"
	}
}

// checkResult is one verify-check outcome the cold verifier reports back inside
// the verdict (see internal/checks). Severity is echoed for the agent's benefit,
// but gateChecks trusts the declared library severity, not this field.
type checkResult struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Pass     bool   `json:"pass"`
	Detail   string `json:"detail"`
}

// gateChecks applies severity gating from the declared check library to the
// verdict the verifier reported. A failing error-severity check forces the
// overall verdict to fail — folded into Failures — even if the agent set
// pass=true; a failing warn-severity check is returned as a non-blocking warning
// line for logging. The library's declared severity wins over whatever severity
// the agent echoed back, so a verifier can't silently downgrade a blocking check.
func gateChecks(library []checks.Check, v verdict) (verdict, []string) {
	if len(library) == 0 || len(v.Checks) == 0 {
		return v, nil
	}
	severity := make(map[string]string, len(library))
	for _, c := range library {
		severity[c.Name] = checks.NormalizeSeverity(c.Severity)
	}
	var warnings []string
	for _, r := range v.Checks {
		if r.Pass {
			continue
		}
		detail := strings.TrimSpace(r.Detail)
		if detail == "" {
			detail = "failed"
		}
		sev, known := severity[r.Name]
		if !known {
			sev = checks.NormalizeSeverity(r.Severity)
		}
		line := fmt.Sprintf("[check:%s] %s", r.Name, detail)
		if checks.Blocks(sev) {
			v.Pass = false
			if !containsLine(v.Failures, line) {
				v.Failures = append(v.Failures, line)
			}
		} else {
			warnings = append(warnings, line)
		}
	}
	return v, warnings
}

func containsLine(lines []string, line string) bool {
	for _, l := range lines {
		if l == line {
			return true
		}
	}
	return false
}

// Verifier is one member of the cross-vendor verify panel: a named, isolated
// agent backend with its own provider/model that independently judges the slice
// and emits its own verdict. Name is a short, filename-safe tag (e.g. "claude",
// "codex", "claude2") used for the member's verdict file, phase label, and ledger
// attribution.
type Verifier struct {
	Name     string
	Provider string
	Runner   agent.Runner
}

type panelResult struct {
	Name    string
	Verdict verdict
}

// verifyAttempt produces one verify verdict for the current attempt. With a
// configured panel it fans out to every cross-vendor verifier and merges by
// policy; otherwise it runs the single primary verifier. Either way the per-check
// severity gate is applied and the effective verdict is written to verifyPath(id)
// so the repair prompt and FileBug read the authoritative result. A non-nil error
// is fatal and propagated (a provider pause or a budget give-up) — it stops the
// phase rather than counting as a verify failure.
//
// A non-empty qaNote means the QA gate is active, so the attempt is bracketed by
// the credential-capture side channel — deferred, to cover the panel path, the
// browser re-verify, and a fatal return alike.
func (p *Pipeline) verifyAttempt(ctx context.Context, id, label, handoff, note, qaNote, checksFragment, rubricNote, lessonsNote, skillsNote, skillsInject, ticketCtx string) (verdict, error) {
	if qaNote != "" {
		_ = os.Remove(qaCapturePath(id))
		defer p.ingestQACapture(ctx, id)
	}
	proofsOn := p.proofsEnabled(note)
	if proofsOn {
		ctx = agent.WithBrowserRecording(ctx)
	}
	if len(p.VerifyPanel) > 0 {
		return p.runPanel(ctx, id, label, handoff, note, qaNote, checksFragment, rubricNote, lessonsNote, skillsNote, skillsInject, ticketCtx, proofsOn)
	}
	verdictPath := verifyPath(id)
	_ = os.Remove(verdictPath)
	prompt := injectInto(skillsInject, verifyTail(p.prompts, id, handoff, verdictPath, note, qaNote, checksFragment, rubricNote, lessonsNote, skillsNote, ticketCtx, proofsOn))
	_, agentErr := p.agentStep(ctx, id, label, prompt)
	// A provider pause (rate/usage limit) or budget give-up must propagate, not be
	// recorded as a verify failure — otherwise a transient 429 burns repair/bugfix
	// attempts and cascades into a bogus quarantine + HITL bug. The panel path
	// (runPanel) already does this; this is the single-verifier mirror.
	if agentErr != nil && isFatalAgentErr(agentErr) {
		return verdict{}, agentErr
	}
	v, ok := readVerdict(verdictPath)
	if agentErr != nil || !ok {
		reason := "verify agent timed out or exited without writing a verdict"
		if agentErr != nil {
			reason = fmt.Sprintf("verify agent failed: %v", agentErr)
		}
		if err := writeFailureVerdict(verdictPath, reason); err != nil {
			return verdict{}, fmt.Errorf("verify %s: write failure verdict: %w", id, err)
		}
		v, _ = readVerdict(verdictPath)
	}
	var warnings []string
	v, warnings = gateChecks(p.Checks, v)
	for _, w := range warnings {
		p.logf("  ⚠ %s", w)
	}
	_ = writeVerdictFile(verdictPath, v)
	return v, nil
}

// runPanel runs each configured verifier as a fresh, isolated process against the
// same handoff brief and on-disk code, gates each verdict by the check library,
// merges them by the configured policy, and writes the merged verdict to
// verifyPath(id). Members run concurrently when PanelParallel is on so panel wall
// clock is the slowest member rather than the sum; results stay position-indexed
// so the merge is identical to the sequential path. A provider pause or budget
// give-up from any member is propagated so the loop stops cleanly (the ticket
// stays resumable on its branch) instead of being recorded as a dissenting fail;
// a plain timeout/crash counts as that member failing.
func (p *Pipeline) runPanel(ctx context.Context, id, label, handoff, note, qaNote, checksFragment, rubricNote, lessonsNote, skillsNote, skillsInject, ticketCtx string, proofsOn bool) (verdict, error) {
	results := make([]panelResult, len(p.VerifyPanel))
	member := func(ctx context.Context, i int) error {
		m := p.VerifyPanel[i]
		memberPath := verifyMemberPath(id, m.Name)
		_ = os.Remove(memberPath)
		memberLabel := label + "-" + m.Name
		prompt := injectInto(skillsInject, verifyTail(p.prompts, id, handoff, memberPath, note, qaNote, checksFragment, rubricNote, lessonsNote, skillsNote, ticketCtx, proofsOn))
		_, agentErr := p.agentStepOn(ctx, id, memberLabel, prompt, m.Runner)
		if agentErr != nil && isFatalAgentErr(agentErr) {
			return agentErr
		}
		v, ok := readVerdict(memberPath)
		if agentErr != nil || !ok {
			reason := m.Name + " verifier timed out or exited without writing a verdict"
			if agentErr != nil {
				reason = fmt.Sprintf("%s verifier failed: %v", m.Name, agentErr)
			}
			v = verdict{Pass: false, Summary: reason, Failures: []string{reason}}
		}
		var warnings []string
		v, warnings = gateChecks(p.Checks, v)
		for _, w := range warnings {
			p.logf("  ⚠ %s: %s", m.Name, w)
		}
		results[i] = panelResult{Name: m.Name, Verdict: v}
		p.logf("  ↳ %s: %s", m.Name, passFailLine(v))
		return nil
	}
	if err := p.fanOutPanel(ctx, member); err != nil {
		return verdict{}, err
	}
	merged := mergeVerdicts(p.PanelPolicy, results)
	_ = writeVerdictFile(verifyPath(id), merged)
	p.logf("  ↳ panel verdict: %s", merged.Summary)
	return merged, nil
}

// fanOutPanel runs member across every panel index, concurrently when
// PanelParallel is on and there are 2+ members, sequentially otherwise. Members
// are isolated (distinct verdict files, phase-labeled logs), so the only
// cross-member coupling is a fatal error: it cancels the errgroup context and
// aborts the still-running members before propagating.
func (p *Pipeline) fanOutPanel(ctx context.Context, member func(context.Context, int) error) error {
	n := len(p.VerifyPanel)
	if !p.PanelParallel || n < 2 {
		for i := range n {
			if err := member(ctx, i); err != nil {
				return err
			}
		}
		return nil
	}
	g, gctx := errgroup.WithContext(ctx)
	for i := range n {
		g.Go(func() error { return member(gctx, i) })
	}
	return g.Wait()
}

// isFatalAgentErr reports whether an agent error must abort the panel and the
// phase (a provider pause that should leave the ticket resumable, or a budget
// give-up that already quarantined it) rather than being treated as one verifier
// dissenting.
func isFatalAgentErr(err error) bool {
	if IsPaused(err) {
		return true
	}
	var g *GiveUpError
	return errors.As(err, &g)
}

func passFailLine(v verdict) string {
	if v.Pass {
		return "pass"
	}
	if s := strings.TrimSpace(v.Summary); s != "" {
		return "fail — " + s
	}
	return "fail"
}

// mergeVerdicts folds the panel members' (already check-gated) verdicts into one
// by policy. The merged verdict fails closed: when it does not pass, every
// dissenting member's failures are carried over (tagged by member) so the repair
// prompt has the full cross-vendor picture.
func mergeVerdicts(policy string, results []panelResult) verdict {
	total := len(results)
	passes := 0
	var failers []string
	var failLines []string
	for _, r := range results {
		if r.Verdict.Pass {
			passes++
			continue
		}
		failers = append(failers, r.Name)
		lines := r.Verdict.Failures
		if len(lines) == 0 {
			if s := strings.TrimSpace(r.Verdict.Summary); s != "" {
				lines = []string{s}
			}
		}
		for _, f := range lines {
			failLines = append(failLines, fmt.Sprintf("[%s] %s", r.Name, f))
		}
	}
	pass := panelPasses(policy, passes, total)
	summary := fmt.Sprintf("panel %s: %d/%d verifiers passed", normalizePolicy(policy), passes, total)
	if len(failers) > 0 {
		summary += " (dissent: " + strings.Join(failers, ", ") + ")"
	}
	merged := verdict{Pass: pass, Summary: summary}
	if !pass {
		if len(failLines) == 0 {
			failLines = []string{summary}
		}
		merged.Failures = failLines
	}
	return merged
}

// panelPasses decides the merged outcome under a policy. unanimous (the default,
// = any single fail blocks) requires every verifier to pass; majority requires a
// strict majority; any-pass merges if at least one verifier passes.
func panelPasses(policy string, passes, total int) bool {
	if total == 0 {
		return false
	}
	switch normalizePolicy(policy) {
	case "majority":
		return passes*2 > total
	case "any-pass":
		return passes > 0
	default: // unanimous
		return passes == total
	}
}

// normalizePolicy canonicalizes a merge-policy string; unknown/empty defaults to
// unanimous, the most conservative (a single dissent blocks the merge).
func normalizePolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "majority":
		return "majority"
	case "any-pass", "any_pass", "anypass":
		return "any-pass"
	default:
		return "unanimous"
	}
}

func verifyMemberPath(id, name string) string {
	return "/tmp/verify-" + id + "-" + name + ".json"
}

func readVerdict(path string) (v verdict, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return verdict{}, false
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return verdict{}, false
	}
	return v, true
}

func writeFailureVerdict(path, reason string) error {
	return writeVerdictFile(path, verdict{Pass: false, Summary: reason, Failures: []string{reason}})
}

func writeVerdictFile(path string, v verdict) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// topFailures returns up to three plain-English failure reasons from a verdict,
// for surfacing under a "verify failed" line. Empty when the verdict carries none
// (e.g. the agent never wrote one).
func topFailures(v verdict) []string {
	if len(v.Failures) > 0 {
		n := len(v.Failures)
		if n > 3 {
			n = 3
		}
		return v.Failures[:n]
	}
	if s := strings.TrimSpace(v.Summary); s != "" {
		return []string{s}
	}
	return nil
}

// agentErrSummary condenses a multi-line agent error into one human line and flags
// provider rate/usage limits. The full detail stays in the provider's own log.
func agentErrSummary(err error) (msg string, rateLimited bool) {
	if agent.IsRateLimited(err) {
		return "provider usage/rate limit reached — see provider log", true
	}
	s := err.Error()
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(strings.ToLower(ln), "error:") {
			return ln, false
		}
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s, false
}

func (v verdict) failureLines() string {
	if len(v.Failures) > 0 {
		lines := v.Failures
		if len(lines) > 20 {
			lines = lines[:20]
		}
		return strings.Join(lines, "\n")
	}
	if v.Summary != "" {
		return v.Summary
	}
	return "see verdict"
}

// ExecGit runs git against a target repo via `git -C <repo>`.
type ExecGit struct {
	Bin  string
	Repo string
}

func (g ExecGit) bin() string {
	if g.Bin != "" {
		return g.Bin
	}
	return "git"
}

// gitWaitDelay bounds Wait once the context has killed git. A networked git forks
// a transport (ssh, git-remote-https) that the kill does not reach and that keeps
// the output pipe open, so without this a context deadline stops bounding the
// call — it only kills git and then blocks on the pipe until the transport gives
// up on its own.
const gitWaitDelay = 2 * time.Second

func (g ExecGit) run(ctx context.Context, args ...string) error {
	full := append([]string{"-C", g.Repo}, args...)
	logger.Debugf("git %s", strings.Join(full, " "))
	cmd := exec.CommandContext(ctx, g.bin(), full...)
	cmd.WaitDelay = gitWaitDelay
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CurrentBranch returns the checked-out branch of the target repo.
func (g ExecGit) CurrentBranch(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git current branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// AddAll stages every change in the target repo (git add -A).
func (g ExecGit) AddAll(ctx context.Context) error { return g.run(ctx, "add", "-A") }

// Commit records the staged changes; noVerify adds --no-verify to bypass hooks.
func (g ExecGit) Commit(ctx context.Context, message string, noVerify bool) error {
	args := []string{"commit"}
	if noVerify {
		args = append(args, "--no-verify")
	}
	return g.run(ctx, append(args, "-m", message)...)
}

// Push pushes ref to remote, setting upstream (git push -u <remote> <ref>);
// noVerify adds --no-verify to bypass local pre-push hooks.
func (g ExecGit) Push(ctx context.Context, remote, ref string, noVerify bool) error {
	args := []string{"push", "-u"}
	if noVerify {
		args = append(args, "--no-verify")
	}
	return g.run(ctx, append(args, remote, ref)...)
}

// PushDryRun runs git push --dry-run --no-verify: it negotiates with the remote
// but transfers nothing and skips local hooks, so a nil result means the remote
// would accept the ref (any real-push failure was a local hook), while a non-nil
// error carries git's own ref-level rejection reason.
func (g ExecGit) PushDryRun(ctx context.Context, remote, ref string) error {
	return g.run(ctx, "push", "--dry-run", "--no-verify", remote, ref)
}

// Checkout switches to ref; force adds -f to discard local changes.
func (g ExecGit) Checkout(ctx context.Context, ref string, force bool) error {
	args := []string{"checkout"}
	if force {
		args = append(args, "-f")
	}
	return g.run(ctx, append(args, ref)...)
}

// CheckoutDetached puts HEAD on ref without claiming a branch (git checkout --detach
// <ref>); force adds -f to discard local changes.
func (g ExecGit) CheckoutDetached(ctx context.Context, ref string, force bool) error {
	args := []string{"checkout", "--detach"}
	if force {
		args = append(args, "-f")
	}
	return g.run(ctx, append(args, ref)...)
}

// WorktreeHolding returns the path of another linked worktree that has branch
// checked out, or "" when none does. git worktree list --porcelain emits a
// `worktree <path>` line followed by the `branch <ref>` that worktree holds; this
// worktree is skipped so only a genuine conflict is reported.
func (g ExecGit) WorktreeHolding(ctx context.Context, branch string) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return "", fmt.Errorf("git worktree list: %w", err)
	}
	top, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	mine := strings.TrimSpace(string(top))
	path := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "worktree "); ok {
			path = rest
			continue
		}
		if line == "branch refs/heads/"+branch && path != mine {
			return path, nil
		}
	}
	return "", nil
}

// CreateBranch creates and switches to branch off base (git checkout -b <branch>
// <base>). --no-track is a no-op for a local base but stops a base given as a
// remote-tracking ref from becoming the new branch's upstream, which would point a
// bare `git push` on a run's branch at the base branch.
func (g ExecGit) CreateBranch(ctx context.Context, branch, base string) error {
	return g.run(ctx, "checkout", "-b", branch, "--no-track", base)
}

// Clean removes untracked files and directories (git clean -fd) from the target
// repo, but never trau's own config/artifacts living there: the project config
// (.trau.ini), a cwd-local config (trau.ini), and the custom-checks dir (.trau/).
// Without these excludes, the quarantine/clean-base path would delete an untracked
// .trau.ini and force first-run onboarding to restart on the next run. This
// matches EnsureCleanBase's intent that untracked tooling rides along safely; -e
// adds gitignore-style patterns on top of the repo's existing ignore rules.
func (g ExecGit) Clean(ctx context.Context) error {
	return g.run(ctx, "clean", "-fd",
		"-e", ".trau.ini",
		"-e", "trau.ini",
		"-e", ".trau/",
	)
}

// BranchExists reports whether refs/heads/<branch> resolves. git rev-parse --verify
// exits non-zero when the ref is absent, which reads as (false, nil) — a missing
// branch is an expected answer, not an error (only the exit status is checked).
func (g ExecGit) BranchExists(ctx context.Context, branch string) (bool, error) {
	err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo, "rev-parse", "--verify", "refs/heads/"+branch).Run()
	return err == nil, nil
}

// FindFeatureBranch returns the first local feature/<id>-* branch, or "" when none
// match (a git for-each-ref taking the first line, errors swallowed).
func (g ExecGit) FindFeatureBranch(ctx context.Context, id string) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"for-each-ref", "--format=%(refname:short)", "refs/heads/feature/"+id+"-*").Output()
	if err != nil {
		return "", nil
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(first), nil
}

// FindEpicBranch returns the first local epic/<id>-* branch (or the exact
// epic/<id>), or "" when none match. Matching on the epic ID — not the title slug
// — makes resolution deterministic: a renamed epic still finds its branch instead
// of creating a second one. Errors are swallowed (treated as "none").
func (g ExecGit) FindEpicBranch(ctx context.Context, id string) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"for-each-ref", "--format=%(refname:short)",
		"refs/heads/epic/"+id+"-*", "refs/heads/epic/"+id).Output()
	if err != nil {
		return "", nil
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(first), nil
}

// FindRemoteEpicBranch returns the first epic/<id>-* (or exact epic/<id>) branch on
// remote, or "" when none. Unlike the local finder a real failure is surfaced: an
// indeterminate remote must NOT fall through to creating a duplicate epic branch.
func (g ExecGit) FindRemoteEpicBranch(ctx context.Context, remote, id string) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"ls-remote", "--heads", remote,
		"refs/heads/epic/"+id+"-*", "refs/heads/epic/"+id).Output()
	if err != nil {
		return "", fmt.Errorf("ls-remote %s epic/%s: %w", remote, id, err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil
	}
	// Each line is "<sha>\trefs/heads/<branch>"; take the ref and drop the prefix.
	_, ref, ok := strings.Cut(line, "\t")
	if !ok {
		return "", nil
	}
	return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/"), nil
}

// DiffStat returns the numstat totals for the symmetric diff base...branch (the
// changes on branch since it diverged from base): file count and summed
// additions/deletions. Binary files count toward Files but contribute no line
// totals (git emits "-" for them). Used by the opt-in time-log hook.
func (g ExecGit) DiffStat(ctx context.Context, base, branch string) (files, additions, deletions int, err error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"diff", "--numstat", base+"..."+branch).Output()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("git diff --numstat %s...%s: %w", base, branch, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		files++
		if a, e := strconv.Atoi(fields[0]); e == nil {
			additions += a
		}
		if d, e := strconv.Atoi(fields[1]); e == nil {
			deletions += d
		}
	}
	return files, additions, deletions, nil
}

// WorktreeDiffStat measures the working-tree changes against base — both the
// tracked edits the build made and the untracked files it created — as a file
// count and summed added+deleted lines. DiffStat only sees committed history,
// which is empty mid-run (the build commits nothing before the commit phase), so
// the size gate needs this working-tree view instead. Binary and unreadable files
// count toward Files but contribute no lines.
func (g ExecGit) WorktreeDiffStat(ctx context.Context, base string) (files, lines int, err error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"diff", "--numstat", base).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("git diff --numstat %s: %w", base, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		files++
		if a, e := strconv.Atoi(fields[0]); e == nil {
			lines += a
		}
		if d, e := strconv.Atoi(fields[1]); e == nil {
			lines += d
		}
	}
	others, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("git ls-files --others: %w", err)
	}
	for _, path := range strings.Split(strings.TrimRight(string(others), "\x00"), "\x00") {
		if path == "" {
			continue
		}
		files++
		if data, e := os.ReadFile(filepath.Join(g.Repo, path)); e == nil {
			lines += strings.Count(string(data), "\n")
		}
	}
	return files, lines, nil
}

// WorktreeChangedFiles lists the repo-relative paths the working tree changes
// against base — the tracked edits plus the untracked files the build created,
// the same view of the slice WorktreeDiffStat sizes.
func (g ExecGit) WorktreeChangedFiles(ctx context.Context, base string) ([]string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"diff", "--name-only", base).Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s: %w", base, err)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	others, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files --others: %w", err)
	}
	for _, path := range strings.Split(strings.TrimRight(string(others), "\x00"), "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// Commits returns the short SHAs unique to branch relative to base (base..branch),
// newest first — the commits trau created on the branch. Used by the time-log hook.
func (g ExecGit) Commits(ctx context.Context, base, branch string) ([]string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"log", "--format=%h", base+".."+branch).Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s..%s: %w", base, branch, err)
	}
	var shas []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			shas = append(shas, s)
		}
	}
	return shas, nil
}

// CommitSubject returns the subject line of ref's commit.
func (g ExecGit) CommitSubject(ctx context.Context, ref string) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"log", "-1", "--format=%s", ref).Output()
	if err != nil {
		return "", fmt.Errorf("git log -1 %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// FirstCommitDate returns the committer date (RFC3339) of the earliest commit
// unique to branch relative to base, or "" when there is none.
func (g ExecGit) FirstCommitDate(ctx context.Context, base, branch string) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"log", "--reverse", "--format=%cI", base+".."+branch).Output()
	if err != nil {
		return "", fmt.Errorf("git log --reverse %s..%s: %w", base, branch, err)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(first), nil
}

// DeleteBranch deletes a local branch (git branch -D <branch>).
func (g ExecGit) DeleteBranch(ctx context.Context, branch string) error {
	return g.run(ctx, "branch", "-D", branch)
}

// DeletePushedBranch deletes the remote branch (git push <remote> --delete <branch>).
func (g ExecGit) DeletePushedBranch(ctx context.Context, remote, branch string) error {
	return g.run(ctx, "push", remote, "--delete", branch)
}

// StatusPorcelain returns tracked-only porcelain status; empty means a clean base.
func (g ExecGit) StatusPorcelain(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// WorktreeDirty reports whether the working tree has any uncommitted change,
// untracked files included (git status --porcelain), so a build that only added
// new files still counts as a change.
func (g ExecGit) WorktreeDirty(ctx context.Context) (bool, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// Stash saves uncommitted tracked changes under msg (git stash push -m); untracked
// files are left in place, matching StatusPorcelain's clean-base semantics.
func (g ExecGit) Stash(ctx context.Context, msg string) error {
	return g.run(ctx, "stash", "push", "-m", msg)
}

// StashPop restores the most recent stash (git stash pop). A pop that stops on
// conflicts exits non-zero and keeps the stash; that is surfaced as the error.
func (g ExecGit) StashPop(ctx context.Context) error {
	return g.run(ctx, "stash", "pop")
}

// Pull fast-forwards branch from remote (git pull --ff-only <remote> <branch>).
func (g ExecGit) Pull(ctx context.Context, remote, branch string) error {
	return g.run(ctx, "pull", "--ff-only", remote, branch)
}

// Fetch updates the remote-tracking ref for branch (git fetch <remote> <branch>),
// leaving the working tree and the local branch alone.
func (g ExecGit) Fetch(ctx context.Context, remote, branch string) error {
	return g.run(ctx, "fetch", remote, branch)
}

// MergeRemote fetches remote/base and merges it into the current branch. A clean
// merge or already-up-to-date returns (false, nil). A merge stopped on conflicts
// returns (true, nil) with the tree left for an agent to resolve. Any other merge
// failure aborts the merge and returns the error.
func (g ExecGit) MergeRemote(ctx context.Context, remote, base string) (bool, error) {
	if err := g.run(ctx, "fetch", remote, base); err != nil {
		return false, err
	}
	if err := g.run(ctx, "merge", "--no-edit", "FETCH_HEAD"); err == nil {
		return false, nil
	}
	if unmerged, _ := g.Unmerged(ctx); strings.TrimSpace(unmerged) != "" {
		return true, nil
	}
	_ = g.MergeAbort(ctx)
	return false, fmt.Errorf("merge %s/%s into current branch failed", remote, base)
}

// MergeAbort aborts an in-progress conflicted merge (git merge --abort).
func (g ExecGit) MergeAbort(ctx context.Context) error { return g.run(ctx, "merge", "--abort") }

// Unmerged lists the still-conflicted paths after a merge (empty when none).
func (g ExecGit) Unmerged(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return "", fmt.Errorf("git diff --diff-filter=U: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ContinueMerge completes a resolved merge (stage all + commit --no-edit). It is
// a no-op when MERGE_HEAD is absent, so a resolving agent that already committed
// the merge does not cause a spurious empty-commit failure.
func (g ExecGit) ContinueMerge(ctx context.Context) error {
	if exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"rev-parse", "-q", "--verify", "MERGE_HEAD").Run() != nil {
		return nil
	}
	if err := g.run(ctx, "add", "-A"); err != nil {
		return err
	}
	return g.run(ctx, "commit", "--no-edit")
}

// RemoteBranchExists reports whether remote has refs/heads/<branch>. ls-remote
// --exit-code returns status 2 when no ref matches, which reads as (false, nil) —
// an expected answer; any other failure (unreachable remote) returns the error so
// the caller never mistakes a network blip for "branch absent".
func (g ExecGit) RemoteBranchExists(ctx context.Context, remote, branch string) (bool, error) {
	err := exec.CommandContext(ctx, g.bin(), "-C", g.Repo,
		"ls-remote", "--heads", "--exit-code", remote, "refs/heads/"+branch).Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 2 {
		return false, nil
	}
	return false, fmt.Errorf("ls-remote %s %s: %w", remote, branch, err)
}

// CheckoutRemoteBranch creates local <branch> at remote/<branch>'s tip and checks
// it out (git fetch <remote> <branch>:<branch>; git checkout <branch>). Used only
// when the branch is absent locally, so the fetch is a clean create with no
// non-fast-forward risk.
func (g ExecGit) CheckoutRemoteBranch(ctx context.Context, remote, branch string) error {
	if err := g.run(ctx, "fetch", remote, branch+":"+branch); err != nil {
		return err
	}
	return g.run(ctx, "checkout", branch)
}

// ExecGitHub runs `gh` against a target repo (resolved from the working directory
// by setting the command's Dir).
type ExecGitHub struct {
	Bin  string
	Repo string
}

func (g ExecGitHub) bin() string {
	if g.Bin != "" {
		return g.Bin
	}
	return "gh"
}

func (g ExecGitHub) output(ctx context.Context, args ...string) (string, error) {
	logger.Debugf("gh %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, g.bin(), args...)
	cmd.Dir = g.Repo
	out, err := cmd.Output()
	if err != nil {
		return strings.TrimSpace(string(out)), withStderr(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// withStderr folds an *exec.ExitError's captured stderr into the error so a failed
// gh command carries gh's actual message instead of a bare "exit status N".
func withStderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

// PRURL returns the open PR's URL for branch, or "" when none exists. A gh error
// (no PR found) is swallowed to "". The state filter is load-bearing: gh's branch
// lookup falls back to the most recent merged/closed PR when the branch has no
// open one, and adopting a merged PR here is how a rebuilt ticket got marked Done
// with its redo commits stranded on the branch (COD-750).
func (g ExecGitHub) PRURL(ctx context.Context, branch string) (string, error) {
	out, err := g.output(ctx, "pr", "view", branch, "--json", "url,state")
	if err != nil {
		return "", nil
	}
	return parseOpenPRURL(out), nil
}

// parseOpenPRURL extracts the URL from a `gh pr view --json url,state` payload,
// returning "" unless the PR is OPEN; malformed JSON reads as not-open.
func parseOpenPRURL(out string) string {
	var pr struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil || pr.State != "OPEN" {
		return ""
	}
	return pr.URL
}

// PRState returns the PR's state (OPEN, MERGED, …), or "" when unknown (a gh error
// is swallowed to "").
func (g ExecGitHub) PRState(ctx context.Context, pr string) (string, error) {
	out, err := g.output(ctx, "pr", "view", pr, "--json", "state", "-q", ".state")
	if err != nil {
		return "", nil
	}
	return out, nil
}

// CreatePR opens a PR against base from head and returns the URL gh prints.
func (g ExecGitHub) CreatePR(ctx context.Context, base, head, title, body string) (string, error) {
	out, err := g.output(ctx, "pr", "create", "--base", base, "--head", head, "--title", title, "--body", body)
	if err != nil {
		return "", fmt.Errorf("gh pr create: %w", err)
	}
	return out, nil
}

// Checks returns the PR's status checks. A gh error reads as no checks, so pollCI
// re-polls rather than aborting.
func (g ExecGitHub) Checks(ctx context.Context, pr string) ([]Check, error) {
	out, err := g.output(ctx, "pr", "checks", pr, "--json", "name,bucket")
	if err != nil {
		return nil, nil
	}
	var checks []Check
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		return nil, nil
	}
	return checks, nil
}

// Merge merges the PR with the given method; deleteBranch adds --delete-branch.
func (g ExecGitHub) Merge(ctx context.Context, pr, method string, deleteBranch bool) error {
	args := []string{"pr", "merge", pr, "--" + method}
	if deleteBranch {
		args = append(args, "--delete-branch")
	}
	logger.Debugf("gh %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, g.bin(), args...)
	cmd.Dir = g.Repo
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh pr merge: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
