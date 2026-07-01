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
	IssuePrefix     string
	LinearAPIKey    string
	ReadyLabel      string
	QuarantineLabel string
	SplitLabel      string
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
	AgentCols             int
	AgentRows             int
	AgentStallWindow      int
	AgentRetries          int
	AgentBackoff          int
	FallbackProviders     []string
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
	RequireCI      bool
	// RequireRepoChanges gates the post-build empty-diff guard: when on (default),
	// a build that left the managed repo unchanged faults instead of advancing to a
	// hollow handoff or empty PR. Set 0 for the rare legitimately no-op ticket.
	RequireRepoChanges bool
	// SizeJudge gates the pre-flight ticket-size guard: when on (default), a cheap
	// LLM judge sizes each ticket before the build, quarantining (or, in an attended
	// single-ticket run, warning about) one too large to finish in a single build
	// window. Set 0 to disable it entirely (no extra call, zero added cost).
	SizeJudge bool
	// LintFix gates the pre-verify lint-fix step: when on (default), the project's
	// automated lint/format fixers run over the working tree just before verify so
	// the verify gate isn't spent self-healing mechanical style noise. LintFixCmd, if
	// set, is that command (run deterministically, zero tokens); left empty, a cheap
	// agent step detects and runs the project's fixers instead. Set LintFix 0 to skip
	// the step entirely.
	LintFix    bool
	LintFixCmd string
	// Cleanup gates the pre-verify cleanup step: when on (default), a cheap agent
	// pass strips AI-slop from the slice's uncommitted diff — unnecessary comments,
	// dead code, over-defensive scaffolding — before verify grades the result. Set
	// Cleanup 0 to skip it.
	Cleanup bool

	BrowserVerify string
	AppURL        string
	VerifyChecks  bool

	VerifyPanel       []string
	VerifyPanelPolicy string

	TUI bool

	LiveView bool

	EpicFlow bool

	// UsageWindow enables the HUD's provider rate-limit window probe (claude OAuth
	// usage, codex app-server, kimi balance). On by default; every probe is
	// metadata-only and fails closed to token/cost totals, so it is safe to leave
	// on. UsageWindowPTY additionally permits the brittle pseudo-terminal /usage
	// scrape — the only route to Kimi-Code-subscription usage — and is off by
	// default because it spawns a second interactive CLI.
	UsageWindow    bool
	UsageWindowPTY bool

	// Lessons enables the durable lessons memory: failure→fix records are appended
	// to runs/memory/lessons.jsonl and relevant ones are recalled into later
	// build/verify/repair prompts. LessonsDistill additionally runs a cheap agent
	// pass to distill a richer takeaway (default off — the free mechanical record
	// is always written when Lessons is on).
	Lessons        bool
	LessonsDistill bool

	// Opt-in, per-ticket time tracking. Off by default: when TimelogEnabled is
	// false none of the time-log code runs and trau behaves exactly as before.
	// Storage is repo|user|none; OutputFormat selects the export rendering;
	// Estimator picks the per-ticket effort estimate (deterministic heuristic, or
	// a cheap agent call). See internal/timelog.
	TimelogEnabled      bool
	TimelogStorage      string
	TimelogOutputFormat string
	TimelogEstimator    string

	RunsDir string

	// Spend ceilings off the normalized token/cost ledger. Zero = no cap
	// (back-compat: a config with no MAX_* knobs enforces nothing). USD caps use
	// the notional cost estimate; token caps the raw total. See internal/budget.
	MaxTicketUSD    float64
	MaxTicketTokens int
	MaxDailyUSD     float64
	MaxDailyTokens  int
}

