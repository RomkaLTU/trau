package pipeline

import (
	"context"
	"errors"
	"os"
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

// TestSmallSlice covers the pure gate over its files/lines/verdict inputs: it
// trips only when the size judge vouched fits_one_window AND the diff is within
// both thresholds, and a false verdict never trips it regardless of size.
func TestSmallSlice(t *testing.T) {
	cases := []struct {
		name          string
		files, lines  int
		fitsOneWindow bool
		want          bool
	}{
		{name: "tiny and fits", files: 3, lines: 40, fitsOneWindow: true, want: true},
		{name: "at both limits", files: smallSliceMaxFiles, lines: smallSliceMaxLines, fitsOneWindow: true, want: true},
		{name: "one file over", files: smallSliceMaxFiles + 1, lines: 40, fitsOneWindow: true, want: false},
		{name: "one line over", files: 3, lines: smallSliceMaxLines + 1, fitsOneWindow: true, want: false},
		{name: "tiny but verdict false", files: 1, lines: 10, fitsOneWindow: false, want: false},
		{name: "large and verdict false", files: 20, lines: 900, fitsOneWindow: false, want: false},
		{name: "empty diff fits", files: 0, lines: 0, fitsOneWindow: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := smallSlice(tc.files, tc.lines, tc.fitsOneWindow); got != tc.want {
				t.Errorf("smallSlice(%d, %d, %v) = %v, want %v", tc.files, tc.lines, tc.fitsOneWindow, got, tc.want)
			}
		})
	}
}

// TestSkipCleanup covers the end-to-end gate runPhases consults: a tiny
// fits_one_window diff skips cleanup, a larger diff or a not-one-window verdict
// still runs it, and every absent-signal path (no verdict, a Git that cannot size
// the tree, a measurement error) fails open so the full chain runs.
func TestSkipCleanup(t *testing.T) {
	const fits = `{"fits_one_window":true,"reason":"","suggested_slices":[]}`
	const tooBig = `{"fits_one_window":false,"reason":"x","suggested_slices":["a"]}`

	cases := []struct {
		name    string
		verdict string // "" = no verdict file written
		git     Git
		want    bool
	}{
		{name: "tiny fits skips", verdict: fits, git: sizeGit{files: 3, lines: 40}, want: true},
		{name: "too many files runs", verdict: fits, git: sizeGit{files: smallSliceMaxFiles + 1, lines: 10}, want: false},
		{name: "too many lines runs", verdict: fits, git: sizeGit{files: 2, lines: smallSliceMaxLines + 1}, want: false},
		{name: "verdict not one window runs", verdict: tooBig, git: sizeGit{files: 1, lines: 5}, want: false},
		{name: "missing verdict fails open", verdict: "", git: sizeGit{files: 1, lines: 5}, want: false},
		{name: "git cannot size fails open", verdict: fits, git: fakeGit{}, want: false},
		{name: "measure error fails open", verdict: fits, git: sizeGit{err: context.DeadlineExceeded}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "COD-64200"
			path := sizeJudgePath(id)
			_ = os.Remove(path)
			t.Cleanup(func() { _ = os.Remove(path) })
			if tc.verdict != "" {
				if err := os.WriteFile(path, []byte(tc.verdict), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Git = tc.git

			if got := p.skipCleanup(context.Background(), id); got != tc.want {
				t.Errorf("skipCleanup = %v, want %v", got, tc.want)
			}
		})
	}
}
