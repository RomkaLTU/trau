// Package doctor runs a preflight health check so users can surface setup
// problems before the loop fails mid-phase. All diagnostics are written to
// stderr; the package never touches stdout.
package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// Report is the outcome of a doctor run.
type Report struct {
	Checks   []Check
	Errors   int
	Warnings int
}

// Check is one preflight result.
type Check struct {
	Name    string
	Status  string // pass | fail | warn
	Message string
}

// Run executes every preflight check and returns a non-nil error when at least
// one required check failed. Warnings do not fail the run.
//
// sources maps each resolved config key to the layer that supplied its value;
// a key absent from the map (or marked LayerDefault) is using a built-in
// default rather than an explicit setting.
func Run(ctx context.Context, cfg config.Config, sources map[string]config.Layer, repoRoot string, stderr io.Writer) (*Report, error) {
	w := &writer{out: stderr}
	rr := &runner{w: w, r: &Report{}}

	w.header("trau doctor")

	checkGit(ctx, rr)
	checkGitHub(ctx, rr)
	checkProvider(ctx, cfg, rr)
	checkConfig(ctx, cfg, sources, repoRoot, rr)
	checkLinearLabels(ctx, cfg, rr)
	checkLinearProject(ctx, cfg, rr)
	checkJira(ctx, cfg, rr)
	checkWritePerms(cfg, repoRoot, rr)
	checkHubDatabase(rr)

	w.blank()
	if rr.r.Errors > 0 {
		w.fail(fmt.Sprintf("%d check(s) failed — fix the ✗ items above before running the loop", rr.r.Errors))
		return rr.r, fmt.Errorf("doctor: %d check(s) failed", rr.r.Errors)
	}
	if rr.r.Warnings > 0 {
		w.pass(fmt.Sprintf("all required checks passed (%d warning(s))", rr.r.Warnings))
	} else {
		w.pass("all checks passed")
	}
	return rr.r, nil
}

type runner struct {
	w *writer
	r *Report
}

func (rr *runner) add(name, status, message, suggestion string) {
	c := Check{Name: name, Status: status, Message: message}
	if suggestion != "" {
		c.Message += "\n    → " + suggestion
	}
	rr.r.Checks = append(rr.r.Checks, c)
	switch status {
	case fail:
		rr.r.Errors++
	case warn:
		rr.r.Warnings++
	}
	rr.w.check(name, status, c.Message)
}

func checkGit(ctx context.Context, rr *runner) {
	bin, err := exec.LookPath("git")
	if err != nil {
		rr.add("git", fail, "git is not on PATH", "install git and make sure it is on your PATH")
		return
	}
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		rr.add("git", fail, "git found but --version failed", "verify your git installation")
		return
	}
	rr.add("git", pass, strings.TrimSpace(string(out)), "")
}

func checkGitHub(ctx context.Context, rr *runner) {
	bin, err := exec.LookPath("gh")
	if err != nil {
		rr.add("gh", fail, "gh is not on PATH", "install the GitHub CLI (https://cli.github.com)")
		return
	}
	if err := exec.CommandContext(ctx, bin, "auth", "status").Run(); err != nil {
		rr.add("gh", fail, "gh is installed but not authenticated", "run `gh auth login`")
		return
	}
	rr.add("gh", pass, "authenticated", "")
}

func checkProvider(ctx context.Context, cfg config.Config, rr *runner) {
	var bin string
	switch cfg.Provider {
	case "claude":
		bin = cfg.ClaudeBin
	case "codex":
		bin = cfg.CodexBin
	case "kimi":
		bin = cfg.KimiBin
	default:
		rr.add("provider", fail, fmt.Sprintf("unknown provider %q", cfg.Provider), "set PROVIDER to claude | codex | kimi")
		return
	}
	if bin == "" {
		bin = cfg.Provider
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		rr.add("provider", fail, fmt.Sprintf("%s (%s) not found on PATH", cfg.Provider, bin), fmt.Sprintf("install %s or set %s_BIN", cfg.Provider, strings.ToUpper(cfg.Provider)))
		return
	}
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		rr.add("provider", warn, fmt.Sprintf("%s found at %s but --version failed (%v)", cfg.Provider, path, err), "")
		return
	}
	rr.add("provider", pass, fmt.Sprintf("%s (%s)", cfg.Provider, strings.TrimSpace(string(out))), "")
}

