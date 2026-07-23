package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// checkpointRepo records one exited repo in the hub's known set and returns its
// root and runs dir, so a checkpoint mutation resolves the repo the same way it
// would for a repo whose loop has since exited.
func checkpointRepo(t *testing.T, home, name string) (root, runsDir string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), name)
	runsDir = filepath.Join(root, ".trau", "runs")
	if err := testStoresAt(t, home).Registrations().Remember([]registry.Repo{{Name: name, Root: root, RunsDir: runsDir}}); err != nil {
		t.Fatalf("seed known repo: %v", err)
	}
	return root, runsDir
}

func markLive(t *testing.T, home, root, runsDir string) {
	t.Helper()
	writeEntry(t, home, registry.Entry{
		PID:       os.Getpid(),
		RepoRoot:  root,
		RunsDir:   runsDir,
		StartedAt: time.Now(),
		Heartbeat: time.Now(),
	})
}

func TestResetHappyPathSpawnsCLIReset(t *testing.T) {
	home := t.TempDir()
	root, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Quarantined})
	fake, ts := controlServer(t, home, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-1/reset", ResetRequest{})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reset status = %d, want 200", res.StatusCode)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1 (reset drives the CLI)", len(fake.captures))
	}
	assertArgs(t, fake.captures[0].Args, []string{"--repo", root, "--reset", "COD-1", "--no-tui"})
	if fake.captures[0].Dir != root {
		t.Errorf("reset Dir = %q, want %q", fake.captures[0].Dir, root)
	}
}

