// Package config loads the loop's configuration and CLI options.
//
// Configuration is layered. From lowest to highest precedence:
//
//  1. built-in defaults
//  2. local config file (./trau.ini, or TRAU_ENV)
//  3. project config file (<repo>/.trau.ini)
//  4. user config file (~/.trau.ini)
//  5. process environment variables (a bare KEY, or the collision-safe
//     TRAU_<KEY> alias which wins over the bare name)
//  6. explicit CLI overrides (e.g. --provider)
//
// The env files are parsed (KEY=value, with # comments) rather than executed,
// so they can never run arbitrary shell. This lets each target project ship its
// own project-level config while users keep personal/machine overrides at home.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Preamble is prepended to EVERY headless prompt so no phase blocks on a
// question — there is no human to answer one.
const Preamble = "[Unattended run] You are running headless inside an automated loop — no human is watching and no input is possible. Never call AskUserQuestion or wait for a reply. When a choice arises, take the most reasonable / recommended default, proceed, and note the assumption in one line. If you truly cannot proceed safely, stop and say why. Do ALL work inline in THIS single agent: the Agent and Workflow tools (subagent spawning and multi-agent fan-out) are intentionally disabled for this loop, because each phase already runs as its own isolated process and fanning out only multiplies token cost without adding any isolation. Do not try to spawn subagents or parallel workers; if you genuinely believe one is unavoidable, stop and explain why in your final summary instead of working around it. (The TaskCreate/TaskUpdate todo-list tools are fine — they do not spawn anything.)"

// Config is the resolved loop configuration. Field defaults and names track the
// trau.ini knobs documented in trau.ini.example.
type Config struct {
	LinearTeam      string
	LinearAPIKey    string
	ReadyLabel      string
	QuarantineLabel string
	Project         string

	BaseBranch string
	Remote     string
	RepoRoot   string

	Provider        string
	TrackerProvider string

	ClaudeConfig          string
	ClaudeBin             string
	ClaudeFlags           string
	AgentTimeout          int
	ClaudeModel           string
	ClaudeEffort          string
	ClaudeDisallowedTools string

	CodexConfig  string
	CodexBin     string
	CodexFlags   string
	CodexProfile string
	CodexModel   string
	CodexEffort  string

	KimiConfig string
	KimiBin    string
	KimiFlags  string
	KimiModel  string

	Routes map[string]string

	MaxIterations  int
	MaxRepairs     int
	MaxBugfixes    int
	AutoMerge      bool
	MergeMethod    string
	CITimeout      int
	CIPoll         int
	ExpectedChecks string

	BrowserVerify string
	AppURL        string

	TUI bool

	EpicFlow bool

	RunsDir string
}

// Defaults returns the built-in configuration used when neither the env file
// nor the environment supplies a value.
func Defaults() Config {
	return Config{
		LinearTeam:            "",
		ReadyLabel:            "ready-for-agent",
		QuarantineLabel:       "needs-human",
		Project:               "",
		BaseBranch:            "main",
		Remote:                "origin",
		RepoRoot:              "",
		Provider:              "claude",
		TrackerProvider:       "linear",
		ClaudeConfig:          "",
		ClaudeBin:             "claude",
		ClaudeFlags:           "--dangerously-skip-permissions",
		AgentTimeout:          900,
		ClaudeModel:           "",
		ClaudeEffort:          "",
		ClaudeDisallowedTools: "Agent,Workflow",
		CodexConfig:           "",
		CodexBin:              "codex",
		CodexFlags:            "--dangerously-bypass-approvals-and-sandbox",
		CodexProfile:          "",
		CodexModel:            "",
		CodexEffort:           "",
		KimiConfig:            "",
		KimiBin:               "kimi",
		KimiFlags:             "",
		KimiModel:             "",
		MaxIterations:         15,
		MaxRepairs:            2,
		MaxBugfixes:           2,
		AutoMerge:             true,
		MergeMethod:           "squash",
		CITimeout:             600,
		CIPoll:                30,
		ExpectedChecks:        "",
		BrowserVerify:         "auto",
		AppURL:                "http://localhost",
		TUI:                   true,
		EpicFlow:              true,
		RunsDir:               "runs",
	}
}

type envLayer struct {
	file map[string]string
	dir  string
	path string
	name Layer
}

// providerGet returns the value for key from a provider-local config file when
// present, otherwise delegates to fallback (the normal layered lookup). It is a
// package-level helper so mode expansion can reuse the same precedence rule.
func providerGet(file map[string]string, src envLayer, key string, fallback func(string) (string, envLayer)) (string, envLayer) {
	if file != nil {
		if v, present := file[key]; present {
			return v, src
		}
	}
	return fallback(key)
}

