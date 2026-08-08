package webserver

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

func postAction(t *testing.T, url string) (int, WorktreeAppView, string) {
	t.Helper()
	res := postJSON(t, url, struct{}{})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusOK {
		var out WorktreeAppView
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode app view: %v", err)
		}
		return res.StatusCode, out, ""
	}
	var fail struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&fail)
	return res.StatusCode, WorktreeAppView{}, fail.Error
}

func getLogs(t *testing.T, url string) (int, WorktreeAppLogsView) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	var out WorktreeAppLogsView
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode logs: %v", err)
		}
	}
	return res.StatusCode, out
}

// TestWorktreeAppControlsStartRestartStop is the QA cockpit end to end: Start
// brings the app up on its own port, Restart bounces it onto a new process, and
// Stop ends it — each answering with the standing the run page renders.
func TestWorktreeAppControlsStartRestartStop(t *testing.T) {
	s, ts, repo, base := appServer(t, "serve-me", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	path := addWorktree(t, repo, dir, "COD-1585")
	postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-1585", Path: path})
	prefix := ts.URL + APIPrefix + "/repos/acme/worktrees/COD-1585/app/"

	code, started, msg := postAction(t, prefix+"start")
	if code != http.StatusOK {
		t.Fatalf("start status = %d (%s), want 200", code, msg)
	}
	if started.State != hubstore.AppRunning || started.URL == "" {
		t.Fatalf("start = %+v, want a running app with a URL", started)
	}

	code, restarted, msg := postAction(t, prefix+"restart")
	if code != http.StatusOK {
		t.Fatalf("restart status = %d (%s), want 200", code, msg)
	}
	if restarted.State != hubstore.AppRunning {
		t.Fatalf("restart = %+v, want a running app", restarted)
	}
	if restarted.PID == started.PID {
		t.Errorf("restart pid = %d, want a different process than %d", restarted.PID, started.PID)
	}

	code, stopped, msg := postAction(t, prefix+"stop")
	if code != http.StatusOK {
		t.Fatalf("stop status = %d (%s), want 200", code, msg)
	}
	if stopped.State != hubstore.AppStopped {
		t.Errorf("stop = %+v, want a stopped app", stopped)
	}
	if stopped.URL != "" {
		t.Errorf("stop URL = %q, want none — a stopped app is not a link", stopped.URL)
	}
	if s.alive(started.PID) {
		t.Errorf("pid %d still alive after stop", started.PID)
	}
}

// TestWorktreeAppControlsReachTheFeed pins the events the run feed is read for: a
// start that came up, a stop that ended it, and a start command that never served.
func TestWorktreeAppControlsReachTheFeed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		serve  bool
		action string
		want   string
	}{
		{name: "start", serve: true, action: "start", want: event.KindAppStarted},
		{name: "failed start", serve: false, action: "start", want: event.KindAppFailed},
		{name: "stop", serve: true, action: "stop", want: event.KindAppStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ts, repo, base := appServer(t, "serve-me", tc.serve)
			dir := filepath.Join(t.TempDir(), "worktrees")
			path := addWorktree(t, repo, dir, "COD-1585")
			postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-1585", Path: path})
			prefix := ts.URL + APIPrefix + "/repos/acme/worktrees/COD-1585/app/"
			if tc.action == "stop" {
				if code, _, msg := postAction(t, prefix+"start"); code != http.StatusOK {
					t.Fatalf("start status = %d (%s), want 200", code, msg)
				}
			}
			if code, _, msg := postAction(t, prefix+tc.action); code != http.StatusOK {
				t.Fatalf("%s status = %d (%s), want 200", tc.action, code, msg)
			}
			evs, err := s.stores.Events().Recent(repo.Root, 20, 0)
			if err != nil {
				t.Fatalf("Recent events: %v", err)
			}
			if !hasEventKind(evs, tc.want) {
				t.Fatalf("no %s event; got %+v", tc.want, evs)
			}
			for _, ev := range evs {
				if ev.Kind == tc.want && !strings.Contains(ev.Fields, `"ticket":"COD-1585"`) {
					t.Errorf("%s fields = %q, want the ticket carried", tc.want, ev.Fields)
				}
			}
		})
	}
}