func checkConfig(ctx context.Context, cfg config.Config, sources map[string]config.Layer, repoRoot string, rr *runner) {
	if repoRoot == "" {
		rr.add("repo", fail, "no target repo resolved", "pass --repo <path>, set TRAU_REPO_ROOT, or run inside a git repository")
		return
	}
	fi, err := os.Stat(repoRoot)
	if err != nil || !fi.IsDir() {
		rr.add("repo", fail, fmt.Sprintf("repo root %q does not exist or is not a directory", repoRoot), "check --repo / TRAU_REPO_ROOT")
		return
	}
	if err := exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", "--git-dir").Run(); err != nil {
		rr.add("repo", fail, fmt.Sprintf("%q is not a git repository", repoRoot), "point --repo at a git checkout")
		return
	}
	rr.add("repo", pass, repoRoot, "")

	switch cfg.TrackerProvider {
	case "linear", "jira", "github":
		rr.add("tracker", pass, cfg.TrackerProvider, "")
	default:
		rr.add("tracker", fail, fmt.Sprintf("unknown tracker provider %q", cfg.TrackerProvider), "set TRACKER_PROVIDER to linear | jira | github")
	}

	if cfg.TrackerProvider == "linear" && strings.TrimSpace(cfg.LinearTeam) == "" {
		rr.add("linear team", fail, "LINEAR_TEAM is empty", "set LINEAR_TEAM in trau.ini or environment")
	}

	required := []struct {
		key           string
		name          string
		suggestion    string
		warnOnDefault bool
	}{
		{"READY_LABEL", "ready label", "set READY_LABEL in trau.ini or environment", true},
		{"QUARANTINE_LABEL", "quarantine label", "set QUARANTINE_LABEL in trau.ini or environment", true},
		{"BASE_BRANCH", "base branch", "set BASE_BRANCH in trau.ini or environment", false},
		{"REMOTE", "remote", "set REMOTE in trau.ini or environment", false},
	}
	for _, r := range required {
		value := configValue(cfg, r.key)
		if strings.TrimSpace(value) == "" {
			rr.add(r.name, fail, fmt.Sprintf("%s is empty", r.key), r.suggestion)
			continue
		}
		if isDefault(sources, r.key) {
			if r.warnOnDefault {
				rr.add(r.name, warn, fmt.Sprintf("%s not set — using default %q", r.key, value), r.suggestion)
			} else {
				rr.add(r.name, pass, fmt.Sprintf("%s=%s (default)", r.key, value), "")
			}
			continue
		}
		rr.add(r.name, pass, fmt.Sprintf("%s=%s (%s)", r.key, value, sources[r.key]), "")
	}
}

func isDefault(sources map[string]config.Layer, key string) bool {
	if sources == nil {
		return true
	}
	src, ok := sources[key]
	return !ok || src == config.LayerDefault
}

func configValue(cfg config.Config, key string) string {
	switch key {
	case "READY_LABEL":
		return cfg.ReadyLabel
	case "QUARANTINE_LABEL":
		return cfg.QuarantineLabel
	case "BASE_BRANCH":
		return cfg.BaseBranch
	case "REMOTE":
		return cfg.Remote
	}
	return ""
}

