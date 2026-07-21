package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// testStores returns the hub store set over a throwaway hub database, for
// servers whose hub state need not survive the test.
func testStores(t *testing.T) *hubstore.Stores {
	t.Helper()
	return testStoresAt(t, t.TempDir())
}

// testStoresAt returns the hub store set over the hub database under home, so two
// store sets opened at the same home share state — the way a serve restart
// re-opens the same database.
func testStoresAt(t *testing.T, home string) *hubstore.Stores {
	t.Helper()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return hubstore.NewStores(home, db.SQL(), nil, hubstore.Retention{})
}

type signalCall struct {
	pid int
	sig syscall.Signal
}

// fakeSupervisor records spawns, captures, and signals instead of touching real
// processes, so the control layer's OS interactions are asserted without
// launching anything.
type fakeSupervisor struct {
	mu         sync.Mutex
	spawns     []SpawnSpec
	captures   []SpawnSpec
	signals    []signalCall
	kills      []int
	pid        int
	spawnErr   error
	captureOut []byte
	captureErr error
	signalErr  error
	killErr    error
	onSignal   func(pid int, sig syscall.Signal)
	onKill     func(pid int)
}

func (f *fakeSupervisor) Spawn(spec SpawnSpec) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spawns = append(f.spawns, spec)
	if f.spawnErr != nil {
		return 0, f.spawnErr
	}
	f.pid++
	return 40000 + f.pid, nil
}

func (f *fakeSupervisor) Capture(_ context.Context, spec SpawnSpec) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captures = append(f.captures, spec)
	if f.captureErr != nil {
		return nil, f.captureErr
	}
	return f.captureOut, nil
}

func (f *fakeSupervisor) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.signalErr != nil {
		return f.signalErr
	}
	f.signals = append(f.signals, signalCall{pid: pid, sig: sig})
	if f.onSignal != nil {
		f.onSignal(pid, sig)
	}
	return nil
}

func (f *fakeSupervisor) Kill(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.killErr != nil {
		return f.killErr
	}
	f.kills = append(f.kills, pid)
	if f.onKill != nil {
		f.onKill(pid)
	}
	return nil
}

func controlServer(t *testing.T, home string, workspace []string) (*fakeSupervisor, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s := New("1.2.3", "127.0.0.1", "", workspace, false, testStoresAt(t, home))
	s.home = home
	fake := &fakeSupervisor{}
	s.sup = fake
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return fake, ts
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	res, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return res
}

func TestStopSignalsRegisteredInstance(t *testing.T) {
	home := t.TempDir()
	fake, ts := controlServer(t, home, nil)

	pid := os.Getpid()
	writeEntry(t, home, registry.Entry{
		PID:       pid,
		RepoRoot:  filepath.Join(t.TempDir(), "acme"),
		StartedAt: time.Now(),
		Heartbeat: time.Now(),
	})

	res := postJSON(t, ts.URL+APIPrefix+"/instances/"+strconv.Itoa(pid)+"/stop", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("stop status = %d, want 202", res.StatusCode)
	}
	if len(fake.signals) != 1 {
		t.Fatalf("signals = %d, want 1", len(fake.signals))
	}
	if fake.signals[0].pid != pid || fake.signals[0].sig != syscall.SIGTERM {
		t.Errorf("signal = %+v, want SIGTERM to pid %d", fake.signals[0], pid)
	}
}

func TestStopUnknownPIDReturns404(t *testing.T) {
	fake, ts := controlServer(t, t.TempDir(), nil)

	res := postJSON(t, ts.URL+APIPrefix+"/instances/424242/stop", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("stop status = %d, want 404 for an unregistered pid", res.StatusCode)
	}
	if len(fake.signals) != 0 {
		t.Errorf("signals = %d, want 0 (never signal an unknown pid)", len(fake.signals))
	}
}

func TestStopRejectsInvalidPID(t *testing.T) {
	_, ts := controlServer(t, t.TempDir(), nil)
	res := postJSON(t, ts.URL+APIPrefix+"/instances/not-a-pid/stop", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("stop status = %d, want 400 for a non-numeric pid", res.StatusCode)
	}
}

