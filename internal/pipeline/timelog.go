package pipeline

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/timelog"
)

// gitDiffStatter is the extra git surface the time-log hook needs beyond the
// shared Git seam. ExecGit satisfies it; a fake Git that does not simply yields an
// empty diff (the estimate then floors to a non-zero minimum), so the hook degrades
// gracefully instead of coupling the Git interface to an opt-in feature.
type gitDiffStatter interface {
	DiffStat(ctx context.Context, base, branch string) (files, additions, deletions int, err error)
	Commits(ctx context.Context, base, branch string) ([]string, error)
	FirstCommitDate(ctx context.Context, base, branch string) (string, error)
}

// recordTimelog writes the per-ticket human-effort time log after a merge. It is
// best-effort and idempotent: gated off by default, it never blocks or fails the
// loop (a write/estimate error logs and continues, like the token/lessons ledgers),
// and re-running an already-merged ticket does not duplicate entries. In an epic
// run it logs per child; the epic parent is never logged.
func (p *Pipeline) recordTimelog(ctx context.Context, id string) {
	if !p.TimelogEnabled || p.TimelogStorage == timelog.StorageNone {
		return
	}
	if p.EpicID != "" && id == p.EpicID {
		return
	}
	repoRoot := strings.TrimSpace(p.RepoRoot)
	if repoRoot == "" {
		return
	}
	path := timelog.Path(p.TimelogStorage, repoRoot, id)
	if path == "" {
		return
	}
	if err := timelog.MigrateLegacy(p.TimelogStorage, repoRoot); err != nil {
		p.logf("  timelog legacy migration error (continuing): %v", err)
	}

	branch := p.State.Get(id, "BRANCH")
	if branch == "" {
		branch, _ = p.Git.FindFeatureBranch(ctx, id)
	}
	base := p.timelogBase(ctx)

	var (
		ds      timelog.DiffStats
		commits []string
		started string
	)
	if differ, ok := p.Git.(gitDiffStatter); ok && branch != "" && base != "" {
		if f, a, d, err := differ.DiffStat(ctx, base, branch); err == nil {
			ds = timelog.DiffStats{Files: f, Additions: a, Deletions: d}
		} else {
			p.logf("  timelog diffstat error (continuing): %v", err)
		}
		commits, _ = differ.Commits(ctx, base, branch)
		started, _ = differ.FirstCommitDate(ctx, base, branch)
	}

	minutes := p.estimateMinutes(ctx, id, ds, commits)
	now := p.nowUTC()
	if started == "" {
		started = now.Format(time.RFC3339)
	}
	completed := now.Format(time.RFC3339)

	title := p.State.Get(id, "TITLE")
	entry := timelog.Entry{
		Date:      now.Format("2006-01-02"),
		Minutes:   minutes,
		Summary:   timelogSummary(title, id),
		DiffStats: ds,
		Commits:   commits,
	}
	meta := timelog.Log{TicketID: id, TicketTitle: title, Branch: branch}
	if err := timelog.Record(path, meta, entry, started, completed); err != nil {
		p.logf("  timelog write error (continuing): %v", err)
		return
	}
	if p.TimelogStorage == timelog.StorageRepo {
		if err := timelog.EnsureGitignore(repoRoot); err != nil {
			p.logf("  timelog .gitignore error (continuing): %v", err)
		}
	}
	if err := timelog.WriteExport(path, p.TimelogOutputFormat); err != nil {
		p.logf("  timelog export error (continuing): %v", err)
	}
	p.logf("  ⏱ logged ~%s effort for %s (.trau/time)", humanMinutes(minutes), id)
}

// timelogBase returns the branch the merged work diverged from: the epic
// integration branch for an epic child, else the configured base branch.
func (p *Pipeline) timelogBase(ctx context.Context) string {
	if p.EpicID != "" {
		if epic, err := p.epicBranchName(ctx); err == nil && epic != "" {
			return epic
		}
	}
	return p.Base
}

// estimateMinutes returns the per-ticket effort estimate. The default heuristic is
// deterministic and free; agent mode runs a cheap estimator and falls back to the
// heuristic on any failure, so the loop never depends on it.
func (p *Pipeline) estimateMinutes(ctx context.Context, id string, ds timelog.DiffStats, commits []string) int {
	if p.TimelogEstimator == timelog.EstimatorAgent {
		if m, ok := p.estimateMinutesAgent(ctx, id, ds, commits); ok {
			return m
		}
	}
	return timelog.HeuristicMinutes(ds, len(commits))
}

// estimateMinutesAgent runs a cheap, isolated agent pass that reads the diff and
// returns a senior-developer effort estimate in minutes using the same anchors as
// the heuristic. It writes the number to a /tmp file and parses it back, mirroring
// the lessons-distill pattern; any error or unparseable output reads as "no
// estimate" so the caller falls back to the deterministic heuristic. It runs
// outside the budget-guarded phase path — a post-merge estimate must never
// quarantine a ticket that already finished its real work.
func (p *Pipeline) estimateMinutesAgent(ctx context.Context, id string, ds timelog.DiffStats, commits []string) (int, bool) {
	path := timelogEstimatePath(id)
	_ = os.Remove(path)
	if _, err := p.agentPhaseOn(ctx, id, "timelog", timelogEstimateInstruction(id, ds, len(commits), path), p.Runner); err != nil {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n <= 0 {
		return 0, false
	}
	if n > 2880 { // clamp to <= 2 days; this is an estimate, not wall-clock
		n = 2880
	}
	return n, true
}

func timelogEstimatePath(id string) string { return "/tmp/timelog-" + id + ".txt" }

func timelogEstimateInstruction(id string, ds timelog.DiffStats, commits int, path string) string {
	return prompts.Render("timelog_estimate", prompts.TimelogEstimateData{
		ID:        id,
		Files:     ds.Files,
		Additions: ds.Additions,
		Deletions: ds.Deletions,
		Commits:   commits,
		Path:      path,
	})
}

func (p *Pipeline) nowUTC() time.Time {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	return now().UTC()
}

func timelogSummary(title, id string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	return id
}

func humanMinutes(m int) string {
	if m < 60 {
		return strconv.Itoa(m) + "m"
	}
	h := m / 60
	if m%60 == 0 {
		return strconv.Itoa(h) + "h"
	}
	return strconv.Itoa(h) + "h" + strconv.Itoa(m%60) + "m"
}
