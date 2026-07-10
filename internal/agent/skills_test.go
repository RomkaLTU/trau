package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSkillInstallArgs(t *testing.T) {
	cases := []struct {
		name string
		pkg  string
		want []string
	}{
		{
			name: "package with skill selector",
			pkg:  "samber/cc-skills-golang@golang-code-style",
			want: []string{"-y", "skills", "add", "samber/cc-skills-golang", "-s", "golang-code-style", "-y"},
		},
		{
			name: "bare repository package",
			pkg:  "vercel-labs/agent-skills",
			want: []string{"-y", "skills", "add", "vercel-labs/agent-skills", "-y"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := skillInstallArgs(SkillRecommendation{Package: tc.pkg})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("skillInstallArgs(%q) = %v, want %v", tc.pkg, got, tc.want)
			}
		})
	}
}

func TestInstalledSkillNames(t *testing.T) {
	repo := t.TempDir()
	mkSkill(t, repo, ".agents/skills", "golang-code-style")
	mkSkill(t, repo, ".agents/skills", "goreleaser")
	if err := os.WriteFile(filepath.Join(repo, ".agents/skills", "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	claudeDir := filepath.Join(repo, ".claude/skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, ".agents/skills/golang-code-style"), filepath.Join(claudeDir, "golang-code-style")); err != nil {
		t.Fatal(err)
	}

	got := InstalledSkillNames(repo)
	want := []string{"golang-code-style", "goreleaser"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InstalledSkillNames = %v, want %v", got, want)
	}

	if names := InstalledSkillNames(""); names != nil {
		t.Fatalf("empty repoRoot = %v, want nil", names)
	}
}

func TestMissingRequiredSkills(t *testing.T) {
	repo := t.TempDir()
	mkSkill(t, repo, ".agents/skills", "golang-code-style")

	got := MissingRequiredSkills(repo, []string{"golang-code-style", "missing-one"})
	want := []string{"missing-one"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingRequiredSkills = %v, want %v", got, want)
	}

	if m := MissingRequiredSkills(repo, nil); m != nil {
		t.Fatalf("no required = %v, want nil", m)
	}
}

func mkSkill(t *testing.T, repo, dir, name string) {
	t.Helper()
	path := filepath.Join(repo, dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("# "+name), 0o644); err != nil {
		t.Fatal(err)
	}
}