func TestStopRejectsNonPOST(t *testing.T) {
	_, ts := controlServer(t, t.TempDir(), nil)
	res, err := http.Get(ts.URL + APIPrefix + "/instances/1/stop")
	if err != nil {
		t.Fatalf("GET stop: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET stop status = %d, want 405", res.StatusCode)
	}
}

func TestControlEndpointsRequireTokenWhenExposed(t *testing.T) {
	s := New("1.2.3", "0.0.0.0", "s3cret", []string{"/repo/acme"}, false, testStores(t))
	fake := &fakeSupervisor{}
	s.sup = fake
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	stop := postJSON(t, ts.URL+APIPrefix+"/instances/1/stop", nil)
	_ = stop.Body.Close()
	if stop.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated stop = %d, want 401 on an exposed bind", stop.StatusCode)
	}
	if len(fake.signals) != 0 {
		t.Errorf("token gate let a control request through: signals=%d", len(fake.signals))
	}
}

func TestInstancesFlagAllowedRepos(t *testing.T) {
	home := t.TempDir()
	live := filepath.Join(t.TempDir(), "acme")
	fresh := filepath.Join(t.TempDir(), "fresh")

	writeEntry(t, home, registry.Entry{
		PID:       os.Getpid(),
		RepoRoot:  live,
		RunsDir:   filepath.Join(live, ".trau", "runs"),
		StartedAt: time.Now(),
		Heartbeat: time.Now(),
	})

	_, ts := controlServer(t, home, []string{live, fresh})
	out := getInstances(t, ts)

	byRoot := map[string]RepoView{}
	for _, r := range out.Repos {
		byRoot[r.Root] = r
	}
	if v, ok := byRoot[live]; !ok || !v.Allowed || !v.Live {
		t.Errorf("live repo view = %+v (present=%v), want allowed+live", v, ok)
	}
	if v, ok := byRoot[fresh]; !ok || !v.Allowed || v.Live {
		t.Errorf("fresh allowlisted repo view = %+v (present=%v), want allowed, not live, startable before first run", v, ok)
	}
}

func TestDryRunReturnsNextTicket(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureOut = []byte("12:00:00 [claude] Asking linear for the next eligible ticket…\n12:00:01 Next up: COD-42\n")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/dry-run", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dry-run status = %d, want 200", res.StatusCode)
	}
	var out DryRunResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode dry-run result: %v", err)
	}
	if out.Ticket != "COD-42" {
		t.Errorf("Ticket = %q, want COD-42", out.Ticket)
	}
	if out.RepoRoot != root {
		t.Errorf("RepoRoot = %q, want %q", out.RepoRoot, root)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1", len(fake.captures))
	}
	assertArgs(t, fake.captures[0].Args, []string{"--repo", root, "--dry-run", "--no-tui"})
	if len(fake.spawns) != 0 {
		t.Errorf("dry-run spawned a loop (%d) — a preview must have no side effects", len(fake.spawns))
	}
}

func TestDryRunNothingEligible(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureOut = []byte("12:00:00 [claude] Asking linear for the next eligible ticket…\n12:00:01 Nothing eligible right now.\n")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/dry-run", nil)
	defer func() { _ = res.Body.Close() }()
	var out DryRunResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode dry-run result: %v", err)
	}
	if out.Ticket != "" {
		t.Errorf("Ticket = %q, want empty when nothing is eligible", out.Ticket)
	}
}

func TestDryRunRefusedForNonAllowlistedRepo(t *testing.T) {
	allowed := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{allowed})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/stranger/dry-run", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("dry-run status = %d, want 403 for an observe-only repo", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 for a refused repo", len(fake.captures))
	}
}

func TestDryRunReportsCaptureFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureErr = os.ErrPermission

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/dry-run", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("dry-run status = %d, want 502 when the preview fails", res.StatusCode)
	}
}

func TestDryRunRequiresTokenWhenExposed(t *testing.T) {
	s := New("1.2.3", "0.0.0.0", "s3cret", []string{"/repo/acme"}, false, testStores(t))
	fake := &fakeSupervisor{}
	s.sup = fake
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/dry-run", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated dry-run = %d, want 401 on an exposed bind", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("token gate let a dry-run through: captures=%d", len(fake.captures))
	}
}

