package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/skillrules"
	"github.com/RomkaLTU/trau/internal/state"
)

// routedRunner is a fakeRunner that reports a fixed provider for every phase, so
// the phase-provider gating can be exercised without a real backend.
type routedRunner struct {
	fakeRunner
	provider string
}

func (r routedRunner) Route(string) (string, string, string) { return r.provider, "", "" }

const menuLine = "Other installed skills are available — load any that genuinely help: tdd."

func TestSkillsMenuGating(t *testing.T) {
	root := repoWithRules(t, []string{"golang-code-style", "tdd"})
	installed := []string{"golang-code-style", "tdd"}
	set := agent.SkillSet{Names: []string{"golang-code-style"}}

	cases := []struct {
		name     string
		provider string
		mode     string
		menu     bool
		want     bool
	}{
		{"claude in instruct mode with the flag on offers the menu", "claude", skillsModeInstruct, true, true},
		{"the flag off leaves the imperative core alone", "claude", skillsModeInstruct, false, false},
		{"codex has no Skill tool to take the offer", "codex", skillsModeInstruct, true, false},
		{"kimi has no Skill tool to take the offer", "kimi", skillsModeInstruct, true, false},
		{"inject mode drops the note entirely", "claude", skillsModeInject, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{
				RepoRoot:   root,
				Runner:     routedRunner{provider: tc.provider},
				SkillsMode: tc.mode,
				SkillsMenu: tc.menu,
			}
			ps := p.resolveSkills(set, installed, agent.PhaseBuild)
			if got := strings.Contains(ps.note, menuLine); got != tc.want {
				t.Errorf("menu rendered = %v, want %v; note = %q", got, tc.want, ps.note)
			}
		})
	}

	t.Run("the verify note carries the menu behind its own core sentence", func(t *testing.T) {
		p := &Pipeline{
			RepoRoot:   root,
			Runner:     routedRunner{provider: "claude"},
			SkillsMode: skillsModeInstruct,
			SkillsMenu: true,
		}
		ps := p.resolveSkills(set, installed, agent.PhaseVerify)
		mustContain(t, "resolveSkills(verify)", ps.note,
			"Load these required skills with the Skill tool before verifying: golang-code-style",
			menuLine,
		)
	})

	panels := []struct {
		name  string
		panel []Verifier
		want  bool
	}{
		{"an all-claude panel still takes the offer", []Verifier{{Name: "claude", Provider: "claude"}, {Name: "claude2", Provider: "claude"}}, true},
		{"a member without a Skill tool drops the menu for the whole panel", []Verifier{{Name: "claude", Provider: "claude"}, {Name: "codex", Provider: "codex"}, {Name: "kimi", Provider: "kimi"}}, false},
	}
	for _, tc := range panels {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{
				RepoRoot:    root,
				Runner:      routedRunner{provider: "claude"},
				SkillsMode:  skillsModeInstruct,
				SkillsMenu:  true,
				VerifyPanel: tc.panel,
			}
			ps := p.resolveSkills(set, installed, agent.PhaseVerify)
			if got := strings.Contains(ps.note, menuLine); got != tc.want {
				t.Errorf("menu rendered = %v, want %v; note = %q", got, tc.want, ps.note)
			}
			if build := p.resolveSkills(set, installed, agent.PhaseBuild); !strings.Contains(build.note, menuLine) {
				t.Errorf("build note lost the menu to the verify panel; note = %q", build.note)
			}
		})
	}
}

// TestVerifyPanelMembersNeverSeeTheMenu pins the panel leak shut end to end: the
// panel members share the one verify note, so a member without a Skill tool must
// keep the menu out of every member's prompt — including the claude member's.
func TestVerifyPanelMembersNeverSeeTheMenu(t *testing.T) {
	const id = "COD-79705"
	calls := &promptLog{}
	panel := []Verifier{
		{Name: "claude", Provider: "claude", Runner: fakeRunner{calls: calls}},
		{Name: "codex", Provider: "codex", Runner: fakeRunner{calls: calls}},
		{Name: "kimi", Provider: "kimi", Runner: fakeRunner{calls: calls}},
	}
	t.Cleanup(func() { removeVerifyArtifacts(t, id, panel) })
	dir := t.TempDir()
	p := &Pipeline{
		State:       state.NewStore(dir),
		RunsDir:     dir,
		RepoRoot:    repoWithRules(t, []string{"golang-code-style", "tdd"}),
		Runner:      routedRunner{provider: "claude"},
		SkillsMode:  skillsModeInstruct,
		SkillsMenu:  true,
		VerifyPanel: panel,
		PanelPolicy: "unanimous",
	}

	delivery := p.resolveSkills(agent.SkillSet{Names: []string{"golang-code-style"}}, []string{"golang-code-style", "tdd"}, agent.PhaseVerify)
	if _, err := p.verifyAttempt(context.Background(), id, "verify", "brief", "", "", "", "", "", delivery.note, delivery.injection, ""); err != nil {
		t.Fatalf("verifyAttempt: %v", err)
	}

	prompts := calls.all()
	if len(prompts) != len(panel) {
		t.Fatalf("recorded %d member prompts, want %d", len(prompts), len(panel))
	}
	for _, c := range prompts {
		if strings.Contains(c.prompt, menuLine) {
			t.Errorf("%s prompt carries the menu: %q", c.label, c.prompt)
		}
	}
}

// TestBuildPromptOffersTheSkillsMenu pins the wiring end to end: with the flag on
// and a claude-routed build, the rendered prompt keeps naming the resolved set and
// adds the installed skills that set left out.
func TestBuildPromptOffersTheSkillsMenu(t *testing.T) {
	calls := &promptLog{}
	p := newTestPipeline(t, routedRunner{fakeRunner: fakeRunner{calls: calls}, provider: "claude"}, &fakeTracker{})
	p.RepoRoot = repoWithRules(t, []string{"golang-code-style", "web-feature"},
		skillrules.Rule{Skill: "golang-code-style", Scope: skillrules.ScopeAlways},
		skillrules.Rule{Skill: "web-feature", Scope: skillrules.ScopeManual},
	)
	p.SkillsMenu = true

	if err := p.Build(context.Background(), "COD-1190"); err != nil {
		t.Fatalf("Build: %v", err)
	}

	build := ""
	for _, c := range calls.all() {
		if c.label == "build" {
			build = c.prompt
			break
		}
	}
	if build == "" {
		t.Fatal("no build prompt captured")
	}
	mustContain(t, "build prompt", build,
		"load these required skills with the Skill tool before implementing: golang-code-style",
		"Other installed skills are available — load any that genuinely help: web-feature.",
	)
}