// Defaults returns the built-in configuration used when neither the env file
// nor the environment supplies a value.
func Defaults() Config {
	return Config{
		LinearTeam:            "",
		IssuePrefix:           "",
		ReadyLabel:            "ready-for-agent",
		QuarantineLabel:       "needs-human",
		SplitLabel:            "needs-split",
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
		AgentCols:             120,
		AgentRows:             40,
		AgentStallWindow:      180,
		AgentRetries:          2,
		AgentBackoff:          10,
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
		RequireCI:             true,
		RequireRepoChanges:    true,
		SizeJudge:             true,
		LintFix:               true,
		Cleanup:               true,
		BrowserVerify:         "auto",
		AppURL:                "http://localhost",
		VerifyChecks:          true,
		VerifyPanelPolicy:     "unanimous",
		TUI:                   true,
		EpicFlow:              true,
		UsageWindow:           true,
		UsageWindowPTY:        false,
		Lessons:               true,
		LessonsDistill:        false,
		TimelogEnabled:        false,
		TimelogStorage:        "repo",
		TimelogOutputFormat:   "default",
		TimelogEstimator:      "heuristic",
		RunsDir:               ".trau/runs",
		MaxTicketUSD:          0,
		MaxTicketTokens:       0,
		MaxDailyUSD:           0,
		MaxDailyTokens:        0,
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
	fnum := func(key string, dst *float64) {
		v, src := get(key)
		if v != "" {
			if f, e := strconv.ParseFloat(strings.TrimSpace(v), 64); e == nil {
				*dst = f
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
	str("ISSUE_PREFIX", &c.IssuePrefix)
	str("LINEAR_API_KEY", &c.LinearAPIKey)
	str("READY_LABEL", &c.ReadyLabel)
	str("QUARANTINE_LABEL", &c.QuarantineLabel)
	str("SPLIT_LABEL", &c.SplitLabel)
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
	num("AGENT_COLS", &c.AgentCols)
	num("AGENT_ROWS", &c.AgentRows)
	num("AGENT_STALL_WINDOW", &c.AgentStallWindow)
	num("AGENT_RETRIES", &c.AgentRetries)
	num("AGENT_BACKOFF", &c.AgentBackoff)
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
	if v, src := get("FALLBACK_PROVIDERS"); v != "" {
		c.FallbackProviders = splitCSV(v)
		sources["FALLBACK_PROVIDERS"] = src.name
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
	if v, src := get("REQUIRE_CI"); v != "" {
		c.RequireCI = v == "1"
		sources["REQUIRE_CI"] = src.name
	}
	if v, src := get("REQUIRE_REPO_CHANGES"); v != "" {
		c.RequireRepoChanges = v == "1"
		sources["REQUIRE_REPO_CHANGES"] = src.name
	}
	if v, src := get("SIZE_JUDGE"); v != "" {
		c.SizeJudge = v == "1"
		sources["SIZE_JUDGE"] = src.name
	}
	if v, src := get("LINT_FIX"); v != "" {
		c.LintFix = v == "1"
		sources["LINT_FIX"] = src.name
	}
	str("LINT_FIX_CMD", &c.LintFixCmd)
	if v, src := get("CLEANUP"); v != "" {
		c.Cleanup = v == "1"
		sources["CLEANUP"] = src.name
	}
	str("BROWSER_VERIFY", &c.BrowserVerify)
	str("APP_URL", &c.AppURL)
	if v, src := get("VERIFY_CHECKS"); v != "" {
		c.VerifyChecks = v == "1"
		sources["VERIFY_CHECKS"] = src.name
	}
	if v, src := get("VERIFY_PANEL"); v != "" {
		c.VerifyPanel = splitCSV(v)
		sources["VERIFY_PANEL"] = src.name
	}
	str("VERIFY_PANEL_POLICY", &c.VerifyPanelPolicy)
	if v, src := get("TRAU_TUI"); v != "" {
		c.TUI = v == "1"
		sources["TRAU_TUI"] = src.name
	}
	if v, src := get("EPIC_FLOW"); v != "" {
		c.EpicFlow = v == "1"
		sources["EPIC_FLOW"] = src.name
	}
	if v, src := get("LESSONS"); v != "" {
		c.Lessons = v == "1"
		sources["LESSONS"] = src.name
	}
	if v, src := get("LESSONS_DISTILL"); v != "" {
		c.LessonsDistill = v == "1"
		sources["LESSONS_DISTILL"] = src.name
	}
	if v, src := get("TIMELOG_ENABLED"); v != "" {
		c.TimelogEnabled = v == "1"
		sources["TIMELOG_ENABLED"] = src.name
	}
	str("TIMELOG_STORAGE", &c.TimelogStorage)
	str("TIMELOG_OUTPUT_FORMAT", &c.TimelogOutputFormat)
	str("TIMELOG_ESTIMATOR", &c.TimelogEstimator)
	if v, src := get("USAGE_WINDOW"); v != "" {
		c.UsageWindow = v == "1"
		sources["USAGE_WINDOW"] = src.name
	}
	if v, src := get("USAGE_WINDOW_PTY"); v != "" {
		c.UsageWindowPTY = v == "1"
		sources["USAGE_WINDOW_PTY"] = src.name
	}
	str("RUNS_DIR", &c.RunsDir)
	fnum("MAX_TICKET_USD", &c.MaxTicketUSD)
	num("MAX_TICKET_TOKENS", &c.MaxTicketTokens)
	fnum("MAX_DAILY_USD", &c.MaxDailyUSD)
	num("MAX_DAILY_TOKENS", &c.MaxDailyTokens)

	c.IssuePrefix = ResolvePrefix(c.IssuePrefix, c.LinearTeam)

	return c, sources, nil
}

// ResolvePrefix settles the issue-identifier prefix used for ticket-ID parsing,
// branch inference, and sentinel matching. An explicit ISSUE_PREFIX wins; failing
// that the tracker team/project key is the natural source (a Linear team keyed COD
// owns COD-123 issues); failing both it falls back to COD for back-compat. The
// result is always upper-cased and trimmed so downstream regexes are stable.
func ResolvePrefix(prefix, team string) string {
	if p := strings.ToUpper(strings.TrimSpace(prefix)); p != "" {
		return p
	}
	if t := strings.ToUpper(strings.TrimSpace(team)); t != "" {
		return t
	}
	return "COD"
}

// ValidatePrefix checks that a ticket id supplied on the command line matches the
// resolved issue prefix. The pre-config arg scan accepts any <PREFIX>-<n> shape; this
// is the after-load gate that rejects a TMS-5 run against a COD-configured repo
// before branch/sentinel parsing silently mismatches. An empty id is a no-op.
func ValidatePrefix(id, prefix string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	want := strings.ToUpper(strings.TrimSpace(prefix))
	got := ""
	if i := strings.LastIndex(id, "-"); i > 0 {
		got = strings.ToUpper(id[:i])
	}
	if got != want {
		return fmt.Errorf("ticket %q does not match the configured issue prefix %s- (got %s-)", id, want, got)
	}
	return nil
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
		"EPIC_FLOW",
		"TIMELOG_ENABLED",
		"TIMELOG_STORAGE",
		"TIMELOG_OUTPUT_FORMAT",
		"TIMELOG_ESTIMATOR",
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

// splitCSV parses a comma-separated knob into a trimmed, non-empty list.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
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
		{Key: "ISSUE_PREFIX", Description: "Issue-ID prefix for ticket parsing (default: the team key, e.g. COD, TMS, ENG)"},
		{Key: "LINEAR_API_KEY", Advanced: true, Description: "Linear personal API key"},
		{Key: "TRACKER_PROVIDER", Default: "linear", Description: "Ticket backend: linear | jira | github", Options: []string{"linear", "jira", "github"}},
		{Key: "READY_LABEL", Default: "ready-for-agent", Description: "Label that marks tickets ready for the loop"},
		{Key: "QUARANTINE_LABEL", Default: "needs-human", Description: "Label applied when a ticket fails"},
		{Key: "PROJECT", Description: "Linear project this repo owns — scopes the ready queue, guards cross-project runs, and targets filed bugs"},
		{Key: "BASE_BRANCH", Default: "main", Description: "Default git base branch"},
		{Key: "REMOTE", Default: "origin", Description: "Git remote name"},
		{Key: "TRAU_REPO_ROOT", Description: "Target app repo path"},
		{Key: "PROVIDER", Default: "claude", Description: "AI provider: claude | codex | kimi", Options: []string{"claude", "codex", "kimi"}},
		{Key: "CLAUDE_CONFIG", Advanced: true, Description: "Provider-local Claude config file"},
		{Key: "CODEX_CONFIG", Advanced: true, Description: "Provider-local Codex config file"},
		{Key: "KIMI_CONFIG", Advanced: true, Description: "Provider-local Kimi config file"},
		{Key: "CLAUDE_BIN", Advanced: true, Default: "claude", Description: "Claude Code binary"},
		{Key: "CLAUDE_FLAGS", Advanced: true, Default: "--dangerously-skip-permissions", Description: "Extra flags passed to Claude"},
		{Key: "AGENT_TIMEOUT", Advanced: true, Default: "900", Description: "Per-agent call hard timeout in seconds"},
		{Key: "AGENT_COLS", Advanced: true, Default: "120", Description: "Width (columns) of the agent PTY; the live view (TUI w / trau watch) reconstructs at this size"},
		{Key: "AGENT_ROWS", Advanced: true, Default: "40", Description: "Height (rows) of the agent PTY; the live view reconstructs at this size"},
		{Key: "AGENT_STALL_WINDOW", Advanced: true, Default: "180", Description: "Kill+recover an agent step that emits no output for this many seconds, before AGENT_TIMEOUT (0 = disabled)"},
		{Key: "AGENT_RETRIES", Advanced: true, Default: "2", Description: "Transient-failure retries (timeout/stall/crash) per provider before falling back / parking the ticket"},
		{Key: "AGENT_BACKOFF", Advanced: true, Default: "10", Description: "Base seconds to wait between transient agent-step retries"},
		{Key: "FALLBACK_PROVIDERS", Advanced: true, Description: "Ordered provider[:model[:effort]] specs to try after the primary's retries are exhausted (e.g. codex,kimi). Empty = retry-only, no provider fallback"},
		{Key: "CLAUDE_MODEL", Advanced: true, Description: "Default Claude model"},
		{Key: "CLAUDE_EFFORT", Advanced: true, Description: "Default Claude reasoning effort"},
		{Key: "CLAUDE_DISALLOWED_TOOLS", Advanced: true, Default: "Agent,Workflow", Description: "Tools disabled inside agents"},
		{Key: "CODEX_BIN", Advanced: true, Default: "codex", Description: "Codex binary"},
		{Key: "CODEX_FLAGS", Advanced: true, Default: "--dangerously-bypass-approvals-and-sandbox", Description: "Extra flags passed to Codex"},
		{Key: "CODEX_PROFILE", Advanced: true, Description: "Codex exec profile"},
		{Key: "CODEX_MODEL", Advanced: true, Description: "Default Codex model"},
		{Key: "CODEX_EFFORT", Advanced: true, Description: "Default Codex reasoning effort"},
		{Key: "KIMI_BIN", Advanced: true, Default: "kimi", Description: "Kimi binary"},
		{Key: "KIMI_FLAGS", Advanced: true, Description: "Extra flags passed to Kimi"},
		{Key: "KIMI_MODEL", Advanced: true, Description: "Default Kimi model alias (from your kimi config.toml [models.*])"},
		{Key: "MAX_ITERATIONS", Default: "15", Description: "Maximum tickets per run"},
		{Key: "MAX_REPAIRS", Default: "2", Description: "Verify-fail quick repair attempts before bugfix"},
		{Key: "MAX_BUGFIXES", Default: "2", Description: "Comprehensive bugfix passes after quick repairs are exhausted"},
		{Key: "AUTO_MERGE", Default: "1", Description: "Merge on green CI (1 = yes, 0 = no)", Bool: true},
		{Key: "MERGE_METHOD", Default: "squash", Description: "Merge strategy: squash | merge | rebase", Options: []string{"squash", "merge", "rebase"}},
		{Key: "CI_TIMEOUT", Default: "600", Description: "Seconds to wait for CI checks"},
		{Key: "CI_POLL", Default: "30", Description: "Seconds between CI polls"},
		{Key: "EXPECTED_CHECKS", Description: "Required CI check names (comma-separated)"},
		{Key: "REQUIRE_CI", Default: "1", Description: "Gate merge on CI; set 0 for repos with no PR CI (1 = yes, 0 = no)", Bool: true},
		{Key: "SIZE_JUDGE", Advanced: true, Default: "1", Description: "Pre-flight LLM size judge: quarantine (or warn) tickets too big for one build window (1 = yes, 0 = no)", Bool: true},
		{Key: "SPLIT_LABEL", Advanced: true, Default: "needs-split", Description: "Label applied to a ticket the size judge flags as too large to build in one window"},
		{Key: "LINT_FIX", Default: "1", Description: "Run the project's lint/format autofixers before verify so verify isn't spent self-healing style noise (1 = yes, 0 = no)", Bool: true},
		{Key: "LINT_FIX_CMD", Description: "Deterministic lint-fix command run before verify (e.g. vendor/bin/pint, npm run lint:fix). Empty = a cheap agent auto-detects and runs the project's fixers"},
		{Key: "CLEANUP", Default: "1", Description: "Strip AI-slop (unnecessary comments, dead code, over-defensive scaffolding) from the slice's diff before verify (1 = yes, 0 = no)", Bool: true},
		{Key: "BROWSER_VERIFY", Default: "auto", Description: "Browser verify: auto | always | never", Options: []string{"auto", "always", "never"}},
		{Key: "APP_URL", Default: "http://localhost", Description: "Local app URL for browser verify"},
		{Key: "VERIFY_CHECKS", Default: "1", Description: "Run the pluggable verify-check library (.trau/checks); 1 = yes, 0 = no", Bool: true},
		{Key: "VERIFY_PANEL", Description: "Cross-vendor verify panel: comma-separated provider:model:effort verifiers (e.g. claude,codex:gpt-5.5,kimi). Empty = single verifier"},
		{Key: "VERIFY_PANEL_POLICY", Default: "unanimous", Description: "Panel verdict merge policy: unanimous | majority | any-pass", Options: []string{"unanimous", "majority", "any-pass"}},
		{Key: "TRAU_TUI", Default: "1", Description: "Enable Bubble Tea TUI (1 = yes, 0 = no)", Bool: true},
		{Key: "EPIC_FLOW", Default: "1", Description: "Process epic sub-issues (1 = yes, 0 = no)", Bool: true},
		{Key: "TIMELOG_ENABLED", Default: "0", Description: "Write a per-ticket effort time log (JSON) after merge (opt-in; 1 = yes, 0 = no)", Bool: true},
		{Key: "TIMELOG_STORAGE", Default: "repo", Description: "Time-log location: repo (<repo>/.dev-flow/time/) | user (~/.dev-flow/time/<repo>/) | none", Options: []string{"repo", "user", "none"}},
		{Key: "TIMELOG_OUTPUT_FORMAT", Default: "default", Description: "Time-log export rendering: default (JSON) | jira-worklog | toggl-csv | plain", Options: []string{"default", "jira-worklog", "toggl-csv", "plain"}},
		{Key: "TIMELOG_ESTIMATOR", Default: "heuristic", Description: "Per-ticket effort estimate: heuristic (deterministic table) | agent (cheap agent call)", Options: []string{"heuristic", "agent"}},
		{Key: "RUNS_DIR", Default: ".trau/runs", Description: "Directory for run artifacts"},
		{Key: "MAX_TICKET_USD", Description: "Per-ticket USD spend cap; over it the ticket is quarantined (empty = no cap)"},
		{Key: "MAX_TICKET_TOKENS", Description: "Per-ticket token spend cap; over it the ticket is quarantined (empty = no cap)"},
		{Key: "MAX_DAILY_USD", Description: "Per-day USD spend cap across all tickets; reaching it stops the run (empty = no cap)"},
		{Key: "MAX_DAILY_TOKENS", Description: "Per-day token spend cap across all tickets; reaching it stops the run (empty = no cap)"},
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
		{Key: "KIMI_BUILD_MODEL", Advanced: true, Description: "Kimi model for build phase"},
		{Key: "KIMI_HANDOFF_MODEL", Advanced: true, Description: "Kimi model for handoff phase"},
		{Key: "KIMI_VERIFY_MODEL", Advanced: true, Description: "Kimi model for verify phase"},
		{Key: "KIMI_REPAIR_MODEL", Advanced: true, Description: "Kimi model for repair phase"},
		{Key: "KIMI_BUGFIX_MODEL", Advanced: true, Description: "Kimi model for comprehensive bugfix phase"},
		{Key: "KIMI_COMMIT_MODEL", Advanced: true, Description: "Kimi model for commit phase"},
		{Key: "KIMI_PICK_MODEL", Advanced: true, Description: "Kimi model for pick phase"},
	}
}

// ProviderTuningMeta enumerates the execution knobs a provider exposes, so the
// settings UI can offer valid pickers instead of free text. Models are
// suggestions (custom values are still allowed); Efforts is the exact set the
// provider's CLI accepts.
type ProviderTuningMeta struct {
	Name    string
	Models  []string
	Efforts []string
}

// ProviderTuningMetas returns the per-provider model/effort option sets used by
// the in-TUI provider settings panel. Effort values reflect each CLI's real
// knob: Claude --effort, Codex -c model_reasoning_effort, Kimi
// KIMI_MODEL_THINKING_EFFORT.
func ProviderTuningMetas() []ProviderTuningMeta {
	return []ProviderTuningMeta{
		{
			Name:    "claude",
			Models:  []string{"opus", "sonnet", "haiku"},
			Efforts: []string{"low", "medium", "high", "xhigh", "max"},
		},
		{
			Name:    "codex",
			Models:  []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini"},
			Efforts: []string{"minimal", "low", "medium", "high", "xhigh"},
		},
		{
			// Kimi Code selects models by alias from the user's config.toml, not by
			// a fixed ID, so Models is populated dynamically by ResolveProviderTunings
			// from KimiModelAliases. It exposes no usable reasoning-effort knob in
			// headless runs (the KIMI_MODEL_THINKING_EFFORT env var only applies via
			// the KIMI_MODEL_* env-provider mechanism), so Efforts is empty.
			Name:    "kimi",
			Models:  nil,
			Efforts: nil,
		},
	}
}

// kimiConfigPath returns the location of the Kimi Code CLI config.toml:
// $KIMI_CODE_HOME/config.toml when set, else ~/.kimi-code/config.toml.
func kimiConfigPath() string {
	if home := os.Getenv("KIMI_CODE_HOME"); home != "" {
		return filepath.Join(home, "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kimi-code", "config.toml")
}

// KimiModelAliases returns the model alias names defined in the user's Kimi Code
// config.toml ([models.<alias>] tables). Kimi's --model flag takes one of these
// user-defined aliases — there is no fixed documented list — so the settings UI
// offers them as real, typo-proof choices. Returns nil when the config is
// absent or defines no models. It scans table headers rather than parsing TOML
// to avoid a dependency for a read this shallow.
func KimiModelAliases() []string {
	path := kimiConfigPath()
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var aliases []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "[models.") || !strings.HasSuffix(line, "]") {
			continue
		}
		alias := strings.TrimSuffix(strings.TrimPrefix(line, "[models."), "]")
		alias = strings.Trim(alias, "\"")
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true
		aliases = append(aliases, alias)
	}
	return aliases
}

// ProviderTuningField is one resolved value plus the layer that supplied it.
type ProviderTuningField struct {
	Value string
	Layer Layer
}

// ProviderPhaseTuning is one phase's model/effort for a provider: the raw
// per-phase override (empty Value means "inherit the default") alongside the
// effective value that actually runs after applying the inherit fallback.
type ProviderPhaseTuning struct {
	Phase     string
	Model     ProviderTuningField
	Effort    ProviderTuningField
	EffModel  string
	EffEffort string
}

// ProviderTuning is the full execution-tuning picture for one provider, consumed
// by the provider settings panel: option sets, default model/effort, and the
// per-phase overrides with their effective resolution.
type ProviderTuning struct {
	Name    string
	Active  bool
	Models  []string
	Efforts []string
	Model   ProviderTuningField
	Effort  ProviderTuningField
	Phases  []ProviderPhaseTuning
}

// ResolveProviderTunings returns the execution-tuning state for every provider.
// Values are read across the standard config layers (env > user > project >
// local); per-phase keys fall back to the provider default for their effective
// value. activeProvider marks which provider the loop currently runs.
func ResolveProviderTunings(localPath, projectPath, userPath, activeProvider string) []ProviderTuning {
	local, _ := ParseEnvFile(localPath)
	proj, _ := ParseEnvFile(projectPath)
	user, _ := ParseEnvFile(userPath)

	rawGet := func(key string) (string, Layer) {
		if !strings.HasPrefix(key, "TRAU_") {
			if v := os.Getenv("TRAU_" + key); v != "" {
				return v, LayerEnv
			}
		}
		if v := os.Getenv(key); v != "" {
			return v, LayerEnv
		}
		if v, ok := user[key]; ok {
			return v, LayerUser
		}
		if v, ok := proj[key]; ok {
			return v, LayerProject
		}
		if v, ok := local[key]; ok {
			return v, LayerLocal
		}
		return "", LayerDefault
	}
	field := func(key string) ProviderTuningField {
		v, l := rawGet(key)
		return ProviderTuningField{Value: v, Layer: l}
	}

	out := make([]ProviderTuning, 0, len(ProviderTuningMetas()))
	for _, meta := range ProviderTuningMetas() {
		prefix := strings.ToUpper(meta.Name) + "_"
		hasEffort := len(meta.Efforts) > 0
		models := meta.Models
		if meta.Name == "kimi" {
			models = KimiModelAliases()
		}
		model := field(prefix + "MODEL")
		effort := ProviderTuningField{}
		if hasEffort {
			effort = field(prefix + "EFFORT")
		}
		pt := ProviderTuning{
			Name:    meta.Name,
			Active:  meta.Name == activeProvider,
			Models:  models,
			Efforts: meta.Efforts,
			Model:   model,
			Effort:  effort,
		}
		for _, ph := range phases {
			pp := strings.ToUpper(ph)
			pm := field(prefix + pp + "_MODEL")
			pe := ProviderTuningField{}
			effEffort := ""
			if hasEffort {
				pe = field(prefix + pp + "_EFFORT")
				effEffort = pe.Value
				if effEffort == "" {
					effEffort = effort.Value
				}
			}
			effModel := pm.Value
			if effModel == "" {
				effModel = model.Value
			}
			pt.Phases = append(pt.Phases, ProviderPhaseTuning{
				Phase:     ph,
				Model:     pm,
				Effort:    pe,
				EffModel:  effModel,
				EffEffort: effEffort,
			})
		}
		out = append(out, pt)
	}
	return out
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
	case "ISSUE_PREFIX":
		return cfg.IssuePrefix
	case "LINEAR_API_KEY":
		return cfg.LinearAPIKey
	case "READY_LABEL":
		return cfg.ReadyLabel
	case "QUARANTINE_LABEL":
		return cfg.QuarantineLabel
	case "SPLIT_LABEL":
		return cfg.SplitLabel
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
	case "AGENT_COLS":
		return strconv.Itoa(cfg.AgentCols)
	case "AGENT_ROWS":
		return strconv.Itoa(cfg.AgentRows)
	case "AGENT_STALL_WINDOW":
		return strconv.Itoa(cfg.AgentStallWindow)
	case "AGENT_RETRIES":
		return strconv.Itoa(cfg.AgentRetries)
	case "AGENT_BACKOFF":
		return strconv.Itoa(cfg.AgentBackoff)
	case "FALLBACK_PROVIDERS":
		return strings.Join(cfg.FallbackProviders, ",")
	case "CLAUDE_MODEL":
		return cfg.ClaudeModel
	case "CLAUDE_EFFORT":
		return cfg.ClaudeEffort
	case "CLAUDE_DISALLOWED_TOOLS":
		return cfg.ClaudeDisallowedTools
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
	case "REQUIRE_CI":
		if cfg.RequireCI {
			return "1"
		}
		return "0"
	case "SIZE_JUDGE":
		if cfg.SizeJudge {
			return "1"
		}
		return "0"
	case "LINT_FIX":
		if cfg.LintFix {
			return "1"
		}
		return "0"
	case "LINT_FIX_CMD":
		return cfg.LintFixCmd
	case "CLEANUP":
		if cfg.Cleanup {
			return "1"
		}
		return "0"
	case "BROWSER_VERIFY":
		return cfg.BrowserVerify
	case "APP_URL":
		return cfg.AppURL
	case "VERIFY_CHECKS":
		if cfg.VerifyChecks {
			return "1"
		}
		return "0"
	case "VERIFY_PANEL":
		return strings.Join(cfg.VerifyPanel, ",")
	case "VERIFY_PANEL_POLICY":
		return cfg.VerifyPanelPolicy
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
	case "TIMELOG_ENABLED":
		if cfg.TimelogEnabled {
			return "1"
		}
		return "0"
	case "TIMELOG_STORAGE":
		return cfg.TimelogStorage
	case "TIMELOG_OUTPUT_FORMAT":
		return cfg.TimelogOutputFormat
	case "TIMELOG_ESTIMATOR":
		return cfg.TimelogEstimator
	case "RUNS_DIR":
		return cfg.RunsDir
	case "MAX_TICKET_USD":
		return floatValue(cfg.MaxTicketUSD)
	case "MAX_TICKET_TOKENS":
		return intValue(cfg.MaxTicketTokens)
	case "MAX_DAILY_USD":
		return floatValue(cfg.MaxDailyUSD)
	case "MAX_DAILY_TOKENS":
		return intValue(cfg.MaxDailyTokens)
	case "CLAUDE_BUILD_MODEL", "CLAUDE_HANDOFF_MODEL", "CLAUDE_VERIFY_MODEL", "CLAUDE_REPAIR_MODEL", "CLAUDE_BUGFIX_MODEL", "CLAUDE_COMMIT_MODEL", "CLAUDE_PICK_MODEL":
		return phaseRouteModel(cfg.Routes, "claude", key)
	case "CLAUDE_BUILD_EFFORT", "CLAUDE_HANDOFF_EFFORT", "CLAUDE_VERIFY_EFFORT", "CLAUDE_REPAIR_EFFORT", "CLAUDE_BUGFIX_EFFORT", "CLAUDE_COMMIT_EFFORT", "CLAUDE_PICK_EFFORT":
		return phaseRouteEffort(cfg.Routes, "claude", key)
	case "CODEX_BUILD_MODEL", "CODEX_HANDOFF_MODEL", "CODEX_VERIFY_MODEL", "CODEX_REPAIR_MODEL", "CODEX_BUGFIX_MODEL", "CODEX_COMMIT_MODEL", "CODEX_PICK_MODEL":
		return phaseRouteModel(cfg.Routes, "codex", key)
	case "CODEX_BUILD_EFFORT", "CODEX_HANDOFF_EFFORT", "CODEX_VERIFY_EFFORT", "CODEX_REPAIR_EFFORT", "CODEX_BUGFIX_EFFORT", "CODEX_COMMIT_EFFORT", "CODEX_PICK_EFFORT":
		return phaseRouteEffort(cfg.Routes, "codex", key)
	case "KIMI_BUILD_MODEL", "KIMI_HANDOFF_MODEL", "KIMI_VERIFY_MODEL", "KIMI_REPAIR_MODEL", "KIMI_BUGFIX_MODEL", "KIMI_COMMIT_MODEL", "KIMI_PICK_MODEL":
		return phaseRouteModel(cfg.Routes, "kimi", key)
	}
	return ""
}

// intValue renders an integer config value, treating 0 as unset ("") so the
// settings editor shows the key as empty rather than a literal 0.
func intValue(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// floatValue renders a float config value as the shortest decimal, treating 0 as
// unset ("").
func floatValue(f float64) string {
	if f == 0 {
		return ""
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
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
	case strings.HasPrefix(key, "KIMI_"):
		prefix = "KIMI_"
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
