package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLintFix is the COD-634 pre-verify lint-fix gate: disabled runs neither the
// command nor the agent; a configured LINT_FIX_CMD runs deterministically in the
// target repo (no agent call) and fails open on a non-zero exit; an empty command
// falls back to the agent step.
func TestLintFix(t *testing.T) {
	cases := []struct {
		name       string
		enabled    bool
		cmd        string
		wantMarker bool
		wantAgent  int
	}{
		{name: "disabled skips both", enabled: false, cmd: "touch marker", wantMarker: false, wantAgent: 0},
		{name: "command runs deterministically", enabled: true, cmd: "touch marker", wantMarker: true, wantAgent: 0},
		{name: "failing command fails open", enabled: true, cmd: "touch marker && exit 1", wantMarker: true, wantAgent: 0},
		{name: "empty command uses the agent", enabled: true, cmd: "", wantMarker: false, wantAgent: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			runner := &countingRunner{results: []error{nil}, name: "claude"}
			p := newTestPipeline(t, runner, &fakeTracker{})
			p.LintFix = tc.enabled
			p.LintFixCmd = tc.cmd
			p.RepoRoot = repo

			if err := p.lintFix(context.Background(), "COD-634"); err != nil {
				t.Fatalf("lintFix err = %v, want nil (fails open)", err)
			}
			_, statErr := os.Stat(filepath.Join(repo, "marker"))
			if gotMarker := statErr == nil; gotMarker != tc.wantMarker {
				t.Errorf("marker present = %v, want %v", gotMarker, tc.wantMarker)
			}
			if runner.calls != tc.wantAgent {
				t.Errorf("agent calls = %d, want %d", runner.calls, tc.wantAgent)
			}
		})
	}
}
