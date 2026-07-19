// Package doctor runs a preflight health check so users can surface setup
// problems before the loop fails mid-phase. All diagnostics are written to
// stderr; the package never touches stdout.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
	"github.com/RomkaLTU/trau/internal/transcriptdb"
	"github.com/RomkaLTU/trau/internal/webserver"
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
// default rather than an explicit setting. version is this binary's version,
// compared against the version a running hub reports.
func Run(ctx context.Context, cfg config.Config, sources map[string]config.Layer, repoRoot, version string, stderr io.Writer) (*Report, error) {
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
	checkWritePerms(repoRoot, rr)
	checkWebHub(ctx, cfg, version, rr)
	checkHubDatabase(rr)
	checkTranscriptDatabase(rr)
	checkLegacyRegistration(rr)
	checkLegacyQueue(repoRoot, rr)
	checkLegacyRunData(cfg, repoRoot, rr)

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

	provider := cfg.EffectiveTrackerProvider()
	switch provider {
	case "internal":
		rr.add("tracker", pass, "internal (no external tracker configured — issues live in the hub)", "")
	case "linear", "jira", "github":
		rr.add("tracker", pass, provider, "")
	default:
		rr.add("tracker", fail, fmt.Sprintf("unknown tracker provider %q", cfg.TrackerProvider), "set TRACKER_PROVIDER to linear | jira | github | internal")
	}

	if provider == "linear" && strings.TrimSpace(cfg.LinearTeam) == "" {
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

// checkWritePerms probes that the repo root is writable, where the loop stages
// git work. The file-era runs-dir writability probe is gone: under ADR 0008 the
// loop writes no durable run data to disk — it flows to the hub.
func checkWritePerms(repoRoot string, rr *runner) {
	if repoRoot == "" {
		return
	}
	if err := probeWrite(filepath.Join(repoRoot, ".trau-doctor-write-test")); err != nil {
		rr.add("write: repo", fail, fmt.Sprintf("cannot write inside repo root: %v", err), "check directory permissions")
		return
	}
	rr.add("write: repo", pass, "repo root is writable", "")
}

func resolveRunsDir(cfg config.Config, repoRoot string) string {
	runsDir := cfg.RunsDir
	if runsDir == "" {
		runsDir = ".trau/runs"
	}
	if !filepath.IsAbs(runsDir) && repoRoot != "" {
		runsDir = filepath.Join(repoRoot, runsDir)
	}
	return runsDir
}

var hubProbeTimeout = 2 * time.Second

// checkWebHub probes the configured serve hub so "the web UI didn't come up" is
// answerable here instead of with lsof and curl. Only a refused connection means
// nothing is listening; a listener that answers wrong — or accepts and then goes
// quiet until the deadline — is an occupied port, which is a different fix.
func checkWebHub(ctx context.Context, cfg config.Config, version string, rr *runner) {
	addr := net.JoinHostPort(webserver.DialHost(cfg.ServeBind), strconv.Itoa(cfg.ServePort))
	ctx, cancel := context.WithTimeout(ctx, hubProbeTimeout)
	defer cancel()

	h, err := probeHubHealth(ctx, "http://"+addr+webserver.APIPrefix+"/health", cfg.ServeToken)
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		reportHubDown(cfg, addr, rr)
		return
	case err != nil:
		rr.add("web hub", warn, fmt.Sprintf("%s is not answering as a trau hub: %v", addr, err),
			"set SERVE_PORT to a free port, or stop whatever owns this one")
		return
	}
	uptime := time.Duration(h.UptimeSeconds * float64(time.Second)).Round(time.Second)
	if h.Version != version {
		rr.add("web hub", warn, fmt.Sprintf("running at %s serving version %s, up %s — this binary is %s", addr, h.Version, uptime, version),
			"run `trau hub restart`")
		return
	}
	rr.add("web hub", pass, fmt.Sprintf("running at %s (version %s, up %s)", addr, h.Version, uptime), "")
}

// reportHubDown names what would (or would not) bring the hub up, so a port with
// nothing on it is distinguishable from a hub nothing is allowed to start.
func reportHubDown(cfg config.Config, addr string, rr *runner) {
	if err := webserver.CheckExposure(cfg.ServeBind, cfg.ServeToken); err != nil {
		rr.add("web hub", warn, fmt.Sprintf("not running on %s, and autostart is blocked: %v", addr, err),
			"set SERVE_TOKEN, or bind loopback-only with SERVE_BIND=127.0.0.1")
		return
	}
	if !cfg.ServeAutostart {
		rr.add("web hub", warn, fmt.Sprintf("not running on %s (SERVE_AUTOSTART=0 — nothing will bring it up)", addr),
			"run `trau serve`, or set SERVE_AUTOSTART=1")
		return
	}
	rr.add("web hub", pass, fmt.Sprintf("not running on %s (SERVE_AUTOSTART=1 — the next run starts it)", addr), "")
}

func probeHubHealth(ctx context.Context, url, token string) (webserver.Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return webserver.Health{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return webserver.Health{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return webserver.Health{}, fmt.Errorf("health probe returned HTTP %d", resp.StatusCode)
	}
	var h webserver.Health
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&h); err != nil {
		return webserver.Health{}, fmt.Errorf("health probe returned no usable payload: %w", err)
	}
	if h.Status != "ok" {
		return webserver.Health{}, fmt.Errorf("health probe reported status %q", h.Status)
	}
	return h, nil
}

// dbHealth is one database's health, projected off either database package so
// reportDatabase can render both the same way.
type dbHealth struct {
	path      string
	exists    bool
	version   int
	sizeBytes int64
	err       error
}

// checkHubDatabase reports the authoritative hub database (opened only by `trau
// serve`) read-only: its path, schema version, on-disk size, and integrity. A
// database not yet created is fine — serve creates it on start.
func checkHubDatabase(rr *runner) {
	h := hubdb.CheckHealth(registry.Home())
	reportDatabase("hub database", dbHealth{h.Path, h.Exists, h.Version, h.SizeBytes, h.Err},
		fmt.Sprintf("move %s aside and restart `trau serve` to recreate it", h.Path), rr)
}

// checkTranscriptDatabase reports the separate transcript database the same way.
// It is the one store safe to delete wholesale (ADR 0008 §4), so a bad file is
// still only a failure the recreate hint clears.
func checkTranscriptDatabase(rr *runner) {
	h := transcriptdb.CheckHealth(registry.Home())
	reportDatabase("transcript database", dbHealth{h.Path, h.Exists, h.Version, h.SizeBytes, h.Err},
		fmt.Sprintf("delete %s and restart `trau serve` to recreate it empty (it holds only transcripts)", h.Path), rr)
}

func reportDatabase(name string, h dbHealth, failSuggestion string, rr *runner) {
	switch {
	case h.err != nil:
		rr.add(name, fail, fmt.Sprintf("%s cannot be opened: %v", h.path, h.err), failSuggestion)
	case !h.exists:
		rr.add(name, pass, fmt.Sprintf("%s (created on first `trau serve`)", h.path), "")
	default:
		rr.add(name, pass, fmt.Sprintf("%s (schema v%d, %s, healthy)", h.path, h.version, humanBytes(h.sizeBytes)), "")
	}
}

// checkLegacyRunData flags file-era run-data files left under the runs dir by the
// pre-DB-first era (ADR 0008): per-ticket state, phase logs and artifacts, the
// lessons ledger, and orphaned event/token streams. The hub imports the
// importable ones on first serve touch; their presence means an unmigrated
// install.
func checkLegacyRunData(cfg config.Config, repoRoot string, rr *runner) {
	runsDir := resolveRunsDir(cfg, repoRoot)
	if runsDir == "" {
		return
	}
	leftover := hubstore.LegacyRunDataFiles(runsDir)
	if len(leftover) == 0 {
		rr.add("legacy run data", pass, "no legacy run-data files", "")
		return
	}
	rr.add("legacy run data", warn,
		fmt.Sprintf("%d legacy run-data file(s) still present under %s (e.g. %s)", len(leftover), runsDir, sampleRunData(leftover)),
		"start `trau serve` once to import checkpoints, artifacts, lessons, and phase logs; the rest is then safe to delete")
}

// sampleRunData names the first few leftover files by their ticket-dir/base so the
// warning is concrete without dumping a long list.
func sampleRunData(paths []string) string {
	const max = 3
	if len(paths) > max {
		paths = paths[:max]
	}
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Join(filepath.Base(filepath.Dir(p)), filepath.Base(p))
	}
	return strings.Join(names, ", ")
}

