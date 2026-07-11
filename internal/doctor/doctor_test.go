package doctor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
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
