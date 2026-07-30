package config

import (
	"os"
	"path/filepath"
	"testing"
)

// legacyRouting is what a Claude config with CLAUDE_MODEL=sonnet resolved to before
// per-phase defaults were fixed: the provider default everywhere except the four
// seeded phases, of which only lintfix sat below it.
var legacyRouting = map[string]string{
	"build":   "claude:sonnet:",
	"verify":  "claude:sonnet:",
	"repair":  "claude:sonnet:",
	"bugfix":  "claude:sonnet:",
	"pick":    "claude:sonnet:",
	"cleanup": "claude:sonnet:",
	"commit":  "claude:sonnet:",
	"handoff": "claude:sonnet:",
	"lintfix": "claude:haiku:",
}

func TestMigratePhaseRoutesPreservesTheOldCascade(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	user := filepath.Join(dir, ".trau.ini")
	if err := os.WriteFile(user, []byte("# my settings\nPROVIDER=claude\nCLAUDE_MODEL=sonnet\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MigratePhaseRoutes("", user, "", home); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered("", user, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for phase, want := range legacyRouting {
		if got := cfg.effectiveRoute(phase); got != want {
			t.Errorf("after migration %s routes to %q, want %q", phase, got, want)
		}
	}

	keys, err := ParseEnvFile(user)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"CLAUDE_BUILD_MODEL", "CLAUDE_VERIFY_MODEL", "CLAUDE_REPAIR_MODEL", "CLAUDE_BUGFIX_MODEL"} {
		if keys[key] != "sonnet" {
			t.Errorf("%s = %q, want sonnet pinned in the file that carried CLAUDE_MODEL", key, keys[key])
		}
	}
	// Phases already sitting on the value the fixed matrix ships are left alone.
	for _, key := range []string{"CLAUDE_PICK_MODEL", "CLAUDE_CLEANUP_MODEL", "CLAUDE_LINTFIX_MODEL"} {
		if _, ok := keys[key]; ok {
			t.Errorf("%s was written, want it left to the built-in default", key)
		}
	}

	before, err := os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigratePhaseRoutes("", user, "", home); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("second run rewrote the file:\n%s\nwant:\n%s", after, before)
	}
}

// TestMigratePhaseRoutesLetsAResetStick is the point of the completion marker: the
// provider default stays in the file after the pass, so a phase the user resets back
// to its fixed default must not be pinned again on the next startup.
func TestMigratePhaseRoutesLetsAResetStick(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	user := filepath.Join(dir, ".trau.ini")
	if err := os.WriteFile(user, []byte("PROVIDER=claude\nCLAUDE_MODEL=sonnet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MigratePhaseRoutes("", user, "", home); err != nil {
		t.Fatal(err)
	}
	if err := DeleteEnvKey(user, "CLAUDE_BUILD_MODEL"); err != nil {
		t.Fatal(err)
	}

	if err := MigratePhaseRoutes("", user, "", home); err != nil {
		t.Fatal(err)
	}

	keys, err := ParseEnvFile(user)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := keys["CLAUDE_BUILD_MODEL"]; ok {
		t.Errorf("CLAUDE_BUILD_MODEL came back as %q, want the reset to stick", got)
	}
	cfg, err := LoadLayered("", user, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.effectiveRoute("build"); got != "claude:opus:" {
		t.Errorf("build routes to %q after the reset, want the fixed default claude:opus:", got)
	}
}

func TestMigratePhaseRoutesLeavesExplicitAndDefaultConfigsAlone(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, ".trau.ini")

	body := "PROVIDER=claude\nCLAUDE_MODEL=sonnet\nCLAUDE_BUILD_MODEL=opus\n"
	if err := os.WriteFile(project, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MigratePhaseRoutes(project, "", "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	keys, err := ParseEnvFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if keys["CLAUDE_BUILD_MODEL"] != "opus" {
		t.Errorf("CLAUDE_BUILD_MODEL = %q, want the explicit opus untouched", keys["CLAUDE_BUILD_MODEL"])
	}
	if keys["CLAUDE_VERIFY_MODEL"] != "sonnet" {
		t.Errorf("CLAUDE_VERIFY_MODEL = %q, want sonnet", keys["CLAUDE_VERIFY_MODEL"])
	}

	// A config that never set a provider default has nothing to preserve.
	clean := filepath.Join(dir, "trau.ini")
	if err := os.WriteFile(clean, []byte("PROVIDER=claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MigratePhaseRoutes("", "", clean, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(clean)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "PROVIDER=claude\n" {
		t.Errorf("clean config rewritten to %q, want it untouched", got)
	}
}

// TestMigratePhaseRoutesMigratesTheAbsentHalf covers a partially configured phase:
// only the half the old cascade steered is pinned, and only where it diverges from
// the new default.
func TestMigratePhaseRoutesMigratesTheAbsentHalf(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	body := "PROVIDER=codex\nCODEX_EFFORT=high\nCODEX_BUILD_EFFORT=low\n"
	if err := os.WriteFile(local, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MigratePhaseRoutes("", "", local, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	keys, err := ParseEnvFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if keys["CODEX_BUILD_EFFORT"] != "low" {
		t.Errorf("CODEX_BUILD_EFFORT = %q, want the explicit low untouched", keys["CODEX_BUILD_EFFORT"])
	}
	if keys["CODEX_VERIFY_EFFORT"] != "high" {
		t.Errorf("CODEX_VERIFY_EFFORT = %q, want high", keys["CODEX_VERIFY_EFFORT"])
	}
	if _, ok := keys["CODEX_VERIFY_MODEL"]; ok {
		t.Error("CODEX_VERIFY_MODEL was written, want the model half left to the default")
	}
}

// TestMigratePhaseRoutesSkipsEnvOnlyDefaults keeps the pass out of files that never
// carried the provider default: an env var has nowhere to be pinned.
func TestMigratePhaseRoutesSkipsEnvOnlyDefaults(t *testing.T) {
	t.Setenv("CLAUDE_MODEL", "sonnet")
	dir := t.TempDir()
	user := filepath.Join(dir, ".trau.ini")
	if err := os.WriteFile(user, []byte("PROVIDER=claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MigratePhaseRoutes("", user, "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "PROVIDER=claude\n" {
		t.Errorf("file rewritten to %q, want it untouched", got)
	}
}

// TestMigratePhaseRoutesLeavesPostUpgradeDefaultsAlone covers the install that has
// nothing to migrate: a provider default written after the first startup under the
// fixed defaults steers the non-phase agents and must stay out of phase routing.
func TestMigratePhaseRoutesLeavesPostUpgradeDefaultsAlone(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	user := filepath.Join(dir, ".trau.ini")
	if err := MigratePhaseRoutes("", user, "", home); err != nil {
		t.Fatal(err)
	}

	body := "PROVIDER=claude\nCLAUDE_MODEL=sonnet\n"
	if err := os.WriteFile(user, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MigratePhaseRoutes("", user, "", home); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("file rewritten to %q, want the provider default left inert", got)
	}
	cfg, err := LoadLayered("", user, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if route := cfg.effectiveRoute("build"); route != "claude:opus:" {
		t.Errorf("build routes to %q, want the fixed default claude:opus:", route)
	}
}