// TestWorktreeAppControlRefusedWhileARunHoldsTheTree is the disabled-with-a-reason
// contract: a loop working in the tree owns its app, and a parked one — the state
// the QA person is looking at the run in — does not.
func TestWorktreeAppControlRefusedWhileARunHoldsTheTree(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  string
		ticket string
		want   int
	}{
		{name: "working run holds it", state: registry.StateWorking, ticket: "COD-1585", want: http.StatusConflict},
		{name: "takeover holds it", state: registry.StateTakeover, ticket: "COD-1585", want: http.StatusConflict},
		{name: "epic sub-issue shares the tree", state: registry.StateWorking, ticket: "COD-1586", want: http.StatusConflict},
		{name: "parked run does not", state: registry.StateParked, ticket: "COD-1585", want: http.StatusOK},
		{name: "idle loop does not", state: registry.StateIdle, ticket: "", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ts, repo, base := appServer(t, "serve-me", true)
			dir := filepath.Join(t.TempDir(), "worktrees")
			path := addWorktree(t, repo, dir, "COD-1585")
			postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-1585", Path: path})
			writeInstanceEntry(t, s, registry.Entry{
				PID:          os.Getpid(),
				RepoRoot:     repo.Root,
				SessionState: tc.state,
				Ticket:       tc.ticket,
				Phase:        "verifying",
			})
			prefix := ts.URL + APIPrefix + "/repos/acme/worktrees/COD-1585/app/"
			code, _, msg := postAction(t, prefix+"start")
			if code != tc.want {
				t.Fatalf("start status = %d (%s), want %d", code, msg, tc.want)
			}
			if tc.want != http.StatusConflict {
				return
			}
			if msg == "" {
				t.Fatal("refusal carried no reason — the button has nothing to say why it is disabled")
			}
			res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/worktrees")
			if err != nil {
				t.Fatalf("list worktrees: %v", err)
			}
			defer func() { _ = res.Body.Close() }()
			var views []WorktreeView
			if err := json.NewDecoder(res.Body).Decode(&views); err != nil {
				t.Fatalf("decode worktrees: %v", err)
			}
			if len(views) != 1 || views[0].Busy == "" {
				t.Fatalf("list = %+v, want the tree carrying the reason it is busy", views)
			}
		})
	}
}