func TestEligibleReturnsTickets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureOut = []byte(`[{"id":"COD-1","title":"First","labels":["ready-for-agent","Feature"],"parent":"COD-805","has_children":false},{"id":"COD-2","title":"Second","labels":[],"parent":"","has_children":true}]`)

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/eligible")
	if err != nil {
		t.Fatalf("GET eligible: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("eligible status = %d, want 200", res.StatusCode)
	}
	var out EligibleResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode eligible result: %v", err)
	}
	if out.RepoRoot != root {
		t.Errorf("RepoRoot = %q, want %q", out.RepoRoot, root)
	}
	if len(out.Tickets) != 2 {
		t.Fatalf("Tickets = %d, want 2", len(out.Tickets))
	}
	if out.Tickets[0].ID != "COD-1" || out.Tickets[0].Title != "First" {
		t.Errorf("Tickets[0] = %+v, want COD-1/First", out.Tickets[0])
	}
	if len(out.Tickets[0].Labels) != 2 || out.Tickets[0].Labels[0] != "ready-for-agent" {
		t.Errorf("Tickets[0].Labels = %v, want [ready-for-agent Feature]", out.Tickets[0].Labels)
	}
	if out.Tickets[0].Parent != "COD-805" || out.Tickets[0].HasChildren {
		t.Errorf("Tickets[0] hierarchy = (%q, %v), want (COD-805, false)", out.Tickets[0].Parent, out.Tickets[0].HasChildren)
	}
	if out.Tickets[1].Parent != "" || !out.Tickets[1].HasChildren {
		t.Errorf("Tickets[1] hierarchy = (%q, %v), want (empty, true)", out.Tickets[1].Parent, out.Tickets[1].HasChildren)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1", len(fake.captures))
	}
	assertArgs(t, fake.captures[0].Args, []string{"--repo", root, "--list-eligible", "--json", "--no-tui"})
	if len(fake.spawns) != 0 {
		t.Errorf("eligible spawned a loop (%d) — listing must have no side effects", len(fake.spawns))
	}
}

func TestEligibleEmptyQueue(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureOut = []byte("[]\n")

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/eligible")
	if err != nil {
		t.Fatalf("GET eligible: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var out EligibleResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode eligible result: %v", err)
	}
	if out.Tickets == nil {
		t.Errorf("Tickets = nil, want an empty array for an empty queue")
	}
	if len(out.Tickets) != 0 {
		t.Errorf("Tickets = %v, want empty", out.Tickets)
	}
}

func TestEligibleRefusedForNonAllowlistedRepo(t *testing.T) {
	allowed := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{allowed})

	res, err := http.Get(ts.URL + APIPrefix + "/repos/stranger/eligible")
	if err != nil {
		t.Fatalf("GET eligible: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("eligible status = %d, want 403 for an observe-only repo", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 for a refused repo", len(fake.captures))
	}
}

func TestEligibleReportsCaptureFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureErr = os.ErrPermission

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/eligible")
	if err != nil {
		t.Fatalf("GET eligible: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("eligible status = %d, want 502 when the listing fails", res.StatusCode)
	}
}

func TestEligibleReportsMalformedOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureOut = []byte("not json at all")

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/eligible")
	if err != nil {
		t.Fatalf("GET eligible: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("eligible status = %d, want 502 when the output cannot be parsed", res.StatusCode)
	}
}

func TestEligibleRejectsNonGET(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/eligible", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST eligible = %d, want 405", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 for a rejected method", len(fake.captures))
	}
}

func TestParseEligibleTickets(t *testing.T) {
	t.Run("normalizes null labels to empty", func(t *testing.T) {
		out, err := parseEligibleTickets([]byte(`[{"id":"COD-1","title":"A"}]`))
		if err != nil {
			t.Fatalf("parseEligibleTickets: %v", err)
		}
		if len(out) != 1 || out[0].Labels == nil {
			t.Errorf("got %+v, want one ticket with a non-nil Labels", out)
		}
	})
	t.Run("empty output is an empty queue", func(t *testing.T) {
		out, err := parseEligibleTickets([]byte("  \n"))
		if err != nil {
			t.Fatalf("parseEligibleTickets(blank): %v", err)
		}
		if out == nil || len(out) != 0 {
			t.Errorf("got %+v, want an empty non-nil slice", out)
		}
	})
	t.Run("malformed output errors", func(t *testing.T) {
		if _, err := parseEligibleTickets([]byte("garbage")); err == nil {
			t.Error("parseEligibleTickets(garbage) should error, not return no tickets")
		}
	})
}

