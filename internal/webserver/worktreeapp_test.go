package webserver

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/proc"
	"github.com/RomkaLTU/trau/internal/registry"
)

// appServer is worktreeServer with an APP_START_CMD in the repo's own ini and a
// supervisor that records app spawns instead of running them. serve decides what a
// spawn does: listen on the allocated port (the app came up) or nothing at all (the
// command that never serves).
func appServer(t *testing.T, startCmd string, serve bool) (*Server, *httptest.Server, registry.Repo, string) {
	t.Helper()
	s, ts, repo, base := worktreeServer(t)
	if startCmd != "" {
		ini := "APP_START_CMD=" + startCmd + "\nWORKTREE_PORT_BASE=" + strconv.Itoa(testPortBase()) + "\n"
		if err := os.WriteFile(config.ProjectConfigPath(repo.Root), []byte(ini), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeSupervisor{}
	var liveMu sync.Mutex
	live := map[int]bool{}
	fake.onAppSpawn = func(spec AppSpec, pid int) {
		liveMu.Lock()
		live[pid] = true
		liveMu.Unlock()
		// The spawned command's own output is what a failed start is judged on, so
		// the fake writes the log the real one would.
		_ = os.MkdirAll(filepath.Dir(spec.LogPath), 0o755)
		_ = os.WriteFile(spec.LogPath, []byte("booting "+spec.Command+"\nEADDRNOTAVAIL\n"), 0o644)
		if !serve {
			return
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+appSpecPort(spec))
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = ln.Close() })
	}
	fake.onKill = func(pid int) {
		liveMu.Lock()
		delete(live, pid)
		liveMu.Unlock()
	}
	s.sup = fake
	// A fake spawn hands back a pid no process ever had, so liveness is answered
	// from what the fake started rather than from the OS. Lanes racing each other
	// read it from their own goroutines, so it is guarded like the real thing.
	s.alive = func(pid int) bool {
		liveMu.Lock()
		defer liveMu.Unlock()
		return live[pid]
	}
	// The readiness window is the whole cost of the never-listens case; a test
	// waits a beat, not a minute.
	s.appReadyWait = 600 * time.Millisecond
	return s, ts, repo, base
}

// testPortBase keeps the allocator well clear of the ports a developer's own
// machine is likely to be serving on while these tests run.
func testPortBase() int { return 47300 }

func appSpecPort(spec AppSpec) string {
	for _, kv := range spec.Env {
		if port, ok := strings.CutPrefix(kv, "TRAU_APP_PORT="); ok {
			return port
		}
	}
	return "0"
}

func postApp(t *testing.T, ts *httptest.Server, ticket string) (int, WorktreeAppView) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/worktrees/app", worktreeAppRequest{Ticket: ticket})
	defer func() { _ = res.Body.Close() }()
	var out WorktreeAppView
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode app view: %v", err)
		}
	}
	return res.StatusCode, out
}

