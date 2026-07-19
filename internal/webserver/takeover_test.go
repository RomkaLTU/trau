package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
)

type launchCall struct {
	app     string
	command string
}

// fakeLauncher records terminal launches instead of opening windows, so the
// takeover orchestration is asserted without a GUI.
type fakeLauncher struct {
	mu       sync.Mutex
	launches []launchCall
	err      error
	onLaunch func()
}

func (f *fakeLauncher) Launch(_ context.Context, app, command string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.launches = append(f.launches, launchCall{app: app, command: command})
	if f.onLaunch != nil {
		f.onLaunch()
	}
	return nil
}

func (f *fakeLauncher) calls() []launchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]launchCall, len(f.launches))
	copy(out, f.launches)
	return out
}

func takeoverServer(t *testing.T, home string) (*Server, *fakeSupervisor, *fakeLauncher, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.goos = "darwin"
	sup := &fakeSupervisor{}
	s.sup = sup
	launcher := &fakeLauncher{}
	s.term = launcher
	s.sessionExists = func(string) bool { return true }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, sup, launcher, ts
}

func fastTakeoverWait(t *testing.T) {
	t.Helper()
	prevStop, prevPoll := takeoverStopTimeout, takeoverPollInterval
	takeoverStopTimeout, takeoverPollInterval = 500*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { takeoverStopTimeout, takeoverPollInterval = prevStop, prevPoll })
}

func seedTakeoverRun(t *testing.T, s *Server, root, ticket string, fields map[string]string) {
	t.Helper()
	repo := registry.Repo{Name: filepath.Base(root), Root: root, RunsDir: repoRunsDir(root)}
	if err := s.stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}
	if fields != nil {
		if err := s.stores.Checkpoints().Upsert(root, ticket, fields); err != nil {
			t.Fatalf("upsert checkpoint: %v", err)
		}
	}
}

func doTakeover(t *testing.T, ts *httptest.Server, repo, ticket string) (int, map[string]any) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/runs/"+ticket+"/takeover", nil)
	defer func() { _ = res.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode takeover body: %v", err)
	}
	return res.StatusCode, body
}

func TestTakeoverStopsLiveRunThenLaunches(t *testing.T) {
	home := t.TempDir()
	s, sup, launcher, ts := takeoverServer(t, home)
	fastTakeoverWait(t)

	root := filepath.Join(t.TempDir(), "acme")
	seedTakeoverRun(t, s, root, "COD-7", map[string]string{"PHASE": "build", "SESSION": "sess-1"})
	entry := registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateWorking,
		Ticket:       "COD-7",
	}
	writeEntry(t, home, entry)

	inst := testStoresAt(t, home).Instances()
	var mu sync.Mutex
	var order []string
	sup.onSignal = func(int, syscall.Signal) {
		parked := entry
		parked.SessionState = registry.StateParked
		_ = inst.Upsert(parked)
		mu.Lock()
		order = append(order, "signal")
		mu.Unlock()
	}
	launcher.onLaunch = func() {
		mu.Lock()
		order = append(order, "launch")
		mu.Unlock()
	}

	status, body := doTakeover(t, ts, "acme", "COD-7")
	if status != http.StatusOK {
		t.Fatalf("takeover status = %d, body %v, want 200", status, body)
	}
	if body["stopped"] != true || body["opened"] != true {
		t.Errorf("body = %v, want stopped and opened true", body)
	}
	if len(sup.signals) != 1 || sup.signals[0].pid != entry.PID || sup.signals[0].sig != syscall.SIGTERM {
		t.Errorf("signals = %+v, want one SIGTERM to pid %d", sup.signals, entry.PID)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if !reflect.DeepEqual(got, []string{"signal", "launch"}) {
		t.Errorf("order = %v, want signal before launch", got)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	want := launchCall{app: "Terminal", command: shellCommand(exe, "takeover", "--repo", root, "COD-7")}
	if calls := launcher.calls(); len(calls) != 1 || calls[0] != want {
		t.Errorf("launches = %+v, want [%+v]", calls, want)
	}
}

func TestTakeoverParkedRunSkipsStop(t *testing.T) {
	home := t.TempDir()
	s, sup, launcher, ts := takeoverServer(t, home)

	root := filepath.Join(t.TempDir(), "acme")
	seedTakeoverRun(t, s, root, "COD-7", map[string]string{"PHASE": "build", "SESSION": "sess-1"})
	writeEntry(t, home, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateParked,
		Ticket:       "COD-7",
	})

	status, body := doTakeover(t, ts, "acme", "COD-7")
	if status != http.StatusOK {
		t.Fatalf("takeover status = %d, body %v, want 200", status, body)
	}
	if body["stopped"] != false || body["opened"] != true {
		t.Errorf("body = %v, want stopped false, opened true", body)
	}
	if len(sup.signals) != 0 {
		t.Errorf("signals = %+v, want none", sup.signals)
	}
	if calls := launcher.calls(); len(calls) != 1 {
		t.Errorf("launches = %+v, want exactly one", calls)
	}
}

