package webserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

func instancesServer(t *testing.T, home string) *httptest.Server {
	t.Helper()
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// ingestedServer is instancesServer with the file-era checkpoints imported,
// mirroring serve startup, so the costs, timeseries, and runs endpoints see the
// fixtures — token calls seeded straight into the authoritative store and legacy
// state files folded in — before it is built.
func ingestedServer(t *testing.T, home string) *httptest.Server {
	t.Helper()
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.importAllCheckpoints()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// writeEntry seeds a loop's presence directly into the hub store under home, the
// way a heartbeat PUT would, so the server built at the same home reads it back.
func writeEntry(t *testing.T, home string, e registry.Entry) {
	t.Helper()
	if err := testStoresAt(t, home).Instances().Upsert(e); err != nil {
		t.Fatalf("upsert instance: %v", err)
	}
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

func TestInstancesEchoesReportedWorkingStateReapsDead(t *testing.T) {
	home := t.TempDir()
	repoRoot := filepath.Join(t.TempDir(), "acme")
	runsDir := filepath.Join(repoRoot, ".trau", "runs")
	stateSince := time.Now().Add(-90 * time.Second)

	writeEntry(t, home, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     repoRoot,
		RunsDir:      runsDir,
		StartedAt:    time.Now().Add(-2 * time.Minute),
		Heartbeat:    time.Now(),
		SessionState: registry.StateWorking,
		Ticket:       "COD-42",
		Phase:        state.Building,
		StateSince:   stateSince,
	})
	writeEntry(t, home, registry.Entry{
		PID:      deadPID(t),
		RepoRoot: filepath.Join(t.TempDir(), "gone"),
		RunsDir:  filepath.Join(t.TempDir(), "gone", ".trau", "runs"),
	})

	ts := instancesServer(t, home)
	out := getInstances(t, ts)

	if len(out.Instances) != 1 {
		t.Fatalf("live instances = %d, want 1 (dead one reaped)", len(out.Instances))
	}
	inst := out.Instances[0]
	if inst.PID != os.Getpid() {
		t.Errorf("PID = %d, want own %d", inst.PID, os.Getpid())
	}
	if inst.Repo != "acme" {
		t.Errorf("Repo = %q, want acme", inst.Repo)
	}
	if inst.SessionState != registry.StateWorking {
		t.Errorf("SessionState = %q, want %q", inst.SessionState, registry.StateWorking)
	}
	if inst.Ticket != "COD-42" {
		t.Errorf("Ticket = %q, want COD-42", inst.Ticket)
	}
	if inst.Phase != state.Building {
		t.Errorf("Phase = %q, want %q", inst.Phase, state.Building)
	}
	if want := stateSince.UTC().Format(time.RFC3339); inst.StateSince != want {
		t.Errorf("StateSince = %q, want %q (reported transition time)", inst.StateSince, want)
	}

	if len(out.Repos) != 1 || !out.Repos[0].Live || out.Repos[0].Root != repoRoot {
		t.Errorf("repos = %+v, want one live repo at %s", out.Repos, repoRoot)
	}
}

func TestInstancesParkedEntrySurfacesReportedTicket(t *testing.T) {
	home := t.TempDir()
	repoRoot := filepath.Join(t.TempDir(), "acme")

	writeEntry(t, home, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     repoRoot,
		RunsDir:      filepath.Join(repoRoot, ".trau", "runs"),
		StartedAt:    time.Now().Add(-2 * time.Minute),
		Heartbeat:    time.Now(),
		SessionState: registry.StateParked,
		Ticket:       "COD-42",
		StateSince:   time.Now().Add(-30 * time.Second),
	})

	ts := instancesServer(t, home)
	out := getInstances(t, ts)

	if len(out.Instances) != 1 {
		t.Fatalf("live instances = %d, want 1", len(out.Instances))
	}
	inst := out.Instances[0]
	if inst.SessionState != registry.StateParked {
		t.Errorf("SessionState = %q, want %q", inst.SessionState, registry.StateParked)
	}
	if inst.Ticket != "COD-42" {
		t.Errorf("Ticket = %q, want COD-42", inst.Ticket)
	}
	if inst.Phase != "" {
		t.Errorf("Phase = %q, want empty (parked reports no phase)", inst.Phase)
	}
}

func TestInstancesLegacyEntryReportsUnknownWithoutDeriving(t *testing.T) {
	home := t.TempDir()
	repoRoot := filepath.Join(t.TempDir(), "acme")
	runsDir := filepath.Join(repoRoot, ".trau", "runs")

	store := state.NewStore(runsDir)
	if err := store.Set("COD-42", "PHASE", state.Building); err != nil {
		t.Fatalf("seed in-flight checkpoint: %v", err)
	}

	writeEntry(t, home, registry.Entry{
		PID:       os.Getpid(),
		RepoRoot:  repoRoot,
		RunsDir:   runsDir,
		StartedAt: time.Now().Add(-2 * time.Minute),
		Heartbeat: time.Now(),
	})

	ts := instancesServer(t, home)
	out := getInstances(t, ts)

	if len(out.Instances) != 1 {
		t.Fatalf("live instances = %d, want 1", len(out.Instances))
	}
	inst := out.Instances[0]
	if inst.SessionState != "unknown" {
		t.Errorf("SessionState = %q, want unknown", inst.SessionState)
	}
	if inst.Ticket != "" {
		t.Errorf("Ticket = %q, want empty (no derivation from checkpoint)", inst.Ticket)
	}
	if inst.Phase != "" {
		t.Errorf("Phase = %q, want empty", inst.Phase)
	}
	if inst.StateSince != "" {
		t.Errorf("StateSince = %q, want empty", inst.StateSince)
	}
}

func TestInstancesRetainsExitedRepos(t *testing.T) {
	home := t.TempDir()
	gone := registry.Repo{Name: "gone", Root: "/repo/gone", RunsDir: "/repo/gone/.trau/runs"}

	store := testStores(t)
	if err := store.Registrations().Remember([]registry.Repo{gone}); err != nil {
		t.Fatalf("seed known repo: %v", err)
	}
	s := New("1.2.3", "127.0.0.1", "", nil, false, store)
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
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

func putJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("new PUT %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	return res
}

func TestInstanceHeartbeatRegistersThenDeregisters(t *testing.T) {
	home := t.TempDir()
	ts := instancesServer(t, home)
	pid := os.Getpid()
	repoRoot := filepath.Join(t.TempDir(), "acme")

	res := putJSON(t, ts.URL+APIPrefix+"/instances/"+strconv.Itoa(pid), instanceHeartbeatBody{
		RepoRoot:     repoRoot,
		RunsDir:      filepath.Join(repoRoot, ".trau", "runs"),
		StartedAt:    time.Now().Add(-time.Minute),
		SessionState: registry.StateWorking,
		Ticket:       "COD-7",
		Phase:        state.Building,
		StateSince:   time.Now().Add(-20 * time.Second),
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT instance status = %d, want 200", res.StatusCode)
	}
	_ = res.Body.Close()

	out := getInstances(t, ts)
	if len(out.Instances) != 1 {
		t.Fatalf("instances after register = %d, want 1", len(out.Instances))
	}
	if inst := out.Instances[0]; inst.PID != pid || inst.SessionState != registry.StateWorking || inst.Ticket != "COD-7" {
		t.Fatalf("registered instance = %+v, want working COD-7 at pid %d", inst, pid)
	}

	del, _ := deleteReq(t, ts, APIPrefix+"/instances/"+strconv.Itoa(pid))
	if del.StatusCode != http.StatusOK {
		t.Fatalf("DELETE instance status = %d, want 200", del.StatusCode)
	}
	if out := getInstances(t, ts); len(out.Instances) != 0 {
		t.Fatalf("instances after deregister = %d, want 0", len(out.Instances))
	}
}

func TestInstanceHeartbeatCarriesActivity(t *testing.T) {
	home := t.TempDir()
	ts := instancesServer(t, home)
	pid := os.Getpid()
	repoRoot := filepath.Join(t.TempDir(), "acme")

	res := putJSON(t, ts.URL+APIPrefix+"/instances/"+strconv.Itoa(pid), instanceHeartbeatBody{
		RepoRoot:     repoRoot,
		RunsDir:      filepath.Join(repoRoot, ".trau", "runs"),
		StartedAt:    time.Now().Add(-time.Minute),
		SessionState: registry.StateWorking,
		Ticket:       "COD-9",
		Phase:        state.HandedOff,
		Activity:     "repair",
		Detail:       "repair2",
		StateSince:   time.Now().Add(-10 * time.Second),
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT instance status = %d, want 200", res.StatusCode)
	}
	_ = res.Body.Close()

	out := getInstances(t, ts)
	if len(out.Instances) != 1 {
		t.Fatalf("instances after register = %d, want 1", len(out.Instances))
	}
	if inst := out.Instances[0]; inst.Activity != "repair" || inst.Detail != "repair2" {
		t.Errorf("activity/detail = %q/%q, want repair/repair2", inst.Activity, inst.Detail)
	}
}

func TestInstanceHeartbeatRejectsInvalidPID(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	res := putJSON(t, ts.URL+APIPrefix+"/instances/not-a-pid", instanceHeartbeatBody{})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT with non-numeric pid = %d, want 400", res.StatusCode)
	}
}

func TestInstancesRejectsUnsupportedMethod(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	req, err := http.NewRequest(http.MethodPut, ts.URL+APIPrefix+"/instances", nil)
	if err != nil {
		t.Fatalf("new PUT request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT instances: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT status = %d, want 405", res.StatusCode)
	}
	if allow := res.Header.Get("Allow"); allow != http.MethodGet {
		t.Errorf("Allow = %q, want %q", allow, http.MethodGet)
	}
}
