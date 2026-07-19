package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

type fakeTakeoverPresence struct {
	state        string
	ticket       string
	deregistered int
}

func (f *fakeTakeoverPresence) SetState(state, ticket, _ string) {
	f.state = state
	f.ticket = ticket
}

func (f *fakeTakeoverPresence) Deregister() { f.deregistered++ }

// takeoverFixture wires a takeoverSession over a file-backed checkpoint store
// and inert seams; tests override the seam under exercise.
func takeoverFixture(t *testing.T) (*takeoverSession, *fakeTakeoverPresence, *state.Store, *bytes.Buffer) {
	t.Helper()
	pres := &fakeTakeoverPresence{}
	cps := state.NewStore(t.TempDir())
	out := &bytes.Buffer{}
	sess := &takeoverSession{
		repoRoot:      filepath.Join(t.TempDir(), "acme"),
		ticket:        "COD-1",
		presence:      pres,
		instances:     func(context.Context) ([]hubclient.Instance, error) { return nil, nil },
		cps:           cps,
		sessionExists: func(string) bool { return true },
		runClaude:     func(string) error { return nil },
		now:           func() time.Time { return time.Date(2026, 7, 19, 10, 30, 0, 0, time.UTC) },
		out:           out,
	}
	return sess, pres, cps, out
}

// TestTakeoverMissingSessionRefuses covers a checkpoint with no recorded
// SESSION: the wrapper exits with the "no resumable" message, never runs
// claude, leaves no stamp, and releases the lock.
func TestTakeoverMissingSessionRefuses(t *testing.T) {
	sess, pres, cps, _ := takeoverFixture(t)
	ran := false
	sess.runClaude = func(string) error { ran = true; return nil }
	_ = cps.Set("COD-1", "PHASE", state.Building)

	err := sess.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no resumable claude session for COD-1") {
		t.Fatalf("run = %v, want the no-resumable-session refusal", err)
	}
	if ran {
		t.Error("claude ran despite a missing SESSION")
	}
	if got := cps.Get("COD-1", "TAKEOVER"); got != "" {
		t.Errorf("TAKEOVER = %q, want unset on a refused takeover", got)
	}
	if pres.deregistered != 1 {
		t.Errorf("deregistered %d times, want 1 — no lock may be left behind", pres.deregistered)
	}
}

// TestTakeoverMissingTranscriptRefuses covers a recorded SESSION whose
// transcript is gone from disk: same refusal, same clean lock release.
func TestTakeoverMissingTranscriptRefuses(t *testing.T) {
	sess, pres, cps, _ := takeoverFixture(t)
	_ = cps.Set("COD-1", "SESSION", "dead-uuid")
	_ = cps.Set("COD-1", "SESSION_PHASE", "build")
	sess.sessionExists = func(string) bool { return false }

	err := sess.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no resumable claude session for COD-1") {
		t.Fatalf("run = %v, want the no-resumable-session refusal", err)
	}
	if pres.deregistered != 1 {
		t.Errorf("deregistered %d times, want 1", pres.deregistered)
	}
}

