package pipeline

import (
	"context"
	"os/exec"
	"strings"

	"github.com/RomkaLTU/trau/internal/activity"
)

// lintFix runs the project's automated lint/format fixers over the working tree
// just before verify, so verify isn't spent self-healing mechanical style noise.
// It fails open — a failing fixer or non-fatal agent error leaves the real issues
// for verify; only a context cancellation, provider pause, or budget give-up
// propagates.
func (p *Pipeline) lintFix(ctx context.Context, id string) error {
	if !p.LintFix {
		return nil
	}
	p.setActivity(id, activity.LintFix, "")
	if strings.TrimSpace(p.LintFixCmd) != "" {
		return p.lintFixCmd(ctx)
	}
	return p.lintFixAgent(ctx, id)
}

func (p *Pipeline) lintFixCmd(ctx context.Context) error {
	p.logf("  ↳ lint-fix: %s", p.LintFixCmd)
	c := exec.CommandContext(ctx, "sh", "-c", p.LintFixCmd)
	if p.RepoRoot != "" {
		c.Dir = p.RepoRoot
	}
	out, err := c.CombinedOutput()
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
	_, err := p.agentStep(ctx, id, "lintfix", lintFixInstruction(id))
	if err != nil && isFatalAgentErr(err) {
		return err
	}
	if err != nil {
		p.logf("  lint-fix agent error (continuing to verify): %v", err)
	}
	return nil
}

func lintFixInstruction(id string) string {
	return "Before the QA verify step for " + id + ", auto-fix mechanical lint and formatting issues in this repository (already checked out) so verify isn't spent on style noise. " +
		"Detect the project's OWN automated fixers from its config — package.json/composer.json scripts (lint:fix, format, pint, php-cs-fixer, eslint --fix, prettier --write), a Makefile target (fmt, lint-fix), a pre-commit config, or the language's standard formatter (gofmt/goimports, ruff --fix, rubocop -a) — and run only those, in autofix mode, over the working tree. Prefer scoping the run to the files changed on this branch. " +
		"Apply the fixes and leave them uncommitted on disk. Do NOT change program logic, do NOT hand-fix anything the tools cannot auto-correct (leave that for verify), and do NOT run the test suite, commit, push, open a PR, or touch the issue tracker. If the project has no automated fixer, make no changes and stop."
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