// TestEnsureWorktreeAppServesTheTreeOnItsOwnPort is the whole point of the slice:
// the run child asks for a verify URL, the hub starts APP_START_CMD in the tree on
// an allocated port, and the URL it hands back is that port on localhost.
func TestEnsureWorktreeAppServesTheTreeOnItsOwnPort(t *testing.T) {
	s, ts, repo, base := appServer(t, "serve-me", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	path := addWorktree(t, repo, dir, "COD-1584")
	postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-1584", Path: path})

	code, app := postApp(t, ts, "COD-1584")
	if code != http.StatusOK {
		t.Fatalf("ensure status = %d, want 200", code)
	}
	if app.State != hubstore.AppRunning || !app.Serving {
		t.Fatalf("app = %+v, want a running, serving app", app)
	}
	if app.Port < testPortBase() {
		t.Errorf("port = %d, want at or above the base %d", app.Port, testPortBase())
	}
	if want := "http://localhost:" + strconv.Itoa(app.Port); app.URL != want {
		t.Errorf("url = %q, want %q", app.URL, want)
	}

	spawns := s.sup.(*fakeSupervisor).appSpawns
	if len(spawns) != 1 {
		t.Fatalf("spawned %d apps, want 1", len(spawns))
	}
	if spawns[0].Dir != path || spawns[0].Command != "serve-me" {
		t.Errorf("spawn = %+v, want %q run in %s", spawns[0], "serve-me", path)
	}
	port := strconv.Itoa(app.Port)
	if !hasEnv(spawns[0].Env, "TRAU_APP_PORT="+port) || !hasEnv(spawns[0].Env, "PORT="+port) {
		t.Errorf("spawn env carries neither TRAU_APP_PORT nor PORT = %s", port)
	}

	// A second ask is answered from the row: the app is already up, so nothing is
	// started twice on a second port.
	if _, again := postApp(t, ts, "COD-1584"); again.Port != app.Port {
		t.Errorf("second ensure port = %d, want the same %d", again.Port, app.Port)
	}
	if got := len(s.sup.(*fakeSupervisor).appSpawns); got != 1 {
		t.Errorf("spawned %d apps across two ensures, want 1", got)
	}
}

// TestEnsureWorktreeAppGivesTwoLanesTwoPorts covers concurrent lanes: two trees
// asking at the same instant must not be handed the same port. The lanes start
// together on purpose — a port is only visible to another allocation once its own
// start has recorded it, so anything short of a real race passes regardless.
func TestEnsureWorktreeAppGivesTwoLanesTwoPorts(t *testing.T) {
	_, ts, repo, base := appServer(t, "serve-me", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	tickets := []string{"COD-1", "COD-2"}
	for _, ticket := range tickets {
		postJSONDiscard(t, base, WorktreeRequest{Ticket: ticket, Path: addWorktree(t, repo, dir, ticket)})
	}

	views := make([]WorktreeAppView, len(tickets))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, ticket := range tickets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, views[i] = postApp(t, ts, ticket)
		}()
	}
	close(start)
	wg.Wait()

	first, second := views[0], views[1]
	if first.State != hubstore.AppRunning || second.State != hubstore.AppRunning {
		t.Fatalf("states = %q/%q, want both running", first.State, second.State)
	}
	if first.Port == second.Port {
		t.Fatalf("both lanes got port %d, want two ports", first.Port)
	}
	// Both apps hold their own port for real: the fake only listens on the port it
	// was handed, so a collision would have left the loser unable to bind.
	for _, view := range views {
		if view.Port <= 0 {
			t.Fatalf("app = %+v, want an allocated port", view)
		}
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(view.Port), time.Second); err != nil {
			t.Errorf("nothing accepting on %s's port %d: %v", view.Ticket, view.Port, err)
		} else {
			_ = conn.Close()
		}
	}
}

// TestEnsureWorktreeAppFailsTheStartThatNeverListens: the command exits or hangs
// without ever serving, so the app is marked failed with its output kept — and the
// call still answers 200, because the run's fallback is to verify without a URL,
// never to fail over a dev server.
func TestEnsureWorktreeAppFailsTheStartThatNeverListens(t *testing.T) {
	s, ts, repo, base := appServer(t, "true", false)
	dir := filepath.Join(t.TempDir(), "worktrees")
	postJSONDiscard(t, base, WorktreeRequest{
		Ticket: "COD-1584", Path: addWorktree(t, repo, dir, "COD-1584"),
	})

	code, app := postApp(t, ts, "COD-1584")
	if code != http.StatusOK {
		t.Fatalf("ensure status = %d, want 200 even for a failed start", code)
	}
	if app.State != hubstore.AppFailed {
		t.Fatalf("state = %q, want %q", app.State, hubstore.AppFailed)
	}
	if app.URL != "" {
		t.Errorf("url = %q, want none for an app that never listened", app.URL)
	}
	if !strings.Contains(app.Output, "EADDRNOTAVAIL") {
		t.Errorf("output = %q, want the captured start output", app.Output)
	}
	if len(s.sup.(*fakeSupervisor).kills) != 1 {
		t.Errorf("killed %d processes, want the one that never served", len(s.sup.(*fakeSupervisor).kills))
	}

	row, _, err := s.stores.Worktrees().ByTicket(repo.Root, "COD-1584")
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if row.App.State != hubstore.AppFailed || row.App.PID != 0 {
		t.Errorf("row app = %+v, want a failed app with no pid", row.App)
	}
}

