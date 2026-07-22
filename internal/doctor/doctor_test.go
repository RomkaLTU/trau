package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/transcriptdb"
	"github.com/RomkaLTU/trau/internal/webserver"
)

func newTestRunner() *runner {
	return &runner{w: &writer{out: &bytes.Buffer{}}, r: &Report{}}
}

func lastCheck(t *testing.T, rr *runner) Check {
	t.Helper()
	if len(rr.r.Checks) == 0 {
		t.Fatal("no checks recorded")
	}
	return rr.r.Checks[len(rr.r.Checks)-1]
}

func TestCheckJiraSkippedForNonJira(t *testing.T) {
	rr := newTestRunner()
	checkJira(context.Background(), config.Config{TrackerProvider: "linear"}, rr)
	if len(rr.r.Checks) != 0 {
		t.Errorf("expected no jira check for linear provider, got %+v", rr.r.Checks)
	}
}

func TestCheckJiraWarnsOnMissingKeys(t *testing.T) {
	rr := newTestRunner()
	checkJira(context.Background(), config.Config{TrackerProvider: "jira", JiraBaseURL: "https://acme.atlassian.net"}, rr)
	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn", c.Status)
	}
	if rr.r.Warnings != 1 || rr.r.Errors != 0 {
		t.Errorf("warnings=%d errors=%d, want 1/0", rr.r.Warnings, rr.r.Errors)
	}
	if !strings.Contains(c.Message, "JIRA_EMAIL") || !strings.Contains(c.Message, "JIRA_API_TOKEN") {
		t.Errorf("message %q should name the missing keys", c.Message)
	}
}

