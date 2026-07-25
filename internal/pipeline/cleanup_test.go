package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
)

func TestCleanup(t *testing.T) {
	cases := []struct {
		name      string
		enabled   bool
		agentErr  error
		wantCalls int
		wantPause bool
	}{
		{name: "disabled skips the agent", enabled: false, wantCalls: 0},
		{name: "enabled runs the agent once", enabled: true, agentErr: nil, wantCalls: 1},
		{name: "ordinary agent error fails open", enabled: true, agentErr: errors.New("boom"), wantCalls: 1},
		{name: "provider pause propagates", enabled: true, agentErr: errors.New("kimi run (cleanup): 429 usage limit reached"), wantCalls: 1, wantPause: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &countingRunner{results: []error{tc.agentErr}, name: "claude"}
			p := newTestPipeline(t, runner, &fakeTracker{})
			p.Cleanup = tc.enabled

			err := p.cleanup(context.Background(), "COD-635")

			switch {
			case tc.wantPause && !IsPaused(err):
				t.Fatalf("cleanup err = %v, want a paused error", err)
			case !tc.wantPause && err != nil:
				t.Fatalf("cleanup err = %v, want nil (fails open)", err)
			}
			if runner.calls != tc.wantCalls {
				t.Errorf("agent calls = %d, want %d", runner.calls, tc.wantCalls)
			}
		})
	}
}

// sizeGit adds the worktreeSizer capability on top of the shared fakeGit so
// skipCleanup can be exercised with a canned working-tree size.
type sizeGit struct {
	fakeGit
	files, lines int
	err          error
}

func (g sizeGit) WorktreeDiffStat(context.Context, string) (int, int, error) {
	return g.files, g.lines, g.err
}

// TestSmallSlice covers the pure gate over its files/lines inputs: it trips only
// when the diff is within both thresholds.
func TestSmallSlice(t *testing.T) {
	cases := []struct {
		name         string
		files, lines int
		want         bool
	}{
		{name: "tiny", files: 3, lines: 40, want: true},
		{name: "at both limits", files: smallSliceMaxFiles, lines: smallSliceMaxLines, want: true},
		{name: "one file over", files: smallSliceMaxFiles + 1, lines: 40, want: false},
		{name: "one line over", files: 3, lines: smallSliceMaxLines + 1, want: false},
		{name: "large", files: 20, lines: 900, want: false},
		{name: "empty diff", files: 0, lines: 0, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := smallSlice(tc.files, tc.lines); got != tc.want {
				t.Errorf("smallSlice(%d, %d) = %v, want %v", tc.files, tc.lines, got, tc.want)
			}
		})
	}
}

