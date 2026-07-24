package skillrules

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMatchPath(t *testing.T) {
	cases := []struct {
		glob string
		path string
		want bool
	}{
		{"web/**", "web/src/routes/skills.tsx", true},
		{"web/**", "internal/web/handler.go", false},
		{"**/*.go", "internal/agent/skills.go", true},
		{"**/*.go", "main.go", true},
		{"**/*.go", "web/src/app.tsx", false},
		{"internal/tui/**", "internal/tui/view.go", true},
		{"internal/tui/**", "internal/tuier/view.go", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/adr/0021.md", false},
		{"docs/adr/*.md", "docs/adr/0021.md", true},
		{"", "main.go", false},
	}

	for _, tc := range cases {
		if got := MatchPath(tc.glob, tc.path); got != tc.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", tc.glob, tc.path, got, tc.want)
		}
	}
}

func TestResolveScopes(t *testing.T) {
	set := Set{Rules: []Rule{
		{Skill: "style", Scope: ScopeAlways},
		{Skill: "verify-only", Scope: ScopeAlways, Phases: []string{PhaseVerify}},
		{Skill: "go", Scope: ScopeAuto, Paths: []string{"**/*.go"}},
		{Skill: "web", Scope: ScopeAuto, Keywords: []string{"web ui"}},
		{Skill: "release", Scope: ScopeManual, Paths: []string{"**/*.go"}},
		{Skill: "half-written", Scope: ScopeAuto},
		{Skill: "typo-scope", Scope: "sometimes", Paths: []string{"**/*.go"}},
	}}

	cases := []struct {
		name  string
		match Match
		want  []string
	}{
		{
			name:  "an always rule rides along with nothing else matching",
			match: Match{Phase: PhaseBuild},
			want:  []string{"style"},
		},
		{
			name:  "a phase-scoped always rule only applies to its phase",
			match: Match{Phase: PhaseVerify},
			want:  []string{"style", "verify-only"},
		},
		{
			name:  "a path glob hits and an unrecognized scope reads as auto",
			match: Match{Phase: PhaseBuild, Paths: []string{"internal/agent/skills.go"}},
			want:  []string{"style", "go", "typo-scope"},
		},
		{
			name:  "a keyword hits on a word boundary",
			match: Match{Phase: PhaseBuild, Text: "polish the Web UI spacing"},
			want:  []string{"style", "web"},
		},
		{
			name:  "a keyword inside a longer word does not hit",
			match: Match{Phase: PhaseBuild, Text: "the webbing uid"},
			want:  []string{"style"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := set.Resolve(tc.match); !slices.Equal(got, tc.want) {
				t.Errorf("Resolve() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("a manual rule never resolves", func(t *testing.T) {
		got := set.Resolve(Match{Phase: PhaseRepair, Paths: []string{"main.go"}})
		if slices.Contains(got, "release") {
			t.Errorf("Resolve() = %v, want the manual skill left out", got)
		}
	})
}

func TestPathsInText(t *testing.T) {
	got := PathsInText("Rework web/src/routes/skills.tsx and everything under internal/tui/** — see COD-1133")
	want := []string{"web/src/routes/skills.tsx", "internal/tui/**"}
	if !slices.Equal(got, want) {
		t.Errorf("PathsInText() = %v, want %v", got, want)
	}
	if got := PathsInText(""); got != nil {
		t.Errorf("PathsInText(\"\") = %v, want nil", got)
	}
}

func TestLoadAndSave(t *testing.T) {
	t.Run("a repo with no rules file reads empty", func(t *testing.T) {
		got, err := Load(t.TempDir())
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if len(got.Rules) != 0 {
			t.Errorf("Load() = %v, want no rules", got.Rules)
		}
	})

	t.Run("a malformed rules file errors", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".trau"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, File), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(repo); err == nil {
			t.Error("Load() error = nil, want a parse error")
		}
	})

	t.Run("a saved set round-trips", func(t *testing.T) {
		repo := t.TempDir()
		want := Set{Rules: []Rule{
			{Skill: "web-feature", Scope: ScopeAuto, Phases: []string{PhaseBuild}, Paths: []string{"web/**"}},
			{Skill: "github-release", Scope: ScopeManual},
		}}
		if err := Save(repo, want); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
		got, err := Load(repo)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if len(got.Rules) != 2 || got.Rules[0].Skill != "web-feature" || got.Rules[0].Paths[0] != "web/**" {
			t.Errorf("Load() = %+v, want the saved rules", got.Rules)
		}
		if got := got.Skills(); !slices.Equal(got, []string{"web-feature", "github-release"}) {
			t.Errorf("Skills() = %v, want both rule skills", got)
		}
	})
}
