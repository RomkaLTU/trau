package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestCodexCatalogContents pins the canonical catalog: models ordered Sol, Terra,
// Luna, then still-supported older releases; efforts exactly the Codex CLI set
// with the stale minimal and API-only none excluded.
func TestCodexCatalogContents(t *testing.T) {
	models := CodexModels()
	want := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}
	if !slices.Equal(models, want) {
		t.Fatalf("CodexModels() = %v, want %v", models, want)
	}
	if models[0] != CodexDefaultModel {
		t.Errorf("default model %q is not first in the suggestions %v", CodexDefaultModel, models)
	}

	efforts := CodexEfforts()
	if !slices.Equal(efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("CodexEfforts() = %v, want [low medium high xhigh max]", efforts)
	}
	for _, dropped := range []string{"minimal", "none"} {
		if slices.Contains(efforts, dropped) {
			t.Errorf("CodexEfforts() must not offer %q", dropped)
		}
	}
	if !slices.Contains(efforts, CodexDefaultEffort) {
		t.Errorf("default effort %q missing from choices %v", CodexDefaultEffort, efforts)
	}
}

// TestCodexDefaultResolution checks a clean configuration resolves codex to
// gpt-5.6-sol at medium effort, the values Codex.args passes explicitly.
func TestCodexDefaultResolution(t *testing.T) {
	if c := Defaults(); c.CodexModel != "gpt-5.6-sol" || c.CodexEffort != "medium" {
		t.Fatalf("Defaults() codex = %q/%q, want gpt-5.6-sol/medium", c.CodexModel, c.CodexEffort)
	}

	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	if err := os.WriteFile(local, []byte("PROVIDER=codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodexModel != "gpt-5.6-sol" || cfg.CodexEffort != "medium" {
		t.Fatalf("clean codex config = %q/%q, want gpt-5.6-sol/medium", cfg.CodexModel, cfg.CodexEffort)
	}
	// No per-phase key means no route entry: the phase inherits the default at the
	// composition root rather than being pinned here.
	if got := cfg.Routes["build"]; got != "" {
		t.Errorf("unset codex build route = %q, want empty (inherits the default)", got)
	}
}

// TestCodexRouteInheritance covers per-phase inheritance and the documented layer
// precedence: an unset dial falls back to the provider default, an explicit
// override wins, and higher layers override lower ones.
func TestCodexRouteInheritance(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")

	write := func(body string) Config {
		t.Helper()
		if err := os.WriteFile(local, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadLayered("", "", local, "")
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	// Effort overridden, model inherits the gpt-5.6-sol default.
	cfg := write("PROVIDER=codex\nCODEX_BUILD_EFFORT=high\n")
	if got := cfg.Routes["build"]; got != "codex:gpt-5.6-sol:high" {
		t.Errorf("build route = %q, want codex:gpt-5.6-sol:high", got)
	}

	// Model overridden, effort inherits the medium default.
	cfg = write("PROVIDER=codex\nCODEX_VERIFY_MODEL=gpt-5.6-terra\n")
	if got := cfg.Routes["verify"]; got != "codex:gpt-5.6-terra:medium" {
		t.Errorf("verify route = %q, want codex:gpt-5.6-terra:medium", got)
	}

	// An explicit default replaces the catalog default for unrouted dials.
	cfg = write("PROVIDER=codex\nCODEX_MODEL=gpt-5.5\nCODEX_EFFORT=high\nCODEX_BUILD_EFFORT=xhigh\n")
	if cfg.CodexModel != "gpt-5.5" || cfg.CodexEffort != "high" {
		t.Fatalf("explicit default = %q/%q, want gpt-5.5/high", cfg.CodexModel, cfg.CodexEffort)
	}
	if got := cfg.Routes["build"]; got != "codex:gpt-5.5:xhigh" {
		t.Errorf("build route = %q, want codex:gpt-5.5:xhigh", got)
	}
}

// TestCodexCustomModel confirms a model ID outside the catalog stays usable both
// as the default and as a per-phase override — the catalog is suggestions, not an
// allowlist.
func TestCodexCustomModel(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	if err := os.WriteFile(local, []byte("PROVIDER=codex\nCODEX_MODEL=acme-internal-gpt\nCODEX_BUILD_MODEL=acme-build-gpt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodexModel != "acme-internal-gpt" {
		t.Fatalf("custom default model = %q, want acme-internal-gpt", cfg.CodexModel)
	}
	if got := cfg.Routes["build"]; got != "codex:acme-build-gpt:medium" {
		t.Errorf("custom build route = %q, want codex:acme-build-gpt:medium", got)
	}
}

// TestCodexProviderTuningsReflectCatalog checks the terminal selectors and config
// metadata draw from the same catalog, and that layer precedence flows through.
func TestCodexProviderTuningsReflectCatalog(t *testing.T) {
	meta := codexTuningMeta(t)
	if !slices.Equal(meta.Models, CodexModels()) || !slices.Equal(meta.Efforts, CodexEfforts()) {
		t.Fatalf("ProviderTuningMetas codex = %v/%v, want catalog", meta.Models, meta.Efforts)
	}

	dir := t.TempDir()
	project := filepath.Join(dir, ".trau.ini")
	if err := os.WriteFile(project, []byte("CODEX_MODEL=gpt-5.6-luna\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tuning := codexTuning(t, ResolveProviderTunings("", project, "", "codex"))
	if !slices.Equal(tuning.Models, CodexModels()) || !slices.Equal(tuning.Efforts, CodexEfforts()) {
		t.Fatalf("resolved codex options = %v/%v, want catalog", tuning.Models, tuning.Efforts)
	}
	if tuning.Model.Value != "gpt-5.6-luna" || tuning.Model.Layer != LayerProject {
		t.Errorf("resolved codex model = %q@%s, want gpt-5.6-luna@project", tuning.Model.Value, tuning.Model.Layer)
	}
	if !tuning.Active {
		t.Error("codex should be marked active when it is the active provider")
	}
}

func codexTuningMeta(t *testing.T) ProviderTuningMeta {
	t.Helper()
	for _, m := range ProviderTuningMetas() {
		if m.Name == "codex" {
			return m
		}
	}
	t.Fatal("no codex ProviderTuningMeta")
	return ProviderTuningMeta{}
}

func codexTuning(t *testing.T, tunings []ProviderTuning) ProviderTuning {
	t.Helper()
	for _, tn := range tunings {
		if tn.Name == "codex" {
			return tn
		}
	}
	t.Fatal("no codex ProviderTuning")
	return ProviderTuning{}
}
