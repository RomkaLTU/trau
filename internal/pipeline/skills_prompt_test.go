package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/prompts"
)

func TestSkillsPromptComposition(t *testing.T) {
	t.Run("no installed skills keeps the self-selection fallback", func(t *testing.T) {
		got := skillsPrompt(prompts.Renderer{}, nil, nil, nil)
		mustContain(t, "skillsPrompt(none)", got, "auto-select and load the project skills")
		mustNotContain(t, "skillsPrompt(none)", got, "Skill tool before implementing")
	})

	t.Run("resolved set is named and nothing is left to self-selection", func(t *testing.T) {
		got := skillsPrompt(prompts.Renderer{},
			[]string{"golang-code-style", "golang-error-handling", "goreleaser"},
			[]string{"golang-code-style", "golang-error-handling"},
			nil,
		)
		mustContain(t, "skillsPrompt(resolved)", got,
			"load these required skills with the Skill tool before implementing: golang-code-style, golang-error-handling",
		)
		mustNotContain(t, "skillsPrompt(resolved)", got,
			"remaining skills", "auto-select", "goreleaser",
		)
	})
}

func TestVerifySkillsPromptComposition(t *testing.T) {
	t.Run("empty set renders nothing", func(t *testing.T) {
		if got := verifySkillsPrompt(prompts.Renderer{}, nil, nil, nil); got != "" {
			t.Fatalf("verifySkillsPrompt(none) = %q, want empty", got)
		}
	})

	t.Run("resolved set is named and nothing is left to self-selection", func(t *testing.T) {
		got := verifySkillsPrompt(prompts.Renderer{},
			[]string{"golang-code-style", "tdd"},
			[]string{"tdd", "browser-harness"},
			nil,
		)
		mustContain(t, "verifySkillsPrompt(resolved)", got,
			"Load these required skills with the Skill tool before verifying: tdd, browser-harness",
		)
		mustNotContain(t, "verifySkillsPrompt(resolved)", got,
			"remaining skills", "golang-code-style",
		)
	})
}

// TestVerifyPromptCarriesSkillsNote pins the verify-prompt injection point: a
// rendered skills note lands in the prompt, an empty one leaves it untouched.
func TestVerifyPromptCarriesSkillsNote(t *testing.T) {
	note := verifySkillsPrompt(prompts.Renderer{}, []string{"tdd"}, []string{"tdd", "browser-harness"}, nil)
	got := verifyTail(prompts.Renderer{}, "COD-1", "", verifyPath("COD-1"), "", "", "", "", "", note, "", "", "", false)
	mustContain(t, "verifyTail(skills)", got, "Load these required skills with the Skill tool before verifying: tdd, browser-harness")

	empty := verifyTail(prompts.Renderer{}, "COD-1", "", verifyPath("COD-1"), "", "", "", "", "", "", "", "", "", false)
	mustNotContain(t, "verifyTail(no-skills)", empty, "before verifying")
}

// TestRepairPromptsReuseBuildSkillsNote pins the repair/bugfix injection: both
// carry the build-flavored skills note unchanged, while cleanup and lint_fix
// prompts stay skill-less.
func TestRepairPromptsReuseBuildSkillsNote(t *testing.T) {
	note := skillsPrompt(prompts.Renderer{}, []string{"golang-code-style"}, []string{"golang-code-style"}, nil)
	want := "load these required skills with the Skill tool before implementing: golang-code-style"

	repair := repairInstruction(prompts.Renderer{}, "COD-1", verifyPath("COD-1"), "", "feature/x", "boom", "", "", "", note, "", "", "")
	mustContain(t, "repairInstruction(skills)", repair, want)
	bugfix := bugfixInstruction(prompts.Renderer{}, "COD-1", verifyPath("COD-1"), "", "feature/x", "boom", "", "", "", note, "", "", "")
	mustContain(t, "bugfixInstruction(skills)", bugfix, want)

	cleanup := cleanupInstruction(prompts.Renderer{}, "COD-1", "")
	mustNotContain(t, "cleanupInstruction", cleanup, "Skill tool")
	lintfix := lintFixInstruction(prompts.Renderer{}, "COD-1")
	mustNotContain(t, "lintFixInstruction", lintfix, "Skill tool")
}