// Layer identifies which configuration layer supplied a value.
type Layer string

const (
	LayerDefault Layer = "default"
	LayerLocal   Layer = "local"
	LayerProject Layer = "project"
	LayerUser    Layer = "user"
	LayerEnv     Layer = "env var"
	LayerCLI     Layer = "CLI"
)

// ConfigItem is one resolved configuration key exposed by the in-TUI settings
// editor. It carries the effective value and the layer that supplied it.
type ConfigItem struct {
	Key      string
	Value    string
	Layer    Layer
	Advanced bool
	// Options enumerates the allowed values for a key. When non-empty the
	// editor presents a picker instead of a free-text field.
	Options []string
	// Bool marks a 1/0 toggle key. The editor renders it as an on/off switch.
	Bool bool
	// Description and Default carry the key's metadata so the editor can
	// explain what it does and what value it falls back to.
	Description string
	Default     string
}

// Config file names. The keys in these files are environment-variable names, so
// the format is a flat INI subset (KEY=value with # comments). The .ini
// extension is recognized out of the box by VS Code and IntelliJ via their
// built-in INI grammar — no plugin — which the legacy dotenv name lacked.
const (
	// LocalConfigName is the cwd-local config file (overridable via TRAU_ENV).
	LocalConfigName = "trau.ini"
	// ProjectConfigName is the repo-root and home (~) config file.
	ProjectConfigName = ".trau.ini"
)

// ProjectConfigPath returns the project- or user-level config file inside dir.
// An empty dir yields "" (no file).
func ProjectConfigPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ProjectConfigName)
}

// LocalConfigPath returns the cwd-local config file: an explicit TRAU_ENV path
// when set, else trau.ini.
func LocalConfigPath() string {
	if p := os.Getenv("TRAU_ENV"); p != "" {
		return p
	}
	return LocalConfigName
}

// Load resolves configuration from a single env file. It exists for backward
// compatibility and tests; new callers should use LoadLayered.
func Load(path string) (Config, error) {
	return LoadWithProvider(path, "")
}

// LoadWithProvider resolves configuration with an explicit provider selector.
// It loads only the single local env file. New callers should prefer
// LoadLayered, but this wrapper keeps existing tests and simple callers working.
func LoadWithProvider(path, provider string) (Config, error) {
	return LoadLayered("", "", path, provider)
}

// LoadLayered resolves configuration from up to three env files plus the
// process environment and an explicit provider override.
//
// Precedence (lowest to highest):
//
//	defaults < localPath < projectPath < userPath < env vars < provider arg.
//
// A missing path is not an error. Provider-local config files named by
// CLAUDE_CONFIG/CODEX_CONFIG/KIMI_CONFIG are resolved relative to the layer
// that supplies the value.
func LoadLayered(projectPath, userPath, localPath, provider string) (Config, error) {
	c, _, err := LoadLayeredWithSources(projectPath, userPath, localPath, provider)
	return c, err
}

