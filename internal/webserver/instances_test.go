package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

func instancesServer(t *testing.T, home string) *httptest.Server {
	t.Helper()
	s := New("1.2.3", "127.0.0.1", "")
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func writeEntry(t *testing.T, home string, e registry.Entry) string {
	t.Helper()
	dir := filepath.Join(home, "instances")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir instances: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.json", e.PID))
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	return path
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return pid
}

func getInstances(t *testing.T, ts *httptest.Server) InstancesResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/instances")
	if err != nil {
		t.Fatalf("GET instances: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out InstancesResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode instances: %v", err)
	}
	return out
}

func TestInstancesListsLiveReapsDeadAndDerivesRun(t *testing.T) {
	home := t.TempDir()
	repoRoot := filepath.Join(t.TempDir(), "acme")
	runsDir := filepath.Join(repoRoot, ".trau", "runs")

	store := state.NewStore(runsDir)
	if err := store.Set("COD-42", "PHASE", state.Building); err != nil {
		t.Fatalf("seed active checkpoint: %v", err)
	}
	if err := store.Set("COD-1", "PHASE", state.Merged); err != nil {
		t.Fatalf("seed terminal checkpoint: %v", err)
	}

	writeEntry(t, home, registry.Entry{
		PID:       os.Getpid(),
		RepoRoot:  repoRoot,
		RunsDir:   runsDir,
		StartedAt: time.Now().Add(-2 * time.Minute),
		Heartbeat: time.Now(),
	})
	deadFile := writeEntry(t, home, registry.Entry{
		PID:      deadPID(t),
		RepoRoot: filepath.Join(t.TempDir(), "gone"),
		RunsDir:  filepath.Join(t.TempDir(), "gone", ".trau", "runs"),
	})

	ts := instancesServer(t, home)
	out := getInstances(t, ts)

	if len(out.Instances) != 1 {
		t.Fatalf("live instances = %d, want 1", len(out.Instances))
	}
	inst := out.Instances[0]
	if inst.PID != os.Getpid() {
		t.Errorf("PID = %d, want own %d", inst.PID, os.Getpid())
	}
	if inst.Repo != "acme" {
		t.Errorf("Repo = %q, want acme", inst.Repo)
	}
	if inst.StartedAt == "" {
		t.Errorf("StartedAt is empty")
	}
	if inst.Ticket != "COD-42" {
		t.Errorf("Ticket = %q, want COD-42 (newest in-flight)", inst.Ticket)
	}
	if inst.Phase != state.Building {
		t.Errorf("Phase = %q, want %q", inst.Phase, state.Building)
	}
	if inst.PhaseSince == "" {
		t.Errorf("PhaseSince is empty, want the checkpoint time")
	}

	if _, err := os.Stat(deadFile); !os.IsNotExist(err) {
		t.Errorf("dead entry not reaped: %v", err)
	}

	if len(out.Repos) != 1 || !out.Repos[0].Live || out.Repos[0].Root != repoRoot {
		t.Errorf("repos = %+v, want one live repo at %s", out.Repos, repoRoot)
	}
}

func TestInstancesRetainsExitedRepos(t *testing.T) {
	home := t.TempDir()
	gone := registry.Repo{Name: "gone", Root: "/repo/gone", RunsDir: "/repo/gone/.trau/runs"}
	seed := map[string]registry.Repo{gone.Root: gone}
	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal repos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "repos.json"), data, 0o644); err != nil {
		t.Fatalf("seed repos.json: %v", err)
	}

	ts := instancesServer(t, home)
	out := getInstances(t, ts)

	if len(out.Instances) != 0 {
		t.Fatalf("instances = %d, want 0 (no live loop)", len(out.Instances))
	}
	if len(out.Repos) != 1 {
		t.Fatalf("repos = %d, want 1 (exited repo retained)", len(out.Repos))
	}
	if out.Repos[0].Root != gone.Root || out.Repos[0].Live {
		t.Errorf("repo = %+v, want %s not live", out.Repos[0], gone.Root)
	}
}

func TestInstancesRejectsNonGET(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	res, err := http.Post(ts.URL+APIPrefix+"/instances", "application/json", nil)
	if err != nil {
		t.Fatalf("POST instances: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}