// TestSkipCleanup covers the end-to-end gate runPhases consults: a tiny diff skips
// cleanup, a larger diff still runs it, and every absent-signal path (a Git that
// cannot size the tree, a measurement error) fails open so the full chain runs.
func TestSkipCleanup(t *testing.T) {
	cases := []struct {
		name string
		git  Git
		want bool
	}{
		{name: "tiny skips", git: sizeGit{files: 3, lines: 40}, want: true},
		{name: "too many files runs", git: sizeGit{files: smallSliceMaxFiles + 1, lines: 10}, want: false},
		{name: "too many lines runs", git: sizeGit{files: 2, lines: smallSliceMaxLines + 1}, want: false},
		{name: "git cannot size fails open", git: fakeGit{}, want: false},
		{name: "measure error fails open", git: sizeGit{err: context.DeadlineExceeded}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Git = tc.git

			if got := p.skipCleanup(context.Background(), "COD-64200"); got != tc.want {
				t.Errorf("skipCleanup = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSkipHandoff covers the same gate applied to the handoff phase: a tiny diff skips
// the standalone handoff agent, a larger diff still runs it, and every absent-signal
// path fails open to the full handoff + verify chain.
func TestSkipHandoff(t *testing.T) {
	cases := []struct {
		name string
		git  Git
		want bool
	}{
		{name: "tiny skips", git: sizeGit{files: 3, lines: 40}, want: true},
		{name: "too many files runs", git: sizeGit{files: smallSliceMaxFiles + 1, lines: 10}, want: false},
		{name: "too many lines runs", git: sizeGit{files: 2, lines: smallSliceMaxLines + 1}, want: false},
		{name: "git cannot size fails open", git: fakeGit{}, want: false},
		{name: "measure error fails open", git: sizeGit{err: context.DeadlineExceeded}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Git = tc.git

			if got := p.skipHandoff(context.Background(), "COD-79900"); got != tc.want {
				t.Errorf("skipHandoff = %v, want %v", got, tc.want)
			}
		})
	}
}

// snapshotGit adds the worktreeSnapshotter capability on top of the shared fakeGit,
// handing out a scripted snapshot per call and a canned tree diff between them.
type snapshotGit struct {
	fakeGit
	trees                        []string
	snapErr                      error
	files, insertions, deletions int
	diffErr                      error

	snaps int
	pairs [][2]string
}

func (g *snapshotGit) SnapshotWorktree(context.Context) (string, error) {
	if g.snapErr != nil {
		return "", g.snapErr
	}
	i := g.snaps
	g.snaps++
	if i >= len(g.trees) {
		i = len(g.trees) - 1
	}
	return g.trees[i], nil
}

func (g *snapshotGit) DiffTrees(_ context.Context, from, to string) (int, int, int, error) {
	g.pairs = append(g.pairs, [2]string{from, to})
	if g.diffErr != nil {
		return 0, 0, 0, g.diffErr
	}
	return g.files, g.insertions, g.deletions, nil
}

// claimRunner answers every phase with a fixed final line, standing in for the
// cleanup agent's self-report.
type claimRunner struct{ final string }

func (r claimRunner) Run(context.Context, string, string) (agent.Result, error) {
	return agent.Result{Final: r.final}, nil
}

// TestCleanupRecordsMeasuredOutcome pins the ground truth: the event carries the
// measured diff — zeros when nothing changed — next to whatever the agent claimed,
// and an unmeasurable tree records nothing rather than invented numbers.
func TestCleanupRecordsMeasuredOutcome(t *testing.T) {
	cases := []struct {
		name       string
		git        Git
		final      string
		wantEvent  bool
		wantFields map[string]float64
		wantClaim  string
	}{
		{
			name:       "measured trim",
			git:        &snapshotGit{trees: []string{"tree-a", "tree-b"}, files: 2, insertions: 1, deletions: 9},
			final:      "done\n\ntrimmed 9 comments/lines across 2 files\n",
			wantEvent:  true,
			wantFields: map[string]float64{"files": 2, "insertions": 1, "deletions": 9},
			wantClaim:  "trimmed 9 comments/lines across 2 files",
		},
		{
			name:       "no-op outranks an inflated claim",
			git:        &snapshotGit{trees: []string{"tree-a", "tree-a"}},
			final:      "trimmed 1398 comments/lines across 40 files",
			wantEvent:  true,
			wantFields: map[string]float64{"files": 0, "insertions": 0, "deletions": 0},
			wantClaim:  "trimmed 1398 comments/lines across 40 files",
		},
		{name: "git cannot snapshot records nothing", git: fakeGit{}, final: "no changes needed"},
		{name: "snapshot error records nothing", git: &snapshotGit{snapErr: context.DeadlineExceeded}, final: "no changes needed"},
		{
			name:  "measurement error records nothing",
			git:   &snapshotGit{trees: []string{"tree-a", "tree-b"}, diffErr: context.DeadlineExceeded},
			final: "no changes needed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := newTestPipeline(t, claimRunner{final: tc.final}, &fakeTracker{})
			p.Cleanup = true
			p.Git = tc.git
			p.Events = event.New(&buf)

			if err := p.cleanup(context.Background(), "COD-1194"); err != nil {
				t.Fatalf("cleanup err = %v, want nil", err)
			}

			evs := kindEvents(t, &buf, event.KindCleanupOutcome)
			if !tc.wantEvent {
				if len(evs) != 0 {
					t.Fatalf("emitted %d cleanup_outcome events, want 0", len(evs))
				}
				return
			}
			if len(evs) != 1 {
				t.Fatalf("emitted %d cleanup_outcome events, want 1", len(evs))
			}
			ev := evs[0]
			for key, want := range tc.wantFields {
				if got, ok := ev.Fields[key].(float64); !ok || got != want {
					t.Errorf("field %q = %v, want %v", key, ev.Fields[key], want)
				}
			}
			if got := ev.Fields["agent_claim"]; got != tc.wantClaim {
				t.Errorf("agent_claim = %v, want %q", got, tc.wantClaim)
			}
			if got := ev.Fields["ticket"]; got != "COD-1194" {
				t.Errorf("ticket = %v, want COD-1194", got)
			}
		})
	}
}

// TestCleanupSnapshotsAroundTheAgent pins the ordering the measurement depends on:
// one snapshot before the agent runs, one once it returns, and those two get diffed.
func TestCleanupSnapshotsAroundTheAgent(t *testing.T) {
	git := &snapshotGit{trees: []string{"before", "after"}, files: 1, deletions: 3}
	var buf bytes.Buffer
	p := newTestPipeline(t, claimRunner{final: "trimmed 3 comments/lines across 1 files"}, &fakeTracker{})
	p.Cleanup = true
	p.Git = git
	p.Events = event.New(&buf)

	if err := p.cleanup(context.Background(), "COD-1194"); err != nil {
		t.Fatalf("cleanup err = %v, want nil", err)
	}

	if git.snaps != 2 {
		t.Errorf("snapshots taken = %d, want 2", git.snaps)
	}
	want := [2]string{"before", "after"}
	if len(git.pairs) != 1 || git.pairs[0] != want {
		t.Errorf("diffed trees = %v, want [%v]", git.pairs, want)
	}
}

func TestCleanupClaim(t *testing.T) {
	cases := []struct {
		name, out, want string
	}{
		{name: "final line after prose", out: "did stuff\n\n  trimmed 4 comments/lines across 2 files  \n\n", want: "trimmed 4 comments/lines across 2 files"},
		{name: "no-op claim", out: "no changes needed", want: "no changes needed"},
		{name: "empty output", out: "\n \n", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanupClaim(tc.out); got != tc.want {
				t.Errorf("cleanupClaim = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExecGitSnapshotWorktree exercises the measurement against real git: snapshots
// bracket both tracked edits and new untracked files, an unchanged tree diffs to
// zero, and snapshotting leaves the repo's own index and working tree untouched.
func TestExecGitSnapshotWorktree(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	write(t, filepath.Join(repo, "keep.txt"), "a\nb\nc\n")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-m", "init")

	g := ExecGit{Repo: repo}
	ctx := context.Background()
	before, err := g.SnapshotWorktree(ctx)
	if err != nil {
		t.Fatalf("snapshot before: %v", err)
	}

	same, err := g.SnapshotWorktree(ctx)
	if err != nil {
		t.Fatalf("snapshot unchanged: %v", err)
	}
	files, insertions, deletions, err := g.DiffTrees(ctx, before, same)
	if err != nil {
		t.Fatalf("diff unchanged trees: %v", err)
	}
	if files != 0 || insertions != 0 || deletions != 0 {
		t.Errorf("unchanged tree = %d files +%d/-%d, want 0/0/0", files, insertions, deletions)
	}

	write(t, filepath.Join(repo, "keep.txt"), "a\nc\n")
	write(t, filepath.Join(repo, "fresh.txt"), "x\ny\n")
	after, err := g.SnapshotWorktree(ctx)
	if err != nil {
		t.Fatalf("snapshot after: %v", err)
	}
	files, insertions, deletions, err = g.DiffTrees(ctx, before, after)
	if err != nil {
		t.Fatalf("diff changed trees: %v", err)
	}
	if files != 2 || insertions != 2 || deletions != 1 {
		t.Errorf("changed tree = %d files +%d/-%d, want 2 files +2/-1", files, insertions, deletions)
	}

	status, err := g.StatusPorcelain(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if want := "M keep.txt"; status != want {
		t.Errorf("status after snapshotting = %q, want %q — the edit must stay unstaged", status, want)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
