package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInjectableSkills(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		dir := filepath.Join(root, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, SkillMetaFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("golang-code-style", "---\nname: golang-code-style\n---\nStyle body.")
	write("tdd", "TDD body.")

	got := LoadInjectableSkills(root, []string{"golang-code-style", "browser-harness", "tdd"})
	if len(got) != 2 {
		t.Fatalf("loaded %d skills, want 2 (out-of-repo browser-harness skipped)", len(got))
	}
	if got[0].Name != "golang-code-style" || got[1].Name != "tdd" {
		t.Fatalf("names/order = %q, %q", got[0].Name, got[1].Name)
	}
	if got[0].Path != ".claude/skills/golang-code-style/SKILL.md" {
		t.Errorf("path = %q, want repo-relative SKILL.md", got[0].Path)
	}
	if !strings.Contains(got[0].Body, "Style body.") {
		t.Errorf("body = %q, want full SKILL.md content", got[0].Body)
	}

	if LoadInjectableSkills("", []string{"tdd"}) != nil {
		t.Error("empty repoRoot should return nil")
	}
	if LoadInjectableSkills(root, nil) != nil {
		t.Error("no names should return nil")
	}
}