func TestEpicPreviewReturnsSubIssues(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureOut = []byte(`[{"id":"COD-1","title":"First","state":"done"},{"id":"COD-2","title":"Second","state":"todo"}]`)

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/epics/COD-530")
	if err != nil {
		t.Fatalf("GET epic preview: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("epic preview status = %d, want 200", res.StatusCode)
	}
	var out EpicPreviewResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode epic preview result: %v", err)
	}
	if out.RepoRoot != root {
		t.Errorf("RepoRoot = %q, want %q", out.RepoRoot, root)
	}
	if out.Epic != "COD-530" {
		t.Errorf("Epic = %q, want COD-530", out.Epic)
	}
	if len(out.SubIssues) != 2 {
		t.Fatalf("SubIssues = %d, want 2", len(out.SubIssues))
	}
	if out.SubIssues[0].ID != "COD-1" || out.SubIssues[0].State != "done" {
		t.Errorf("SubIssues[0] = %+v, want COD-1/done", out.SubIssues[0])
	}
	if out.SubIssues[1].State != "todo" {
		t.Errorf("SubIssues[1].State = %q, want todo", out.SubIssues[1].State)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1", len(fake.captures))
	}
	assertArgs(t, fake.captures[0].Args, []string{"--repo", root, "--list-epic", "COD-530", "--json", "--no-tui"})
	if len(fake.spawns) != 0 {
		t.Errorf("epic preview spawned a loop (%d) — previewing must have no side effects", len(fake.spawns))
	}
}

func TestEpicPreviewChildlessEpic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureOut = []byte("[]\n")

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/epics/COD-530")
	if err != nil {
		t.Fatalf("GET epic preview: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var out EpicPreviewResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode epic preview result: %v", err)
	}
	if out.SubIssues == nil {
		t.Errorf("SubIssues = nil, want an empty array for a childless epic")
	}
	if len(out.SubIssues) != 0 {
		t.Errorf("SubIssues = %v, want empty", out.SubIssues)
	}
}

func TestEpicPreviewRejectsMalformedEpic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/epics/not-an-epic!")
	if err != nil {
		t.Fatalf("GET epic preview: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("epic preview status = %d, want 400 for a malformed epic id", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 (never run the binary for a malformed epic id)", len(fake.captures))
	}
}

func TestEpicPreviewRefusedForNonAllowlistedRepo(t *testing.T) {
	allowed := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{allowed})

	res, err := http.Get(ts.URL + APIPrefix + "/repos/stranger/epics/COD-530")
	if err != nil {
		t.Fatalf("GET epic preview: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("epic preview status = %d, want 403 for an observe-only repo", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 for a refused repo", len(fake.captures))
	}
}

func TestEpicPreviewReportsCaptureFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureErr = os.ErrPermission

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/epics/COD-530")
	if err != nil {
		t.Fatalf("GET epic preview: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("epic preview status = %d, want 502 when the preview fails (unknown epic / tracker unavailable)", res.StatusCode)
	}
}

func TestEpicPreviewReportsMalformedOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.captureOut = []byte("not json at all")

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/epics/COD-530")
	if err != nil {
		t.Fatalf("GET epic preview: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("epic preview status = %d, want 502 when the output cannot be parsed", res.StatusCode)
	}
}

func TestEpicPreviewRejectsNonGET(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/epics/COD-530", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST epic preview = %d, want 405", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want 0 for a rejected method", len(fake.captures))
	}
}