// TestSkillsPromptsHardenAgainstMangledNames is the COD-1502 guard: Claude Code's
// Skill tool matches exactly and suggests nothing, so a model that re-types a name
// with spaces runs the phase without the skill. Every instruct-mode sentence that
// names a set must therefore say the names are literal and tell the agent to retry
// on "Unknown skill"; a repo that names nothing keeps its self-selection wording.
func TestSkillsPromptsHardenAgainstMangledNames(t *testing.T) {
	const (
		exact = "Pass each name to the Skill tool exactly as written — the names contain no spaces"
		retry = `if a Skill call returns "Unknown skill", re-call it with the name copied exactly from this list`
	)

	build := skillsPrompt(prompts.Renderer{}, []string{"golang-pro"}, []string{"golang-pro"}, nil)
	mustContain(t, "skillsPrompt(resolved)", build, exact, retry)
	verify := verifySkillsPrompt(prompts.Renderer{}, []string{"tdd"}, []string{"tdd"}, []string{"browser-harness"})
	mustContain(t, "verifySkillsPrompt(resolved)", verify, exact, retry)

	none := skillsPrompt(prompts.Renderer{}, nil, nil, nil)
	mustNotContain(t, "skillsPrompt(none)", none, exact, retry)
}

// TestInjectModeCarriesNoSkillsSentence pins that the retry nudge rides the
// instruct-mode sentence alone: an inject-mode phase hands the agent SKILL.md
// content and never names the Skill tool, so it is unchanged by the hardening.
func TestInjectModeCarriesNoSkillsSentence(t *testing.T) {
	p := newTestPipeline(t, routedRunner{provider: "codex"}, &fakeTracker{})
	p.RepoRoot = repoWithSkill(t, "web-feature")
	p.SkillsMode = skillsModeInject

	got := p.resolveSkills(agent.SkillSet{Names: []string{"web-feature"}}, []string{"web-feature"}, agent.PhaseBuild)
	if got.note != "" {
		t.Fatalf("inject-mode note = %q, want empty", got.note)
	}
	mustNotContain(t, "inject-mode injection", got.injection, "Unknown skill", "exactly as written")
}

// failingVerdictRunner records every prompt and fails the first verify verdict,
// so one Verify drives the verify and repair prompts through the real phase code.
type failingVerdictRunner struct {
	path  string
	calls *promptLog
}

func (r *failingVerdictRunner) Run(_ context.Context, prompt, label string) (agent.Result, error) {
	r.calls.record(label, prompt)
	data, _ := json.Marshal(verdict{Pass: label != "verify", Summary: "boom", Failures: []string{"boom"}})
	_ = os.WriteFile(r.path, data, 0o644)
	return agent.Result{}, nil
}

func (r *failingVerdictRunner) Route(string) (string, string, string) { return "claude", "", "" }

// TestPhasePromptsAlwaysNameASkillSet is the never-empty guard: in a repo whose
// skills match no pin and no project-type recommendation, build, verify and
// repair still name an explicit set — the whole installed list — and verify with
// no test-token skill names the build set rather than falling silent.
func TestPhasePromptsAlwaysNameASkillSet(t *testing.T) {
	id := "COD-91132"
	writeHandoff(t, id)
	calls := &promptLog{}
	runner := &failingVerdictRunner{path: verifyPath(id), calls: calls}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.RepoRoot = repoWithSkill(t, "web-feature")
	p.MaxRepairs = 1

	if err := p.build(context.Background(), id, false); err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := p.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	want := map[string]string{
		"build":   "load these required skills with the Skill tool before implementing: web-feature",
		"verify":  "Load these required skills with the Skill tool before verifying: web-feature",
		"repair1": "load these required skills with the Skill tool before implementing: web-feature",
	}
	for _, c := range calls.all() {
		sentence, ok := want[c.label]
		if !ok {
			continue
		}
		if !strings.Contains(c.prompt, sentence) {
			t.Errorf("%s prompt does not name the resolved skill set: want %q", c.label, sentence)
		}
		if strings.Contains(c.prompt, "remaining skills") {
			t.Errorf("%s prompt still offers self-selection", c.label)
		}
		delete(want, c.label)
	}
	for label := range want {
		t.Errorf("%s phase never ran", label)
	}
}
