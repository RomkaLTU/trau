package pipeline

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/prompts"
)

func TestSkillsPromptComposition(t *testing.T) {
	t.Run("no skills keeps the self-selection note", func(t *testing.T) {
		got := skillsPrompt(prompts.Renderer{}, nil, nil)
		mustContain(t, "skillsPrompt(none)", got, "auto-select and load the project skills")
		mustNotContain(t, "skillsPrompt(none)", got, "this repo has skills")
	})

	t.Run("installed only names the skills", func(t *testing.T) {
		got := skillsPrompt(prompts.Renderer{}, []string{"golang-code-style", "golang-error-handling"}, nil)
		mustContain(t, "skillsPrompt(installed)", got,
			"this repo has skills: golang-code-style, golang-error-handling",
			"with the Skill tool before implementing",
		)
		mustNotContain(t, "skillsPrompt(installed)", got, "auto-select", "Load these required skills")
	})

	t.Run("required set names them explicitly", func(t *testing.T) {
		got := skillsPrompt(prompts.Renderer{},
			[]string{"golang-code-style", "golang-error-handling", "goreleaser"},
			[]string{"golang-code-style", "golang-error-handling"},
		)
		mustContain(t, "skillsPrompt(required)", got,
			"this repo has skills: golang-code-style, golang-error-handling, goreleaser",
			"Load these required skills with the Skill tool before implementing: golang-code-style, golang-error-handling",
			"then load any of the remaining skills",
		)
	})

	t.Run("required naming a missing skill drops it and stays installed-only", func(t *testing.T) {
		got := skillsPrompt(prompts.Renderer{},
			[]string{"golang-code-style"},
			[]string{"golang-code-style", "nonexistent-skill"},
		)
		mustContain(t, "skillsPrompt(required+missing)", got,
			"this repo has skills: golang-code-style",
			"Load these required skills with the Skill tool before implementing: golang-code-style",
		)
		mustNotContain(t, "skillsPrompt(required+missing)", got, "nonexistent-skill")
	})

	t.Run("all required missing falls back to self-selection among installed", func(t *testing.T) {
		got := skillsPrompt(prompts.Renderer{}, []string{"golang-code-style"}, []string{"nonexistent-skill"})
		mustContain(t, "skillsPrompt(all-missing)", got,
			"this repo has skills: golang-code-style",
			"Load the ones relevant to this ticket",
		)
		mustNotContain(t, "skillsPrompt(all-missing)", got, "Load these required skills", "nonexistent-skill")
	})
}

func TestVerifySkillsPromptComposition(t *testing.T) {
	t.Run("no skills and no browser stays empty", func(t *testing.T) {
		if got := verifySkillsPrompt(prompts.Renderer{}, nil, false); got != "" {
			t.Fatalf("verifySkillsPrompt(none) = %q, want empty", got)
		}
	})

	t.Run("installed only names the skills", func(t *testing.T) {
		got := verifySkillsPrompt(prompts.Renderer{}, []string{"golang-code-style", "web-feature"}, false)
		mustContain(t, "verifySkillsPrompt(installed)", got,
			"This repo has skills: golang-code-style, web-feature",
			"with the Skill tool before verifying",
		)
		mustNotContain(t, "verifySkillsPrompt(installed)", got, "browser-harness", "Load these required skills")
	})

	t.Run("test skill is auto-required from installed names", func(t *testing.T) {
		got := verifySkillsPrompt(prompts.Renderer{}, []string{"golang-code-style", "tdd"}, false)
		mustContain(t, "verifySkillsPrompt(test-skill)", got,
			"This repo has skills: golang-code-style, tdd",
			"Load these required skills with the Skill tool before verifying: tdd",
		)
		mustNotContain(t, "verifySkillsPrompt(test-skill)", got, "browser-harness")
	})

	t.Run("browser verify adds browser-harness", func(t *testing.T) {
		got := verifySkillsPrompt(prompts.Renderer{}, []string{"golang-code-style", "tdd"}, true)
		mustContain(t, "verifySkillsPrompt(browser)", got,
			"Load these required skills with the Skill tool before verifying: tdd, browser-harness",
		)
	})

	t.Run("browser verify without repo skills still requires the harness", func(t *testing.T) {
		got := verifySkillsPrompt(prompts.Renderer{}, nil, true)
		mustContain(t, "verifySkillsPrompt(browser-only)", got,
			"Load these required skills with the Skill tool before verifying: browser-harness",
		)
		mustNotContain(t, "verifySkillsPrompt(browser-only)", got, "This repo has skills")
	})
}

// TestVerifyPromptCarriesSkillsNote pins the verify-prompt injection point: a
// rendered skills note lands in the prompt, an empty one leaves it untouched.
func TestVerifyPromptCarriesSkillsNote(t *testing.T) {
	note := verifySkillsPrompt(prompts.Renderer{}, []string{"tdd"}, true)
	got := verifyTail(prompts.Renderer{}, "COD-1", "", verifyPath("COD-1"), "", "", "", "", "", note, "")
	mustContain(t, "verifyTail(skills)", got, "Load these required skills with the Skill tool before verifying: tdd, browser-harness")

	empty := verifyTail(prompts.Renderer{}, "COD-1", "", verifyPath("COD-1"), "", "", "", "", "", "", "")
	mustNotContain(t, "verifyTail(no-skills)", empty, "before verifying")
}

// TestRepairPromptsReuseBuildSkillsNote pins the repair/bugfix injection: both
// carry the build-flavored skills note unchanged, while cleanup and lint_fix
// prompts stay skill-less.
func TestRepairPromptsReuseBuildSkillsNote(t *testing.T) {
	note := skillsPrompt(prompts.Renderer{}, []string{"golang-code-style"}, []string{"golang-code-style"})
	want := "Load these required skills with the Skill tool before implementing: golang-code-style"

	repair := repairInstruction(prompts.Renderer{}, "COD-1", verifyPath("COD-1"), "", "feature/x", "boom", "", "", "", note, "")
	mustContain(t, "repairInstruction(skills)", repair, want)
	bugfix := bugfixInstruction(prompts.Renderer{}, "COD-1", verifyPath("COD-1"), "", "feature/x", "boom", "", "", "", note, "")
	mustContain(t, "bugfixInstruction(skills)", bugfix, want)

	cleanup := cleanupInstruction(prompts.Renderer{}, "COD-1", "")
	mustNotContain(t, "cleanupInstruction", cleanup, "Skill tool")
	lintfix := lintFixInstruction(prompts.Renderer{}, "COD-1")
	mustNotContain(t, "lintFixInstruction", lintfix, "Skill tool")
}
