package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseArgsReadsTheWorktreeFlag(t *testing.T) {
	opts, err := ParseArgs([]string{"COD-1580", "--worktree", "../wt-test"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.WorkTree != "../wt-test" {
		t.Errorf("WorkTree = %q, want ../wt-test", opts.WorkTree)
	}
	if opts.Parent != "COD-1580" {
		t.Errorf("Parent = %q, want COD-1580", opts.Parent)
	}
	if _, err := ParseArgs([]string{"--worktree"}); err == nil {
		t.Error("--worktree without a value was accepted")
	}
}

func TestResolveWorkTree(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "wt-test")
	if err := os.MkdirAll(filepath.Join(tree, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		flag    string
		want    string
		wantErr bool
	}{
		{name: "unset", flag: "", want: ""},
		{name: "blank", flag: "   ", want: ""},
		{name: "working tree", flag: tree, want: tree},
		{name: "trailing separator", flag: tree + string(filepath.Separator), want: tree},
		{name: "missing", flag: filepath.Join(dir, "nope"), wantErr: true},
		{name: "not a directory", flag: file, wantErr: true},
		{name: "not a working tree", flag: plain, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWorkTree(tt.flag)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveWorkTree(%q) = %q, want an error", tt.flag, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveWorkTree(%q): %v", tt.flag, err)
			}
			if got != tt.want {
				t.Fatalf("ResolveWorkTree(%q) = %q, want %q", tt.flag, got, tt.want)
			}
		})
	}
}

func TestResolveWorkTreeMakesARelativePathAbsolute(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "wt-rel", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := ResolveWorkTree(filepath.Join(".", "wt-rel"))
	if err != nil {
		t.Fatalf("ResolveWorkTree: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("ResolveWorkTree = %q, want an absolute path", got)
	}
	if filepath.Base(got) != "wt-rel" {
		t.Fatalf("ResolveWorkTree = %q, want it to end at wt-rel", got)
	}
}

// TestWorkRootAndResultDir pins the two answers the run entry points read: which
// tree the work happens in, and where the agent-interface result channel lives.
// Repo identity is never one of them.
func TestWorkRootAndResultDir(t *testing.T) {
	tests := []struct {
		name          string
		cfg           Config
		wantWorkRoot  string
		wantResultDir string
	}{
		{
			name:          "no worktree",
			cfg:           Config{RepoRoot: "/repo/shipflock", RunsDir: ".trau/runs"},
			wantWorkRoot:  "/repo/shipflock",
			wantResultDir: ".trau/runs",
		},
		{
			name:          "worktree",
			cfg:           Config{RepoRoot: "/repo/shipflock", WorkTree: "/tmp/wt-test", RunsDir: ".trau/runs"},
			wantWorkRoot:  "/tmp/wt-test",
			wantResultDir: filepath.Join("/tmp/wt-test", ".trau/runs"),
		},
		{
			name:          "worktree with an absolute runs dir",
			cfg:           Config{RepoRoot: "/repo/shipflock", WorkTree: "/tmp/wt-test", RunsDir: "/var/runs"},
			wantWorkRoot:  "/tmp/wt-test",
			wantResultDir: "/var/runs",
		},
		{
			name:          "worktree with no runs dir",
			cfg:           Config{RepoRoot: "/repo/shipflock", WorkTree: "/tmp/wt-test"},
			wantWorkRoot:  "/tmp/wt-test",
			wantResultDir: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.WorkRoot(); got != tt.wantWorkRoot {
				t.Errorf("WorkRoot() = %q, want %q", got, tt.wantWorkRoot)
			}
			if got := tt.cfg.ResultDir(); got != tt.wantResultDir {
				t.Errorf("ResultDir() = %q, want %q", got, tt.wantResultDir)
			}
			if tt.cfg.RepoRoot != "/repo/shipflock" {
				t.Errorf("RepoRoot = %q — the worktree must not move repo identity", tt.cfg.RepoRoot)
			}
		})
	}
}

// TestProjectLayerIgnoresTheWorktree is the config-layer half of the invariant:
// the project layer keeps coming from the registered root's .trau.ini, never
// from a file that happens to sit in the worktree.
func TestProjectLayerIgnoresTheWorktree(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ProjectConfigName), []byte("BASE_BRANCH=registered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ProjectConfigName), []byte("BASE_BRANCH=worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered(ProjectConfigPath(root), "", "", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.RepoRoot = root
	cfg.WorkTree = work
	if cfg.BaseBranch != "registered" {
		t.Fatalf("BASE_BRANCH = %q, want registered — the project layer must stay on the registered root", cfg.BaseBranch)
	}
	if cfg.WorkRoot() != work {
		t.Fatalf("WorkRoot() = %q, want %q", cfg.WorkRoot(), work)
	}
}