// TestEnsureWorktreeAppWithoutAStartCommandServesNothing pins the empty
// APP_START_CMD case: nothing is spawned and the answer says the repo serves no
// app, which is what leaves the run's configured APP_URL alone.
func TestEnsureWorktreeAppWithoutAStartCommandServesNothing(t *testing.T) {
	s, ts, repo, base := appServer(t, "", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	postJSONDiscard(t, base, WorktreeRequest{
		Ticket: "COD-1584", Path: addWorktree(t, repo, dir, "COD-1584"),
	})

	code, app := postApp(t, ts, "COD-1584")
	if code != http.StatusOK {
		t.Fatalf("ensure status = %d, want 200", code)
	}
	if app.Serving || app.URL != "" || app.State != hubstore.AppStopped {
		t.Fatalf("app = %+v, want a non-serving, stopped answer", app)
	}
	if got := len(s.sup.(*fakeSupervisor).appSpawns); got != 0 {
		t.Errorf("spawned %d apps without an APP_START_CMD, want 0", got)
	}
}

// TestWorktreeAppServingSaysWhetherTheRepoServesAnAppAtAll is what a reader needs
// to tell a stopped app from no app at all: both read app_state=stopped, and only
// the repo with an APP_START_CMD has a Start worth offering.
func TestWorktreeAppServingSaysWhetherTheRepoServesAnAppAtAll(t *testing.T) {
	for _, tc := range []struct {
		name     string
		startCmd string
		want     bool
	}{
		{name: "with a start command", startCmd: "serve-me", want: true},
		{name: "without one", startCmd: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, repo, base := appServer(t, tc.startCmd, true)
			dir := filepath.Join(t.TempDir(), "worktrees")
			row := decodeWorktree(t, postJSON(t, base, WorktreeRequest{
				Ticket: "COD-1584", Path: addWorktree(t, repo, dir, "COD-1584"),
			}))
			if row.Serving != tc.want {
				t.Fatalf("serving = %v, want %v", row.Serving, tc.want)
			}
			if row.AppState != hubstore.AppStopped {
				t.Fatalf("app state = %q, want %q either way", row.AppState, hubstore.AppStopped)
			}
		})
	}
}

func TestEnsureWorktreeAppRefusesATicketWithNoTree(t *testing.T) {
	_, ts, _, _ := appServer(t, "serve-me", true)
	if code, _ := postApp(t, ts, "COD-404"); code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a ticket with no worktree", code)
	}
}

// TestWorktreeDeleteStopsTheAppFirst: removal always stops the app, so a tree that
// leaves the disk never leaves a dev server behind holding its port.
func TestWorktreeDeleteStopsTheAppFirst(t *testing.T) {
	s, ts, repo, base := appServer(t, "serve-me", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	row := decodeWorktree(t, postJSON(t, base, WorktreeRequest{
		Ticket: "COD-1584", Path: addWorktree(t, repo, dir, "COD-1584"),
	}))
	_, app := postApp(t, ts, "COD-1584")
	if app.State != hubstore.AppRunning {
		t.Fatalf("app state = %q, want it running before the delete", app.State)
	}

	res, body := deleteReq(t, ts, APIPrefix+"/repos/acme/worktrees/"+strconv.FormatInt(row.ID, 10))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (body %s)", res.StatusCode, body)
	}
	if got := s.sup.(*fakeSupervisor).kills; len(got) != 1 || got[0] != app.PID {
		t.Fatalf("kills = %v, want the app's pid %d", got, app.PID)
	}
	settled, _, err := s.stores.Worktrees().ByTicket(repo.Root, "COD-1584")
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if settled.App.State != hubstore.AppStopped || settled.App.PID != 0 {
		t.Errorf("app after delete = %+v, want it stopped with no pid", settled.App)
	}
}

