package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// realGitRepo makes an actual git repository so the inspect endpoint's git checks
// (origin remote, default branch) run against real plumbing rather than a planted
// .git stub.
func realGitRepo(t *testing.T, dir, branch, origin string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", branch)
	git("commit", "--allow-empty", "-m", "init")
	if origin != "" {
		git("remote", "add", "origin", origin)
	}
	return dir
}

func inspectServer(t *testing.T, home string) *httptest.Server {
	t.Helper()
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postInspect(t *testing.T, ts *httptest.Server, path string) (*http.Response, RepoInspection) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/inspect", InspectRequest{Path: path})
	var out RepoInspection
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode inspection: %v", err)
		}
	}
	return res, out
}

func findingFor(t *testing.T, insp RepoInspection, label string) DetectionFinding {
	t.Helper()
	for _, f := range insp.Findings {
		if f.Label == label {
			return f
		}
	}
	t.Fatalf("no finding labelled %q in %+v", label, insp.Findings)
	return DetectionFinding{}
}

func TestInspectFreshRepo(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	if err := os.WriteFile(filepath.Join(userHome, config.ProjectConfigName), []byte("LINEAR_API_KEY=lin_secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := realGitRepo(t, filepath.Join(t.TempDir(), "melga"), "main", "https://github.com/rd/melga.git")
	ts := inspectServer(t, t.TempDir())

	res, insp := postInspect(t, ts, repo)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if insp.RepoName != "melga" || insp.HasTrauIni || insp.DetectedProvider != "" || insp.Prefill != nil {
		t.Fatalf("fresh inspection = %+v, want no config / no prefill", insp)
	}
	if insp.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want main", insp.DefaultBranch)
	}
	if len(insp.Credentials) != 1 || insp.Credentials[0] != (InspectCredential{Provider: "linear", Layer: "user"}) {
		t.Errorf("credentials = %+v, want one linear/user", insp.Credentials)
	}
	if f := findingFor(t, insp, "git repository"); f.State != findingOK || !strings.Contains(f.Value, "melga") {
		t.Errorf("git finding = %+v", f)
	}
	if f := findingFor(t, insp, ".trau.ini"); f.State != findingMissing {
		t.Errorf(".trau.ini finding = %+v, want missing", f)
	}
	if f := findingFor(t, insp, "tracker provider"); f.State != findingMissing {
		t.Errorf("provider finding = %+v, want missing", f)
	}
	if f := findingFor(t, insp, "linear credentials"); f.State != findingInfo {
		t.Errorf("linear finding = %+v, want info", f)
	}
	if f := findingFor(t, insp, "jira credentials"); f.State != findingMissing {
		t.Errorf("jira finding = %+v, want missing", f)
	}
}

func TestInspectHalfConfiguredMismatch(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	if err := os.WriteFile(filepath.Join(userHome, config.ProjectConfigName), []byte("LINEAR_API_KEY=lin_secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := realGitRepo(t, filepath.Join(t.TempDir(), "melga"), "develop", "https://github.com/rd/melga.git")
	writeRepoINI(t, repo, "JIRA_BASE_URL=https://acme.atlassian.net\nJIRA_EMAIL=dev@acme.co\nJIRA_API_TOKEN=jira_secret\n")
	ts := inspectServer(t, t.TempDir())

	res, insp := postInspect(t, ts, repo)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if !insp.HasTrauIni || insp.DetectedProvider != "" || insp.Prefill != nil {
		t.Fatalf("half-configured inspection = %+v, want .trau.ini present, no explicit provider, no prefill", insp)
	}
	if insp.DefaultBranch != "develop" {
		t.Errorf("default branch = %q, want develop", insp.DefaultBranch)
	}
	// The melga trap: credentials present but TRACKER_PROVIDER unset must warn.
	f := findingFor(t, insp, "tracker provider")
	if f.State != findingWarn || !strings.Contains(f.Detail, "TRACKER_PROVIDER") {
		t.Fatalf("provider finding = %+v, want warn naming TRACKER_PROVIDER", f)
	}
	if f := findingFor(t, insp, ".trau.ini"); f.State != findingWarn {
		t.Errorf(".trau.ini finding = %+v, want warn (partial)", f)
	}
	if f := findingFor(t, insp, "jira credentials"); f.State != findingOK {
		t.Errorf("jira finding = %+v, want ok", f)
	}
	if f := findingFor(t, insp, "linear credentials"); f.State != findingInfo {
		t.Errorf("linear finding = %+v, want info", f)
	}
	if !hasCredential(insp.Credentials, "jira", "project") || !hasCredential(insp.Credentials, "linear", "user") {
		t.Errorf("credentials = %+v, want jira/project + linear/user", insp.Credentials)
	}
}

func TestInspectConfiguredPrefill(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	if err := os.WriteFile(filepath.Join(userHome, config.ProjectConfigName), []byte("LINEAR_API_KEY=lin_secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := realGitRepo(t, filepath.Join(t.TempDir(), "loop"), "master", "https://github.com/RomkaLTU/loop.git")
	writeRepoINI(t, repo, "TRACKER_PROVIDER=linear\nLINEAR_TEAM=COD\nREADY_LABEL=ready-for-agent\nEPIC_FLOW=1\n")
	ts := inspectServer(t, t.TempDir())

	res, insp := postInspect(t, ts, repo)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if !insp.HasTrauIni || insp.DetectedProvider != "linear" || insp.DefaultBranch != "master" {
		t.Fatalf("configured inspection = %+v, want linear/master/.trau.ini", insp)
	}
	if insp.Prefill == nil {
		t.Fatalf("prefill missing on a configured re-run")
	}
	want := InspectPrefill{Provider: "linear", Team: "COD", ReadyLabel: "ready-for-agent", EpicFlow: true}
	if *insp.Prefill != want {
		t.Errorf("prefill = %+v, want %+v", *insp.Prefill, want)
	}
	if f := findingFor(t, insp, "tracker provider"); f.State != findingOK || f.Value != "linear" {
		t.Errorf("provider finding = %+v, want ok/linear", f)
	}
	if f := findingFor(t, insp, ".trau.ini"); f.State != findingOK {
		t.Errorf(".trau.ini finding = %+v, want ok (complete)", f)
	}
	if f := findingFor(t, insp, "linear credentials"); f.State != findingOK {
		t.Errorf("linear finding = %+v, want ok", f)
	}
	if f := findingFor(t, insp, "jira credentials"); f.State != findingInfo {
		t.Errorf("jira finding = %+v, want info", f)
	}
}

func TestInspectRefusedOnExposedBind(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "acme", "dir")
	s := New("1.2.3", "0.0.0.0", "s3cret", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, body := authReq(t, http.MethodPost, ts.URL+APIPrefix+"/repos/inspect", "s3cret", InspectRequest{Path: repo})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("inspect = %d, want 403 (%s)", res.StatusCode, body)
	}
	if !strings.Contains(body, "SERVE_ALLOW_REGISTER") {
		t.Errorf("refusal %q does not name SERVE_ALLOW_REGISTER", body)
	}
}

func hasCredential(creds []InspectCredential, provider, layer string) bool {
	for _, c := range creds {
		if c.Provider == provider && c.Layer == layer {
			return true
		}
	}
	return false
}

// registerSyncServer builds a loopback hub whose Reader factory returns fake, so a
// test can register a repo and assert the seed-sync outcome the 201 body carries.
func registerSyncServer(t *testing.T, fake tracker.Reader) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.newReader = func(config.Config) (tracker.Reader, error) { return fake, nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postRegister(t *testing.T, ts *httptest.Server, req RegisterRepoRequest) (*http.Response, RegisterRepoResponse) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos", req)
	var out RegisterRepoResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	return res, out
}

func TestRegisterReturnsSeedSyncOutcome(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	ts := registerSyncServer(t, fake)
	repo := gitRepo(t, t.TempDir(), "acme", "dir")
	writeRepoINI(t, repo, "LINEAR_TEAM=COD\n")

	res, out := postRegister(t, ts, RegisterRepoRequest{Path: repo})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}
	if out.Sync == nil || !out.Sync.OK || out.Sync.SyncResponse == nil {
		t.Fatalf("sync outcome = %+v, want a successful seed sync", out.Sync)
	}
	if out.Sync.Issues != 1 || out.Sync.Comments != 1 || out.Sync.Provider != "linear" {
		t.Errorf("sync outcome = %+v, want 1 issue/1 comment/linear", out.Sync)
	}
	if fake.syncCalls != 1 {
		t.Errorf("syncCalls = %d, want 1", fake.syncCalls)
	}
}

func TestRegisterSyncFalseSkipsSeedSync(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	ts := registerSyncServer(t, fake)
	repo := gitRepo(t, t.TempDir(), "acme", "dir")
	writeRepoINI(t, repo, "LINEAR_TEAM=COD\n")

	no := false
	res, out := postRegister(t, ts, RegisterRepoRequest{Path: repo, Sync: &no})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}
	if out.Sync != nil {
		t.Errorf("sync outcome = %+v, want nil when sync:false", out.Sync)
	}
	if fake.syncCalls != 0 {
		t.Errorf("syncCalls = %d, want 0 when sync:false", fake.syncCalls)
	}
}

func TestRegisterSeedSyncFailureNonBlocking(t *testing.T) {
	fake := &fakeReader{syncErr: errStub("tracker offline")}
	ts := registerSyncServer(t, fake)
	repo := gitRepo(t, t.TempDir(), "acme", "dir")
	writeRepoINI(t, repo, "LINEAR_TEAM=COD\n")

	res, out := postRegister(t, ts, RegisterRepoRequest{Path: repo})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201 even when the seed sync fails", res.StatusCode)
	}
	if out.Sync == nil || out.Sync.OK || out.Sync.Error == "" {
		t.Fatalf("sync outcome = %+v, want a recorded failure", out.Sync)
	}
	if out.Sync.SyncResponse != nil {
		t.Errorf("failed sync must not carry counts: %+v", out.Sync)
	}
}

func TestRepoGitignoreIdempotency(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "acme", "dir")
	s := New("1.2.3", "127.0.0.1", "", []string{repo}, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	for i := 0; i < 2; i++ {
		res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/gitignore", nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("gitignore call %d = %d, want 200", i, res.StatusCode)
		}
		_ = res.Body.Close()
	}

	data, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if n := strings.Count(string(data), ".trau.ini"); n != 1 {
		t.Errorf(".trau.ini appears %d times in .gitignore, want exactly 1:\n%s", n, data)
	}
}
