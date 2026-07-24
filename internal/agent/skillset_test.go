package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/RomkaLTU/trau/internal/skillrules"
)

func skillRepo(t *testing.T, projectManifest string, names ...string) string {
	t.Helper()
	repo := t.TempDir()
	if projectManifest != "" {
		if err := os.WriteFile(filepath.Join(repo, projectManifest), []byte("module example.test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names {
		mkSkill(t, repo, ".claude/skills", name)
	}
	return repo
}

func writeRules(t *testing.T, repo string, rules ...skillrules.Rule) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repo, ".trau"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(skillrules.Set{Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, skillrules.File), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBuildSkillsChain walks the build resolution chain step by step: the
// configured pin first, then the project type's recommended set, then every
// installed skill — and never an empty set while the repo has skills.
func TestBuildSkillsChain(t *testing.T) {
	installed := []string{"golang-code-style", "golang-performance", "goreleaser"}

	cases := []struct {
		name       string
		manifest   string
		required   []string
		wantNames  []string
		wantSource string
	}{
		{
			name:       "pinned names win",
			manifest:   "go.mod",
			required:   []string{"goreleaser"},
			wantNames:  []string{"goreleaser"},
			wantSource: SkillsSourceRequired,
		},
		{
			name:       "uninstalled pins drop through to the recommended set",
			manifest:   "go.mod",
			required:   []string{"nonexistent-skill"},
			wantNames:  []string{"golang-code-style", "golang-performance"},
			wantSource: SkillsSourceRecommended,
		},
		{
			name:       "no pin and no recognized project type names everything installed",
			required:   nil,
			wantNames:  installed,
			wantSource: SkillsSourceInstalled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewSkillResolver(skillRepo(t, tc.manifest, installed...), tc.required, nil).Build(SkillContext{})
			if !slices.Equal(got.Names, tc.wantNames) {
				t.Errorf("Build().Names = %v, want %v", got.Names, tc.wantNames)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Build().Source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}

	t.Run("a repo with no skills resolves empty", func(t *testing.T) {
		if got := NewSkillResolver(t.TempDir(), []string{"golang-pro"}, nil).Build(SkillContext{}); len(got.Names) != 0 {
			t.Errorf("Build().Names = %v, want empty", got.Names)
		}
	})
}

// TestVerifySkillsChain covers the verify union and its fallback: the verify pin,
// the installed test-token skills and browser-harness join into one set, and an
// empty union hands over to the build set rather than leaving verify skill-less.
func TestVerifySkillsChain(t *testing.T) {
	t.Run("pin, test skills and the browser harness join", func(t *testing.T) {
		repo := skillRepo(t, "go.mod", "golang-code-style", "tdd", "web-feature")
		got := NewSkillResolver(repo, nil, []string{"web-feature"}).Verify(SkillContext{}, true)
		want := []string{"web-feature", "tdd", "browser-harness"}
		if !slices.Equal(got.Names, want) {
			t.Errorf("Verify(true).Names = %v, want %v", got.Names, want)
		}
		if got.Source != SkillsSourceVerifyPins {
			t.Errorf("Verify(true).Source = %q, want %q", got.Source, SkillsSourceVerifyPins)
		}
	})

	t.Run("no test skill and no pin falls back to the build set", func(t *testing.T) {
		repo := skillRepo(t, "go.mod", "golang-code-style", "goreleaser")
		got := NewSkillResolver(repo, []string{"goreleaser"}, nil).Verify(SkillContext{}, false)
		want := []string{"goreleaser"}
		if !slices.Equal(got.Names, want) {
			t.Errorf("Verify(false).Names = %v, want %v", got.Names, want)
		}
		if got.Source != SkillsSourceRequired {
			t.Errorf("Verify(false).Source = %q, want %q", got.Source, SkillsSourceRequired)
		}
	})

	t.Run("the browser harness is named even when the repo installs nothing", func(t *testing.T) {
		got := NewSkillResolver(t.TempDir(), nil, nil).Verify(SkillContext{}, true)
		if !slices.Equal(got.Names, []string{browserSkill}) {
			t.Errorf("Verify(true).Names = %v, want [%s]", got.Names, browserSkill)
		}
	})

	t.Run("a skill-less repo with no browser resolves empty", func(t *testing.T) {
		if got := NewSkillResolver(t.TempDir(), nil, nil).Verify(SkillContext{}, false); len(got.Names) != 0 {
			t.Errorf("Verify(false).Names = %v, want empty", got.Names)
		}
	})
}

// TestRoutingRulesResolution is the routing contract: a build matches the
// ticket's text, a verify and repair match the slice's diff, always-skills ride
// along everywhere, and manual skills stay out of every automatic set.
func TestRoutingRulesResolution(t *testing.T) {
	newRepo := func(t *testing.T) string {
		t.Helper()
		repo := skillRepo(t, "go.mod",
			"bubbletea", "github-release", "golang-code-style", "golang-pro", "web-feature")
		writeRules(t, repo,
			skillrules.Rule{Skill: "golang-code-style", Scope: skillrules.ScopeAlways},
			skillrules.Rule{Skill: "golang-pro", Scope: skillrules.ScopeAuto, Paths: []string{"**/*.go"}},
			skillrules.Rule{Skill: "web-feature", Scope: skillrules.ScopeAuto, Paths: []string{"web/**"}, Keywords: []string{"web ui"}},
			skillrules.Rule{Skill: "bubbletea", Scope: skillrules.ScopeAuto, Paths: []string{"internal/tui/**"}},
			skillrules.Rule{Skill: "github-release", Scope: skillrules.ScopeManual},
		)
		return repo
	}

	t.Run("a web-only ticket names the web skill and the always set", func(t *testing.T) {
		r := NewSkillResolver(newRepo(t), nil, nil)
		got := r.Build(SkillContext{Text: "Widen the sidebar in web/src/routes/skills.tsx"})
		want := []string{"golang-code-style", "web-feature"}
		if !slices.Equal(got.Names, want) {
			t.Fatalf("Build().Names = %v, want %v", got.Names, want)
		}
		if got.Source != SkillsSourceRules {
			t.Errorf("Build().Source = %q, want %q", got.Source, SkillsSourceRules)
		}
	})

	t.Run("a keyword hit matches a ticket that names no path", func(t *testing.T) {
		r := NewSkillResolver(newRepo(t), nil, nil)
		got := r.Build(SkillContext{Text: "Polish the web UI spacing"})
		if !slices.Contains(got.Names, "web-feature") {
			t.Errorf("Build().Names = %v, want it to include web-feature", got.Names)
		}
	})

	t.Run("a Go-only diff names the Go set and a web diff adds the web skill", func(t *testing.T) {
		r := NewSkillResolver(newRepo(t), nil, nil)
		goOnly := r.Verify(SkillContext{Changed: []string{"internal/agent/skills.go"}}, false)
		if want := []string{"golang-code-style", "golang-pro"}; !slices.Equal(goOnly.Names, want) {
			t.Fatalf("Verify(go-only).Names = %v, want %v", goOnly.Names, want)
		}
		withWeb := r.Verify(SkillContext{Changed: []string{"internal/agent/skills.go", "web/src/app.tsx"}}, false)
		if !slices.Contains(withWeb.Names, "web-feature") {
			t.Errorf("Verify(web diff).Names = %v, want it to include web-feature", withWeb.Names)
		}
	})

	t.Run("a phase after build ignores the ticket's keywords", func(t *testing.T) {
		r := NewSkillResolver(newRepo(t), nil, nil)
		sc := SkillContext{Text: "Polish the web UI spacing", Changed: []string{"internal/agent/skills.go"}}
		for phase, set := range map[string]SkillSet{"verify": r.Verify(sc, false), "repair": r.Repair(sc)} {
			if slices.Contains(set.Names, "web-feature") {
				t.Errorf("%s routed on the ticket text: %v", phase, set.Names)
			}
		}
	})

	t.Run("a diff that cannot be listed matches nothing but the always set", func(t *testing.T) {
		r := NewSkillResolver(newRepo(t), nil, nil)
		got := r.Verify(SkillContext{Text: "Widen the sidebar in web/src/routes/skills.tsx"}, false)
		if want := []string{"golang-code-style"}; !slices.Equal(got.Names, want) {
			t.Errorf("Verify(no diff).Names = %v, want %v", got.Names, want)
		}
	})

	t.Run("repair matches the diff the same way verify does", func(t *testing.T) {
		r := NewSkillResolver(newRepo(t), nil, nil)
		got := r.Repair(SkillContext{Changed: []string{"internal/tui/view.go"}})
		want := []string{"golang-code-style", "golang-pro", "bubbletea"}
		if !slices.Equal(got.Names, want) {
			t.Errorf("Repair().Names = %v, want %v", got.Names, want)
		}
	})

	t.Run("a manual skill never lands in an automatic set", func(t *testing.T) {
		r := NewSkillResolver(newRepo(t), nil, nil)
		sets := map[string]SkillSet{
			"build":  r.Build(SkillContext{Text: "cut a github release for v2"}),
			"verify": r.Verify(SkillContext{Changed: []string{".goreleaser.yaml"}}, false),
			"repair": r.Repair(SkillContext{Changed: []string{".goreleaser.yaml"}}),
		}
		for phase, set := range sets {
			if slices.Contains(set.Names, "github-release") {
				t.Errorf("%s named the manual skill: %v", phase, set.Names)
			}
		}
	})

	t.Run("a pin still names a manual skill", func(t *testing.T) {
		r := NewSkillResolver(newRepo(t), []string{"github-release"}, nil)
		got := r.Build(SkillContext{Text: "release chores"})
		if !slices.Contains(got.Names, "github-release") {
			t.Errorf("Build().Names = %v, want it to include the pinned github-release", got.Names)
		}
		if got.Source != SkillsSourceRules+" + "+SkillsSourceRequired {
			t.Errorf("Build().Source = %q, want the rules and the pin", got.Source)
		}
	})

	t.Run("rules resolving empty fall back to the chain", func(t *testing.T) {
		repo := skillRepo(t, "go.mod", "golang-code-style", "golang-performance", "goreleaser")
		writeRules(t, repo, skillrules.Rule{Skill: "goreleaser", Scope: skillrules.ScopeAuto, Paths: []string{"dist/**"}})
		got := NewSkillResolver(repo, nil, nil).Build(SkillContext{Text: "Rename a Go helper"})
		want := []string{"golang-code-style", "golang-performance"}
		if !slices.Equal(got.Names, want) || got.Source != SkillsSourceRecommended {
			t.Errorf("Build() = %v (%s), want %v (%s)", got.Names, got.Source, want, SkillsSourceRecommended)
		}
	})

	t.Run("a rule naming an uninstalled skill is reported and dropped", func(t *testing.T) {
		repo := skillRepo(t, "go.mod", "golang-code-style")
		writeRules(t, repo,
			skillrules.Rule{Skill: "golang-code-style", Scope: skillrules.ScopeAlways},
			skillrules.Rule{Skill: browserSkill, Scope: skillrules.ScopeAlways},
			skillrules.Rule{Skill: "typo-skill", Scope: skillrules.ScopeAlways},
		)
		r := NewSkillResolver(repo, nil, nil)
		if got := r.UnknownRuleSkills(); !slices.Equal(got, []string{"typo-skill"}) {
			t.Errorf("UnknownRuleSkills() = %v, want [typo-skill]", got)
		}
		got := r.Build(SkillContext{})
		if !slices.Equal(got.Names, []string{"golang-code-style", browserSkill}) {
			t.Errorf("Build().Names = %v, want the installed and known out-of-repo skills only", got.Names)
		}
	})

	t.Run("a malformed rules file is surfaced and falls back to the chain", func(t *testing.T) {
		repo := skillRepo(t, "go.mod", "golang-code-style", "golang-performance")
		if err := os.MkdirAll(filepath.Join(repo, ".trau"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, skillrules.File), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := NewSkillResolver(repo, nil, nil)
		if r.RulesError() == nil {
			t.Fatal("RulesError() = nil, want a parse error")
		}
		if got := r.Build(SkillContext{}); got.Source != SkillsSourceRecommended {
			t.Errorf("Build().Source = %q, want the chain to take over", got.Source)
		}
	})
}

// TestSkillSetOrigins is the per-entry provenance contract the plan preview
// reads: every resolved name carries the chain step that produced it, so a union
// set can say which of its halves each name came from.
func TestSkillSetOrigins(t *testing.T) {
	repo := skillRepo(t, "go.mod", "golang-code-style", "golang-pro", "goreleaser")
	writeRules(t, repo,
		skillrules.Rule{Skill: "golang-code-style", Scope: skillrules.ScopeAlways},
		skillrules.Rule{Skill: "golang-pro", Scope: skillrules.ScopeAuto, Paths: []string{"**/*.go"}},
	)

	t.Run("a union splits its names across the rules and the pins", func(t *testing.T) {
		r := NewSkillResolver(repo, []string{"goreleaser"}, nil)
		got := r.Build(SkillContext{Text: "Rework internal/agent/skills.go"})
		want := map[string]string{
			"golang-code-style": SkillsSourceRules,
			"golang-pro":        SkillsSourceRules,
			"goreleaser":        SkillsSourceRequired,
		}
		for name, source := range want {
			if got.Origins[name] != source {
				t.Errorf("origin of %s = %q, want %q", name, got.Origins[name], source)
			}
		}
	})

	t.Run("a fallback set attributes every name to the chain step", func(t *testing.T) {
		bare := skillRepo(t, "go.mod", "golang-code-style", "goreleaser")
		got := NewSkillResolver(bare, nil, nil).Build(SkillContext{})
		if got.Source == SkillsSourceRules {
			t.Fatalf("Source = %q, want a fallback step", got.Source)
		}
		for _, name := range got.Names {
			if got.Origins[name] != got.Source {
				t.Errorf("origin of %s = %q, want %q", name, got.Origins[name], got.Source)
			}
		}
	})
}