func TestTakeoverStopWaitTimeout(t *testing.T) {
	home := t.TempDir()
	s, sup, launcher, ts := takeoverServer(t, home)
	fastTakeoverWait(t)

	root := filepath.Join(t.TempDir(), "acme")
	seedTakeoverRun(t, s, root, "COD-7", map[string]string{"PHASE": "build", "SESSION": "sess-1"})
	writeEntry(t, home, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateWorking,
		Ticket:       "COD-7",
	})

	status, body := doTakeover(t, ts, "acme", "COD-7")
	if status != http.StatusGatewayTimeout {
		t.Fatalf("takeover status = %d, body %v, want 504", status, body)
	}
	if len(sup.signals) != 1 {
		t.Errorf("signals = %+v, want the stop attempt", sup.signals)
	}
	if calls := launcher.calls(); len(calls) != 0 {
		t.Errorf("launches = %+v, want none after timeout", calls)
	}
}

func TestTakeoverConflicts(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		ticket string
		reason string
		errHas string
	}{
		{name: "busy with another ticket", state: registry.StateWorking, ticket: "COD-9", reason: "repo_busy", errHas: "COD-9"},
		{name: "already taken over", state: registry.StateTakeover, ticket: "COD-7", reason: "already_taken_over", errHas: "taken over"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			s, sup, launcher, ts := takeoverServer(t, home)

			root := filepath.Join(t.TempDir(), "acme")
			seedTakeoverRun(t, s, root, "COD-7", map[string]string{"PHASE": "build", "SESSION": "sess-1"})
			writeEntry(t, home, registry.Entry{
				PID:          os.Getpid(),
				RepoRoot:     root,
				StartedAt:    time.Now(),
				Heartbeat:    time.Now(),
				SessionState: tc.state,
				Ticket:       tc.ticket,
			})

			status, body := doTakeover(t, ts, "acme", "COD-7")
			if status != http.StatusConflict {
				t.Fatalf("takeover status = %d, body %v, want 409", status, body)
			}
			if body["reason"] != tc.reason {
				t.Errorf("reason = %v, want %s", body["reason"], tc.reason)
			}
			if msg, _ := body["error"].(string); !strings.Contains(msg, tc.errHas) {
				t.Errorf("error = %q, want it to mention %q", msg, tc.errHas)
			}
			if len(sup.signals) != 0 || len(launcher.calls()) != 0 {
				t.Errorf("signals = %+v, launches = %+v, want neither", sup.signals, launcher.calls())
			}
		})
	}
}

