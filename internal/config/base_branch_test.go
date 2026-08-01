package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepoWithOriginHEAD makes a real repo whose origin/HEAD points at branch —
// the ref BASE_BRANCH detection reads. The checked-out branch is deliberately
// something else, so a detection that fell back to the local HEAD would show.
// An empty branch leaves origin/HEAD unset.
func gitRepoWithOriginHEAD(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "work")
	if branch != "" {
		git("remote", "add", "origin", "https://example.com/repo.git")
		git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch)
	}
	return dir
}

func TestLoadDetectsBaseBranchFromOriginHEAD(t *testing.T) {
	project := ProjectConfigPath(gitRepoWithOriginHEAD(t, "master"))

	cfg, sources, err := LoadLayeredWithSources(project, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseBranch != "master" {
		t.Fatalf("BaseBranch = %q, want master", cfg.BaseBranch)
	}
	if src, ok := sources["BASE_BRANCH"]; ok {
		t.Fatalf("BASE_BRANCH sourced from %q, want no layer", src)
	}

	items, err := ResolveConfigItems(cfg, "", project, "", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Key != "BASE_BRANCH" {
			continue
		}
		if it.Value != "master" || it.Layer != LayerDefault {
			t.Fatalf("BASE_BRANCH item = %s/%s, want %s/master", it.Layer, it.Value, LayerDefault)
		}
	}
}

func TestLoadConfiguredBaseBranchSkipsDetection(t *testing.T) {
	cases := []struct {
		name  string
		layer string
	}{
		{"project file", "project"},
		{"local file", "local"},
		{"user file", "user"},
		{"env var", "env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := gitRepoWithOriginHEAD(t, "master")
			paths := map[string]string{
				"project": ProjectConfigPath(repo),
				"local":   filepath.Join(repo, LocalConfigName),
				"user":    filepath.Join(t.TempDir(), ProjectConfigName),
			}
			switch tc.layer {
			case "env":
				t.Setenv("BASE_BRANCH", "develop")
			default:
				if err := os.WriteFile(paths[tc.layer], []byte("BASE_BRANCH=develop\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			cfg, err := LoadLayered(paths["project"], paths["user"], paths["local"], "")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.BaseBranch != "develop" {
				t.Fatalf("BaseBranch = %q, want develop", cfg.BaseBranch)
			}
		})
	}
}

func TestLoadBaseBranchFallsBackToMain(t *testing.T) {
	cases := map[string]string{
		"non-git directory":     t.TempDir(),
		"repo without a remote": gitRepoWithOriginHEAD(t, ""),
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadLayered(ProjectConfigPath(dir), "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.BaseBranch != "main" {
				t.Fatalf("BaseBranch = %q, want main", cfg.BaseBranch)
			}
		})
	}
}
