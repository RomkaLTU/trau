package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
)

func TestResolveSkillsInjectMode(t *testing.T) {
	root := repoWithSkill(t, "golang-code-style")
	p := &Pipeline{RepoRoot: root, SkillsMode: skillsModeInject}
	set := agent.SkillSet{Names: []string{"golang-code-style"}, Source: agent.SkillsSourceRequired}

	ps := p.resolveSkills(set, []string{"golang-code-style"}, false)
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
}

func TestResolveSkillsInstructMode(t *testing.T) {
	root := repoWithSkill(t, "golang-code-style")
	p := &Pipeline{RepoRoot: root, SkillsMode: skillsModeInstruct}
	set := agent.SkillSet{Names: []string{"golang-code-style"}}

	ps := p.resolveSkills(set, []string{"golang-code-style"}, false)
	if ps.injection != "" {
		t.Errorf("instruct mode should not inject, got %q", ps.injection)
	}
	if !strings.Contains(ps.note, "Skill tool") {
		t.Errorf("instruct note should name the Skill tool, got %q", ps.note)
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
