package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadRouting(t *testing.T, files map[string]string) RoutingFingerprint {
	t.Helper()
	dir := t.TempDir()
	paths := map[string]string{}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[name] = path
	}
	cfg, err := LoadLayered(paths[".trau.ini"], paths["user.ini"], paths["trau.ini"], "")
	if err != nil {
		t.Fatal(err)
	}
	return ResolveRouting(cfg)
}

// TestResolveRoutingKeys pins the fingerprint's inputs: the active provider, every
// phase's fully expanded route, and the required skills — nothing else.
func TestResolveRoutingKeys(t *testing.T) {
	fp := ResolveRouting(Config{
		Provider:       "claude",
		ClaudeModel:    "opus",
		ClaudeEffort:   "xhigh",
		Routes:         map[string]string{"verify": "claude:opus:high", "lintfix": "claude:haiku"},
		RequiredSkills: []string{"golang-pro", "bubbletea"},
	})

	want := map[string]string{
		"PROVIDER":        "claude",
		"REQUIRED_SKILLS": "bubbletea,golang-pro",
		"PHASE_BUILD":     "claude:opus:xhigh",
		"PHASE_HANDOFF":   "claude:opus:xhigh",
		"PHASE_VERIFY":    "claude:opus:high",
		"PHASE_REPAIR":    "claude:opus:xhigh",
		"PHASE_BUGFIX":    "claude:opus:xhigh",
		"PHASE_CLEANUP":   "claude:opus:xhigh",
		"PHASE_LINTFIX":   "claude:haiku:xhigh",
		"PHASE_COMMIT":    "claude:opus:xhigh",
		"PHASE_PICK":      "claude:opus:xhigh",
	}
	if len(fp.Keys) != len(want) {
		t.Fatalf("keys = %v, want exactly %d entries", fp.Keys, len(want))
	}
	for key, value := range want {
		if fp.Keys[key] != value {
			t.Errorf("keys[%s] = %q, want %q", key, fp.Keys[key], value)
		}
	}
	if fp.Hash == "" {
		t.Error("hash is empty, want a digest over the keys")
	}
}

// TestResolveRoutingIsLayerAgnostic is the cohort contract: two repos whose
// effective routing matches hash identically, no matter which config file supplied
// each value, and required skills listed in a different order are the same config.
func TestResolveRoutingIsLayerAgnostic(t *testing.T) {
	fromProject := loadRouting(t, map[string]string{
		".trau.ini": "PROVIDER=claude\nCLAUDE_MODEL=opus\nCLAUDE_VERIFY_EFFORT=high\nREQUIRED_SKILLS=golang-pro,bubbletea\n",
	})
	fromUserAndLocal := loadRouting(t, map[string]string{
		"user.ini":  "CLAUDE_MODEL=opus\nCLAUDE_VERIFY_EFFORT=high\n",
		"trau.ini":  "PROVIDER=claude\nREQUIRED_SKILLS=bubbletea,golang-pro\n",
		".trau.ini": "",
	})
	if fromProject.Keys["PHASE_VERIFY"] != "claude:opus:high" {
		t.Fatalf("PHASE_VERIFY = %q, want claude:opus:high — the fixture stopped exercising layering", fromProject.Keys["PHASE_VERIFY"])
	}
	if fromProject.Hash != fromUserAndLocal.Hash {
		t.Fatalf("hash %s != %s, want the same effective routing to hash identically", fromProject.Hash, fromUserAndLocal.Hash)
	}
}

// TestResolveRoutingHashTracksRoutingKeys checks every routing lever moves the
// hash and that a non-routing key — a credential especially — never does.
func TestResolveRoutingHashTracksRoutingKeys(t *testing.T) {
	base := Config{
		Provider:       "claude",
		ClaudeModel:    "opus",
		ClaudeEffort:   "xhigh",
		Routes:         map[string]string{"verify": "claude:opus:xhigh"},
		RequiredSkills: []string{"golang-pro"},
	}
	baseHash := ResolveRouting(base).Hash

	cases := []struct {
		name  string
		mutet func(*Config)
		want  bool
	}{
		{"phase effort", func(c *Config) { c.Routes = map[string]string{"verify": "claude:opus:high"} }, true},
		{"phase model", func(c *Config) { c.Routes = map[string]string{"verify": "claude:sonnet:xhigh"} }, true},
		{"phase provider", func(c *Config) { c.Routes = map[string]string{"verify": "codex:gpt-5:xhigh"} }, true},
		{"default provider", func(c *Config) { c.Provider = "codex" }, true},
		{"default model", func(c *Config) { c.ClaudeModel = "sonnet" }, true},
		{"default effort", func(c *Config) { c.ClaudeEffort = "high" }, true},
		{"required skills", func(c *Config) { c.RequiredSkills = []string{"golang-pro", "bubbletea"} }, true},
		{"api key", func(c *Config) { c.LinearAPIKey = "lin_api_secret" }, false},
		{"serve token", func(c *Config) { c.ServeToken = "s3cret" }, false},
		{"max iterations", func(c *Config) { c.MaxIterations = 7 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutet(&cfg)
			got := ResolveRouting(cfg).Hash
			if changed := got != baseHash; changed != tc.want {
				t.Fatalf("hash changed = %v, want %v (base %s, got %s)", changed, tc.want, baseHash, got)
			}
		})
	}
}

// TestResolveRoutingExcludesSecrets keeps credentials out of the diff surface: a
// value the hub would echo back in a config_change event must never carry one.
func TestResolveRoutingExcludesSecrets(t *testing.T) {
	fp := ResolveRouting(Config{
		Provider:     "claude",
		ClaudeModel:  "opus",
		LinearAPIKey: "lin_api_secret",
		JiraAPIToken: "jira_secret",
		ServeToken:   "serve_secret",
	})
	for key, value := range fp.Keys {
		for _, secret := range []string{"lin_api_secret", "jira_secret", "serve_secret"} {
			if value == secret {
				t.Errorf("keys[%s] leaked %q", key, secret)
			}
		}
	}
}
