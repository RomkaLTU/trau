package pipeline

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/prompts"
)

// lintFix runs the project's automated lint/format fixers over the working tree
// just before verify, so verify isn't spent self-healing mechanical style noise.
// It fails open — a failing fixer or non-fatal agent error leaves the real issues
// for verify; only a context cancellation, provider pause, or budget give-up
// propagates.
func (p *Pipeline) lintFix(ctx context.Context, id string) error {
	if !p.LintFix || p.skipping(config.SkipLintFix) {
		return nil
	}
	p.setActivity(id, activity.LintFix, "")
	if p.FolderRepo {
		return p.folderLintFix(ctx, id)
	}
	if cmd := strings.TrimSpace(p.sliceLintFixCmd(ctx)); cmd != "" {
		return p.lintFixCmd(ctx, p.workRoot(), "lint-fix", cmd)
	}
	return p.lintFixAgent(ctx, id)
}

func (p *Pipeline) lintFixCmd(ctx context.Context, dir, label, cmd string) error {
	out, err := p.runDirCmd(ctx, dir, label, cmd)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.logf("  ⚠ lint-fix command exited non-zero (continuing to verify): %v", err)
		for _, line := range tailLines(string(out), 3) {
			p.logf("      %s", line)
		}
		return nil
	}
	p.logf("  ✓ lint-fix applied")
	return nil
}

func (p *Pipeline) lintFixAgent(ctx context.Context, id string) error {
	p.logf("  ↳ lint-fix: detecting and running the project's autofixers")
	_, err := p.agentStep(ctx, id, "lintfix", lintFixInstruction(p.prompts, id))
	if err != nil && isFatalAgentErr(err) {
		return err
	}
	if err != nil {
		p.logf("  lint-fix agent error (continuing to verify): %v", err)
	}
	return nil
}

func lintFixInstruction(r prompts.Renderer, id string) string {
	return r.Render("lint_fix", prompts.LintFixData{ID: id})
}

// runRepoCmd runs one of the repo's own shell commands from the tree the run
// works in, echoing it under label so the run log shows what was invoked before
// its output.
func (p *Pipeline) runRepoCmd(ctx context.Context, label, cmd string) ([]byte, error) {
	return p.runDirCmd(ctx, p.workRoot(), label, cmd)
}

// runDirCmd runs cmd from dir — the repo root, or one Child repo of a Folder
// repo, which is the workspace its own checks and knobs belong to.
func (p *Pipeline) runDirCmd(ctx context.Context, dir, label, cmd string) ([]byte, error) {
	p.logf("  ↳ %s: %s", label, cmd)
	c := shellCommand(ctx, cmd)
	if dir != "" {
		c.Dir = dir
	}
	return c.CombinedOutput()
}

// shellCommand hands cmd to the host's shell: sh on unix, and on Windows the
// interpreter COMSPEC names — cmd.exe unless the environment points elsewhere —
// since stock Windows ships no sh.
func shellCommand(ctx context.Context, cmd string) *exec.Cmd {
	if runtime.GOOS != "windows" {
		return exec.CommandContext(ctx, "sh", "-c", cmd)
	}
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	return exec.CommandContext(ctx, shell, "/c", cmd)
}

func tailLines(s string, n int) []string {
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