// LoadLayeredWithSources is the same as LoadLayered but also returns a map from
// each resolved key to the layer that supplied its effective value. It is used
// by the in-TUI settings editor to show precedence without re-implementing the
// merge logic.
func LoadLayeredWithSources(projectPath, userPath, localPath, provider string) (Config, map[string]Layer, error) {
	c := Defaults()
	sources := map[string]Layer{}

	local, err := ParseEnvFile(localPath)
	if err != nil {
		return c, sources, err
	}
	proj, err := ParseEnvFile(projectPath)
	if err != nil {
		return c, sources, err
	}
	user, err := ParseEnvFile(userPath)
	if err != nil {
		return c, sources, err
	}

	localLayer := envLayer{file: local, dir: dirOf(localPath), path: localPath, name: LayerLocal}
	projLayer := envLayer{file: proj, dir: dirOf(projectPath), path: projectPath, name: LayerProject}
	userLayer := envLayer{file: user, dir: dirOf(userPath), path: userPath, name: LayerUser}

	get := func(key string) (string, envLayer) {
		if !strings.HasPrefix(key, "TRAU_") {
			if v := os.Getenv("TRAU_" + key); v != "" {
				return v, envLayer{name: LayerEnv}
			}
		}
		if v := os.Getenv(key); v != "" {
			return v, envLayer{name: LayerEnv}
		}
		for _, layer := range []envLayer{userLayer, projLayer, localLayer} {
			if v, ok := layer.file[key]; ok {
				return v, layer
			}
		}
		return "", envLayer{name: LayerDefault}
	}
	str := func(key string, dst *string) {
		v, src := get(key)
		if v != "" {
			*dst = v
			sources[key] = src.name
		}
	}
	num := func(key string, dst *int) {
		v, src := get(key)
		if v != "" {
			if n, e := strconv.Atoi(strings.TrimSpace(v)); e == nil {
				*dst = n
				sources[key] = src.name
			}
		}
	}

	providerFile := func(configKey string) (map[string]string, envLayer, error) {
		v, src := get(configKey)
		if v == "" {
			return nil, envLayer{}, nil
		}
		f, err := ParseEnvFile(resolveSiblingPath(src.path, v))
		return f, src, err
	}
	providerStr := func(providerFile map[string]string, src envLayer, key string, dst *string) {
		v, srcLayer := providerGet(providerFile, src, key, get)
		if v != "" {
			*dst = v
			sources[key] = srcLayer.name
		}
	}

	str("LINEAR_TEAM", &c.LinearTeam)
	str("LINEAR_API_KEY", &c.LinearAPIKey)
	str("READY_LABEL", &c.ReadyLabel)
	str("QUARANTINE_LABEL", &c.QuarantineLabel)
	str("PROJECT", &c.Project)
	str("BASE_BRANCH", &c.BaseBranch)
	str("REMOTE", &c.Remote)
	str("TRAU_REPO_ROOT", &c.RepoRoot)
	str("PROVIDER", &c.Provider)
	if provider != "" {
		c.Provider = provider
		sources["PROVIDER"] = LayerCLI
	}
	str("TRACKER_PROVIDER", &c.TrackerProvider)
	str("CLAUDE_CONFIG", &c.ClaudeConfig)
	str("CODEX_CONFIG", &c.CodexConfig)
	str("KIMI_CONFIG", &c.KimiConfig)

	claudeFile, claudeSrc, err := providerFile("CLAUDE_CONFIG")
	if err != nil {
		return c, sources, err
	}
	codexFile, codexSrc, err := providerFile("CODEX_CONFIG")
	if err != nil {
		return c, sources, err
	}
	kimiFile, kimiSrc, err := providerFile("KIMI_CONFIG")
	if err != nil {
		return c, sources, err
	}

	providerStr(claudeFile, claudeSrc, "CLAUDE_BIN", &c.ClaudeBin)
	providerStr(claudeFile, claudeSrc, "CLAUDE_FLAGS", &c.ClaudeFlags)
	num("AGENT_TIMEOUT", &c.AgentTimeout)
	providerStr(claudeFile, claudeSrc, "CLAUDE_MODEL", &c.ClaudeModel)
	providerStr(claudeFile, claudeSrc, "CLAUDE_DISALLOWED_TOOLS", &c.ClaudeDisallowedTools)
	providerStr(claudeFile, claudeSrc, "CLAUDE_EFFORT", &c.ClaudeEffort)
	providerStr(codexFile, codexSrc, "CODEX_BIN", &c.CodexBin)
	providerStr(codexFile, codexSrc, "CODEX_FLAGS", &c.CodexFlags)
	providerStr(codexFile, codexSrc, "CODEX_PROFILE", &c.CodexProfile)
	providerStr(codexFile, codexSrc, "CODEX_MODEL", &c.CodexModel)
	providerStr(codexFile, codexSrc, "CODEX_EFFORT", &c.CodexEffort)
	providerStr(kimiFile, kimiSrc, "KIMI_BIN", &c.KimiBin)
	providerStr(kimiFile, kimiSrc, "KIMI_FLAGS", &c.KimiFlags)
	providerStr(kimiFile, kimiSrc, "KIMI_MODEL", &c.KimiModel)

	routes := map[string]string{}
	phaseGet := func(key string) (string, Layer) {
		v, src := get(key)
		return v, src.name
	}
	switch c.Provider {
	case "claude":
		phaseGet = func(key string) (string, Layer) {
			v, src := providerGet(claudeFile, claudeSrc, key, get)
			return v, src.name
		}
	case "codex":
		phaseGet = func(key string) (string, Layer) {
			v, src := providerGet(codexFile, codexSrc, key, get)
			return v, src.name
		}
	case "kimi":
		phaseGet = func(key string) (string, Layer) {
			v, src := providerGet(kimiFile, kimiSrc, key, get)
			return v, src.name
		}
	}
	addProviderPhaseRoutesWithSources(routes, sources, c.Provider, c, phaseGet)
	if len(routes) > 0 {
		c.Routes = routes
	}
	num("MAX_ITERATIONS", &c.MaxIterations)
	num("MAX_REPAIRS", &c.MaxRepairs)
	num("MAX_BUGFIXES", &c.MaxBugfixes)
	if v, src := get("AUTO_MERGE"); v != "" {
		c.AutoMerge = v == "1"
		sources["AUTO_MERGE"] = src.name
	}
	str("MERGE_METHOD", &c.MergeMethod)
	num("CI_TIMEOUT", &c.CITimeout)
	num("CI_POLL", &c.CIPoll)
	str("EXPECTED_CHECKS", &c.ExpectedChecks)
	str("BROWSER_VERIFY", &c.BrowserVerify)
	str("APP_URL", &c.AppURL)
	if v, src := get("TRAU_TUI"); v != "" {
		c.TUI = v == "1"
		sources["TRAU_TUI"] = src.name
	}
	if v, src := get("EPIC_FLOW"); v != "" {
		c.EpicFlow = v == "1"
		sources["EPIC_FLOW"] = src.name
	}
	str("RUNS_DIR", &c.RunsDir)

	return c, sources, nil
}

