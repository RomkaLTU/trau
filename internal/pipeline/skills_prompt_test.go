package pipeline

import "testing"

func TestSkillsPromptComposition(t *testing.T) {
	t.Run("no skills keeps the self-selection note", func(t *testing.T) {
		got := skillsPrompt(nil, nil)
		mustContain(t, "skillsPrompt(none)", got, "auto-select and load the project skills")
		mustNotContain(t, "skillsPrompt(none)", got, "this repo has skills")
	})

	t.Run("installed only names the skills", func(t *testing.T) {
		got := skillsPrompt([]string{"golang-code-style", "golang-error-handling"}, nil)
		mustContain(t, "skillsPrompt(installed)", got,
			"this repo has skills: golang-code-style, golang-error-handling",
			"with the Skill tool before implementing",
		)
		mustNotContain(t, "skillsPrompt(installed)", got, "auto-select", "Load these required skills")
	})

	t.Run("required set names them explicitly", func(t *testing.T) {
		got := skillsPrompt(
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
		got := skillsPrompt(
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
		got := skillsPrompt([]string{"golang-code-style"}, []string{"nonexistent-skill"})
		mustContain(t, "skillsPrompt(all-missing)", got,
			"this repo has skills: golang-code-style",
			"Load the ones relevant to this ticket",
		)
		mustNotContain(t, "skillsPrompt(all-missing)", got, "Load these required skills", "nonexistent-skill")
	})
}
