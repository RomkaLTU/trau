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

// TestDetectProjectTypeWorkspaces: a monorepo root whose own manifest names no
// framework is classified by its workspace manifests — pnpm-workspace.yaml or the
// package.json workspaces field — with the most specific framework winning.
func TestDetectProjectTypeWorkspaces(t *testing.T) {
	write := func(t *testing.T, root, rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("pnpm workspaces surface next", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", `{"name":"mono","private":true,"devDependencies":{"turbo":"^2"}}`)
		write(t, root, "pnpm-workspace.yaml", "packages:\n  - \"apps/*\"\n  - \"packages/ui\"\n")
		write(t, root, "apps/web/package.json", `{"name":"web","dependencies":{"next":"15.0.0"}}`)
		write(t, root, "packages/ui/package.json", `{"name":"ui","dependencies":{"react":"19.0.0"}}`)
		if got := DetectProjectType(root); got != "nextjs" {
			t.Errorf("DetectProjectType = %q, want %q", got, "nextjs")
		}
	})

	t.Run("package.json workspaces surface react", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", `{"name":"mono","private":true,"workspaces":["packages/*"]}`)
		write(t, root, "packages/app/package.json", `{"name":"app","dependencies":{"react":"19.0.0"}}`)
		if got := DetectProjectType(root); got != "react" {
			t.Errorf("DetectProjectType = %q, want %q", got, "react")
		}
	})

	t.Run("workspaces object form", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", `{"name":"mono","private":true,"workspaces":{"packages":["apps/*"]}}`)
		write(t, root, "apps/site/package.json", `{"name":"site","dependencies":{"next":"15.0.0"}}`)
		if got := DetectProjectType(root); got != "nextjs" {
			t.Errorf("DetectProjectType = %q, want %q", got, "nextjs")
		}
	})

	t.Run("plain node repo stays node", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", `{"name":"svc","dependencies":{"express":"^4"}}`)
		if got := DetectProjectType(root); got != "node" {
			t.Errorf("DetectProjectType = %q, want %q", got, "node")
		}
	})
}

// TestWorkspaceAppURL: the APP_URLS entry whose workspace holds the slice's
// changed files wins, keyed by package name, directory path, or base name;
// unmatched or ambiguous slices yield "" so the caller keeps its fallback.
func TestWorkspaceAppURL(t *testing.T) {
	write := func(t *testing.T, root, rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	monorepo := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		write(t, root, "package.json", `{"name":"mono","private":true,"workspaces":["apps/*"]}`)
		write(t, root, "apps/web/package.json", `{"name":"@acme/web"}`)
		write(t, root, "apps/api/package.json", `{"name":"@acme/api"}`)
		return root
	}

	urls := map[string]string{"web": "http://localhost:3000", "apps/api": "http://localhost:3001"}

	t.Run("directory base name", func(t *testing.T) {
		got := WorkspaceAppURL(monorepo(t), urls, []string{"apps/web/src/page.tsx"})
		if got != "http://localhost:3000" {
			t.Errorf("WorkspaceAppURL = %q, want %q", got, "http://localhost:3000")
		}
	})

	t.Run("relative directory path", func(t *testing.T) {
		got := WorkspaceAppURL(monorepo(t), urls, []string{"apps/api/routes.ts"})
		if got != "http://localhost:3001" {
			t.Errorf("WorkspaceAppURL = %q, want %q", got, "http://localhost:3001")
		}
	})

	t.Run("manifest package name", func(t *testing.T) {
		byName := map[string]string{"@acme/api": "http://localhost:3001"}
		got := WorkspaceAppURL(monorepo(t), byName, []string{"apps/api/routes.ts"})
		if got != "http://localhost:3001" {
			t.Errorf("WorkspaceAppURL = %q, want %q", got, "http://localhost:3001")
		}
	})

	t.Run("dominant workspace wins", func(t *testing.T) {
		changed := []string{"apps/web/a.tsx", "apps/web/b.tsx", "apps/api/routes.ts"}
		got := WorkspaceAppURL(monorepo(t), urls, changed)
		if got != "http://localhost:3000" {
			t.Errorf("WorkspaceAppURL = %q, want %q", got, "http://localhost:3000")
		}
	})

	t.Run("tied workspaces keep the fallback", func(t *testing.T) {
		changed := []string{"apps/web/a.tsx", "apps/api/routes.ts"}
		if got := WorkspaceAppURL(monorepo(t), urls, changed); got != "" {
			t.Errorf("WorkspaceAppURL = %q, want empty", got)
		}
	})

	t.Run("root-only changes match nothing", func(t *testing.T) {
		if got := WorkspaceAppURL(monorepo(t), urls, []string{"README.md"}); got != "" {
			t.Errorf("WorkspaceAppURL = %q, want empty", got)
		}
	})

	t.Run("no urls configured", func(t *testing.T) {
		if got := WorkspaceAppURL(monorepo(t), nil, []string{"apps/web/a.tsx"}); got != "" {
			t.Errorf("WorkspaceAppURL = %q, want empty", got)
		}
	})
}

// TestOwningWorkspaceDir: the workspace holding the plurality of the slice's
// changed files wins; no match or a tie yields "" so the caller keeps its
// repo-root fallback.
func TestOwningWorkspaceDir(t *testing.T) {
	write := func(t *testing.T, root, rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	monorepo := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		write(t, root, "package.json", `{"name":"mono","private":true,"workspaces":["apps/*"]}`)
		write(t, root, "apps/web/package.json", `{"name":"@acme/web"}`)
		write(t, root, "apps/api/package.json", `{"name":"@acme/api"}`)
		return root
	}

	t.Run("single workspace owns the diff", func(t *testing.T) {
		root := monorepo(t)
		got := OwningWorkspaceDir(root, []string{"apps/web/src/page.tsx"})
		if want := filepath.Join(root, "apps/web"); got != want {
			t.Errorf("OwningWorkspaceDir = %q, want %q", got, want)
		}
	})

	t.Run("dominant workspace wins", func(t *testing.T) {
		root := monorepo(t)
		changed := []string{"apps/web/a.tsx", "apps/web/b.tsx", "apps/api/routes.ts"}
		got := OwningWorkspaceDir(root, changed)
		if want := filepath.Join(root, "apps/web"); got != want {
			t.Errorf("OwningWorkspaceDir = %q, want %q", got, want)
		}
	})

	t.Run("tied workspaces yield empty", func(t *testing.T) {
		root := monorepo(t)
		changed := []string{"apps/web/a.tsx", "apps/api/routes.ts"}
		if got := OwningWorkspaceDir(root, changed); got != "" {
			t.Errorf("OwningWorkspaceDir = %q, want empty", got)
		}
	})

	t.Run("root-only changes match nothing", func(t *testing.T) {
		root := monorepo(t)
		if got := OwningWorkspaceDir(root, []string{"README.md"}); got != "" {
			t.Errorf("OwningWorkspaceDir = %q, want empty", got)
		}
	})

	t.Run("no changed files", func(t *testing.T) {
		if got := OwningWorkspaceDir(monorepo(t), nil); got != "" {
			t.Errorf("OwningWorkspaceDir = %q, want empty", got)
		}
	})

	t.Run("no repo root", func(t *testing.T) {
		if got := OwningWorkspaceDir("", []string{"apps/web/a.tsx"}); got != "" {
			t.Errorf("OwningWorkspaceDir = %q, want empty", got)
		}
	})
}