var phases = []string{"build", "handoff", "verify", "repair", "bugfix", "commit", "pick"}

func addProviderPhaseRoutesWithSources(routes map[string]string, sources map[string]Layer, provider string, c Config, get func(string) (string, Layer)) {
	var defaultModel, defaultEffort string
	switch provider {
	case "claude":
		defaultModel = c.ClaudeModel
		defaultEffort = c.ClaudeEffort
	case "codex":
		defaultModel = c.CodexModel
		defaultEffort = c.CodexEffort
	case "kimi":
		defaultModel = c.KimiModel

	default:
		return
	}

	prefix := strings.ToUpper(provider) + "_"
	for _, ph := range phases {
		phasePrefix := prefix + strings.ToUpper(ph) + "_"
		model, modelSrc := get(phasePrefix + "MODEL")
		effort, effortSrc := get(phasePrefix + "EFFORT")
		if model == "" && effort == "" {
			continue
		}
		if model == "" {
			model = defaultModel
			modelSrc = sources[prefix+"MODEL"]
			if modelSrc == "" {
				modelSrc = LayerDefault
			}
		}
		if effort == "" {
			effort = defaultEffort
			effortSrc = sources[prefix+"EFFORT"]
			if effortSrc == "" {
				effortSrc = LayerDefault
			}
		}
		routes[ph] = routeSpec(provider, model, effort)
		if sources != nil {
			sources[phasePrefix+"MODEL"] = modelSrc
			sources[phasePrefix+"EFFORT"] = effortSrc
		}
	}
}

func routeSpec(provider, model, effort string) string {
	if effort != "" {
		return provider + ":" + model + ":" + effort
	}
	if model != "" {
		return provider + ":" + model
	}
	return provider
}

// ResolveRepoRoot locates the target app repo, per ADR 0001 §2: the --repo flag
// wins, else TRAU_REPO_ROOT (env/trau.ini, passed
// as envRoot), else the current directory's git top-level via gitTop. gitTop is
// injected so the fallback is testable without a real repo; production callers pass
// GitToplevel. All git/gh operations act on the resolved root, never the trau tree.
func ResolveRepoRoot(flagRepo, envRoot string, gitTop func() (string, error)) (string, error) {
	if flagRepo != "" {
		return flagRepo, nil
	}
	if envRoot != "" {
		return envRoot, nil
	}
	return gitTop()
}

// GitToplevel returns the current directory's git top-level (git rev-parse
// --show-toplevel) — the third and final fallback for locating the target repo. The
// error names the explicit overrides so an out-of-repo invocation is actionable.
func GitToplevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", errors.New("not inside a git repository — pass --repo <path> or set TRAU_REPO_ROOT")
	}
	return strings.TrimSpace(string(out)), nil
}

func resolveSiblingPath(basePath, p string) string {
	if p == "" || filepath.IsAbs(p) || basePath == "" {
		return p
	}
	dir := filepath.Dir(basePath)
	if dir == "." || dir == "" {
		return p
	}
	return filepath.Join(dir, p)
}

func dirOf(path string) string {
	if path == "" {
		return ""
	}
	dir := filepath.Dir(path)
	if dir == "." {
		return ""
	}
	return dir
}

func ParseEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		out[key] = unquote(stripInlineComment(line[eq+1:]))
	}
	return out, sc.Err()
}