// TestTakeoverHappyPath drives the whole wrapper with the claude binary faked
// by a script: the lock is held as takeover with the ticket, the recorded
// session is resumed via `--resume <sid>` in the repo root, the checkpoint
// carries the TAKEOVER stamp and takeover anomaly, the hand-back hint names the
// phase, and the lock is released on exit.
func TestTakeoverHappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude script needs a POSIX shell")
	}
	sess, pres, cps, out := takeoverFixture(t)
	if err := os.MkdirAll(sess.repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(t.TempDir(), "args")
	bin := filepath.Join(t.TempDir(), "claude")
	script := fmt.Sprintf("#!/bin/sh\necho \"$(pwd -P) $*\" > %q\n", argsFile)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sess.runClaude = claudeResumeRunner(bin, sess.repoRoot)
	_ = cps.Set("COD-1", "SESSION", "0b5a5e2e-8f66-4b41-9dcd-0f2c4d1a9b77")
	_ = cps.Set("COD-1", "SESSION_PHASE", "repair2")
	_ = cps.Set("COD-1", "ANOMALIES", "2")

	if err := sess.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake claude never ran: %v", err)
	}
	resolvedRoot, _ := filepath.EvalSymlinks(sess.repoRoot)
	want := resolvedRoot + " --resume 0b5a5e2e-8f66-4b41-9dcd-0f2c4d1a9b77"
	if got := strings.TrimSpace(string(recorded)); got != want {
		t.Errorf("claude invocation = %q, want %q", got, want)
	}
	if got, wantTS := cps.Get("COD-1", "TAKEOVER"), "2026-07-19T10:30:00Z"; got != wantTS {
		t.Errorf("TAKEOVER = %q, want %q", got, wantTS)
	}
	if got := cps.Get("COD-1", "ANOMALIES"); got != "2,takeover" {
		t.Errorf("ANOMALIES = %q, want %q", got, "2,takeover")
	}
	if pres.state != registry.StateTakeover || pres.ticket != "COD-1" {
		t.Errorf("presence = (%q, %q), want (takeover, COD-1)", pres.state, pres.ticket)
	}
	if pres.deregistered != 1 {
		t.Errorf("deregistered %d times, want 1", pres.deregistered)
	}
	hint := out.String()
	if !strings.Contains(hint, "repair2") || !strings.Contains(hint, "Run next") {
		t.Errorf("hand-back hint = %q, want the resumed phase and the Run next pointer", hint)
	}
}

// TestTakeoverDeregistersOnChildFailure pins the lock lifecycle when claude
// exits non-zero: the wrapper still prints the hand-back hint, releases the
// lock, and surfaces the child's exit code.
func TestTakeoverDeregistersOnChildFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude script needs a POSIX shell")
	}
	sess, pres, cps, out := takeoverFixture(t)
	if err := os.MkdirAll(sess.repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sess.runClaude = claudeResumeRunner(bin, sess.repoRoot)
	_ = cps.Set("COD-1", "SESSION", "uuid")
	_ = cps.Set("COD-1", "SESSION_PHASE", "build")

	err := sess.run(context.Background())
	var se silentExit
	if !errors.As(err, &se) || se.code != 3 {
		t.Fatalf("run = %v, want silentExit{3}", err)
	}
	if pres.deregistered != 1 {
		t.Errorf("deregistered %d times, want 1 — a failed child must still release the lock", pres.deregistered)
	}
	if !strings.Contains(out.String(), "Run next") {
		t.Errorf("output = %q, want the hand-back hint even after a child failure", out.String())
	}
}

// TestTakeoverRefusesActiveRun covers the belt-and-braces recheck after the
// lock registers: a working/grazing/stopping instance in the same repo refuses
// the takeover and releases the lock; idle, parked, and other-repo instances do
// not block.
func TestTakeoverRefusesActiveRun(t *testing.T) {
	sess, pres, cps, _ := takeoverFixture(t)
	ran := false
	sess.runClaude = func(string) error { ran = true; return nil }
	sess.instances = func(context.Context) ([]hubclient.Instance, error) {
		return []hubclient.Instance{
			{PID: 11, RepoRoot: "/elsewhere", SessionState: registry.StateWorking, Ticket: "COD-8"},
			{PID: 12, RepoRoot: sess.repoRoot, SessionState: registry.StateIdle},
			{PID: 13, RepoRoot: sess.repoRoot, SessionState: registry.StateParked, Ticket: "COD-1"},
			{PID: 4242, RepoRoot: sess.repoRoot, SessionState: registry.StateWorking, Ticket: "COD-9"},
		}, nil
	}
	_ = cps.Set("COD-1", "SESSION", "uuid")

	err := sess.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "PID 4242") || !strings.Contains(err.Error(), "COD-9") {
		t.Fatalf("run = %v, want a refusal naming the active run", err)
	}
	if ran {
		t.Error("claude ran despite an active run in the repo")
	}
	if pres.deregistered != 1 {
		t.Errorf("deregistered %d times, want 1", pres.deregistered)
	}
}

