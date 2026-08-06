package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/sanitize"
)

func TestResolveSkillsInjectMode(t *testing.T) {
	root := repoWithSkill(t, "golang-code-style")
	p := &Pipeline{RepoRoot: root, SkillsMode: skillsModeInject}
	set := agent.SkillSet{Names: []string{"golang-code-style"}, Source: agent.SkillsSourceRequired}

	ps := p.resolveSkills(set, []string{"golang-code-style"}, agent.PhaseBuild)
	if ps.note != "" {
		t.Errorf("inject mode should drop the Skill-tool note, got %q", ps.note)
	}
	if !strings.Contains(ps.injection, ".claude/skills/golang-code-style/SKILL.md") {
		t.Errorf("injection missing repo-relative path:\n%s", ps.injection)
	}
	if !strings.Contains(ps.injection, "# golang-code-style") {
		t.Errorf("injection missing SKILL.md content:\n%s", ps.injection)
	}
	if len(ps.activated) != 1 || ps.activated[0] != "golang-code-style" {
		t.Errorf("activated = %v, want [golang-code-style]", ps.activated)
	}

	full := injectInto(ps.injection, "RENDERED TEMPLATE BODY")
	if !strings.HasPrefix(full, ps.injection) || !strings.Contains(full, "RENDERED TEMPLATE BODY") {
		t.Error("injectInto should prepend the block ahead of the rendered template")
	}

	// The injected block carries the SKILL.md verbatim, so a literal control byte in
	// repo content lands straight on the prompt path.
	t.Run("a NUL in a SKILL.md body cannot poison the spawn", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, ".claude", "skills", "nul-fixture")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "# nul-fixture\n\nAssert the parser rejects \x00 input.\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}

		p := &Pipeline{RepoRoot: root, SkillsMode: skillsModeInject}
		set := agent.SkillSet{Names: []string{"nul-fixture"}, Source: agent.SkillsSourceRequired}
		ps := p.resolveSkills(set, []string{"nul-fixture"}, agent.PhaseBuild)

		composed := injectInto(ps.injection, "RENDERED TEMPLATE BODY")
		if !strings.ContainsRune(composed, 0) {
			t.Fatal("the fixture no longer reproduces the hazard — the SKILL.md byte never reached the prompt")
		}

		clean, changed := sanitize.PromptText(composed)
		if !changed {
			t.Error("the backend seam did not report the prompt as changed")
		}
		if strings.ContainsRune(clean, 0) {
			t.Error("the assembled prompt still holds a raw NUL; fork/exec would reject the argv")
		}
		if !strings.Contains(clean, sanitize.NULEscape) {
			t.Error("the scrubbed prompt hides that the source material held a NUL")
		}
		for _, want := range []string{"# nul-fixture", "Assert the parser rejects", "RENDERED TEMPLATE BODY"} {
			if !strings.Contains(clean, want) {
				t.Errorf("scrubbing lost prompt content: missing %q", want)
			}
		}
	})
}

func TestResolveSkillsInstructMode(t *testing.T) {
	root := repoWithSkill(t, "golang-code-style")
	p := &Pipeline{RepoRoot: root, SkillsMode: skillsModeInstruct}
	set := agent.SkillSet{Names: []string{"golang-code-style"}}

	ps := p.resolveSkills(set, []string{"golang-code-style"}, agent.PhaseBuild)
	if ps.injection != "" {
		t.Errorf("instruct mode should not inject, got %q", ps.injection)
	}
	if !strings.Contains(ps.note, "Skill tool") {
		t.Errorf("instruct note should name the Skill tool, got %q", ps.note)
	}
}

// TestSkillsModeDefaultIsProviderAware pins the unset-SKILLS_MODE default: claude
// keeps the Skill-tool sentence, and a phase routed to a provider without that
// tool gets the SKILL.md content inline. An explicit mode applies to every provider.
func TestSkillsModeDefaultIsProviderAware(t *testing.T) {
	root := repoWithSkill(t, "golang-code-style")
	installed := []string{"golang-code-style"}
	set := agent.SkillSet{Names: []string{"golang-code-style"}}

	cases := []struct {
		name       string
		provider   string
		configured string
		want       string
	}{
		{"claude keeps instruct", "claude", "", skillsModeInstruct},
		{"codex has no Skill tool to name a set at", "codex", "", skillsModeInject},
		{"kimi has no Skill tool to name a set at", "kimi", "", skillsModeInject},
		{"an explicit auto is the same as unset", "codex", "auto", skillsModeInject},
		{"an explicit instruct still reaches codex", "codex", skillsModeInstruct, skillsModeInstruct},
		{"an explicit inject still reaches claude", "claude", skillsModeInject, skillsModeInject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{RepoRoot: root, Runner: routedRunner{provider: tc.provider}, SkillsMode: tc.configured}
			ps := p.resolveSkills(set, installed, agent.PhaseBuild)
			if ps.mode != tc.want {
				t.Fatalf("mode = %q, want %q", ps.mode, tc.want)
			}
			if tc.want == skillsModeInject {
				if ps.note != "" || ps.injection == "" {
					t.Errorf("inject delivery should carry the block alone; note = %q, injection = %q", ps.note, ps.injection)
				}
				return
			}
			if ps.injection != "" || !strings.Contains(ps.note, "Skill tool") {
				t.Errorf("instruct delivery should carry the note alone; note = %q, injection = %q", ps.note, ps.injection)
			}
		})
	}

	t.Run("one panel member without the tool injects for the whole panel", func(t *testing.T) {
		p := &Pipeline{
			RepoRoot:    root,
			Runner:      routedRunner{provider: "claude"},
			VerifyPanel: []Verifier{{Name: "claude", Provider: "claude"}, {Name: "codex", Provider: "codex"}},
		}
		if got := p.resolveSkills(set, installed, agent.PhaseVerify).mode; got != skillsModeInject {
			t.Errorf("verify mode = %q, want %q", got, skillsModeInject)
		}
		if got := p.resolveSkills(set, installed, agent.PhaseBuild).mode; got != skillsModeInstruct {
			t.Errorf("build mode = %q, want %q — the panel only speaks for verify", got, skillsModeInstruct)
		}
	})
}

