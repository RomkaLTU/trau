package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
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
			got := NewSkillResolver(skillRepo(t, tc.manifest, installed...), tc.required, nil).Build()
			if !slices.Equal(got.Names, tc.wantNames) {
				t.Errorf("Build().Names = %v, want %v", got.Names, tc.wantNames)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Build().Source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}

	t.Run("a repo with no skills resolves empty", func(t *testing.T) {
		if got := NewSkillResolver(t.TempDir(), []string{"golang-pro"}, nil).Build(); len(got.Names) != 0 {
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
		got := NewSkillResolver(repo, nil, []string{"web-feature"}).Verify(true)
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
		got := NewSkillResolver(repo, []string{"goreleaser"}, nil).Verify(false)
		want := []string{"goreleaser"}
		if !slices.Equal(got.Names, want) {
			t.Errorf("Verify(false).Names = %v, want %v", got.Names, want)
		}
		if got.Source != SkillsSourceRequired {
			t.Errorf("Verify(false).Source = %q, want %q", got.Source, SkillsSourceRequired)
		}
	})

	t.Run("the browser harness is named even when the repo installs nothing", func(t *testing.T) {
		got := NewSkillResolver(t.TempDir(), nil, nil).Verify(true)
		if !slices.Equal(got.Names, []string{browserSkill}) {
			t.Errorf("Verify(true).Names = %v, want [%s]", got.Names, browserSkill)
		}
	})

	t.Run("a skill-less repo with no browser resolves empty", func(t *testing.T) {
		if got := NewSkillResolver(t.TempDir(), nil, nil).Verify(false); len(got.Names) != 0 {
			t.Errorf("Verify(false).Names = %v, want empty", got.Names)
		}
	})
}
