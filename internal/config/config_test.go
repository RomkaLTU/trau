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
