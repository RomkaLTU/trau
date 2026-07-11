package pipeline

import (
	"context"
	"errors"
	"testing"
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