func checkLinearLabels(ctx context.Context, cfg config.Config, rr *runner) {
	if cfg.TrackerProvider != "linear" {
		return
	}
	if strings.TrimSpace(cfg.LinearTeam) == "" {
		return
	}
	if strings.TrimSpace(cfg.LinearAPIKey) == "" {
		rr.add("linear labels", warn, "skipped label check (no LINEAR_API_KEY)", "set LINEAR_API_KEY to verify labels offline, or they will be checked at runtime via the Linear MCP")
		return
	}
	client := linearapi.New(cfg.LinearAPIKey)
	team, err := client.TeamByKey(ctx, cfg.LinearTeam)
	if err != nil {
		rr.add("linear labels", fail, fmt.Sprintf("could not look up team %q: %v", cfg.LinearTeam, err), "verify LINEAR_API_KEY and LINEAR_TEAM")
		return
	}
	labels, err := client.Labels(ctx, team.ID)
	if err != nil {
		rr.add("linear labels", fail, fmt.Sprintf("could not list labels for team %q: %v", cfg.LinearTeam, err), "verify LINEAR_API_KEY permissions")
		return
	}
	var missing []string
	for _, name := range []string{cfg.ReadyLabel, cfg.QuarantineLabel} {
		if _, ok := labels[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		rr.add("linear labels", fail, fmt.Sprintf("missing label(s) in team %q: %s", cfg.LinearTeam, strings.Join(missing, ", ")), "create the labels in Linear or run `trau` onboarding to auto-create them")
		return
	}
	rr.add("linear labels", pass, fmt.Sprintf("%s and %s exist in team %q", cfg.ReadyLabel, cfg.QuarantineLabel, cfg.LinearTeam), "")
}

// checkLinearProject flags the cross-project pick hole: with PROJECT unset,
// every ownership guard (the pick's project filter, the pre-branch refusal) is
// disabled, and on a Linear team hosting several projects the loop can pick a
// ticket that belongs to a different repo. With an API key the ready queue is
// inspected so the warning is precise; without one the hole is reported as-is.
func checkLinearProject(ctx context.Context, cfg config.Config, rr *runner) {
	if cfg.TrackerProvider != "linear" {
		return
	}
	if proj := strings.TrimSpace(cfg.Project); proj != "" {
		rr.add("linear project", pass, fmt.Sprintf("PROJECT=%s — picks are scoped to this repo's project", proj), "")
		return
	}
	suggestion := "set PROJECT=<Linear project name> in this repo's .trau.ini so tickets from other projects are refused"
	if strings.TrimSpace(cfg.LinearAPIKey) == "" || strings.TrimSpace(cfg.LinearTeam) == "" {
		rr.add("linear project", warn, "PROJECT is empty — cross-project guards are off, any ticket in the team can be picked here", suggestion)
		return
	}
	client := linearapi.New(cfg.LinearAPIKey)
	team, err := client.TeamByKey(ctx, cfg.LinearTeam)
	if err != nil {
		rr.add("linear project", warn, "PROJECT is empty — cross-project guards are off, any ticket in the team can be picked here", suggestion)
		return
	}
	candidates, err := client.Pick(ctx, team.ID, cfg.ReadyLabel)
	if err != nil {
		rr.add("linear project", warn, "PROJECT is empty — cross-project guards are off, any ticket in the team can be picked here", suggestion)
		return
	}
	projects := map[string]bool{}
	var names []string
	for _, c := range candidates {
		if name := strings.TrimSpace(c.Project.Name); name != "" && !projects[name] {
			projects[name] = true
			names = append(names, name)
		}
	}
	if len(names) > 1 {
		rr.add("linear project", warn, fmt.Sprintf("PROJECT is empty and the ready queue spans %d projects (%s) — a ticket from another repo's project can be picked here", len(names), strings.Join(names, ", ")), suggestion)
		return
	}
	rr.add("linear project", warn, "PROJECT is empty — the ready queue is currently single-project, but nothing stops a foreign ticket from being picked here later", suggestion)
}

// checkJira validates the Jira REST credentials and, when they are present,
// pings the site for a live auth check. Missing keys are a warning, not a
// failure: the tracker falls back to the Rovo MCP, so a single-account MCP user
// can still run. The token and Authorization header are never printed.
func checkJira(ctx context.Context, cfg config.Config, rr *runner) {
	if cfg.TrackerProvider != "jira" {
		return
	}
	var missing []string
	if strings.TrimSpace(cfg.JiraBaseURL) == "" {
		missing = append(missing, "JIRA_BASE_URL")
	}
	if strings.TrimSpace(cfg.JiraEmail) == "" {
		missing = append(missing, "JIRA_EMAIL")
	}
	if strings.TrimSpace(cfg.JiraAPIToken) == "" {
		missing = append(missing, "JIRA_API_TOKEN")
	}
	if len(missing) > 0 {
		rr.add("jira auth", warn,
			fmt.Sprintf("%s not set — Jira ops will go through the Rovo MCP", strings.Join(missing, ", ")),
			"set the Jira REST keys in ~/.trau.ini (or a per-repo .trau.ini) for fast direct API access")
		return
	}
	if err := jiraapi.New(cfg.JiraBaseURL, cfg.JiraEmail, cfg.JiraAPIToken).Ping(ctx); err != nil {
		if hint := jiraapi.AuthErrorMessage(err); hint != "" {
			rr.add("jira auth", fail, "Jira REST authentication failed", hint)
			return
		}
		rr.add("jira auth", fail, fmt.Sprintf("Jira REST ping failed: %v", err),
			"verify JIRA_BASE_URL is reachable and the token is valid")
		return
	}
	rr.add("jira auth", pass, fmt.Sprintf("authenticated to %s as %s", cfg.JiraBaseURL, cfg.JiraEmail), "")
}

func checkWritePerms(cfg config.Config, repoRoot string, rr *runner) {
	if repoRoot != "" {
		if err := probeWrite(filepath.Join(repoRoot, ".trau-doctor-write-test")); err != nil {
			rr.add("write: repo", fail, fmt.Sprintf("cannot write inside repo root: %v", err), "check directory permissions")
		} else {
			rr.add("write: repo", pass, "repo root is writable", "")
		}
	}

	runsDir := cfg.RunsDir
	if runsDir == "" {
		runsDir = ".trau/runs"
	}
	if !filepath.IsAbs(runsDir) && repoRoot != "" {
		runsDir = filepath.Join(repoRoot, runsDir)
	}
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		rr.add("write: runs dir", fail, fmt.Sprintf("cannot create runs dir %q: %v", runsDir, err), "check directory permissions or set RUNS_DIR")
		return
	}
	if err := probeWrite(filepath.Join(runsDir, ".trau-doctor-write-test")); err != nil {
		rr.add("write: runs dir", fail, fmt.Sprintf("cannot write to runs dir %q: %v", runsDir, err), "check directory permissions or set RUNS_DIR")
	} else {
		rr.add("write: runs dir", pass, fmt.Sprintf("%s is writable", runsDir), "")
	}
}