// humanBytes renders a byte count with a binary unit suffix for the DB size line.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// checkLegacyRegistration flags a repos.json or workspace.json left behind by the
// file era. The hub imports and deletes these on first serve start (ADR 0007);
// one still present means a half-completed upgrade.
func checkLegacyRegistration(rr *runner) {
	leftover := hubstore.LegacyFiles(registry.Home())
	if len(leftover) == 0 {
		rr.add("legacy registration", pass, "no legacy registration files", "")
		return
	}
	rr.add("legacy registration", warn,
		fmt.Sprintf("legacy registration file(s) still present: %s", strings.Join(leftover, ", ")),
		"start `trau serve` once to import and remove them")
}

// checkLegacyQueue flags a repo's file-era .trau/queue.json left behind by the
// upgrade to the hub database (ADR 0007). The hub imports and deletes it on
// first touch; one still present means a half-completed upgrade.
func checkLegacyQueue(repoRoot string, rr *runner) {
	if repoRoot == "" {
		return
	}
	path, present := hubstore.LegacyQueueFile(repoRoot)
	if !present {
		rr.add("legacy queue", pass, "no legacy queue file", "")
		return
	}
	rr.add("legacy queue", warn,
		fmt.Sprintf("legacy queue file still present: %s", path),
		"start `trau serve` once to import and remove it")
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