// WriteProjectEnv writes a tidy project-level env file with the supplied
// key/value pairs. It creates the parent directory if needed. The order is
// stable so the generated file reads like the example template. Existing keys
// in the file are preserved; values override them.
func WriteProjectEnv(path string, values map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	merged, _ := ParseEnvFile(path)
	for k, v := range values {
		merged[k] = v
	}
	order := []string{
		"LINEAR_TEAM",
		"READY_LABEL",
		"QUARANTINE_LABEL",
		"PROJECT",
		"BASE_BRANCH",
		"REMOTE",
		"TRAU_REPO_ROOT",
		"PROVIDER",
	}
	var b strings.Builder
	b.WriteString("# Trau project-level configuration.\n")
	b.WriteString("# Generated by `trau` first-run onboarding.\n\n")
	for _, key := range order {
		if v, ok := merged[key]; ok {
			_, _ = fmt.Fprintf(&b, "%s=%s\n", key, v)
			delete(merged, key)
		}
	}
	var extras []string
	for key := range merged {
		extras = append(extras, key)
	}
	sort.Strings(extras)
	for _, key := range extras {
		_, _ = fmt.Fprintf(&b, "%s=%s\n", key, merged[key])
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// WriteEnvFile writes key/value pairs to an env file, preserving existing
// content and comments when the file already exists. Keys in values are added
// or updated; other lines are left unchanged. This is used for user-level
// config (~/.trau.ini) so onboarding does not clobber personal settings.
func WriteEnvFile(path string, values map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	existing, _ := ParseEnvFile(path)
	for k, v := range values {
		existing[k] = v
	}

	var lines []string
	seen := map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		for _, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") {
				lines = append(lines, raw)
				continue
			}
			eq := strings.IndexByte(line, '=')
			if eq <= 0 {
				lines = append(lines, raw)
				continue
			}
			key := strings.TrimSpace(line[:eq])
			if v, ok := existing[key]; ok {
				lines = append(lines, fmt.Sprintf("%s=%s", key, v))
				seen[key] = true
			}
		}
	}

	for key, v := range existing {
		if seen[key] {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s=%s", key, v))
	}

	// Trim trailing blank lines then add one newline.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func stripInlineComment(v string) string {
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			return v[:i]
		}
	}
	return v
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// KeyMeta describes a single known configuration key for the settings editor.
type KeyMeta struct {
	Key         string
	Default     string
	Advanced    bool
	Description string
	// Options enumerates the allowed values; when set the editor offers a
	// picker rather than free text.
	Options []string
	// Bool marks a 1/0 toggle key, rendered as an on/off switch.
	Bool bool
}

