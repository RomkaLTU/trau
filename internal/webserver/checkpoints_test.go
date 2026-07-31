package webserver

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

func TestCheckpointMutationRefusedWhileLive(t *testing.T) {
	home := t.TempDir()
	root, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Quarantined})
	markLive(t, home, root, runsDir)
	fake, ts := controlServer(t, home, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-1/advance", AdvanceRequest{})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("advance while live = %d, want 409", res.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
		Live  bool   `json:"live"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if !body.Live || body.Error == "" {
		t.Errorf("conflict body = %+v, want live flag + explanation", body)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 (no mutation reaches the repo while a loop is live)", len(fake.captures))
	}
	if !stateFileExists(runsDir, "COD-1") {
		t.Errorf("checkpoint was dropped while a loop was live — the guard must leave state untouched")
	}
}

// Reset, clear and reconcile are CLI and MCP verbs now, with the automatic
// sweeps covering the rest — the HTTP endpoints must stay gone.
func TestRetiredCheckpointEndpointsAreGone(t *testing.T) {
	home := t.TempDir()
	_, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Quarantined})
	_, ts := controlServer(t, home, nil)

	for _, path := range []string{
		"/repos/acme/runs/COD-1/reset",
		"/repos/acme/runs/COD-1/clear",
		"/repos/acme/reconcile",
	} {
		res := postJSON(t, ts.URL+APIPrefix+path, nil)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, res.StatusCode)
		}
	}
}
