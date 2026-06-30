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

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/budget"
	"github.com/RomkaLTU/trau/internal/checks"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/logger"
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

	Push(ctx context.Context, remote, ref string) error

	Checkout(ctx context.Context, ref string, force bool) error

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

	// Commits returns the short SHAs on branch but not base (base..branch).
	Commits(ctx context.Context, base, branch string) ([]string, error)

	Pull(ctx context.Context, remote, branch string) error

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
// was already merged, so the outer loop can skip counting it and keep picking.
var (
	ErrCIFailed    = errors.New("a required CI check failed")
	ErrCITimeout   = errors.New("CI timed out waiting for required checks")
	ErrAlreadyDone = errors.New("ticket already done")
)

// Ledger is the pipeline's view of the token/cost sink: it points the bucket at
// the current ticket (SetTicket) and reports accumulated spend for budget
// enforcement (Total per ticket, DayTotal for the per-day window). *tokens.Sink
// satisfies it; kept as a narrow interface so pipeline doesn't depend on the
// tokens package.
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

// Pipeline holds the collaborators a ticket run needs. One Pipeline is
// constructed per process and reused across tickets.
type Pipeline struct {
	Runner      agent.Runner
	State       *state.Store
	Git         Git
	GitHub      GitHub
	Tracker     tracker.Tracker
	Tokens      Ledger
	Budget      budget.Limits
	RunsDir     string
	Base        string
	Remote      string
	Prefix      string
	MaxRepairs  int
	MaxBugfixes int

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

	Checks         []checks.Check
	VerifyPanel    []Verifier
	PanelPolicy    string
	BrowserVerify  string
	AppURL         string
	AutoMerge      bool
	MergeMethod    string
	ExpectedChecks string
	RequireCI      bool
	// RequireRepoChanges gates the post-build empty-diff guard (config
	// REQUIRE_REPO_CHANGES, default on). When set, a build that left the managed
	// repo unchanged faults instead of advancing to a hollow handoff or empty PR.
	RequireRepoChanges bool
	CITimeout          int
	CIPoll             int
	Lessons            bool
	LessonsDistill     bool
	Sleep              func(time.Duration)
	Renderer           console.Renderer

	// Now supplies the current time for the per-day budget window; nil defaults
	// to time.Now (overridable in tests).
	Now func() time.Time

	EpicID     string
	epicBranch string

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

// Resume runs a ticket through the phases not yet checkpointed. It buckets token
// logs to the ticket, restores the recorded feature branch (auto-resetting the
// ticket when that branch is gone), then runs each phase whose rank exceeds the
// resume point (fi = Idx(from)); from="" runs everything fresh. A *GiveUpError
// from build (no feature branch) is funneled into giveUp here; verify and the CI
// gate run giveUp themselves and return the resulting *GiveUpError, which passes
// straight through.
func (p *Pipeline) Resume(ctx context.Context, id, from string) error {
	if err := p.EnsureOwnedProject(ctx, id); err != nil {
		return err
	}
	if p.Tokens != nil {
		p.Tokens.SetTicket(id)
	}
	if p.Renderer != nil {
		p.Renderer.SetTicket(id)

		p.setTitle(p.State.Get(id, "TITLE"))
	}
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
		if err := p.assertRepoChanged(ctx, id); err != nil {
			return err
		}
	}
	if fi < 3 {
		if err := p.Handoff(ctx, id); err != nil {
			return err
		}
	}
	if fi < 4 {
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
//   - anything else: an UNEXPECTED error — funnel into the blameless fault path,
//     which preserves the WIP on the branch without quarantining or filing a bug.
func (p *Pipeline) classifyPhaseErr(ctx context.Context, id string, err error) error {
	switch {
	case err == nil, errors.Is(err, ErrAlreadyDone), IsPaused(err):
		return err
	case isGiveUp(err):
		return p.handleGiveUp(ctx, id, err)
	default:
		return p.fault(ctx, id, err)
	}
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

// fault preserves the partial work of a ticket aborted by an unexpected error and
// returns a *FaultError tagged with the phase it died in. The ticket is left at
// its last checkpoint so a rerun resumes it; the loop driver stops the session on
// the *FaultError rather than dragging a dirty tree or a re-faulting ticket on.
func (p *Pipeline) fault(ctx context.Context, id string, err error) error {
	phase := p.State.Get(id, "PHASE")
	p.finalizeFault(ctx, id)
	reason := fmt.Sprintf("unexpected error during %s: %v", NextPhaseLabel(phase), err)
	_ = p.State.Set(id, "FAILURE_REASON", reason)
	p.logf("  ⚠ %s could not finish during %s — work saved, ticket left resumable", id, NextPhaseLabel(phase))
	return &FaultError{ID: id, Phase: phase, Err: err}
}

// finalizeFault mirrors finalizeFailed's preserve-and-clean — commit the WIP to
// the feature branch, push it best-effort, then return the working tree to a clean
// base — but it does NOT quarantine the ticket or file a bug, and it leaves PHASE
// untouched so the ticket stays resumable.
func (p *Pipeline) finalizeFault(ctx context.Context, id string) {
	branch, _ := p.Git.CurrentBranch(ctx)
	if branch != p.Base {
		_ = p.Git.AddAll(ctx)
		_ = p.Git.Commit(ctx, fmt.Sprintf("wip(%s): incomplete attempt — rerun trau to resume", id), true)
		if err := p.Git.Push(ctx, p.Remote, "HEAD"); err == nil {
			p.logf("  saved attempt to %s/%s", p.Remote, branch)
		} else {
			p.logf("  saved attempt to local branch %s", branch)
		}
	}
	_ = p.Git.Checkout(ctx, p.Base, true)
	_ = p.Git.Clean(ctx)
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

// EnsureCleanBase guards the loop's fresh-pick path: it refuses to run when TRACKED
// files have uncommitted changes (untracked tooling rides along safely), then checks
// out the base branch and fast-forwards it from the remote (best-effort). The
// resume path deliberately skips this — the feature branch's WIP IS the work.
func (p *Pipeline) EnsureCleanBase(ctx context.Context) error {
	dirty, err := p.Git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("ensure clean base: git status: %w", err)
	}
	if strings.TrimSpace(dirty) != "" {
		return fmt.Errorf("tracked files have uncommitted changes — aborting so I don't touch your WIP")
	}
	if err := p.Git.Checkout(ctx, p.Base, false); err != nil {
		return fmt.Errorf("ensure clean base: checkout %s: %w", p.Base, err)
	}
	_ = p.Git.Pull(ctx, p.Remote, p.Base)
	return nil
}

// Reset discards a ticket's attempt: drop its feature branch (local + remote) and
// saved state + /tmp artifacts, then send Linear back to an unstarted/ready state so
// the picker re-selects it. Every git step is best-effort — a stale ref or a remote
// that already pruned the branch must not stop the reset. The recorded BRANCH is
// preferred; with none, the first matching feature/<id>-* branch is used.
func (p *Pipeline) Reset(ctx context.Context, id string) error {
	branch := p.State.Get(id, "BRANCH")
	if branch == "" {
		branch, _ = p.Git.FindFeatureBranch(ctx, id)
	}
	_ = p.Git.Checkout(ctx, p.Base, true)
	if branch != "" && branch != p.Base {
		_ = p.Git.DeleteBranch(ctx, branch)
		_ = p.Git.DeletePushedBranch(ctx, p.Remote, branch)
	}
	_ = os.Remove(handoffPath(id))
	_ = os.Remove(verifyPath(id))
	_ = os.Remove(rubricPath(id))
	_ = p.State.RemoveState(id)
	if branch != "" {
		p.logf("  reset %s: cleared saved state + branch %s", id, branch)
	} else {
		p.logf("  reset %s: cleared saved state", id)
	}
	return p.Tracker.Reset(ctx, id)
}

// CheckoutBranch checks out ticket id's recorded feature branch in the target repo
// so a user inspecting an incomplete or quarantined result lands directly on its
// preserved WIP. It resolves the branch from saved state, falling back to the
// first matching feature/<id>-* branch, and returns the branch it switched to.
func (p *Pipeline) CheckoutBranch(ctx context.Context, id string) (string, error) {
	branch := p.State.Get(id, "BRANCH")
	if branch == "" {
		branch, _ = p.Git.FindFeatureBranch(ctx, id)
	}
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
	p.phaseStart("build")

	_ = os.Remove(handoffPath(id))
	_ = os.Remove(verifyPath(id))
	_ = os.Remove(rubricPath(id))

	if err := p.State.Set(id, "PHASE", state.Building); err != nil {
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
	if _, err := p.agentStep(ctx, id, "build", buildInstruction(id, branch, note)); err != nil {
		return err
	}

	if err := p.State.Set(id, "PHASE", state.Built); err != nil {
		return fmt.Errorf("build %s: checkpoint built: %w", id, err)
	}
	return nil
}

func (p *Pipeline) resolveBuildBranch(ctx context.Context, id string) (string, error) {
	branch := p.State.Get(id, "BRANCH")
	if branch == "" {
		branch, _ = p.Git.FindFeatureBranch(ctx, id)
	}
	if branch != "" {
		if err := p.Git.Checkout(ctx, branch, false); err != nil {
			return "", fmt.Errorf("build %s: checkout %s: %w", id, branch, err)
		}
		return branch, nil
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
	base := p.Base
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
// empty PR. Build leaves its work uncommitted (the commit phase runs later), so
// "nothing here" means BOTH a clean working tree (untracked files included) AND no
// commits on the branch beyond base. REQUIRE_REPO_CHANGES=0 disables it for the
// rare legitimately no-op ticket.
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

// Handoff runs the handoff skill to write the QA brief to exactly
// /tmp/handoff-<ID>.md, then checkpoints handed_off.
func (p *Pipeline) Handoff(ctx context.Context, id string) error {
	p.phaseStart("handoff")
	if _, err := p.agentStep(ctx, id, "handoff", handoffTail(id)); err != nil {
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
	if err := p.State.Set(id, "PHASE", state.HandedOff); err != nil {
		return fmt.Errorf("handoff %s: checkpoint handed_off: %w", id, err)
	}
	return nil
}

func (p *Pipeline) persistHandoff(id string) {
	data, err := os.ReadFile(handoffPath(id))
	if err != nil || len(data) == 0 {
		return
	}
	dir := filepath.Join(p.RunsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "handoff.md"), data, 0o644)
}

// restoreHandoff copies the durable runs/<ID>/handoff.md back to /tmp when /tmp
// lost it (wiped on reboot), so a resumed verify reuses the exact brief the
// handoff produced — and the matching rubric — instead of regenerating a fresh
// pair. Best-effort: it leaves /tmp untouched when a non-empty copy is already
// there or no durable copy exists.
func (p *Pipeline) restoreHandoff(id string) {
	if fi, err := os.Stat(handoffPath(id)); err == nil && fi.Size() > 0 {
		return
	}
	data, err := os.ReadFile(filepath.Join(p.RunsDir, id, "handoff.md"))
	if err != nil || len(data) == 0 {
		return
	}
	_ = os.WriteFile(handoffPath(id), data, 0o644)
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
	p.phaseStart("verify")
	handoff := handoffPath(id)

	p.restoreHandoff(id)
	p.restoreRubric(id)

	if fi, err := os.Stat(handoff); err != nil || fi.Size() == 0 {
		if _, err := p.agentStep(ctx, id, "handoff", handoffTail(id)); err != nil {
			return err
		}
		p.persistHandoff(id)
		p.persistRubric(id)
	}

	if fi, err := os.Stat(handoff); err == nil && fi.Size() > 0 {
		p.logf("  ↳ handoff: %s → verify", fmtBytes(fi.Size()))
	} else {
		p.logf("  ⚠ handoff brief missing/empty — verify will run without a QA brief")
	}

	verdictPath := verifyPath(id)
	note := browserNote(p.BrowserVerify, p.AppURL)
	branch := p.State.Get(id, "BRANCH")

	rubricRef, rubricOK := p.activeRubric(id)
	if rubricOK {
		p.logf("  ↳ rubric: runs/%s/rubric.json → verify", id)
	} else {
		p.logf("  ⚠ no usable rubric — verify grades from the brief alone")
	}
	rubricVerify := verifyRubricNote(rubricRef)
	rubricRepair := repairRubricNote(rubricRef)
	lessonsVerify := verifyLessonsNote(p.recallLessons(p.lessonQuery(id)))

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
	var lastFail verdict
	for {
		v, err := p.verifyAttempt(ctx, id, label, handoff, note, checksFragment, rubricVerify, lessonsVerify)
		if err != nil {
			return err
		}
		if v.Pass {
			passed = true
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
			if _, err := p.agentStep(ctx, id, fmt.Sprintf("repair%d", repairAttempt), repairInstruction(id, verdictPath, handoff, branch, v.failureLines(), rubricRepair, lessonsRepair)); err != nil {
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
			if _, err := p.agentStep(ctx, id, fmt.Sprintf("bugfix%d", bugfixAttempt), bugfixInstruction(id, verdictPath, handoff, branch, v.failureLines(), rubricRepair, lessonsRepair)); err != nil {
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
	if repairAttempt > 0 || bugfixAttempt > 0 {
		p.recordLesson(ctx, id, lastFail, attemptLabel(repairAttempt, bugfixAttempt), lessonResultRepaired)
		p.logf("  ✓ verify passed (after %d repair attempt(s) and %d bugfix attempt(s))", repairAttempt, bugfixAttempt)
	} else {
		p.logf("  ✓ verify passed")
	}
	if err := p.State.Set(id, "PHASE", state.Verified); err != nil {
		return fmt.Errorf("verify %s: checkpoint verified: %w", id, err)
	}
	return nil
}

func (p *Pipeline) giveUp(ctx context.Context, id, reason string) error {
	// Idempotent: a ticket already quarantined this run (e.g. a budget guard that
	// fired inside build, whose *GiveUpError then flows through handleGiveUp) must
	// not be finalized or quarantined twice.
	if p.State.Get(id, "PHASE") == state.Quarantined {
		return &GiveUpError{ID: id, Reason: reason}
	}
	p.finalizeFailed(ctx, id)
	if err := p.State.Set(id, "PHASE", state.Quarantined); err != nil {
		return fmt.Errorf("give up %s: checkpoint quarantined: %w", id, err)
	}
	_ = p.State.Set(id, "FAILURE_REASON", reason)
	p.logf("  ✗ quarantining %s: %s", id, reason)
	if err := p.Tracker.Quarantine(ctx, id, reason); err != nil {
		p.logf("  quarantine MCP error (continuing): %v", err)
	}
	return &GiveUpError{ID: id, Reason: reason}
}

func (p *Pipeline) finalizeFailed(ctx context.Context, id string) {
	branch, _ := p.Git.CurrentBranch(ctx)
	if branch != p.Base {
		_ = p.Git.AddAll(ctx)
		_ = p.Git.Commit(ctx, fmt.Sprintf("wip(%s): quarantined attempt — needs human", id), true)
		if err := p.Git.Push(ctx, p.Remote, "HEAD"); err == nil {
			p.logf("  saved attempt to %s/%s", p.Remote, branch)
		} else {
			p.logf("  saved attempt to local branch %s", branch)
		}
	}
	_ = p.Git.Checkout(ctx, p.Base, true)
	_ = p.Git.Clean(ctx)
}

// CommitAndPR ships the verified slice: the commit phase stages and commits ONLY
// this ticket's files, then the branch is pushed and a PR opened against Base — or
// an existing PR reused when a prior run already created one. It checkpoints
// pr_open with PR/PR_URL and moves the ticket to In Review with the PR link.
// A push/PR failure aborts this ticket (returned to the caller) without
// quarantining — the WIP stays on the branch for a later resume.
func (p *Pipeline) CommitAndPR(ctx context.Context, id string) error {
	p.phaseStart("commit")
	rubricRef, _ := p.activeRubric(id)
	if _, err := p.agentStep(ctx, id, "commit", commitInstruction(id, commitRubricNote(rubricRef))); err != nil {
		return err
	}
	if err := p.retryGH(ctx, "git push", func() error {
		return p.Git.Push(ctx, p.Remote, "HEAD")
	}); err != nil {
		return fmt.Errorf("commit %s: push: %w", id, err)
	}

	p.phaseStart("pr")
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
		prURL, err = p.createOrAdoptPR(ctx, prBase, branch, id+": "+prDesc(branch), prBody(id))
		if err != nil {
			return fmt.Errorf("commit %s: pr create: %w", id, err)
		}
	}
	p.logf("  PR %s", prURL)
	if err := p.State.Set(id, "PR", prNumber(prURL)); err != nil {
		return fmt.Errorf("commit %s: record PR: %w", id, err)
	}
	if err := p.State.Set(id, "PR_URL", prURL); err != nil {
		return fmt.Errorf("commit %s: record PR_URL: %w", id, err)
	}
	if err := p.State.Set(id, "PHASE", state.PROpen); err != nil {
		return fmt.Errorf("commit %s: checkpoint pr_open: %w", id, err)
	}
	if err := p.Tracker.SetStatus(ctx, id, "In Review", "Attach this PR link to the issue: "+prURL+"."); err != nil {
		p.logf("  status (In Review) error: %v", err)
	}
	return nil
}

// CIAndMerge is the CI gate + merge. It reconciles first: a PR a prior run
// already merged is marked Done without re-merging. Otherwise it polls
// CI; on green it squash-merges and deletes the branch when AutoMerge is set (else
// it stops at the open PR), moves the ticket to Done, and checkpoints merged. A CI
// failure or timeout gives up — preserving the branch and quarantining without
// aborting the loop.
func (p *Pipeline) CIAndMerge(ctx context.Context, id string) error {
	pr := p.State.Get(id, "PR")
	if prState, _ := p.GitHub.PRState(ctx, pr); prState == "MERGED" {
		if err := p.markDone(ctx, id, "  ✓ %s already merged — marked Done"); err != nil {
			return err
		}
		return ErrAlreadyDone
	}

	p.phaseStart("ci")
	if err := p.pollCI(ctx, pr); err != nil {
		p.logf("  ✗ CI: %v", err)
		return p.giveUp(ctx, id, "CI not green")
	}
	if !p.AutoMerge {
		p.logf("  green CI — leaving merge to you (AUTO_MERGE=0)")
		return nil
	}
	p.phaseStart("merge")
	if err := p.retryGH(ctx, "gh pr merge", func() error {
		if st, _ := p.GitHub.PRState(ctx, pr); st == "MERGED" {
			return nil
		}
		return p.GitHub.Merge(ctx, pr, p.MergeMethod, true)
	}); err != nil {
		return fmt.Errorf("merge %s: %w", id, err)
	}
	return p.markDone(ctx, id, "  ✓ merged %s, marked Done")
}

func (p *Pipeline) markDone(ctx context.Context, id, logFmt string) error {
	if err := p.Tracker.SetStatus(ctx, id, "Done", ""); err != nil {
		p.logf("  status (Done) error: %v", err)
	}
	if err := p.State.Set(id, "PHASE", state.Merged); err != nil {
		return fmt.Errorf("merge %s: checkpoint merged: %w", id, err)
	}
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
			return ErrCIFailed
		case ciGreen:
			return nil
		}
		if waited >= p.CITimeout {
			if !sawCheck && len(expected) == 0 {
				p.logf("  ⓘ no checks ever appeared — if this repo has no PR CI, set REQUIRE_CI=0 to skip the gate")
			}
			return ErrCITimeout
		}
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

func prBody(id string) string {
	return fmt.Sprintf("## Summary\nAutomated implementation of %s via the Trau loop.\n\n## Test plan\n- [x] Pest suite for this slice\n- [x] QA verify pass (browser for UI slices)\n\nLinear: %s", id, id)
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
	p.writeTranscript(id, phase, res.Final)
	return res.Final, err
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

func (p *Pipeline) writeTranscript(id, phase, content string) {
	dir := filepath.Join(p.RunsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, phase+".log"), []byte(content), 0o644)
}

func (p *Pipeline) logf(format string, a ...any) {
	if p.Renderer != nil {
		p.Renderer.Logf(format, a...)
	}
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
	p.logf("  ⏸ paused — %s usage/rate limit reached during %s", prov, phase)
	p.logf("  ↳ %s left resumable on its branch; rerun trau when the limit resets", id)
	return &PausedError{ID: id, Phase: phase, Provider: prov, Reason: prov + " rate/usage limit reached"}
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
	p.logf("  ⏸ paused — %s needs re-authentication during %s (run the provider's /login)", prov, phase)
	p.logf("  ↳ %s left resumable on its branch; rerun trau after re-authenticating %s", id, prov)
	return &PausedError{ID: id, Phase: phase, Provider: prov, Reason: prov + " authentication required — re-login"}
}

// isAuthFailure reports whether err is (or wraps) the agent's auth/login-wall
// sentinel — a provider state that retrying can't fix and that isn't the ticket's
// fault, so the loop pauses blamelessly rather than burning retries.
func isAuthFailure(err error) bool {
	return errors.Is(err, agent.ErrAuthRequired)
}

// guardBudget enforces the configured spend ceilings before an agent call. It
// reads the LIVE ledger totals (this ticket's runs/<ID>/tokens.jsonl and the day's
// spend across all buckets) and, on the first cap reached, quarantines the ticket
// via giveUp with a cost-overrun reason — halting before the next call adds to the
// bill. A nil ledger or no configured cap is a no-op (back-compat).
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

// dailySpend reads the day's accumulated spend across every ticket bucket, keyed on
// the local date from p.Now (defaulting to time.Now).
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

func (p *Pipeline) phaseStart(phase string) {
	if p.Renderer != nil {
		p.Renderer.PhaseStart(phase)
	}
}

func (p *Pipeline) setTitle(title string) {
	if title != "" && p.Renderer != nil {
		p.Renderer.SetTitle(title)
	}
}

const resumeNote = " A previous attempt may have left partial work on this branch; continue from it rather than starting over."

func buildInstruction(id, branch, note string) string {
	return "Implement " + id + " on branch " + branch + " (already checked out). This is an unattended run: auto-select and load the project skills relevant to this ticket — do NOT pause to ask which skills to load. Always include the project's test skill (e.g. pest-testing); add domain skills based on what the ticket actually touches (e.g. inertia-react-development and tailwindcss-development for UI, medialibrary-development for uploads, pennant-development for feature flags, the relevant *-development skill for each area)." + note + " Implement the ticket fully and run only the tests relevant to this slice (the new or changed test files for this ticket) — not the entire suite. Do not commit, push, or open a PR — stop after implementation."
}

func fmtBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}

func handoffPath(id string) string { return "/tmp/handoff-" + id + ".md" }

func verifyPath(id string) string { return "/tmp/verify-" + id + ".json" }

func handoffTail(id string) string {
	return "Write a QA brief for " + id + ": the concrete, checkable behaviors a manual QA tester must verify for this slice, in priority order. Don't duplicate content already in the ticket, PRD, or diff — focus on what to check and how. Redact any secrets. Save it to exactly " + handoffPath(id) + " (overwrite if present) and nowhere else." + rubricInstruction(id)
}

func browserNote(mode, appURL string) string {
	switch mode {
	case "always":
		return "Also drive the running app at " + appURL + " via the browser-harness skill to exercise the UI in the handoff."
	case "auto":
		return "If this slice has a UI surface, also drive the running app at " + appURL + " via the browser-harness skill; skip the browser for backend-only slices."
	default:
		return ""
	}
}

func verifyTail(id, handoff, verdict, note, checksFragment, rubricNote, lessonsNote string) string {
	return "Cold, adversarial QA verification of " + id + " against the QA brief at " + handoff + ". Treat the code on disk and the brief as the only sources of truth; your job is to find what does NOT work." + rubricNote + lessonsNote + " Run only the tests relevant to this slice (the new or changed test files for this ticket) using the project's test runner — not the whole suite. For each behavior the brief lists, confirm it actually holds. " + note + " Distinguish defects in this slice's own code from pre-existing or out-of-scope issues. When finished, write a JSON verdict to exactly " + verdict + ": {\"pass\": true|false, \"summary\": \"one line\", \"failures\": [\"...\"]}. Set pass=false if any relevant test fails or any behavior in the brief does not work; failures lists each concrete problem (empty when pass is true)." + checksFragment + " Do not commit, push, or open a PR."
}

func commitInstruction(id, rubricNote string) string {
	return "Commit the implementation for " + id + ". Stage and commit ONLY files that are part of " + id + "; never commit unrelated untracked files or tooling (e.g. scripts/, *.env)." + rubricNote + " Group related changes into atomic, dependency-ordered commits (foundational changes first; keep refactors, features, and fixes in distinct commits). Use Conventional Commits: '<type>(scope): <subject>' (type ∈ feat|fix|refactor|docs|style|test|chore), imperative mood, subject under 72 characters, with a 'Refs: " + id + "' trailer; match the project's existing git-log style if it differs. The commit message must contain ONLY the subject and body: do NOT add any 'Co-authored-by:'/'Co-Authored-By:' trailer, a '🤖 Generated with Claude Code' line, or any mention of AI/assistant authorship, and remove them if your environment adds them by default."
}

func repairInstruction(id, verdict, handoff, branch, fails, rubricNote, lessonsNote string) string {
	return id + " verification FAILED. QA verdict file: " + verdict + ". QA brief: " + handoff + ". Failures:\n" +
		fails + "\n\nYou are on branch " + branch + " with this slice's implementation uncommitted." + rubricNote + lessonsNote + " If this is a DEFECT IN THIS SLICE'S OWN code, find the root cause and fix it with minimal, targeted changes, then run the relevant Pest tests to confirm. If the failure is actually a pre-existing or out-of-scope bug NOT caused by this slice, do NOT hack around it — change nothing and say so clearly. Do not commit, push, or open a PR."
}

func bugfixInstruction(id, verdict, handoff, branch, fails, rubricNote, lessonsNote string) string {
	return id + " verification FAILED after initial quick repairs. QA verdict file: " + verdict + ". QA brief: " + handoff + ". Failures:\n" +
		fails + "\n\nYou are on branch " + branch + " with this slice's implementation uncommitted." + rubricNote + lessonsNote + " This is a comprehensive bug-fix pass: read the full verdict, identify every failure that is a DEFECT IN THIS SLICE'S OWN code, and fix ALL of them with minimal, targeted changes. Do not stop after the first fix. Run the relevant tests (and browser checks if applicable) to confirm every failure is resolved before finishing. If a failure is a pre-existing or out-of-scope bug NOT caused by this slice, do NOT hack around it — note it clearly. Do not commit, push, or open a PR."
}

type verdict struct {
	Pass     bool          `json:"pass"`
	Summary  string        `json:"summary"`
	Failures []string      `json:"failures"`
	Checks   []checkResult `json:"checks,omitempty"`
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
func (p *Pipeline) verifyAttempt(ctx context.Context, id, label, handoff, note, checksFragment, rubricNote, lessonsNote string) (verdict, error) {
	if len(p.VerifyPanel) > 0 {
		return p.runPanel(ctx, id, label, handoff, note, checksFragment, rubricNote, lessonsNote)
	}
	verdictPath := verifyPath(id)
	_ = os.Remove(verdictPath)
	prompt := verifyTail(id, handoff, verdictPath, note, checksFragment, rubricNote, lessonsNote)
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
// verifyPath(id). A provider pause or budget give-up from any member is
// propagated so the loop stops cleanly (the ticket stays resumable on its branch)
// instead of being recorded as a dissenting fail; a plain timeout/crash counts as
// that member failing.
func (p *Pipeline) runPanel(ctx context.Context, id, label, handoff, note, checksFragment, rubricNote, lessonsNote string) (verdict, error) {
	results := make([]panelResult, 0, len(p.VerifyPanel))
	for _, m := range p.VerifyPanel {
		memberPath := verifyMemberPath(id, m.Name)
		_ = os.Remove(memberPath)
		memberLabel := label + "-" + m.Name
		prompt := verifyTail(id, handoff, memberPath, note, checksFragment, rubricNote, lessonsNote)
		_, agentErr := p.agentStepOn(ctx, id, memberLabel, prompt, m.Runner)
		if agentErr != nil && isFatalAgentErr(agentErr) {
			return verdict{}, agentErr
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
		results = append(results, panelResult{Name: m.Name, Verdict: v})
		p.logf("  ↳ %s: %s", m.Name, passFailLine(v))
	}
	merged := mergeVerdicts(p.PanelPolicy, results)
	_ = writeVerdictFile(verifyPath(id), merged)
	p.logf("  ↳ panel verdict: %s", merged.Summary)
	return merged, nil
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
	s := err.Error()
	low := strings.ToLower(s)
	if strings.Contains(low, "rate_limit") || strings.Contains(low, "rate limit") ||
		strings.Contains(low, "usage limit") || strings.Contains(low, "quota") || strings.Contains(s, "429") {
		return "provider usage/rate limit reached — see provider log", true
	}
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

func (g ExecGit) run(ctx context.Context, args ...string) error {
	full := append([]string{"-C", g.Repo}, args...)
	logger.Debugf("git %s", strings.Join(full, " "))
	if out, err := exec.CommandContext(ctx, g.bin(), full...).CombinedOutput(); err != nil {
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

// Push pushes ref to remote, setting upstream (git push -u <remote> <ref>).
func (g ExecGit) Push(ctx context.Context, remote, ref string) error {
	return g.run(ctx, "push", "-u", remote, ref)
}

// Checkout switches to ref; force adds -f to discard local changes.
func (g ExecGit) Checkout(ctx context.Context, ref string, force bool) error {
	args := []string{"checkout"}
	if force {
		args = append(args, "-f")
	}
	return g.run(ctx, append(args, ref)...)
}

// CreateBranch creates and switches to branch off base (git checkout -b <branch> <base>).
func (g ExecGit) CreateBranch(ctx context.Context, branch, base string) error {
	return g.run(ctx, "checkout", "-b", branch, base)
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

// Pull fast-forwards branch from remote (git pull --ff-only <remote> <branch>).
func (g ExecGit) Pull(ctx context.Context, remote, branch string) error {
	return g.run(ctx, "pull", "--ff-only", remote, branch)
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
// (no PR found) is swallowed to "".
func (g ExecGitHub) PRURL(ctx context.Context, branch string) (string, error) {
	out, err := g.output(ctx, "pr", "view", branch, "--json", "url", "-q", ".url")
	if err != nil {
		return "", nil
	}
	return out, nil
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
