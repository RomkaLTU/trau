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

// TestLoadWorktreeKeys covers the WORKTREES catalog: the four keys load, the
// dotenv copy default is what a repo gets without saying anything, and every one
// reports its own effective value back to the settings surface.
func TestLoadWorktreeKeys(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	body := "WORKTREES=1\nWORKTREES_DIR=/srv/trees\nWORKTREE_SETUP_CMD=npm ci\nWORKTREE_COPY=.env,config/*.local\n"
	if err := os.WriteFile(local, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Worktrees {
		t.Error("Worktrees = false, want it on")
	}
	if cfg.WorktreesRoot() != "/srv/trees" {
		t.Errorf("WorktreesRoot() = %q, want /srv/trees", cfg.WorktreesRoot())
	}
	if cfg.WorktreeSetupCmd != "npm ci" {
		t.Errorf("WorktreeSetupCmd = %q", cfg.WorktreeSetupCmd)
	}
	if got := cfg.WorktreeCopyGlobs(); len(got) != 2 || got[0] != ".env" || got[1] != "config/*.local" {
		t.Errorf("WorktreeCopyGlobs() = %v, want the two configured globs", got)
	}
	for key, want := range map[string]string{
		"WORKTREES":          "1",
		"WORKTREES_DIR":      "/srv/trees",
		"WORKTREE_SETUP_CMD": "npm ci",
		"WORKTREE_COPY":      ".env,config/*.local",
	} {
		if got := keyValue(cfg, key); got != want {
			t.Errorf("keyValue(%s) = %q, want %q", key, got, want)
		}
	}
}

// TestWorktreeDefaults pins the off-by-default stance and the two derived
// defaults: the dotenv globs, and a worktrees root under TRAU_HOME.
func TestWorktreeDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)

	cfg, err := LoadLayered("", "", filepath.Join(t.TempDir(), "missing.ini"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Worktrees {
		t.Error("Worktrees defaults on — it must be opt-in")
	}
	if want := filepath.Join(home, "worktrees"); cfg.WorktreesRoot() != want {
		t.Errorf("WorktreesRoot() = %q, want %q", cfg.WorktreesRoot(), want)
	}
	if cfg.WorktreeCopy != DefaultWorktreeCopy {
		t.Errorf("WorktreeCopy = %q, want %q", cfg.WorktreeCopy, DefaultWorktreeCopy)
	}
}

// TestWorktreeLanes covers WORKTREE_PARALLEL end to end: it loads and reports
// itself like every other key, it caps concurrency only where worktrees isolate
// the trees, and anything below one lane reads as the serial drain.
func TestWorktreeLanes(t *testing.T) {
	if got := Defaults().WorktreeParallel; got != DefaultWorktreeParallel {
		t.Fatalf("default WorktreeParallel = %d, want %d", got, DefaultWorktreeParallel)
	}
	if got := Defaults().WorktreeLanes(); got != 1 {
		t.Errorf("WorktreeLanes() without worktrees = %d, want 1", got)
	}

	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	if err := os.WriteFile(local, []byte("WORKTREES=1\nWORKTREE_PARALLEL=6\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorktreeParallel != 6 {
		t.Errorf("WorktreeParallel = %d, want 6", cfg.WorktreeParallel)
	}
	if got := cfg.WorktreeLanes(); got != 6 {
		t.Errorf("WorktreeLanes() = %d, want 6", got)
	}
	if got := keyValue(cfg, "WORKTREE_PARALLEL"); got != "6" {
		t.Errorf("keyValue(WORKTREE_PARALLEL) = %q, want 6", got)
	}

	serial := filepath.Join(t.TempDir(), "trau.ini")
	if err := os.WriteFile(serial, []byte("WORKTREE_PARALLEL=6\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	shared, err := LoadLayered("", "", serial, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := shared.WorktreeLanes(); got != 1 {
		t.Errorf("WorktreeLanes() without WORKTREES = %d, want 1 — a shared checkout runs one at a time", got)
	}

	zero := filepath.Join(t.TempDir(), "trau.ini")
	if err := os.WriteFile(zero, []byte("WORKTREES=1\nWORKTREE_PARALLEL=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	off, err := LoadLayered("", "", zero, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := off.WorktreeLanes(); got != 1 {
		t.Errorf("WorktreeLanes() for 0 = %d, want 1", got)
	}

	var meta KeyMeta
	for _, m := range KnownKeys() {
		if m.Key == "WORKTREE_PARALLEL" {
			meta = m
		}
	}
	if meta.Key == "" {
		t.Fatal("WORKTREE_PARALLEL missing from the settings catalog")
	}
	if meta.Group != sectionWorktrees || meta.Kind != "int" || !meta.WebEditable || meta.Default != "4" {
		t.Errorf("WORKTREE_PARALLEL meta = %+v, want a web-editable int in the worktrees section defaulting to 4", meta)
	}
}

// TestWorktreeCopyCanBeSwitchedOff: the default is non-empty, so an empty value
// has to reach the config or "copy nothing else" would be unreachable.
func TestWorktreeCopyCanBeSwitchedOff(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	if err := os.WriteFile(local, []byte("WORKTREE_COPY=\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.WorktreeCopyGlobs(); len(got) != 0 {
		t.Errorf("WorktreeCopyGlobs() = %v, want none", got)
	}
}

// TestResolveRunWorkTreeAcceptsATreeNotYetProvisioned: with WORKTREES=1 the flag
// names the tree trau is about to create, so a path that is not there yet resolves
// to empty rather than failing the run before it starts. Without the key the flag
// keeps its operator meaning and a missing tree is still an error.
func TestResolveRunWorkTreeAcceptsATreeNotYetProvisioned(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "worktrees", "shipflock", "COD-1581")

	got, err := ResolveRunWorkTree(missing, true)
	if err != nil {
		t.Fatalf("ResolveRunWorkTree with worktrees on: %v", err)
	}
	if got != "" {
		t.Errorf("ResolveRunWorkTree = %q, want empty until the run provisions it", got)
	}
	if _, err := ResolveRunWorkTree(missing, false); err == nil {
		t.Error("a missing tree was accepted with worktrees off")
	}

	// A tree already standing there resolves as usual, so a resume works in it
	// from the first git command.
	if err := os.MkdirAll(filepath.Join(missing, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveRunWorkTree(missing, true); err != nil || got != missing {
		t.Errorf("ResolveRunWorkTree on an existing tree = (%q, %v), want %q", got, err, missing)
	}
}