func TestCheckJiraLiveAuthPass(t *testing.T) {
	const token = "s3cr3t-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/myself" {
			t.Errorf("ping path = %q, want /rest/api/3/myself", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"accountId":"x"}`))
	}))
	defer srv.Close()

	rr := newTestRunner()
	checkJira(context.Background(), config.Config{
		TrackerProvider: "jira", JiraBaseURL: srv.URL, JiraEmail: "me@acme.com", JiraAPIToken: token,
	}, rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q msg=%q, want pass", c.Status, c.Message)
	}
	if strings.Contains(c.Message, token) {
		t.Errorf("check message leaked the token: %q", c.Message)
	}
}

func TestCheckJiraLiveAuth401(t *testing.T) {
	const token = "s3cr3t-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	rr := newTestRunner()
	checkJira(context.Background(), config.Config{
		TrackerProvider: "jira", JiraBaseURL: srv.URL, JiraEmail: "me@acme.com", JiraAPIToken: token,
	}, rr)
	c := lastCheck(t, rr)
	if c.Status != fail {
		t.Errorf("status = %q, want fail", c.Status)
	}
	if rr.r.Errors != 1 {
		t.Errorf("errors = %d, want 1", rr.r.Errors)
	}
	if !strings.Contains(c.Message, jiraapi.TokenHelpURL) {
		t.Errorf("401 message %q should carry the regenerate URL", c.Message)
	}
	if strings.Contains(c.Message, token) {
		t.Errorf("check message leaked the token: %q", c.Message)
	}
}

func TestCheckLinearProjectSkippedForNonLinear(t *testing.T) {
	rr := newTestRunner()
	checkLinearProject(context.Background(), config.Config{TrackerProvider: "jira"}, rr)
	if len(rr.r.Checks) != 0 {
		t.Errorf("expected no linear project check for jira provider, got %+v", rr.r.Checks)
	}
}

func TestCheckLinearProjectPassWhenScoped(t *testing.T) {
	rr := newTestRunner()
	checkLinearProject(context.Background(), config.Config{TrackerProvider: "linear", Project: "trau"}, rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q, want pass", c.Status)
	}
	if !strings.Contains(c.Message, "PROJECT=trau") {
		t.Errorf("message %q should name the configured project", c.Message)
	}
}

func TestCheckJiraProjectSkippedForNonJira(t *testing.T) {
	rr := newTestRunner()
	checkJiraProject(config.Config{TrackerProvider: "linear", Project: "trau"}, rr)
	if len(rr.r.Checks) != 0 {
		t.Errorf("expected no jira project check for linear provider, got %+v", rr.r.Checks)
	}
}

func TestCheckJiraProjectPassOnProjectFallback(t *testing.T) {
	rr := newTestRunner()
	checkJiraProject(config.Config{TrackerProvider: "jira", Project: "MLG"}, rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q, want pass", c.Status)
	}
	if !strings.Contains(c.Message, "PROJECT=MLG") {
		t.Errorf("message %q should name the project key it falls back to", c.Message)
	}
}

func TestCheckJiraProjectMixedCaseProvider(t *testing.T) {
	rr := newTestRunner()
	checkJiraProject(config.Config{TrackerProvider: "Jira", Project: "MLG"}, rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q, want pass", c.Status)
	}
	if !strings.Contains(c.Message, "PROJECT=MLG") {
		t.Errorf("message %q should name the project key it falls back to", c.Message)
	}
}

func TestCheckJiraProjectFailsWhenUnset(t *testing.T) {
	rr := newTestRunner()
	checkJiraProject(config.Config{TrackerProvider: "jira"}, rr)
	c := lastCheck(t, rr)
	if c.Status != fail {
		t.Errorf("status = %q, want fail", c.Status)
	}
	if rr.r.Errors != 1 {
		t.Errorf("errors = %d, want 1", rr.r.Errors)
	}
	if !strings.Contains(c.Message, "LINEAR_TEAM") {
		t.Errorf("suggestion %q should name LINEAR_TEAM as the key to set", c.Message)
	}
}

func TestCheckLegacyRegistrationClean(t *testing.T) {
	t.Setenv("TRAU_HOME", t.TempDir())
	rr := newTestRunner()
	checkLegacyRegistration(rr)
	if c := lastCheck(t, rr); c.Status != pass {
		t.Errorf("status = %q, want pass on a fresh home", c.Status)
	}
}

func TestCheckLegacyRegistrationFlagsLeftover(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "workspace.json"), []byte(`{"repos":[]}`), 0o644); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}
	rr := newTestRunner()
	checkLegacyRegistration(rr)
	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn with a leftover legacy file", c.Status)
	}
	if !strings.Contains(c.Message, "workspace.json") {
		t.Errorf("message %q should name the leftover file", c.Message)
	}
}

func TestCheckLegacyQueueClean(t *testing.T) {
	rr := newTestRunner()
	checkLegacyQueue(t.TempDir(), rr)
	if c := lastCheck(t, rr); c.Status != pass {
		t.Errorf("status = %q, want pass on a repo with no queue.json", c.Status)
	}
}

func TestCheckLegacyQueueFlagsLeftover(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".trau"), 0o755); err != nil {
		t.Fatalf("mkdir .trau: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".trau", "queue.json"), []byte(`{"items":[]}`), 0o644); err != nil {
		t.Fatalf("seed legacy queue: %v", err)
	}
	rr := newTestRunner()
	checkLegacyQueue(repoRoot, rr)
	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn with a leftover queue.json", c.Status)
	}
	if !strings.Contains(c.Message, "queue.json") {
		t.Errorf("message %q should name the leftover file", c.Message)
	}
}

func TestCheckLegacyQueueSkippedWithoutRepo(t *testing.T) {
	rr := newTestRunner()
	checkLegacyQueue("", rr)
	if len(rr.r.Checks) != 0 {
		t.Errorf("expected no queue check without a repo root, got %+v", rr.r.Checks)
	}
}

func TestCheckHubDatabaseNotYetCreated(t *testing.T) {
	t.Setenv("TRAU_HOME", t.TempDir())
	rr := newTestRunner()
	checkHubDatabase(rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q, want pass", c.Status)
	}
	if !strings.Contains(c.Message, hubdb.Filename) {
		t.Errorf("message %q should name the database path", c.Message)
	}
}

func TestCheckHubDatabaseHealthy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	_ = db.Close()

	rr := newTestRunner()
	checkHubDatabase(rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q msg=%q, want pass", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "schema v") || !strings.Contains(c.Message, "healthy") {
		t.Errorf("message %q should report the schema version and health", c.Message)
	}
}

func TestCheckHubDatabaseCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)
	if err := os.WriteFile(hubdb.Path(home), []byte(strings.Repeat("garbage ", 64)), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	rr := newTestRunner()
	checkHubDatabase(rr)
	c := lastCheck(t, rr)
	if c.Status != fail {
		t.Errorf("status = %q, want fail", c.Status)
	}
	if !strings.Contains(c.Message, "cannot be opened") {
		t.Errorf("message %q should explain the open failure", c.Message)
	}
}

// TestCheckLinearProjectWarnsWhenUnset is the COD-158-in-trau guard: with
// PROJECT empty every cross-project pick guard is off, and doctor must say so
// (here without an API key, so the generic warning applies).
func TestCheckLinearProjectWarnsWhenUnset(t *testing.T) {
	rr := newTestRunner()
	checkLinearProject(context.Background(), config.Config{TrackerProvider: "linear", LinearTeam: "COD"}, rr)
	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn", c.Status)
	}
	if !strings.Contains(c.Message, "cross-project") || !strings.Contains(c.Message, "PROJECT") {
		t.Errorf("message %q should explain the disabled cross-project guards", c.Message)
	}
}

func TestCheckTranscriptDatabaseNotYetCreated(t *testing.T) {
	t.Setenv("TRAU_HOME", t.TempDir())
	rr := newTestRunner()
	checkTranscriptDatabase(rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q, want pass", c.Status)
	}
	if !strings.Contains(c.Message, transcriptdb.Filename) {
		t.Errorf("message %q should name the transcript database path", c.Message)
	}
}

func TestCheckTranscriptDatabaseHealthy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)
	db, err := transcriptdb.Open(home)
	if err != nil {
		t.Fatalf("open transcript db: %v", err)
	}
	_ = db.Close()

	rr := newTestRunner()
	checkTranscriptDatabase(rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q msg=%q, want pass", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "schema v") || !strings.Contains(c.Message, "healthy") {
		t.Errorf("message %q should report the schema version and health", c.Message)
	}
}

func TestCheckLegacyRunDataClean(t *testing.T) {
	rr := newTestRunner()
	checkLegacyRunData(config.Config{RunsDir: t.TempDir()}, "", rr)
	if c := lastCheck(t, rr); c.Status != pass {
		t.Errorf("status = %q, want pass on an empty runs dir", c.Status)
	}
}

func TestCheckLegacyRunDataFlagsLeftover(t *testing.T) {
	runsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runsDir, "COD-1"), 0o755); err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "COD-1", "state"), []byte("PHASE=built\n"), 0o644); err != nil {
		t.Fatalf("seed legacy state file: %v", err)
	}
	rr := newTestRunner()
	checkLegacyRunData(config.Config{RunsDir: runsDir}, "", rr)
	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn with a leftover run-data file", c.Status)
	}
	if !strings.Contains(c.Message, "COD-1/state") {
		t.Errorf("message %q should sample the leftover file", c.Message)
	}
}

func hubConfig(t *testing.T, rawURL string) config.Config {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse hub url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse hub port: %v", err)
	}
	return config.Config{ServeBind: u.Hostname(), ServePort: port, ServeAutostart: true}
}

func TestCheckWebHubHealthy(t *testing.T) {
	const token = "hub-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != webserver.APIPrefix+"/health" {
			t.Errorf("probe path = %q, want %s/health", r.URL.Path, webserver.APIPrefix)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q, want the configured bearer token", got)
		}
		_ = json.NewEncoder(w).Encode(webserver.Health{Status: "ok", Version: "2.0.0", UptimeSeconds: 90})
	}))
	defer srv.Close()

	cfg := hubConfig(t, srv.URL)
	cfg.ServeToken = token
	rr := newTestRunner()
	checkWebHub(context.Background(), cfg, "2.0.0", rr)

	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q, want pass for a healthy matching hub (%s)", c.Status, c.Message)
	}
	for _, want := range []string{srv.Listener.Addr().String(), "2.0.0", "1m30s"} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("message %q should contain %q", c.Message, want)
		}
	}
}

func TestCheckWebHubWarnsOnVersionMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(webserver.Health{Status: "ok", Version: "1.9.0", UptimeSeconds: 5})
	}))
	defer srv.Close()

	rr := newTestRunner()
	checkWebHub(context.Background(), hubConfig(t, srv.URL), "2.0.0", rr)

	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn when the hub runs another version", c.Status)
	}
	for _, want := range []string{"1.9.0", "2.0.0", "trau hub restart"} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("message %q should contain %q", c.Message, want)
		}
	}
}

func TestCheckWebHubForeignListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	restore := hubProbeTimeout
	hubProbeTimeout = 150 * time.Millisecond
	defer func() { hubProbeTimeout = restore }()

	rr := newTestRunner()
	checkWebHub(context.Background(), hubConfig(t, "http://"+ln.Addr().String()), "2.0.0", rr)

	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn for a non-hub listener", c.Status)
	}
	if !strings.Contains(c.Message, "not answering as a trau hub") || !strings.Contains(c.Message, ln.Addr().String()) {
		t.Errorf("message %q should name the occupied address and say it is not a hub", c.Message)
	}
}

func TestCheckWebHubNotRunning(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*config.Config)
		status  string
		wantMsg string
	}{
		{"autostart on", func(*config.Config) {}, pass, "SERVE_AUTOSTART=1"},
		{"autostart off", func(c *config.Config) { c.ServeAutostart = false }, warn, "SERVE_AUTOSTART=0"},
		{"exposed without token", func(c *config.Config) { c.ServeBind = "0.0.0.0" }, warn, "SERVE_TOKEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hubConfig(t, "http://"+addr)
			tc.mutate(&cfg)
			rr := newTestRunner()
			checkWebHub(context.Background(), cfg, "2.0.0", rr)

			c := lastCheck(t, rr)
			if c.Status != tc.status {
				t.Errorf("status = %q, want %q (%s)", c.Status, tc.status, c.Message)
			}
			if !strings.Contains(c.Message, tc.wantMsg) {
				t.Errorf("message %q should contain %q", c.Message, tc.wantMsg)
			}
			if !strings.Contains(c.Message, strconv.Itoa(cfg.ServePort)) {
				t.Errorf("message %q should name the probed port", c.Message)
			}
		})
	}
}

func TestCheckBrowserVerify(t *testing.T) {
	cases := []struct {
		name    string
		cfg     config.Config
		status  string
		warns   int
		wantMsg string
	}{
		{"never skips", config.Config{BrowserVerify: "never"}, "", 0, ""},
		{"empty mode skips", config.Config{}, "", 0, ""},
		{"auto without app url warns", config.Config{BrowserVerify: "auto"}, warn, 1, "APP_URL is empty"},
		{"always without app url warns", config.Config{BrowserVerify: "always"}, warn, 1, "APP_URL is empty"},
		{"auto with app url passes", config.Config{BrowserVerify: "auto", AppURL: "http://localhost:3000"}, pass, 0, "APP_URL target"},
		{"always with app urls passes", config.Config{BrowserVerify: "always", AppURLs: map[string]string{"web": "http://localhost:3000"}}, pass, 0, "APP_URL target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := newTestRunner()
			checkBrowserVerify(tc.cfg, rr)
			if tc.status == "" {
				if len(rr.r.Checks) != 0 {
					t.Fatalf("expected no check, got %+v", rr.r.Checks)
				}
				return
			}
			c := lastCheck(t, rr)
			if c.Status != tc.status {
				t.Errorf("status = %q, want %q (%s)", c.Status, tc.status, c.Message)
			}
			if rr.r.Warnings != tc.warns {
				t.Errorf("warnings = %d, want %d", rr.r.Warnings, tc.warns)
			}
			if !strings.Contains(c.Message, tc.wantMsg) {
				t.Errorf("message %q should contain %q", c.Message, tc.wantMsg)
			}
		})
	}
}

func installSkill(t *testing.T, repo, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repo, ".agents", "skills", name), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSkillsSkippedWithoutSkills(t *testing.T) {
	rr := newTestRunner()
	checkSkills(config.Config{}, t.TempDir(), rr)
	if len(rr.r.Checks) != 0 {
		t.Errorf("expected no skills check for a repo without skills, got %+v", rr.r.Checks)
	}
}

func TestCheckSkillsPassWhenPinned(t *testing.T) {
	repo := t.TempDir()
	installSkill(t, repo, "golang-code-style")
	rr := newTestRunner()
	checkSkills(config.Config{RequiredSkills: []string{"golang-code-style"}}, repo, rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q, want pass", c.Status)
	}
	if !strings.Contains(c.Message, "golang-code-style") {
		t.Errorf("message %q should name the pinned skills", c.Message)
	}
}

func TestCheckSkillsWarnsWhenUnpinned(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installSkill(t, repo, "golang-code-style")
	installSkill(t, repo, "unrelated-skill")
	rr := newTestRunner()
	checkSkills(config.Config{}, repo, rr)
	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn", c.Status)
	}
	if !strings.Contains(c.Message, "REQUIRED_SKILLS is unset") {
		t.Errorf("message %q should flag the unset pin", c.Message)
	}
	if !strings.Contains(c.Message, "REQUIRED_SKILLS=golang-code-style") {
		t.Errorf("message %q should suggest the recommended installed skill", c.Message)
	}
	if strings.Contains(c.Message, "unrelated-skill") {
		t.Errorf("message %q should not suggest a non-recommended skill when a recommended one is present", c.Message)
	}
}

func TestCheckSkillsSuggestionFallsBackToInstalled(t *testing.T) {
	repo := t.TempDir()
	installSkill(t, repo, "web-feature")
	rr := newTestRunner()
	checkSkills(config.Config{}, repo, rr)
	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn", c.Status)
	}
	if !strings.Contains(c.Message, "REQUIRED_SKILLS=web-feature") {
		t.Errorf("message %q should fall back to the installed names", c.Message)
	}
}

// configLayers writes the three layer files into a temp dir and returns their
// paths. An empty body means the layer has no file at all.
func configLayers(t *testing.T, project, local, user string) config.LayerPaths {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if body == "" {
			return path
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return config.LayerPaths{
		Project: write(".trau.ini", project),
		Local:   write("trau.ini", local),
		User:    write("home.trau.ini", user),
	}
}

func TestCheckConfigLayersNamesEveryLayer(t *testing.T) {
	paths := configLayers(t, "BASE_BRANCH=main\n", "", "REMOTE=origin\n")
	rr := newTestRunner()
	checkConfigLayers(paths, rr)

	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q, want pass", c.Status)
	}
	for _, want := range []string{paths.Project, paths.Local, paths.User} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("message %q should name %q", c.Message, want)
		}
	}
	if !strings.Contains(c.Message, "(absent)") {
		t.Errorf("message %q should mark the layer with no file as absent", c.Message)
	}
}

func TestCheckConfigLayersFlagsCwdLocalFile(t *testing.T) {
	rr := newTestRunner()
	checkConfigLayers(configLayers(t, "", "BASE_BRANCH=main\n", ""), rr)
	if msg := lastCheck(t, rr).Message; !strings.Contains(msg, "cwd-local trau.ini present") {
		t.Errorf("message %q should call out the stray cwd-local file", msg)
	}

	rr = newTestRunner()
	checkConfigLayers(configLayers(t, "BASE_BRANCH=main\n", "", ""), rr)
	if msg := lastCheck(t, rr).Message; strings.Contains(msg, "cwd-local trau.ini present") {
		t.Errorf("message %q should not flag a cwd-local file that does not exist", msg)
	}
}

func TestCheckConfigShadowingWarns(t *testing.T) {
	t.Setenv("CLAUDE_MODEL", "")
	t.Setenv("TRAU_CLAUDE_MODEL", "")
	paths := configLayers(t, "", "CLAUDE_MODEL=\n", "CLAUDE_MODEL=claude-opus-4-8\n")

	rr := newTestRunner()
	checkConfigShadowing(paths, rr)

	c := lastCheck(t, rr)
	if c.Status != warn || rr.r.Warnings != 1 {
		t.Errorf("status = %q warnings = %d, want warn/1", c.Status, rr.r.Warnings)
	}
	for _, want := range []string{"CLAUDE_MODEL", paths.Local, paths.User, "claude-opus-4-8"} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("message %q should name %q", c.Message, want)
		}
	}
}

func TestCheckConfigShadowingRedactsSecret(t *testing.T) {
	const token = "s3cr3t-token"
	t.Setenv("JIRA_API_TOKEN", "")
	t.Setenv("TRAU_JIRA_API_TOKEN", "")
	rr := newTestRunner()
	checkConfigShadowing(configLayers(t, "JIRA_API_TOKEN=\n", "", "JIRA_API_TOKEN="+token+"\n"), rr)

	c := lastCheck(t, rr)
	if strings.Contains(c.Message, token) {
		t.Errorf("shadowing warning leaked the token: %q", c.Message)
	}
	if !strings.Contains(c.Message, config.RedactedValue) {
		t.Errorf("message %q should redact the shadowed credential", c.Message)
	}
}

func TestCheckConfigShadowingQuietOnPlainOverride(t *testing.T) {
	t.Setenv("BASE_BRANCH", "")
	t.Setenv("TRAU_BASE_BRANCH", "")
	rr := newTestRunner()
	checkConfigShadowing(configLayers(t, "BASE_BRANCH=develop\n", "", "BASE_BRANCH=main\n"), rr)

	c := lastCheck(t, rr)
	if c.Status != pass || rr.r.Warnings != 0 {
		t.Errorf("status = %q warnings = %d, want pass/0 (%s)", c.Status, rr.r.Warnings, c.Message)
	}
}