func TestEpicPreviewRequiresTokenWhenExposed(t *testing.T) {
	s := New("1.2.3", "0.0.0.0", "s3cret", []string{"/repo/acme"}, false, testStores(t))
	fake := &fakeSupervisor{}
	s.sup = fake
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/epics/COD-530")
	if err != nil {
		t.Fatalf("GET epic preview: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated epic preview = %d, want 401 on an exposed bind", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("token gate let an epic preview through: captures=%d", len(fake.captures))
	}
}

func TestParseEpicSubIssues(t *testing.T) {
	t.Run("empty output is a childless epic", func(t *testing.T) {
		out, err := parseEpicSubIssues([]byte("  \n"))
		if err != nil {
			t.Fatalf("parseEpicSubIssues(blank): %v", err)
		}
		if out == nil || len(out) != 0 {
			t.Errorf("got %+v, want an empty non-nil slice", out)
		}
	})
	t.Run("malformed output errors", func(t *testing.T) {
		if _, err := parseEpicSubIssues([]byte("garbage")); err == nil {
			t.Error("parseEpicSubIssues(garbage) should error, not return no sub-issues")
		}
	})
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestChildEnvStripsNestedLoopMarker locks the fix for hub spawns dying on the
// nested-loop guard: a hub autostarted from inside a loop inherits TRAU_ACTIVE,
// and passing it on made every --list-epic / start child refuse with exit 1.
func TestChildEnvStripsNestedLoopMarker(t *testing.T) {
	t.Setenv("TRAU_ACTIVE", "1")
	t.Setenv("TRAU_HOME", "/elsewhere")
	env := childEnv("/hub-home")
	var home string
	for _, kv := range env {
		if strings.HasPrefix(kv, "TRAU_ACTIVE=") {
			t.Errorf("childEnv kept %q; a hub spawn must not look like a nested loop", kv)
		}
		if strings.HasPrefix(kv, "TRAU_HOME=") {
			home = kv
		}
	}
	if home != "TRAU_HOME=/hub-home" {
		t.Errorf("childEnv home = %q, want the hub's home pinned", home)
	}
}

func TestChildEnvStripsNestedLoopMarkerWithoutHome(t *testing.T) {
	t.Setenv("TRAU_ACTIVE", "1")
	for _, kv := range childEnv("") {
		if strings.HasPrefix(kv, "TRAU_ACTIVE=") {
			t.Errorf("childEnv kept %q with no home override", kv)
		}
	}
}

// spawnSleeper starts a real child that sleeps for seconds and returns its
// pid, reaped in the background like a real spawned loop so stopAndWait's
// process-liveness polling observes its exit promptly rather than a zombie.
func spawnSleeper(t *testing.T, seconds string) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep "+seconds)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd.Process.Pid
}

// spawnTermIgnorer starts a real child that ignores SIGTERM and sleeps for
// seconds, so an escalation test can drive it to a real group SIGKILL.
func spawnTermIgnorer(t *testing.T, seconds string) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep "+seconds)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start SIGTERM-ignoring child: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd.Process.Pid
}

// stopWaitServer returns a Server wired to a fakeSupervisor with the poll
// cadence compressed, so stopAndWait tests never sleep for real seconds.
func stopWaitServer(t *testing.T, home string) (*Server, *fakeSupervisor) {
	t.Helper()
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	fake := &fakeSupervisor{}
	s.sup = fake

	prevPoll, prevConfirm := stopWaitPoll, stopKillConfirm
	stopWaitPoll, stopKillConfirm = 5*time.Millisecond, time.Second
	t.Cleanup(func() { stopWaitPoll, stopKillConfirm = prevPoll, prevConfirm })
	return s, fake
}

func TestStopAndWaitGracefulExitNeverEscalates(t *testing.T) {
	s, fake := stopWaitServer(t, t.TempDir())
	pid := spawnSleeper(t, "0.05")

	if err := s.stopAndWait(pid, time.Second); err != nil {
		t.Fatalf("stopAndWait: %v", err)
	}
	if len(fake.signals) != 1 || fake.signals[0].pid != pid || fake.signals[0].sig != syscall.SIGTERM {
		t.Errorf("signals = %+v, want one SIGTERM to %d", fake.signals, pid)
	}
	if len(fake.kills) != 0 {
		t.Errorf("kills = %v, want none — the child exited inside the grace period", fake.kills)
	}
}