// TestTakeoverRefusesSecondTakeover pins the one-takeover-per-repo invariant:
// a takeover already holding the repo from another process refuses this one,
// which releases its own lock without touching the checkpoint.
func TestTakeoverRefusesSecondTakeover(t *testing.T) {
	sess, pres, cps, _ := takeoverFixture(t)
	sess.pid = 100
	ran := false
	sess.runClaude = func(string) error { ran = true; return nil }
	sess.instances = func(context.Context) ([]hubclient.Instance, error) {
		return []hubclient.Instance{
			{PID: 100, RepoRoot: sess.repoRoot, SessionState: registry.StateTakeover, Ticket: "COD-1"},
			{PID: 4242, RepoRoot: sess.repoRoot, SessionState: registry.StateTakeover, Ticket: "COD-7"},
		}, nil
	}
	_ = cps.Set("COD-1", "SESSION", "uuid")

	err := sess.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "PID 4242") || !strings.Contains(err.Error(), "COD-7") {
		t.Fatalf("run = %v, want a refusal naming the live takeover", err)
	}
	if ran {
		t.Error("claude ran despite a live takeover in the repo")
	}
	if got := cps.Get("COD-1", "TAKEOVER"); got != "" {
		t.Errorf("TAKEOVER = %q, want unset on a refused takeover", got)
	}
	if pres.deregistered != 1 {
		t.Errorf("deregistered %d times, want 1", pres.deregistered)
	}
}

// TestTakeoverIgnoresOwnLockEntry covers the session seeing its own presence
// entry in the instance list: the lock it just registered must not read as a
// competing takeover.
func TestTakeoverIgnoresOwnLockEntry(t *testing.T) {
	sess, _, cps, _ := takeoverFixture(t)
	sess.pid = 100
	ran := false
	sess.runClaude = func(string) error { ran = true; return nil }
	sess.instances = func(context.Context) ([]hubclient.Instance, error) {
		return []hubclient.Instance{
			{PID: 100, RepoRoot: sess.repoRoot, SessionState: registry.StateTakeover, Ticket: "COD-1"},
		}, nil
	}
	_ = cps.Set("COD-1", "SESSION", "uuid")

	if err := sess.run(context.Background()); err != nil {
		t.Fatalf("run = %v, want success when only our own lock entry is listed", err)
	}
	if !ran {
		t.Error("claude never ran")
	}
}

func TestAppendAnomaly(t *testing.T) {
	cases := []struct{ current, want string }{
		{current: "", want: "takeover"},
		{current: "2", want: "2,takeover"},
		{current: "takeover", want: "takeover"},
		{current: "2,takeover", want: "2,takeover"},
	}
	for _, tc := range cases {
		if got := appendAnomaly(tc.current, "takeover"); got != tc.want {
			t.Errorf("appendAnomaly(%q) = %q, want %q", tc.current, got, tc.want)
		}
	}
}

// TestTakenOverRefusal covers the loop-start guard end to end against a stub
// hub: a live takeover instance for the repo refuses with the PID and ticket, a
// takeover elsewhere or none at all lets the loop start, and an unreachable hub
// never blocks (no hub means no lock to honor).
func TestTakenOverRefusal(t *testing.T) {
	root := "/src/acme"
	serve := func(instances string) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/instances", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"instances":%s,"repos":[]}`, instances)
		})
		return httptest.NewServer(mux)
	}

	ts := serve(fmt.Sprintf(`[{"pid":314,"repo":"acme","repo_root":%q,"session_state":"takeover","ticket":"COD-7"}]`, root))
	defer ts.Close()
	err := takenOverRefusal(context.Background(), hubclient.New(ts.URL, ""), root)
	if err == nil || !strings.Contains(err.Error(), "PID 314") || !strings.Contains(err.Error(), "COD-7") {
		t.Fatalf("refusal = %v, want a refusal naming PID 314 and COD-7", err)
	}

	other := serve(`[{"pid":314,"repo":"other","repo_root":"/src/other","session_state":"takeover","ticket":"COD-7"},{"pid":315,"repo":"acme","repo_root":"/src/acme","session_state":"working","ticket":"COD-8"}]`)
	defer other.Close()
	if err := takenOverRefusal(context.Background(), hubclient.New(other.URL, ""), root); err != nil {
		t.Fatalf("refusal = %v, want nil when no takeover holds this repo", err)
	}

	down := serve(`[]`)
	down.Close()
	if err := takenOverRefusal(context.Background(), hubclient.New(down.URL, ""), root); err != nil {
		t.Fatalf("refusal = %v, want nil when the hub is unreachable", err)
	}
}