// TestWorktreeAppLogsTailsTheCapture is the log viewer's contract: the last n lines
// of what the start command printed, oldest first, flagged when more was left above.
func TestWorktreeAppLogsTailsTheCapture(t *testing.T) {
	s, ts, repo, base := appServer(t, "serve-me", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	path := addWorktree(t, repo, dir, "COD-1585")
	postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-1585", Path: path})

	logPath := s.appLogPath(repo.Root, "COD-1585")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for i := range 50 {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	url := ts.URL + APIPrefix + "/repos/acme/worktrees/COD-1585/app/logs"
	code, all := getLogs(t, url)
	if code != http.StatusOK {
		t.Fatalf("logs status = %d, want 200", code)
	}
	if len(all.Lines) != 50 || all.Lines[0] != "line 0" || all.Lines[49] != "line 49" {
		t.Fatalf("logs = %d lines (%v…), want all 50 oldest first", len(all.Lines), all.Lines)
	}
	if all.Truncated {
		t.Error("truncated on a capture that fit whole")
	}

	code, tail := getLogs(t, url+"?n=5")
	if code != http.StatusOK {
		t.Fatalf("tail status = %d, want 200", code)
	}
	if len(tail.Lines) != 5 || tail.Lines[4] != "line 49" {
		t.Fatalf("tail = %v, want the last 5 lines", tail.Lines)
	}
	if !tail.Truncated {
		t.Error("tail not flagged truncated, so the viewer implies the app said nothing before it")
	}
	if _, capped := getLogs(t, url+"?n=999999"); len(capped.Lines) != 50 {
		t.Errorf("capped tail = %d lines, want the whole 50-line capture", len(capped.Lines))
	}
}

// TestWorktreeAppLogsAnswerAnEmptyCaptureAsAList pins the wire shape the viewer
// reads: a tree whose app has printed nothing — or has not started once yet, so the
// file is not there at all — answers an empty list, never a null the log panel
// would then fail to measure.
func TestWorktreeAppLogsAnswerAnEmptyCaptureAsAList(t *testing.T) {
	s, ts, repo, base := appServer(t, "serve-me", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	path := addWorktree(t, repo, dir, "COD-1585")
	postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-1585", Path: path})

	url := ts.URL + APIPrefix + "/repos/acme/worktrees/COD-1585/app/logs"
	cases := []struct {
		name    string
		prepare func()
	}{
		{name: "no capture yet", prepare: func() {}},
		{name: "capture printed nothing", prepare: func() {
			logPath := s.appLogPath(repo.Root, "COD-1585")
			if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(logPath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare()
			res, err := http.Get(url)
			if err != nil {
				t.Fatalf("GET %s: %v", url, err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("logs status = %d, want 200", res.StatusCode)
			}
			var body struct {
				Lines *[]string `json:"lines"`
			}
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatalf("decode logs: %v", err)
			}
			if body.Lines == nil {
				t.Fatal("lines came back null, so the viewer reads a length off nothing")
			}
			if len(*body.Lines) != 0 {
				t.Fatalf("lines = %v, want none captured", *body.Lines)
			}
		})
	}
}

// TestWorktreeAppEndpointsGuardTheirInputs keeps the new routes behind the same
// findRepo and row guards every other worktree endpoint answers to.
func TestWorktreeAppEndpointsGuardTheirInputs(t *testing.T) {
	_, ts, repo, base := appServer(t, "serve-me", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	path := addWorktree(t, repo, dir, "COD-1585")
	postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-1585", Path: path})

	cases := []struct {
		name string
		url  string
		want int
	}{
		{
			name: "unknown repo",
			url:  ts.URL + APIPrefix + "/repos/nope/worktrees/COD-1585/app/start",
			want: http.StatusNotFound,
		},
		{
			name: "unknown worktree",
			url:  ts.URL + APIPrefix + "/repos/acme/worktrees/COD-404/app/start",
			want: http.StatusNotFound,
		},
		{
			name: "unknown action",
			url:  ts.URL + APIPrefix + "/repos/acme/worktrees/COD-1585/app/detonate",
			want: http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, _ := postAction(t, tc.url); code != tc.want {
				t.Errorf("status = %d, want %d", code, tc.want)
			}
		})
	}
	if code, _ := getLogs(t, ts.URL+APIPrefix+"/repos/acme/worktrees/COD-404/app/logs"); code != http.StatusNotFound {
		t.Errorf("logs for an unknown tree = %d, want 404", code)
	}
	if code, _ := getLogs(t, ts.URL+APIPrefix+"/repos/nope/worktrees/COD-1585/app/logs"); code != http.StatusNotFound {
		t.Errorf("logs for an unknown repo = %d, want 404", code)
	}
}

// TestWorktreeAppControlNoStartCommand keeps a repo that serves no app honest: the
// control answers, says it serves nothing, and starts no process.
func TestWorktreeAppControlNoStartCommand(t *testing.T) {
	_, ts, repo, base := appServer(t, "", true)
	dir := filepath.Join(t.TempDir(), "worktrees")
	path := addWorktree(t, repo, dir, "COD-1585")
	postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-1585", Path: path})

	code, view, msg := postAction(t, ts.URL+APIPrefix+"/repos/acme/worktrees/COD-1585/app/start")
	if code != http.StatusOK {
		t.Fatalf("start status = %d (%s), want 200", code, msg)
	}
	if view.Serving || view.State != hubstore.AppStopped {
		t.Errorf("view = %+v, want a stopped, non-serving app", view)
	}
}
