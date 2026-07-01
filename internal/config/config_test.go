package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfigItemsLayerPrecedence(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	project := filepath.Join(dir, ".trau.ini")
	user := filepath.Join(dir, "user.ini")

	if err := os.WriteFile(local, []byte("BASE_BRANCH=local-main\nMAX_ITERATIONS=5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte("BASE_BRANCH=project-main\nPROVIDER=codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("BASE_BRANCH=user-main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered(project, user, local, "")
	if err != nil {
		t.Fatal(err)
	}

	items, err := ResolveConfigItems(cfg, local, project, user, "", Options{})
	if err != nil {
		t.Fatal(err)
	}

	byKey := map[string]ConfigItem{}
	for _, it := range items {
		byKey[it.Key] = it
	}

	if got := byKey["BASE_BRANCH"]; got.Layer != LayerUser || got.Value != "user-main" {
		t.Fatalf("BASE_BRANCH: want user/user-main, got %s/%s", got.Layer, got.Value)
	}
	if got := byKey["PROVIDER"]; got.Layer != LayerProject || got.Value != "codex" {
		t.Fatalf("PROVIDER: want project/codex, got %s/%s", got.Layer, got.Value)
	}
	if got := byKey["MAX_ITERATIONS"]; got.Layer != LayerLocal || got.Value != "5" {
		t.Fatalf("MAX_ITERATIONS: want local/5, got %s/%s", got.Layer, got.Value)
	}
}

func TestResolveConfigItemsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	project := filepath.Join(dir, ".trau.ini")
	user := filepath.Join(dir, "user.ini")

	if err := os.WriteFile(project, []byte("LINEAR_TEAM=project-team\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINEAR_TEAM", "env-team")

	cfg, err := LoadLayered(project, user, local, "")
	if err != nil {
		t.Fatal(err)
	}

	items, err := ResolveConfigItems(cfg, local, project, user, "", Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, it := range items {
		if it.Key == "LINEAR_TEAM" {
			if it.Layer != LayerEnv || it.Value != "env-team" {
				t.Fatalf("LINEAR_TEAM: want env/env-team, got %s/%s", it.Layer, it.Value)
			}
			return
		}
	}
	t.Fatal("LINEAR_TEAM not found in resolved items")
}

func TestResolveConfigItemsCLIOverride(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	project := filepath.Join(dir, ".trau.ini")
	user := filepath.Join(dir, "user.ini")

	if err := os.WriteFile(project, []byte("PROVIDER=codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered(project, user, local, "kimi")
	if err != nil {
		t.Fatal(err)
	}

	items, err := ResolveConfigItems(cfg, local, project, user, "kimi", Options{Provider: "kimi"})
	if err != nil {
		t.Fatal(err)
	}

	for _, it := range items {
		if it.Key == "PROVIDER" {
			if it.Layer != LayerCLI || it.Value != "kimi" {
				t.Fatalf("PROVIDER: want CLI/kimi, got %s/%s", it.Layer, it.Value)
			}
			return
		}
	}
	t.Fatal("PROVIDER not found in resolved items")
}

func TestWriteConfigLayerUpdatesFile(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	project := filepath.Join(dir, ".trau.ini")
	user := filepath.Join(dir, "user.ini")

	if err := os.WriteFile(project, []byte("BASE_BRANCH=main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteConfigLayer("project", local, project, user, "BASE_BRANCH", "develop"); err != nil {
		t.Fatal(err)
	}

	got, err := ParseEnvFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if got["BASE_BRANCH"] != "develop" {
		t.Fatalf("BASE_BRANCH: want develop, got %s", got["BASE_BRANCH"])
	}

	if err := WriteConfigLayer("user", local, project, user, "LINEAR_API_KEY", "secret"); err != nil {
		t.Fatal(err)
	}
	got, err = ParseEnvFile(user)
	if err != nil {
		t.Fatal(err)
	}
	if got["LINEAR_API_KEY"] != "secret" {
		t.Fatalf("LINEAR_API_KEY: want secret, got %s", got["LINEAR_API_KEY"])
	}
}

func TestWriteConfigLayerPreservesComments(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.ini")

	original := "# machine config\nLINEAR_API_KEY=old\n# provider flags\nCLAUDE_FLAGS=--foo\n"
	if err := os.WriteFile(user, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteConfigLayer("user", "", "", user, "LINEAR_API_KEY", "new"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !contains(content, "# machine config") {
		t.Fatal("comment block was dropped")
	}
	if !contains(content, "LINEAR_API_KEY=new") {
		t.Fatal("updated value not found")
	}
	if !contains(content, "CLAUDE_FLAGS=--foo") {
		t.Fatal("untouched key was dropped")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestKimiPhaseRouteReadsProviderFile(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	provider := filepath.Join(dir, "kimi.ini")

	if err := os.WriteFile(provider, []byte("KIMI_BUILD_MODEL=kimi-build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("PROVIDER=kimi\nKIMI_CONFIG=kimi.ini\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routes == nil || cfg.Routes["build"] != "kimi:kimi-build" {
		t.Fatalf("build route: want kimi:kimi-build, got %s", cfg.Routes["build"])
	}
}

// TestSeedsCheapDefaultRoutes is the COD-641/COD-643 guard: with no phase keys
// set, a Claude run seeds cleanup/sizejudge/commit/handoff onto sonnet and lintfix
// onto haiku instead of inheriting the Opus default, build/verify stay unseeded
// (Opus), and an explicit per-phase key still wins.
func TestSeedsCheapDefaultRoutes(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")

	if err := os.WriteFile(local, []byte("PROVIDER=claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	for phase, want := range map[string]string{
		"cleanup":   "claude:sonnet",
		"sizejudge": "claude:sonnet",
		"commit":    "claude:sonnet",
		"handoff":   "claude:sonnet",
		"lintfix":   "claude:haiku",
	} {
		if got := cfg.Routes[phase]; got != want {
			t.Errorf("seeded Routes[%q] = %q, want %q", phase, got, want)
		}
	}

	// build/verify are deliberately left unseeded so they keep the Opus default.
	for _, phase := range []string{"build", "verify"} {
		if got := cfg.Routes[phase]; got != "" {
			t.Errorf("unseeded Routes[%q] = %q, want empty (inherits Opus default)", phase, got)
		}
	}

	// An explicit per-phase model overrides the seed.
	if err := os.WriteFile(local, []byte("PROVIDER=claude\nCLAUDE_COMMIT_MODEL=opus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, model, _ := parseRouteSpec(cfg.Routes["commit"]); model != "opus" {
		t.Errorf("explicit CLAUDE_COMMIT_MODEL: commit model = %q, want opus (route %q)", model, cfg.Routes["commit"])
	}

	// The seed is Claude-only: a non-claude provider leaves the phases unseeded.
	if err := os.WriteFile(local, []byte("PROVIDER=codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"commit", "handoff", "cleanup", "lintfix", "sizejudge"} {
		if got := cfg.Routes[phase]; got != "" {
			t.Errorf("codex Routes[%q] = %q, want empty (Claude tiers must not leak)", phase, got)
		}
	}
}

// TestRecoveryDefaults pins the COD-583 transient-recovery defaults: stall
// detection and retry are on out of the box, provider fallback is opt-in.
func TestRecoveryDefaults(t *testing.T) {
	c := Defaults()
	if c.AgentStallWindow != 180 {
		t.Errorf("AgentStallWindow default = %d, want 180", c.AgentStallWindow)
	}
	if c.AgentRetries != 2 {
		t.Errorf("AgentRetries default = %d, want 2", c.AgentRetries)
	}
	if c.AgentBackoff != 10 {
		t.Errorf("AgentBackoff default = %d, want 10", c.AgentBackoff)
	}
	if len(c.FallbackProviders) != 0 {
		t.Errorf("FallbackProviders default = %v, want empty (retry-only)", c.FallbackProviders)
	}
}

// TestRecoveryConfigParsing checks the new keys parse, including the
// comma-separated, whitespace-tolerant FALLBACK_PROVIDERS chain.
func TestRecoveryConfigParsing(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	content := "AGENT_STALL_WINDOW=90\nAGENT_RETRIES=3\nAGENT_BACKOFF=5\nFALLBACK_PROVIDERS=codex, kimi\n"
	if err := os.WriteFile(local, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.AgentStallWindow != 90 {
		t.Errorf("AgentStallWindow = %d, want 90", cfg.AgentStallWindow)
	}
	if cfg.AgentRetries != 3 {
		t.Errorf("AgentRetries = %d, want 3", cfg.AgentRetries)
	}
	if cfg.AgentBackoff != 5 {
		t.Errorf("AgentBackoff = %d, want 5", cfg.AgentBackoff)
	}
	if got := cfg.FallbackProviders; len(got) != 2 || got[0] != "codex" || got[1] != "kimi" {
		t.Errorf("FallbackProviders = %v, want [codex kimi]", got)
	}
}