// KnownKeys returns the canonical list of editable configuration keys. The
// order is the order the editor presents them; advanced keys are hidden behind
// a toggle by default.
func KnownKeys() []KeyMeta {
	return []KeyMeta{
		{Key: "LINEAR_TEAM", Description: "Linear team / Jira project / GitHub repo"},
		{Key: "LINEAR_API_KEY", Advanced: true, Description: "Linear personal API key"},
		{Key: "TRACKER_PROVIDER", Default: "linear", Description: "Ticket backend: linear | jira | github", Options: []string{"linear", "jira", "github"}},
		{Key: "READY_LABEL", Default: "ready-for-agent", Description: "Label that marks tickets ready for the loop"},
		{Key: "QUARANTINE_LABEL", Default: "needs-human", Description: "Label applied when a ticket fails"},
		{Key: "PROJECT", Description: "Optional Linear project for filed bugs"},
		{Key: "BASE_BRANCH", Default: "main", Description: "Default git base branch"},
		{Key: "REMOTE", Default: "origin", Description: "Git remote name"},
		{Key: "TRAU_REPO_ROOT", Description: "Target app repo path"},
		{Key: "PROVIDER", Default: "claude", Description: "AI provider: claude | codex | kimi", Options: []string{"claude", "codex", "kimi"}},
		{Key: "CLAUDE_CONFIG", Advanced: true, Description: "Provider-local Claude config file"},
		{Key: "CODEX_CONFIG", Advanced: true, Description: "Provider-local Codex config file"},
		{Key: "KIMI_CONFIG", Advanced: true, Description: "Provider-local Kimi config file"},
		{Key: "CLAUDE_BIN", Advanced: true, Default: "claude", Description: "Claude Code binary"},
		{Key: "CLAUDE_FLAGS", Advanced: true, Default: "--dangerously-skip-permissions", Description: "Extra flags passed to Claude"},
		{Key: "AGENT_TIMEOUT", Advanced: true, Default: "900", Description: "Per-agent call timeout in seconds"},
		{Key: "CLAUDE_MODEL", Advanced: true, Description: "Default Claude model"},
		{Key: "CLAUDE_DISALLOWED_TOOLS", Advanced: true, Default: "Agent,Workflow", Description: "Tools disabled inside agents"},
		{Key: "CLAUDE_EFFORT", Advanced: true, Description: "Default Claude reasoning effort"},
		{Key: "CODEX_BIN", Advanced: true, Default: "codex", Description: "Codex binary"},
		{Key: "CODEX_FLAGS", Advanced: true, Default: "--dangerously-bypass-approvals-and-sandbox", Description: "Extra flags passed to Codex"},
		{Key: "CODEX_PROFILE", Advanced: true, Description: "Codex exec profile"},
		{Key: "CODEX_MODEL", Advanced: true, Description: "Default Codex model"},
		{Key: "CODEX_EFFORT", Advanced: true, Description: "Default Codex reasoning effort"},
		{Key: "KIMI_BIN", Advanced: true, Default: "kimi", Description: "Kimi binary"},
		{Key: "KIMI_FLAGS", Advanced: true, Description: "Extra flags passed to Kimi"},
		{Key: "KIMI_MODEL", Advanced: true, Description: "Default Kimi model"},
		{Key: "MAX_ITERATIONS", Default: "15", Description: "Maximum tickets per run"},
		{Key: "MAX_REPAIRS", Default: "2", Description: "Verify-fail quick repair attempts before bugfix"},
		{Key: "MAX_BUGFIXES", Default: "2", Description: "Comprehensive bugfix passes after quick repairs are exhausted"},
		{Key: "AUTO_MERGE", Default: "1", Description: "Merge on green CI (1 = yes, 0 = no)", Bool: true},
		{Key: "MERGE_METHOD", Default: "squash", Description: "Merge strategy: squash | merge | rebase", Options: []string{"squash", "merge", "rebase"}},
		{Key: "CI_TIMEOUT", Default: "600", Description: "Seconds to wait for CI checks"},
		{Key: "CI_POLL", Default: "30", Description: "Seconds between CI polls"},
		{Key: "EXPECTED_CHECKS", Description: "Required CI check names (comma-separated)"},
		{Key: "BROWSER_VERIFY", Default: "auto", Description: "Browser verify: auto | always | never", Options: []string{"auto", "always", "never"}},
		{Key: "APP_URL", Default: "http://localhost", Description: "Local app URL for browser verify"},
		{Key: "TRAU_TUI", Default: "1", Description: "Enable Bubble Tea TUI (1 = yes, 0 = no)", Bool: true},
		{Key: "EPIC_FLOW", Default: "1", Description: "Process epic sub-issues (1 = yes, 0 = no)", Bool: true},
		{Key: "RUNS_DIR", Default: "runs", Description: "Directory for run artifacts"},
		{Key: "CLAUDE_BUILD_MODEL", Advanced: true, Description: "Claude model for build phase"},
		{Key: "CLAUDE_BUILD_EFFORT", Advanced: true, Description: "Claude effort for build phase"},
		{Key: "CLAUDE_HANDOFF_MODEL", Advanced: true, Description: "Claude model for handoff phase"},
		{Key: "CLAUDE_HANDOFF_EFFORT", Advanced: true, Description: "Claude effort for handoff phase"},
		{Key: "CLAUDE_VERIFY_MODEL", Advanced: true, Description: "Claude model for verify phase"},
		{Key: "CLAUDE_VERIFY_EFFORT", Advanced: true, Description: "Claude effort for verify phase"},
		{Key: "CLAUDE_REPAIR_MODEL", Advanced: true, Description: "Claude model for repair phase"},
		{Key: "CLAUDE_REPAIR_EFFORT", Advanced: true, Description: "Claude effort for repair phase"},
		{Key: "CLAUDE_BUGFIX_MODEL", Advanced: true, Description: "Claude model for comprehensive bugfix phase"},
		{Key: "CLAUDE_BUGFIX_EFFORT", Advanced: true, Description: "Claude effort for comprehensive bugfix phase"},
		{Key: "CLAUDE_COMMIT_MODEL", Advanced: true, Description: "Claude model for commit phase"},
		{Key: "CLAUDE_COMMIT_EFFORT", Advanced: true, Description: "Claude effort for commit phase"},
		{Key: "CLAUDE_PICK_MODEL", Advanced: true, Description: "Claude model for pick phase"},
		{Key: "CLAUDE_PICK_EFFORT", Advanced: true, Description: "Claude effort for pick phase"},
		{Key: "CODEX_BUILD_MODEL", Advanced: true, Description: "Codex model for build phase"},
		{Key: "CODEX_BUILD_EFFORT", Advanced: true, Description: "Codex effort for build phase"},
		{Key: "CODEX_HANDOFF_MODEL", Advanced: true, Description: "Codex model for handoff phase"},
		{Key: "CODEX_HANDOFF_EFFORT", Advanced: true, Description: "Codex effort for handoff phase"},
		{Key: "CODEX_VERIFY_MODEL", Advanced: true, Description: "Codex model for verify phase"},
		{Key: "CODEX_VERIFY_EFFORT", Advanced: true, Description: "Codex effort for verify phase"},
		{Key: "CODEX_REPAIR_MODEL", Advanced: true, Description: "Codex model for repair phase"},
		{Key: "CODEX_REPAIR_EFFORT", Advanced: true, Description: "Codex effort for repair phase"},
		{Key: "CODEX_BUGFIX_MODEL", Advanced: true, Description: "Codex model for comprehensive bugfix phase"},
		{Key: "CODEX_BUGFIX_EFFORT", Advanced: true, Description: "Codex effort for comprehensive bugfix phase"},
		{Key: "CODEX_COMMIT_MODEL", Advanced: true, Description: "Codex model for commit phase"},
		{Key: "CODEX_COMMIT_EFFORT", Advanced: true, Description: "Codex effort for commit phase"},
		{Key: "CODEX_PICK_MODEL", Advanced: true, Description: "Codex model for pick phase"},
		{Key: "CODEX_PICK_EFFORT", Advanced: true, Description: "Codex effort for pick phase"},
	}
}