// phaseRoutedVerdictRunner fails the first verify and passes every retry while
// reporting a per-phase provider, so one run can span backends the way a ROUTES
// config does.
type phaseRoutedVerdictRunner struct {
	path      string
	calls     *promptLog
	providers map[string]string
}

func (r *phaseRoutedVerdictRunner) Run(_ context.Context, prompt, label string) (agent.Result, error) {
	r.calls.record(label, prompt)
	data, _ := json.Marshal(verdict{Pass: label != "verify", Summary: "boom", Failures: []string{"boom"}})
	_ = os.WriteFile(r.path, data, 0o644)
	return agent.Result{}, nil
}

func (r *phaseRoutedVerdictRunner) Route(label string) (string, string, string) {
	return r.providers[agent.RouteKey(label)], "", ""
}

// TestPhaseDeliveryFollowsItsOwnRoute drives a claude verify into a codex bugfix:
// each prompt carries the delivery its own provider can use, and the skills_planned
// receipts record the two effective modes separately.
func TestPhaseDeliveryFollowsItsOwnRoute(t *testing.T) {
	id := "COD-91191"
	writeHandoff(t, id)
	calls := &promptLog{}
	runner := &phaseRoutedVerdictRunner{path: verifyPath(id), calls: calls, providers: map[string]string{
		agent.PhaseVerify: "claude",
		agent.PhaseBugfix: "codex",
	}}
	var buf bytes.Buffer
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.Events = event.New(&buf)
	p.RepoRoot = repoWithSkill(t, "golang-code-style")
	p.MaxRepairs = 0
	p.MaxBugfixes = 1

	if err := p.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	prompts := map[string]string{}
	for _, c := range calls.all() {
		prompts[c.label] = c.prompt
	}
	mustContain(t, "verify prompt", prompts["verify"], "Load these required skills with the Skill tool before verifying: golang-code-style")
	mustContain(t, "bugfix1 prompt", prompts["bugfix1"], "===== SKILL: golang-code-style")
	mustNotContain(t, "bugfix1 prompt", prompts["bugfix1"], "Skill tool before implementing")

	modes := map[string]string{}
	for _, ev := range kindEvents(t, &buf, event.KindSkillsPlanned) {
		modes[ev.Phase] = strField(ev.Fields, "mode")
	}
	if modes["verify"] != skillsModeInstruct || modes["bugfix"] != skillsModeInject {
		t.Errorf("skills_planned modes = %v, want verify instruct and bugfix inject", modes)
	}
}

// recordingVerdictRunner passes verify on every call while recording the prompt
// each phase ran under, so a full Verify can be inspected end-to-end.
type recordingVerdictRunner struct {
	path  string
	calls *promptLog
}

func (r *recordingVerdictRunner) Run(_ context.Context, prompt, label string) (agent.Result, error) {
	if r.calls != nil {
		r.calls.record(label, prompt)
	}
	data, _ := json.Marshal(verdict{Pass: true, Summary: "ok"})
	_ = os.WriteFile(r.path, data, 0o644)
	return agent.Result{}, nil
}

func TestVerifyInjectModeDeliversSkillContent(t *testing.T) {
	id := "COD-1135"
	writeHandoff(t, id)
	root := repoWithSkill(t, "golang-code-style")
	calls := &promptLog{}
	runner := &recordingVerdictRunner{path: verifyPath(id), calls: calls}

	var buf bytes.Buffer
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.Events = event.New(&buf)
	p.SkillsExpected = func(string) bool { return true }
	p.RepoRoot = root
	p.SkillsMode = skillsModeInject

	if err := p.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	verifyPrompt := ""
	for _, c := range calls.all() {
		if strings.HasPrefix(c.label, "verify") {
			verifyPrompt = c.prompt
			break
		}
	}
	if verifyPrompt == "" {
		t.Fatal("no verify prompt captured")
	}
	if !strings.Contains(verifyPrompt, ".claude/skills/golang-code-style/SKILL.md") {
		t.Errorf("verify prompt missing skill path:\n%s", verifyPrompt)
	}
	if !strings.Contains(verifyPrompt, "# golang-code-style") {
		t.Error("verify prompt missing injected SKILL.md content")
	}
	if strings.Contains(verifyPrompt, "with the Skill tool") {
		t.Error("inject mode must drop the Skill-tool instruction sentence")
	}

	if evs := kindEvents(t, &buf, event.KindVerifyNoSkills); len(evs) != 0 {
		t.Errorf("inject mode must suppress verify_no_skills, got %d", len(evs))
	}
	planned := kindEvents(t, &buf, event.KindSkillsPlanned)
	if len(planned) == 0 {
		t.Fatal("expected an activated skills_planned event")
	}
	if got := strField(planned[0].Fields, "mode"); got != skillsModeInject {
		t.Errorf("activated event mode = %q, want %q", got, skillsModeInject)
	}
}
