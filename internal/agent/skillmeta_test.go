package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeSkillManifest(t *testing.T, repo, name, manifest string) {
	t.Helper()
	dir := filepath.Join(repo, ".claude/skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if manifest == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, SkillMetaFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestInstalledSkillsMetadata: the inventory reads each skill's declared name and
// description, and a directory whose SKILL.md is missing or has no readable
// frontmatter reads as invalid rather than as a healthy install.
func TestInstalledSkillsMetadata(t *testing.T) {
	repo := t.TempDir()
	writeSkillManifest(t, repo, "clean", "---\nname: clean\ndescription: \"Use when writing Go code.\"\n---\n\n# Clean\n")
	writeSkillManifest(t, repo, "folded", "---\nname: folded\ndescription: >-\n  Build a web UI feature\n  end to end.\nlicense: MIT\n---\n")
	writeSkillManifest(t, repo, "broken-yaml", "---\nname: broken-yaml\ndescription: Use when: things happen\n\ttab: bad\n---\n")
	writeSkillManifest(t, repo, "no-frontmatter", "# just a heading\n")
	writeSkillManifest(t, repo, "no-manifest", "")

	got := InstalledSkills(repo)
	byName := make(map[string]SkillMeta, len(got))
	for _, m := range got {
		byName[m.Name] = m
	}

	if len(got) != 5 {
		t.Fatalf("InstalledSkills() returned %d skills, want 5", len(got))
	}
	if m := byName["clean"]; m.DeclaredName != "clean" || m.Description != "Use when writing Go code." || m.Invalid {
		t.Errorf("clean = %+v, want the declared name and description", m)
	}
	if m := byName["folded"]; m.Description != "Build a web UI feature end to end." || m.Invalid {
		t.Errorf("folded = %+v, want the folded description collapsed onto one line", m)
	}
	if m := byName["broken-yaml"]; m.Invalid || m.DeclaredName != "broken-yaml" {
		t.Errorf("broken-yaml = %+v, want the line scan to recover the name", m)
	}
	for _, name := range []string{"no-frontmatter", "no-manifest"} {
		if m := byName[name]; !m.Invalid {
			t.Errorf("%s = %+v, want it flagged invalid", name, m)
		}
	}
}

func TestSuggestedKeywords(t *testing.T) {
	got := SuggestedKeywords("Use this when building a web UI feature with React components and routing.")
	want := []string{"building", "web", "feature", "react", "components", "routing"}
	if !slices.Equal(got, want) {
		t.Errorf("SuggestedKeywords() = %v, want %v", got, want)
	}
	if got := SuggestedKeywords(""); got != nil {
		t.Errorf("SuggestedKeywords(\"\") = %v, want nil", got)
	}
}