// ResolveConfigItems returns every known config key with its effective value and
// the layer that supplied it. The result is sorted in the order of KnownKeys.
// CLI-sourced values are only recorded when opts supplies the override.
func ResolveConfigItems(cfg Config, localPath, projectPath, userPath string, provider string, opts Options) ([]ConfigItem, error) {
	_, sources, err := LoadLayeredWithSources(projectPath, userPath, localPath, provider)
	if err != nil {
		return nil, err
	}
	if opts.Provider != "" {
		sources["PROVIDER"] = LayerCLI
	}
	if opts.Repo != "" {
		sources["TRAU_REPO_ROOT"] = LayerCLI
	}

	items := make([]ConfigItem, 0, len(KnownKeys()))
	for _, meta := range KnownKeys() {
		value := keyValue(cfg, meta.Key)
		if value == "" {
			value = meta.Default
		}
		layer := sources[meta.Key]
		if layer == "" {
			layer = LayerDefault
		}
		items = append(items, ConfigItem{
			Key:         meta.Key,
			Value:       value,
			Layer:       layer,
			Advanced:    meta.Advanced,
			Options:     meta.Options,
			Bool:        meta.Bool,
			Description: meta.Description,
			Default:     meta.Default,
		})
	}
	return items, nil
}

func keyValue(cfg Config, key string) string {
	switch key {
	case "LINEAR_TEAM":
		return cfg.LinearTeam
	case "LINEAR_API_KEY":
		return cfg.LinearAPIKey
	case "READY_LABEL":
		return cfg.ReadyLabel
	case "QUARANTINE_LABEL":
		return cfg.QuarantineLabel
	case "PROJECT":
		return cfg.Project
	case "BASE_BRANCH":
		return cfg.BaseBranch
	case "REMOTE":
		return cfg.Remote
	case "TRAU_REPO_ROOT":
		return cfg.RepoRoot
	case "PROVIDER":
		return cfg.Provider
	case "TRACKER_PROVIDER":
		return cfg.TrackerProvider
	case "CLAUDE_CONFIG":
		return cfg.ClaudeConfig
	case "CODEX_CONFIG":
		return cfg.CodexConfig
	case "KIMI_CONFIG":
		return cfg.KimiConfig
	case "CLAUDE_BIN":
		return cfg.ClaudeBin
	case "CLAUDE_FLAGS":
		return cfg.ClaudeFlags
	case "AGENT_TIMEOUT":
		return strconv.Itoa(cfg.AgentTimeout)
	case "CLAUDE_MODEL":
		return cfg.ClaudeModel
	case "CLAUDE_DISALLOWED_TOOLS":
		return cfg.ClaudeDisallowedTools
	case "CLAUDE_EFFORT":
		return cfg.ClaudeEffort
	case "CODEX_BIN":
		return cfg.CodexBin
	case "CODEX_FLAGS":
		return cfg.CodexFlags
	case "CODEX_PROFILE":
		return cfg.CodexProfile
	case "CODEX_MODEL":
		return cfg.CodexModel
	case "CODEX_EFFORT":
		return cfg.CodexEffort
	case "KIMI_BIN":
		return cfg.KimiBin
	case "KIMI_FLAGS":
		return cfg.KimiFlags
	case "KIMI_MODEL":
		return cfg.KimiModel
	case "MAX_ITERATIONS":
		return strconv.Itoa(cfg.MaxIterations)
	case "MAX_REPAIRS":
		return strconv.Itoa(cfg.MaxRepairs)
	case "MAX_BUGFIXES":
		return strconv.Itoa(cfg.MaxBugfixes)
	case "AUTO_MERGE":
		if cfg.AutoMerge {
			return "1"
		}
		return "0"
	case "MERGE_METHOD":
		return cfg.MergeMethod
	case "CI_TIMEOUT":
		return strconv.Itoa(cfg.CITimeout)
	case "CI_POLL":
		return strconv.Itoa(cfg.CIPoll)
	case "EXPECTED_CHECKS":
		return cfg.ExpectedChecks
	case "BROWSER_VERIFY":
		return cfg.BrowserVerify
	case "APP_URL":
		return cfg.AppURL
	case "TRAU_TUI":
		if cfg.TUI {
			return "1"
		}
		return "0"
	case "EPIC_FLOW":
		if cfg.EpicFlow {
			return "1"
		}
		return "0"
	case "RUNS_DIR":
		return cfg.RunsDir
	case "CLAUDE_BUILD_MODEL", "CLAUDE_HANDOFF_MODEL", "CLAUDE_VERIFY_MODEL", "CLAUDE_REPAIR_MODEL", "CLAUDE_BUGFIX_MODEL", "CLAUDE_COMMIT_MODEL", "CLAUDE_PICK_MODEL":
		return phaseRouteModel(cfg.Routes, "claude", key)
	case "CLAUDE_BUILD_EFFORT", "CLAUDE_HANDOFF_EFFORT", "CLAUDE_VERIFY_EFFORT", "CLAUDE_REPAIR_EFFORT", "CLAUDE_BUGFIX_EFFORT", "CLAUDE_COMMIT_EFFORT", "CLAUDE_PICK_EFFORT":
		return phaseRouteEffort(cfg.Routes, "claude", key)
	case "CODEX_BUILD_MODEL", "CODEX_HANDOFF_MODEL", "CODEX_VERIFY_MODEL", "CODEX_REPAIR_MODEL", "CODEX_BUGFIX_MODEL", "CODEX_COMMIT_MODEL", "CODEX_PICK_MODEL":
		return phaseRouteModel(cfg.Routes, "codex", key)
	case "CODEX_BUILD_EFFORT", "CODEX_HANDOFF_EFFORT", "CODEX_VERIFY_EFFORT", "CODEX_REPAIR_EFFORT", "CODEX_BUGFIX_EFFORT", "CODEX_COMMIT_EFFORT", "CODEX_PICK_EFFORT":
		return phaseRouteEffort(cfg.Routes, "codex", key)
	}
	return ""
}