// checkHubDatabase reports the hub SQLite database (opened only by `trau serve`)
// read-only: its path, applied schema version, and whether it opens cleanly. A
// database that has not been created yet is fine — serve creates it on start.
func checkHubDatabase(rr *runner) {
	h := hubdb.CheckHealth(registry.Home())
	switch {
	case h.Err != nil:
		rr.add("hub database", fail, fmt.Sprintf("%s cannot be opened: %v", h.Path, h.Err),
			fmt.Sprintf("move %s aside and restart `trau serve` to recreate it", h.Path))
	case !h.Exists:
		rr.add("hub database", pass, fmt.Sprintf("%s (created on first `trau serve`)", h.Path), "")
	default:
		rr.add("hub database", pass, fmt.Sprintf("%s (schema v%d, healthy)", h.Path, h.Version), "")
	}
}

func probeWrite(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	_ = f.Close()
	return os.Remove(path)
}

const (
	pass = "pass"
	fail = "fail"
	warn = "warn"
)

type writer struct {
	out io.Writer
}

func (w *writer) header(s string) {
	_, _ = fmt.Fprintf(w.out, "\n=== %s ===\n\n", s)
}

func (w *writer) blank() {
	_, _ = fmt.Fprintln(w.out)
}

func (w *writer) pass(msg string) {
	_, _ = fmt.Fprintf(w.out, "✓ %s\n", msg)
}

func (w *writer) fail(msg string) {
	_, _ = fmt.Fprintf(w.out, "✗ %s\n", msg)
}

func (w *writer) check(name, status, message string) {
	glyph := "✓"
	switch status {
	case fail:
		glyph = "✗"
	case warn:
		glyph = "⚠"
	}
	_, _ = fmt.Fprintf(w.out, "%s %s: %s\n", glyph, name, message)
}