func TestTakeoverNoResumableSession(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]string
		exists bool
	}{
		{name: "checkpoint without session", fields: map[string]string{"PHASE": "build"}, exists: true},
		{name: "transcript gone", fields: map[string]string{"PHASE": "build", "SESSION": "sess-1"}, exists: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			s, _, launcher, ts := takeoverServer(t, home)
			s.sessionExists = func(string) bool { return tc.exists }

			root := filepath.Join(t.TempDir(), "acme")
			seedTakeoverRun(t, s, root, "COD-7", tc.fields)

			status, body := doTakeover(t, ts, "acme", "COD-7")
			if status != http.StatusConflict {
				t.Fatalf("takeover status = %d, body %v, want 409", status, body)
			}
			if body["reason"] != "no_resumable_session" {
				t.Errorf("reason = %v, want no_resumable_session", body["reason"])
			}
			if calls := launcher.calls(); len(calls) != 0 {
				t.Errorf("launches = %+v, want none", calls)
			}
		})
	}
}

func TestTakeoverNonDarwin(t *testing.T) {
	home := t.TempDir()
	s, sup, launcher, ts := takeoverServer(t, home)
	s.goos = "linux"

	root := filepath.Join(t.TempDir(), "acme")
	seedTakeoverRun(t, s, root, "COD-7", map[string]string{"PHASE": "build", "SESSION": "sess-1"})

	status, body := doTakeover(t, ts, "acme", "COD-7")
	if status != http.StatusNotImplemented {
		t.Fatalf("takeover status = %d, body %v, want 501", status, body)
	}
	if len(sup.signals) != 0 || len(launcher.calls()) != 0 {
		t.Errorf("signals = %+v, launches = %+v, want neither", sup.signals, launcher.calls())
	}
}

func TestTakeoverUsesConfiguredTerminalApp(t *testing.T) {
	home := t.TempDir()
	s, _, launcher, ts := takeoverServer(t, home)

	root := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, config.ProjectConfigName), []byte("TERMINAL_APP=iTerm\n"), 0o644); err != nil {
		t.Fatalf("write repo config: %v", err)
	}
	seedTakeoverRun(t, s, root, "COD-7", map[string]string{"PHASE": "build", "SESSION": "sess-1"})

	status, body := doTakeover(t, ts, "acme", "COD-7")
	if status != http.StatusOK {
		t.Fatalf("takeover status = %d, body %v, want 200", status, body)
	}
	calls := launcher.calls()
	if len(calls) != 1 || calls[0].app != "iTerm" {
		t.Errorf("launches = %+v, want one iTerm launch", calls)
	}
}

func TestOsascriptArgs(t *testing.T) {
	command := `'/Applications/My App/trau' 'takeover' '--repo' '/tmp/repo' 'COD-7'`
	quoted := `"'/Applications/My App/trau' 'takeover' '--repo' '/tmp/repo' 'COD-7'"`

	terminal := osascriptArgs("Terminal", command)
	wantTerminal := []string{
		"-e", `tell application "Terminal" to do script ` + quoted,
		"-e", `tell application "Terminal" to activate`,
	}
	if !reflect.DeepEqual(terminal, wantTerminal) {
		t.Errorf("Terminal args = %q, want %q", terminal, wantTerminal)
	}

	iterm := osascriptArgs("iTerm", command)
	wantITerm := []string{
		"-e", `tell application "iTerm" to create window with default profile command ` + quoted,
		"-e", `tell application "iTerm" to activate`,
	}
	if !reflect.DeepEqual(iterm, wantITerm) {
		t.Errorf("iTerm args = %q, want %q", iterm, wantITerm)
	}
}

func TestAppleScriptString(t *testing.T) {
	got := appleScriptString(`say "hi" \ bye`)
	want := `"say \"hi\" \\ bye"`
	if got != want {
		t.Errorf("appleScriptString = %s, want %s", got, want)
	}
}

func TestShellCommand(t *testing.T) {
	got := shellCommand("/Applications/My App/trau", "takeover", "--repo", "/tmp/it's here", "COD-7")
	want := `'/Applications/My App/trau' 'takeover' '--repo' '/tmp/it'\''s here' 'COD-7'`
	if got != want {
		t.Errorf("shellCommand = %s, want %s", got, want)
	}
}