func TestResetMergedRequiresForce(t *testing.T) {
	home := t.TempDir()
	_, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Merged, "PR": "7"})
	fake, ts := controlServer(t, home, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-1/reset", ResetRequest{Force: false})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("reset-merged status = %d, want 409 without force", res.StatusCode)
	}
	var body struct {
		Error         string `json:"error"`
		RequiresForce bool   `json:"requires_force"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if !body.RequiresForce {
		t.Errorf("reset-merged body = %+v, want requires_force so the UI can escalate", body)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 (a merged ticket must not be reset without force)", len(fake.captures))
	}
}

func TestResetMergedWithForceProceeds(t *testing.T) {
	home := t.TempDir()
	root, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Merged, "PR": "7"})
	fake, ts := controlServer(t, home, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-1/reset", ResetRequest{Force: true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("forced reset status = %d, want 200", res.StatusCode)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1", len(fake.captures))
	}
	assertArgs(t, fake.captures[0].Args, []string{"--repo", root, "--reset", "COD-1", "--no-tui", "--force"})
}

func TestClearDropsLocalCheckpointOnly(t *testing.T) {
	home := t.TempDir()
	_, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Quarantined})
	fake, ts := controlServer(t, home, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-1/clear", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear status = %d, want 200", res.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["was"] != state.Quarantined {
		t.Errorf("clear reported was = %q, want the dropped phase %q", body["was"], state.Quarantined)
	}
	if stateFileExists(runsDir, "COD-1") {
		t.Errorf("checkpoint still present after clear")
	}
	if len(fake.captures) != 0 || len(fake.spawns) != 0 {
		t.Errorf("clear touched a process (captures=%d spawns=%d) — it is local-only", len(fake.captures), len(fake.spawns))
	}
}

func TestReconcileReportsClearedTickets(t *testing.T) {
	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	fake, ts := controlServer(t, home, nil)
	fake.captureOut = []byte(`{"tickets":[],"total":{"tokens":0,"cost":0},"reconciled":["COD-3","COD-7"]}`)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/reconcile", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reconcile status = %d, want 200", res.StatusCode)
	}
	var out ReconcileResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode reconcile result: %v", err)
	}
	if len(out.Reconciled) != 2 || out.Reconciled[0] != "COD-3" || out.Reconciled[1] != "COD-7" {
		t.Errorf("reconciled = %v, want [COD-3 COD-7]", out.Reconciled)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1", len(fake.captures))
	}
	assertArgs(t, fake.captures[0].Args, []string{"--repo", root, "--status", "--json", "--no-tui"})
}

func TestReconcileNothingStaleReturnsEmptyArray(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	fake, ts := controlServer(t, home, nil)
	fake.captureOut = []byte(`{"tickets":[],"total":{"tokens":0,"cost":0}}`)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/reconcile", nil)
	defer func() { _ = res.Body.Close() }()
	var out ReconcileResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode reconcile result: %v", err)
	}
	if out.Reconciled == nil || len(out.Reconciled) != 0 {
		t.Errorf("reconciled = %v, want an empty array, never null", out.Reconciled)
	}
}

func TestCheckpointMutationsRefusedWhileLive(t *testing.T) {
	home := t.TempDir()
	root, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Quarantined})
	markLive(t, home, root, runsDir)
	fake, ts := controlServer(t, home, nil)

	cases := []struct {
		name string
		path string
		body any
	}{
		{"reset", "/repos/acme/runs/COD-1/reset", ResetRequest{}},
		{"clear", "/repos/acme/runs/COD-1/clear", nil},
		{"reconcile", "/repos/acme/reconcile", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := postJSON(t, ts.URL+APIPrefix+c.path, c.body)
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("%s while live = %d, want 409", c.name, res.StatusCode)
			}
			var body struct {
				Error string `json:"error"`
				Live  bool   `json:"live"`
			}
			_ = json.NewDecoder(res.Body).Decode(&body)
			if !body.Live || body.Error == "" {
				t.Errorf("%s conflict body = %+v, want live flag + explanation", c.name, body)
			}
		})
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 (no mutation reaches the repo while a loop is live)", len(fake.captures))
	}
	if !stateFileExists(runsDir, "COD-1") {
		t.Errorf("checkpoint was dropped while a loop was live — the guard must leave state untouched")
	}
}

func TestCheckpointMutationsRefusedWhileTakenOver(t *testing.T) {
	home := t.TempDir()
	root, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Quarantined})
	writeEntry(t, home, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		RunsDir:      runsDir,
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateTakeover,
		Ticket:       "COD-1",
	})
	fake, ts := controlServer(t, home, nil)

	cases := []struct {
		name string
		path string
		body any
	}{
		{"reset", "/repos/acme/runs/COD-1/reset", ResetRequest{}},
		{"clear", "/repos/acme/runs/COD-1/clear", nil},
		{"reconcile", "/repos/acme/reconcile", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := postJSON(t, ts.URL+APIPrefix+c.path, c.body)
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("%s while taken over = %d, want 409", c.name, res.StatusCode)
			}
			var body struct {
				Error  string `json:"error"`
				Reason string `json:"reason"`
			}
			_ = json.NewDecoder(res.Body).Decode(&body)
			if body.Reason != "taken_over" {
				t.Errorf("%s reason = %q, want taken_over", c.name, body.Reason)
			}
			for _, want := range []string{"acme", strconv.Itoa(os.Getpid()), "COD-1"} {
				if !strings.Contains(body.Error, want) {
					t.Errorf("%s error = %q, want it to name %q", c.name, body.Error, want)
				}
			}
		})
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 (no mutation reaches a repo a terminal holds)", len(fake.captures))
	}
	if !stateFileExists(runsDir, "COD-1") {
		t.Errorf("checkpoint was dropped under a live takeover — the guard must leave state untouched")
	}
}

func TestResetUnknownRepo404(t *testing.T) {
	_, ts := controlServer(t, t.TempDir(), nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/ghost/runs/COD-1/reset", ResetRequest{})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("reset unknown repo = %d, want 404", res.StatusCode)
	}
}

func TestClearUnknownRun404(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-404/clear", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("clear unknown run = %d, want 404", res.StatusCode)
	}
}

func TestReconcileReportsCaptureFailure(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	fake, ts := controlServer(t, home, nil)
	fake.captureErr = os.ErrPermission

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/reconcile", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("reconcile status = %d, want 502 when the child fails", res.StatusCode)
	}
}

func TestCheckpointMutationsRejectNonPOST(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)
	for _, path := range []string{
		"/repos/acme/runs/COD-1/reset",
		"/repos/acme/runs/COD-1/clear",
		"/repos/acme/reconcile",
	} {
		res, err := http.Get(ts.URL + APIPrefix + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want 405", path, res.StatusCode)
		}
	}
}

func TestCheckpointMutationsRequireTokenWhenExposed(t *testing.T) {
	s := New("1.2.3", "0.0.0.0", "s3cret", nil, false, testStores(t))
	fake := &fakeSupervisor{}
	s.sup = fake
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	for _, path := range []string{
		"/repos/acme/runs/COD-1/reset",
		"/repos/acme/runs/COD-1/clear",
		"/repos/acme/reconcile",
	} {
		res := postJSON(t, ts.URL+APIPrefix+path, ResetRequest{})
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s = %d, want 401 on an exposed bind", path, res.StatusCode)
		}
	}
	if len(fake.captures) != 0 {
		t.Errorf("token gate let a mutation through: captures=%d", len(fake.captures))
	}
}
