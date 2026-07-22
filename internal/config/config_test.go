package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEffectiveTrackerProvider(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"default linear with no config falls back to internal", Config{TrackerProvider: "linear"}, "internal"},
		{"empty provider with no config falls back to internal", Config{}, "internal"},
		{"linear with a team stays linear", Config{TrackerProvider: "linear", LinearTeam: "COD"}, "linear"},
		{"linear with an api key stays linear", Config{TrackerProvider: "linear", LinearAPIKey: "k"}, "linear"},
		{"explicit internal is honored", Config{TrackerProvider: "internal"}, "internal"},
		{"explicit jira is honored", Config{TrackerProvider: "jira"}, "jira"},
		{"explicit github is honored", Config{TrackerProvider: "github"}, "github"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.EffectiveTrackerProvider(); got != tc.want {
				t.Fatalf("EffectiveTrackerProvider() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveSyncProvider(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		sources map[string]Layer
		want    string
	}{
		{
			name: "unset provider with full project jira creds infers jira",
			cfg:  Config{TrackerProvider: "linear", LinearAPIKey: "user-key", JiraBaseURL: "u", JiraEmail: "e", JiraAPIToken: "t"},
			sources: map[string]Layer{
				"JIRA_BASE_URL":  LayerProject,
				"JIRA_EMAIL":     LayerProject,
				"JIRA_API_TOKEN": LayerProject,
			},
			want: "jira",
		},
		{
			name: "explicit linear is honored over project jira creds",
			cfg:  Config{TrackerProvider: "linear", LinearTeam: "COD"},
			sources: map[string]Layer{
				"TRACKER_PROVIDER": LayerProject,
				"JIRA_BASE_URL":    LayerProject,
				"JIRA_EMAIL":       LayerProject,
				"JIRA_API_TOKEN":   LayerProject,
			},
			want: "linear",
		},
		{
			name:    "explicit jira is honored",
			cfg:     Config{TrackerProvider: "jira"},
			sources: map[string]Layer{"TRACKER_PROVIDER": LayerUser},
			want:    "jira",
		},
		{
			name: "jira creds only at user layer do not infer jira",
			cfg:  Config{TrackerProvider: "linear", LinearAPIKey: "k", JiraBaseURL: "u", JiraEmail: "e", JiraAPIToken: "t"},
			sources: map[string]Layer{
				"JIRA_BASE_URL":  LayerUser,
				"JIRA_EMAIL":     LayerUser,
				"JIRA_API_TOKEN": LayerUser,
			},
			want: "linear",
		},
		{
			name: "partial project jira creds do not infer jira",
			cfg:  Config{TrackerProvider: "linear", LinearAPIKey: "k", JiraBaseURL: "u", JiraEmail: "e"},
			sources: map[string]Layer{
				"JIRA_BASE_URL": LayerProject,
				"JIRA_EMAIL":    LayerProject,
			},
			want: "linear",
		},
		{
			name:    "unset provider with no tracker config falls back to internal",
			cfg:     Config{TrackerProvider: "linear"},
			sources: map[string]Layer{},
			want:    "internal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ResolveSyncProvider(tc.sources); got != tc.want {
				t.Fatalf("ResolveSyncProvider() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTrackerKey(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"linear uses the team", Config{TrackerProvider: "linear", LinearTeam: "COD"}, "COD"},
		{"jira uses the team when set", Config{TrackerProvider: "jira", LinearTeam: "TMS", Project: "MLG"}, "TMS"},
		{"jira falls back to project when team unset", Config{TrackerProvider: "jira", Project: "MLG"}, "MLG"},
		{"mixed-case jira still falls back to project", Config{TrackerProvider: "Jira", Project: "MLG"}, "MLG"},
		{"jira with neither key is empty", Config{TrackerProvider: "jira"}, ""},
		{"non-jira never falls back to project", Config{TrackerProvider: "linear", LinearAPIKey: "k", Project: "trau"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.TrackerKey(); got != tc.want {
				t.Fatalf("TrackerKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

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

	if got := byKey["BASE_BRANCH"]; got.Layer != LayerProject || got.Value != "project-main" {
		t.Fatalf("BASE_BRANCH: want project/project-main, got %s/%s", got.Layer, got.Value)
	}
	if got := byKey["PROVIDER"]; got.Layer != LayerProject || got.Value != "codex" {
		t.Fatalf("PROVIDER: want project/codex, got %s/%s", got.Layer, got.Value)
	}
	if got := byKey["MAX_ITERATIONS"]; got.Layer != LayerLocal || got.Value != "5" {
		t.Fatalf("MAX_ITERATIONS: want local/5, got %s/%s", got.Layer, got.Value)
	}
}

// JIRA_EPIC_TYPE is the override for the project's own hierarchy-level-1 lookup,
// so it has to survive the load and reach the settings surfaces by its catalog key.
func TestLoadJiraEpicType(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	if err := os.WriteFile(local, []byte("TRACKER_PROVIDER=jira\nJIRA_EPIC_TYPE=Feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JiraEpicType != "Feature" {
		t.Fatalf("JiraEpicType = %q, want Feature", cfg.JiraEpicType)
	}
	if got := keyValue(cfg, "JIRA_EPIC_TYPE"); got != "Feature" {
		t.Errorf("keyValue(JIRA_EPIC_TYPE) = %q, want Feature", got)
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

func TestWriteConfigLayerNormalizesAppURLs(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, ".trau.ini")

	want := "api=http://localhost:3001,web=http://localhost:3000"
	for _, value := range []string{
		"web=http://localhost:3000,api=http://localhost:3001",
		"api=http://localhost:3001, web=http://localhost:3000",
	} {
		if err := WriteConfigLayer("project", "", project, "", "APP_URLS", value); err != nil {
			t.Fatal(err)
		}
		got, err := ParseEnvFile(project)
		if err != nil {
			t.Fatal(err)
		}
		if got["APP_URLS"] != want {
			t.Fatalf("APP_URLS after writing %q: want %s, got %s", value, want, got["APP_URLS"])
		}
	}
}

func TestDeleteConfigLayerRemovesKey(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, ".trau.ini")
	user := filepath.Join(dir, "user.ini")

	original := "# machine config\nLINEAR_API_KEY=old\n# provider flags\nCLAUDE_FLAGS=--foo\n"
	if err := os.WriteFile(user, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := DeleteConfigLayer("user", "", project, user, "LINEAR_API_KEY"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(content), "LINEAR_API_KEY") {
		t.Fatalf("unset left the key behind: %q", content)
	}
	if !contains(string(content), "# machine config") || !contains(string(content), "# provider flags") {
		t.Fatalf("unset dropped comments: %q", content)
	}
	if !contains(string(content), "CLAUDE_FLAGS=--foo") {
		t.Fatalf("unset dropped an unrelated key: %q", content)
	}

	if err := os.WriteFile(project, []byte("BASE_BRANCH=develop\nREMOTE=origin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := DeleteConfigLayer("project", "", project, user, "BASE_BRANCH"); err != nil {
		t.Fatal(err)
	}
	got, err := ParseEnvFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["BASE_BRANCH"]; ok {
		t.Fatalf("project unset left BASE_BRANCH behind: %v", got)
	}
	if got["REMOTE"] != "origin" {
		t.Fatalf("project unset dropped an unrelated key: %v", got)
	}

	if err := DeleteConfigLayer("user", "", project, filepath.Join(dir, "absent.ini"), "ANYTHING"); err != nil {
		t.Fatalf("unset of a missing file should be a no-op, got %v", err)
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
// set, a Claude run seeds cleanup/commit/handoff onto sonnet and lintfix onto
// haiku instead of inheriting the Opus default, build/verify stay unseeded
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
		"cleanup": "claude:sonnet",
		"commit":  "claude:sonnet",
		"handoff": "claude:sonnet",
		"lintfix": "claude:haiku",
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
	for _, phase := range []string{"commit", "handoff", "cleanup", "lintfix"} {
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

func TestLoadThemeKeys(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")

	if err := os.WriteFile(local, []byte("THEME=nord\nTHEME_BRAND=#ff0000\nTHEME_FAINT=cccccc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Theme != "nord" {
		t.Errorf("Theme = %q, want nord", cfg.Theme)
	}
	want := map[string]string{"brand": "#ff0000", "faint": "cccccc"}
	if len(cfg.ThemeColors) != len(want) {
		t.Fatalf("ThemeColors = %v, want %v", cfg.ThemeColors, want)
	}
	for role, hex := range want {
		if cfg.ThemeColors[role] != hex {
			t.Errorf("ThemeColors[%q] = %q, want %q", role, cfg.ThemeColors[role], hex)
		}
	}

	items, err := ResolveConfigItems(cfg, local, "", "", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]ConfigItem{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	if got := byKey["THEME"]; got.Layer != LayerLocal || got.Value != "nord" {
		t.Errorf("THEME: want local/nord, got %s/%s", got.Layer, got.Value)
	}
	if got := byKey["THEME_BRAND"]; got.Layer != LayerLocal || got.Value != "#ff0000" {
		t.Errorf("THEME_BRAND: want local/#ff0000, got %s/%s", got.Layer, got.Value)
	}
}

func TestLoadThemeDefault(t *testing.T) {
	cfg, err := LoadLayered("", "", filepath.Join(t.TempDir(), "missing.ini"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "default" {
		t.Errorf("Theme = %q, want default", cfg.Theme)
	}
	if cfg.ThemeColors != nil {
		t.Errorf("ThemeColors = %v, want nil", cfg.ThemeColors)
	}
}

func TestKnownKeysCatalogMetadata(t *testing.T) {
	keys := KnownKeys()
	byKey := make(map[string]KeyMeta, len(keys))
	sections := map[string]bool{}
	for _, s := range configSections {
		sections[s] = true
	}

	for _, m := range keys {
		byKey[m.Key] = m
		if m.Group == "" {
			t.Errorf("%s has no Section", m.Key)
			continue
		}
		if !sections[m.Group] {
			t.Errorf("%s has Section %q outside the catalog set", m.Key, m.Group)
		}
	}

	editable := []string{
		"MAX_ITERATIONS", "THEME", "PROJECT", "LINEAR_API_KEY", "JIRA_API_TOKEN",
		"GRILL_MODEL", "TRANSCRIPT_RETENTION", "SERVE_AUTOSTART",
		"CLAUDE_MODEL", "CLAUDE_BUILD_MODEL", "THEME_BRAND", "BASE_BRANCH",
	}
	for _, k := range editable {
		if !byKey[k].WebEditable {
			t.Errorf("%s should be web-editable", k)
		}
	}

	readOnly := []string{
		"CLAUDE_BIN", "CODEX_BIN", "KIMI_BIN", "CLAUDE_FLAGS", "CODEX_FLAGS",
		"CLAUDE_CONFIG", "LINT_FIX_CMD", "SERVE_BIND", "SERVE_PORT", "SERVE_TOKEN",
		"SERVE_ALLOW_REGISTER", "RUNS_DIR", "TRAU_REPO_ROOT", "SERVE_WORKSPACE",
	}
	for _, k := range readOnly {
		if byKey[k].WebEditable {
			t.Errorf("%s must stay read-only over the web", k)
		}
	}

	kinds := map[string]string{
		"MAX_ITERATIONS":  "int",
		"CI_TIMEOUT":      "int",
		"GRILL_RETENTION": "int",
		"THEME_BRAND":     "color",
		"MAX_TICKET_USD":  "",
	}
	for k, want := range kinds {
		if got := byKey[k].Kind; got != want {
			t.Errorf("%s Kind = %q, want %q", k, got, want)
		}
	}

	for _, k := range []string{"CLAUDE_MODEL", "CODEX_MODEL", "CLAUDE_BUILD_MODEL", "CODEX_PICK_MODEL"} {
		if len(byKey[k].Suggestions) == 0 {
			t.Errorf("%s should carry model suggestions", k)
		}
	}
	for _, k := range []string{"CLAUDE_EFFORT", "CODEX_EFFORT", "CLAUDE_BUILD_EFFORT", "CODEX_VERIFY_EFFORT"} {
		if len(byKey[k].Options) == 0 {
			t.Errorf("%s should carry effort options", k)
		}
	}
}

func TestLoadAppURLs(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	if err := os.WriteFile(local, []byte("APP_URLS=web=http://localhost:3000, api=http://localhost:3001,broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"web": "http://localhost:3000", "api": "http://localhost:3001"}
	if len(cfg.AppURLs) != len(want) {
		t.Fatalf("AppURLs = %v, want %v", cfg.AppURLs, want)
	}
	for name, url := range want {
		if cfg.AppURLs[name] != url {
			t.Errorf("AppURLs[%q] = %q, want %q", name, cfg.AppURLs[name], url)
		}
	}

	if got := keyValue(cfg, "APP_URLS"); got != "api=http://localhost:3001,web=http://localhost:3000" {
		t.Errorf("keyValue(APP_URLS) = %q", got)
	}
}

func TestLoadAppURLsDefault(t *testing.T) {
	cfg, err := LoadLayered("", "", filepath.Join(t.TempDir(), "missing.ini"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppURLs != nil {
		t.Errorf("AppURLs = %v, want nil", cfg.AppURLs)
	}
}

func TestWorkspaceOverride(t *testing.T) {
	t.Run("key set in the workspace file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".trau.ini"), []byte("LINT_FIX_CMD=npm run lint:fix\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		v, ok := WorkspaceOverride(dir, "LINT_FIX_CMD")
		if !ok || v != "npm run lint:fix" {
			t.Errorf("WorkspaceOverride = (%q, %v), want (%q, true)", v, ok, "npm run lint:fix")
		}
	})

	t.Run("key not set in the workspace file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".trau.ini"), []byte("AGENT_TIMEOUT=60\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if v, ok := WorkspaceOverride(dir, "LINT_FIX_CMD"); ok {
			t.Errorf("WorkspaceOverride = (%q, %v), want ok=false", v, ok)
		}
	})

	t.Run("no workspace file", func(t *testing.T) {
		if v, ok := WorkspaceOverride(t.TempDir(), "LINT_FIX_CMD"); ok {
			t.Errorf("WorkspaceOverride = (%q, %v), want ok=false", v, ok)
		}
	})

	t.Run("no workspace dir", func(t *testing.T) {
		if v, ok := WorkspaceOverride("", "LINT_FIX_CMD"); ok {
			t.Errorf("WorkspaceOverride = (%q, %v), want ok=false", v, ok)
		}
	})
}
