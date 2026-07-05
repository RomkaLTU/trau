package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/registry"
)

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
	pid        int
	spawnErr   error
	captureOut []byte
	captureErr error
	signalErr  error
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
	return nil
}

func controlServer(t *testing.T, home string, workspace []string) (*fakeSupervisor, *httptest.Server) {
	t.Helper()
	s := New("1.2.3", "127.0.0.1", "", workspace)
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

func TestStartSpawnsLoopInAllowlistedRepo(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, home, []string{root})

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d, want 202", res.StatusCode)
	}
	var out StartResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode start result: %v", err)
	}
	if out.PID <= 0 {
		t.Errorf("PID = %d, want a spawned pid", out.PID)
	}
	if out.RepoRoot != root {
		t.Errorf("RepoRoot = %q, want %q", out.RepoRoot, root)
	}
	if out.Repo != "acme" {
		t.Errorf("Repo = %q, want acme", out.Repo)
	}

	if len(fake.spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(fake.spawns))
	}
	spec := fake.spawns[0]
	if spec.Dir != root {
		t.Errorf("spawn Dir = %q, want %q", spec.Dir, root)
	}
	wantArgs := []string{"--repo", root, "--no-tui"}
	if len(spec.Args) != len(wantArgs) {
		t.Fatalf("spawn Args = %v, want %v", spec.Args, wantArgs)
	}
	for i, a := range wantArgs {
		if spec.Args[i] != a {
			t.Errorf("spawn Args[%d] = %q, want %q", i, spec.Args[i], a)
		}
	}
	if !hasEnv(spec.Env, "TRAU_HOME="+home) {
		t.Errorf("spawn Env missing TRAU_HOME=%s (pins the child to the hub registry)", home)
	}
}

func TestStartAcceptsRepoBaseName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "salonradar")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: "salonradar"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("start by name status = %d, want 202", res.StatusCode)
	}
	if len(fake.spawns) != 1 || fake.spawns[0].Dir != root {
		t.Fatalf("spawns = %+v, want one in %s", fake.spawns, root)
	}
}

func TestStartRefusedForNonAllowlistedRepo(t *testing.T) {
	allowed := filepath.Join(t.TempDir(), "acme")
	other := filepath.Join(t.TempDir(), "stranger")
	fake, ts := controlServer(t, t.TempDir(), []string{allowed})

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: other})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("start status = %d, want 403 for observe-only repo", res.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["error"] == "" {
		t.Errorf("403 body missing error message")
	}
	if len(fake.spawns) != 0 {
		t.Errorf("spawns = %d, want 0 (nothing started for a refused repo)", len(fake.spawns))
	}
}

func TestStartRefusedWhenAllowlistEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), nil)

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("start status = %d, want 403 when no workspace is allowlisted", res.StatusCode)
	}
	if len(fake.spawns) != 0 {
		t.Errorf("spawns = %d, want 0", len(fake.spawns))
	}
}

func TestStartReportsSpawnFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})
	fake.spawnErr = os.ErrPermission

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("start status = %d, want 500 when spawn fails", res.StatusCode)
	}
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
	s := New("1.2.3", "0.0.0.0", "s3cret", []string{"/repo/acme"})
	fake := &fakeSupervisor{}
	s.sup = fake
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	start := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: "/repo/acme"})
	_ = start.Body.Close()
	if start.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated start = %d, want 401 on an exposed bind", start.StatusCode)
	}
	stop := postJSON(t, ts.URL+APIPrefix+"/instances/1/stop", nil)
	_ = stop.Body.Close()
	if stop.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated stop = %d, want 401 on an exposed bind", stop.StatusCode)
	}
	if len(fake.spawns) != 0 || len(fake.signals) != 0 {
		t.Errorf("token gate let a control request through: spawns=%d signals=%d", len(fake.spawns), len(fake.signals))
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

func TestStartRunsSingleTicket(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root, Ticket: "COD-693"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("run-ticket status = %d, want 202", res.StatusCode)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(fake.spawns))
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-693", "--once"})
}

func TestStartRunsEpic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root, Epic: "COD-530"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("run-epic status = %d, want 202", res.StatusCode)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(fake.spawns))
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-530"})
}

func TestStartProviderOverrideIsPerRun(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	overridden := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root, Ticket: "COD-693", Provider: "codex"})
	_ = overridden.Body.Close()
	plain := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root, Ticket: "COD-694"})
	_ = plain.Body.Close()

	if len(fake.spawns) != 2 {
		t.Fatalf("spawns = %d, want 2", len(fake.spawns))
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-693", "--once", "--provider", "codex"})
	if hasArg(fake.spawns[1].Args, "--provider") {
		t.Errorf("second run carried a --provider override: %v — an override must apply only to the run it was submitted with", fake.spawns[1].Args)
	}
}

func TestStartRejectsTicketAndEpicTogether(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root, Ticket: "COD-1", Epic: "COD-2"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when ticket and epic are both set", res.StatusCode)
	}
	if len(fake.spawns) != 0 {
		t.Errorf("spawns = %d, want 0 for a rejected request", len(fake.spawns))
	}
}

func TestStartRejectsMalformedTicket(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root, Ticket: "--force"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-ticket value", res.StatusCode)
	}
	if len(fake.spawns) != 0 {
		t.Errorf("spawns = %d, want 0 (never launch a run for a malformed ticket)", len(fake.spawns))
	}
}

func TestStartRejectsBlankTicket(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	fake, ts := controlServer(t, t.TempDir(), []string{root})

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: root, Ticket: "   "})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a whitespace-only ticket", res.StatusCode)
	}
	if len(fake.spawns) != 0 {
		t.Errorf("spawns = %d, want 0 (a blank ticket must not fall through to a plain pick-next loop)", len(fake.spawns))
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
	s := New("1.2.3", "0.0.0.0", "s3cret", []string{"/repo/acme"})
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
	fake.captureOut = []byte(`[{"id":"COD-1","title":"First","labels":["ready-for-agent","Feature"]},{"id":"COD-2","title":"Second","labels":[]}]`)

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
	s := New("1.2.3", "0.0.0.0", "s3cret", []string{"/repo/acme"})
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

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func hasEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