func phaseRouteModel(routes map[string]string, provider, key string) string {
	phase := phaseFromRouteKey(key)
	if phase == "" {
		return ""
	}
	routeProvider, model, _ := parseRouteSpec(routes[phase])
	if routeProvider == provider || provider == "kimi" {
		return model
	}
	return ""
}

func phaseRouteEffort(routes map[string]string, provider, key string) string {
	phase := phaseFromRouteKey(key)
	if phase == "" {
		return ""
	}
	_, _, effort := parseRouteSpec(routes[phase])
	return effort
}

func phaseFromRouteKey(key string) string {
	prefix := ""
	switch {
	case strings.HasPrefix(key, "CLAUDE_"):
		prefix = "CLAUDE_"
	case strings.HasPrefix(key, "CODEX_"):
		prefix = "CODEX_"
	default:
		return ""
	}
	rest := strings.TrimPrefix(key, prefix)
	for _, ph := range phases {
		if strings.HasPrefix(rest, strings.ToUpper(ph)+"_") {
			return ph
		}
	}
	return ""
}

func parseRouteSpec(spec string) (provider, model, effort string) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) > 0 {
		provider = parts[0]
	}
	if len(parts) > 1 {
		model = parts[1]
	}
	if len(parts) > 2 {
		effort = parts[2]
	}
	return
}

// WriteConfigLayer writes value for key to the named layer's config file. The
// path arguments are the same ones passed to LoadLayered. layer must be one of
// "local", "project", or "user".
func WriteConfigLayer(layer, localPath, projectPath, userPath, key, value string) error {
	switch layer {
	case "local":
		return WriteEnvFile(localPath, map[string]string{key: value})
	case "project":
		return WriteProjectEnv(projectPath, map[string]string{key: value})
	case "user":
		return WriteEnvFile(userPath, map[string]string{key: value})
	default:
		return fmt.Errorf("unsupported config layer %q (expected local|project|user)", layer)
	}
}