// TestReconcileWorktreeAppClearsADeadApp is the boot sweep: a recorded pid that is
// gone leaves a row promising a URL nothing answers, so it returns to stopped. A pid
// still alive is this hub's to adopt and is left exactly as it stands.
func TestReconcileWorktreeAppClearsADeadApp(t *testing.T) {
	s, _, repo, base := appServer(t, "serve-me", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	row := decodeWorktree(t, postJSON(t, base, WorktreeRequest{
		Ticket: "COD-1584", Path: addWorktree(t, repo, dir, "COD-1584"),
	}))
	dead := hubstore.WorktreeApp{Port: 47399, PID: 999999, State: hubstore.AppRunning}
	if _, err := s.stores.Worktrees().SetApp(repo.Root, row.ID, dead); err != nil {
		t.Fatalf("seed a dead app: %v", err)
	}

	s.reconcileWorktrees()

	after, _, err := s.stores.Worktrees().ByTicket(repo.Root, "COD-1584")
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if after.App.State != hubstore.AppStopped {
		t.Errorf("app state = %q, want %q once its process is gone", after.App.State, hubstore.AppStopped)
	}
	if after.State != hubstore.WorktreeActive {
		t.Errorf("worktree state = %q, want the standing tree untouched", after.State)
	}

	live := hubstore.WorktreeApp{Port: 47399, PID: 4242, State: hubstore.AppRunning}
	s.alive = func(pid int) bool { return pid == live.PID }
	if _, err := s.stores.Worktrees().SetApp(repo.Root, row.ID, live); err != nil {
		t.Fatalf("seed a live app: %v", err)
	}
	s.reconcileWorktrees()
	adopted, _, err := s.stores.Worktrees().ByTicket(repo.Root, "COD-1584")
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if adopted.App != live {
		t.Errorf("app = %+v, want the live app re-adopted as %+v", adopted.App, live)
	}
}

// TestAllocateAppPortSkipsAPortAlreadyBound proves the bind probe: a port a
// developer's own server holds is not handed to a tree just because no row claims
// it.
func TestAllocateAppPortSkipsAPortAlreadyBound(t *testing.T) {
	s, _, _, _ := appServer(t, "serve-me", true)
	base := testPortBase() + 500
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(base))
	if err != nil {
		t.Skipf("cannot bind %d to stand in for a busy port: %v", base, err)
	}
	defer func() { _ = ln.Close() }()

	port, release, err := s.allocateAppPort(base)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	defer release()
	if port == base {
		t.Fatalf("allocated the bound port %d", port)
	}
	if port < base {
		t.Errorf("allocated %d, want a port at or above the base %d", port, base)
	}
}

func hasEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// TestRealSpawnAppServesFromTheTreeAndDiesWithItsGroup exercises the one part the
// fake supervisor stands in for: the real spawn. It proves the command runs under a
// shell in the worktree with the allocated port in its environment, that its output
// lands in the captured log, that readiness is detected by the port accepting, and
// that killing the process group takes the whole tree down — a dev server's real
// children, not just the shell trau started.
func TestRealSpawnAppServesFromTheTreeAndDiesWithItsGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the listener stand-in is a POSIX shell one-liner")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 stands in for the app that listens")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	port := testPortBase() + 601
	command := `echo "serving $TRAU_APP_PORT from $(pwd)"; python3 -c "
import socket,os,time
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', int(os.environ['PORT']))); s.listen(8)
time.sleep(30)"`

	pid, err := osSupervisor{}.SpawnApp(AppSpec{
		Dir: dir, Command: command, Env: appEnv(port), LogPath: logPath,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { _ = proc.KillGroup(pid) })

	if !waitForPort(t.Context(), port, logPath, 15*time.Second) {
		t.Fatalf("the app never listened on %d; captured output = %s", port, appLogTail(logPath))
	}
	if out := appLogTail(logPath); !strings.Contains(out, "serving "+strconv.Itoa(port)) || !strings.Contains(out, dir) {
		t.Errorf("captured output = %q, want the command's own output from %s", out, dir)
	}

	if err := proc.KillGroup(pid); err != nil {
		t.Fatalf("kill the app's group: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !portFree(port) {
		if time.Now().After(deadline) {
			t.Fatal("the port is still held after the group was killed — the listener outlived its shell")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