func TestStopAndWaitEscalatesToGroupKill(t *testing.T) {
	s, fake := stopWaitServer(t, t.TempDir())
	pid := spawnTermIgnorer(t, "5")
	fake.onKill = func(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }

	if err := s.stopAndWait(pid, 20*time.Millisecond); err != nil {
		t.Fatalf("stopAndWait: %v", err)
	}
	if len(fake.signals) != 1 || fake.signals[0].sig != syscall.SIGTERM {
		t.Errorf("signals = %+v, want one SIGTERM before escalating", fake.signals)
	}
	if len(fake.kills) != 1 || fake.kills[0] != pid {
		t.Errorf("kills = %v, want one group kill of %d", fake.kills, pid)
	}
	if registry.Alive(pid) {
		t.Error("pid still alive after stopAndWait escalated")
	}
}

func TestStopAndWaitStalePIDSucceedsImmediately(t *testing.T) {
	s, fake := stopWaitServer(t, t.TempDir())
	pid := deadPID(t)

	if err := s.stopAndWait(pid, time.Second); err != nil {
		t.Fatalf("stopAndWait(dead pid): %v", err)
	}
	if len(fake.signals) != 0 || len(fake.kills) != 0 {
		t.Errorf("signals=%v kills=%v, want neither for an already-dead pid", fake.signals, fake.kills)
	}
}

func TestStopAndWaitSettlesForceKilledRun(t *testing.T) {
	home := t.TempDir()
	s, fake := stopWaitServer(t, home)
	repoRoot := filepath.Join(t.TempDir(), "acme")
	pid := spawnTermIgnorer(t, "5")
	fake.onKill = func(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }

	writeEntry(t, home, registry.Entry{
		PID:          pid,
		RepoRoot:     repoRoot,
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateWorking,
		Ticket:       "COD-42",
		Phase:        state.Building,
	})
	if err := s.stores.Checkpoints().Upsert(repoRoot, "COD-42", map[string]string{"PHASE": state.Building, "TITLE": "example"}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	if err := s.stopAndWait(pid, 20*time.Millisecond); err != nil {
		t.Fatalf("stopAndWait: %v", err)
	}

	if _, found, err := s.stores.Instances().Get(pid); err != nil || found {
		t.Errorf("registry entry after force-kill: found=%v err=%v, want removed", found, err)
	}
	row, found, err := s.stores.Checkpoints().One(repoRoot, "COD-42")
	if err != nil || !found {
		t.Fatalf("checkpoint after force-kill: found=%v err=%v", found, err)
	}
	if row.FailureReason != "shutdown" {
		t.Errorf("FailureReason = %q, want shutdown", row.FailureReason)
	}
	if checkpointField(row.Data, "FAILURE_CLASS") != state.FailStopped {
		t.Errorf("FAILURE_CLASS = %q, want %q", checkpointField(row.Data, "FAILURE_CLASS"), state.FailStopped)
	}
	if row.Title != "example" {
		t.Errorf("Title = %q, want the pre-existing field preserved", row.Title)
	}
}

func TestStopAndWaitLeavesGracefullyDeregisteredRunUntouched(t *testing.T) {
	home := t.TempDir()
	s, _ := stopWaitServer(t, home)
	repoRoot := filepath.Join(t.TempDir(), "acme")
	pid := spawnSleeper(t, "0.05")

	writeEntry(t, home, registry.Entry{
		PID:          pid,
		RepoRoot:     repoRoot,
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateWorking,
		Ticket:       "COD-42",
	})
	if err := s.stores.Checkpoints().Upsert(repoRoot, "COD-42", map[string]string{"PHASE": state.Building}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	// The loop deregisters itself on a clean exit; simulate that racing ahead
	// of stopAndWait's own settle so the helper finds nothing left to do.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = s.stores.Instances().Remove(pid)
	}()

	if err := s.stopAndWait(pid, time.Second); err != nil {
		t.Fatalf("stopAndWait: %v", err)
	}
	row, found, err := s.stores.Checkpoints().One(repoRoot, "COD-42")
	if err != nil || !found {
		t.Fatalf("checkpoint after a clean deregister: found=%v err=%v", found, err)
	}
	if row.FailureReason != "" || checkpointField(row.Data, "FAILURE_CLASS") != "" {
		t.Errorf("checkpoint = %+v, want untouched — the loop already deregistered on its own", row)
	}
}
