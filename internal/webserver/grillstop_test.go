package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// grillStubSleep holds its stdout open through a child that outlives the shell, so a
// stop that ended only the shell would leave the turn draining a pipe nothing writes.
const grillStubSleep = `#!/bin/sh
printf '{"type":"system","subtype":"init","session_id":"sid-stop"}\n'
: > "$GRILL_STUB_DIR/started"
sleep 60
printf '{"type":"result","subtype":"success","session_id":"sid-stop","is_error":false}\n'
`

// grillRateLimitEvent is the rate-limit telemetry claude prints on every turn,
// verbatim: it reports a limit nowhere near hit, but the wall classifier reads its
// "rate_limit" text as one.
const grillRateLimitEvent = `{"type":"rate_limit_event","rate_limit_info":` +
	`{"status":"allowed_warning","resetsAt":1786100400,"rateLimitType":"seven_day",` +
	`"utilization":0.53,"isUsingOverage":false},"session_id":"sid-stop"}`

func postGrillStop(t *testing.T, ts *httptest.Server, sid string, want int) GrillSessionView {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sid+"/stop", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != want {
		t.Fatalf("stop status = %d, want %d", res.StatusCode, want)
	}
	var v GrillSessionView
	if want == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
			t.Fatalf("decode stop: %v", err)
		}
	}
	return v
}

func grillStopError(t *testing.T, ts *httptest.Server, sid string, want int) string {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sid+"/stop", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != want {
		t.Fatalf("stop status = %d, want %d", res.StatusCode, want)
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode stop error: %v", err)
	}
	return body["error"]
}

func walkGrillStates(t *testing.T, stores *hubstore.Stores, sid int64, states []string) {
	t.Helper()
	for _, state := range states {
		if _, err := stores.Grill().Transition(sid, state, ""); err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
	}
}

func grillNoticeTexts(t *testing.T, stores *hubstore.Stores, sid int64) []string {
	t.Helper()
	msgs, err := stores.Grill().Messages(sid, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == hubstore.GrillRoleSystem && m.Kind == hubstore.GrillKindInfo {
			out = append(out, grillMessageText(m.Payload))
		}
	}
	return out
}

// A stop is only offered for a turn that can still be ended: a settled session has
// none, and neither does one already waiting on the user with its child gone.
func TestGrillStopRefused(t *testing.T) {
	tests := []struct {
		name   string
		states []string
		active bool
		want   string
	}{
		{name: "finished", states: []string{hubstore.GrillFinished}, want: "session no longer accepts a stop"},
		{
			name:   "applied",
			states: []string{hubstore.GrillFinished, hubstore.GrillApplied},
			want:   "session no longer accepts a stop",
		},
		{name: "abandoned", states: []string{hubstore.GrillAbandoned}, want: "session no longer accepts a stop"},
		{
			name:   "finished while its turn winds down",
			states: []string{hubstore.GrillFinished},
			active: true,
			want:   "session no longer accepts a stop",
		},
		{name: "waiting with no turn", states: []string{hubstore.GrillWaiting}, want: "no turn is running"},
		{name: "parked with no turn", states: []string{hubstore.GrillParked}, want: "no turn is running"},
		{name: "stalled with no turn", states: []string{hubstore.GrillStalled}, want: "no turn is running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, stores, repo, srv := grillHookServer(t)
			killed := 0
			srv.grillTurnActive = func(int64) bool { return tt.active }
			srv.stopGrillTurn = func(int64) { killed++ }

			sess := createGrill(t, ts, repo, "COD-1")
			sid, _ := strconv.ParseInt(sess.ID, 10, 64)
			walkGrillStates(t, stores, sid, tt.states)

			if got := grillStopError(t, ts, sess.ID, http.StatusConflict); got != tt.want {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
			if killed != 0 {
				t.Errorf("refused stop signalled the runner %d times", killed)
			}
		})
	}
}

func TestGrillStopParks(t *testing.T) {
	tests := []struct {
		name       string
		states     []string
		active     bool
		wantReason string
	}{
		{name: "running", wantReason: grillStoppedReason},
		{
			name:       "waiting on a live child",
			states:     []string{hubstore.GrillWaiting},
			active:     true,
			wantReason: grillStoppedReason,
		},
		// A session the ask_user idle timeout parked while its child kept working is
		// already where the stop lands, so only the child dies.
		{
			name:   "parked on a live child",
			states: []string{hubstore.GrillWaiting, hubstore.GrillParked},
			active: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, stores, repo, srv := grillHookServer(t)
			killed := 0
			srv.grillTurnActive = func(int64) bool { return tt.active }
			srv.stopGrillTurn = func(int64) { killed++ }

			sess := createGrill(t, ts, repo, "COD-1")
			sid, _ := strconv.ParseInt(sess.ID, 10, 64)
			walkGrillStates(t, stores, sid, tt.states)

			view := postGrillStop(t, ts, sess.ID, http.StatusOK)
			if view.State != hubstore.GrillParked || view.ParkedReason != tt.wantReason {
				t.Errorf("view = %s (%q), want parked (%q)", view.State, view.ParkedReason, tt.wantReason)
			}
			if killed != 1 {
				t.Errorf("runner signalled %d times, want once", killed)
			}
			if notices := grillNoticeTexts(t, stores, sid); len(notices) != 1 || notices[0] != "You stopped the agent mid-turn." {
				t.Errorf("notices = %q", notices)
			}
		})
	}
}

// A double click is one gesture: the second request answers with the session the
// first parked instead of noticing the transcript twice. Once the turn it killed is
// gone the session has nothing left to stop, like any other parked one.
func TestGrillStopTwiceNoticesOnce(t *testing.T) {
	ts, stores, repo, srv := grillHookServer(t)
	killed := 0
	var live atomic.Bool
	live.Store(true)
	srv.grillTurnActive = func(int64) bool { return live.Load() }
	srv.stopGrillTurn = func(int64) { killed++ }

	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)

	postGrillStop(t, ts, sess.ID, http.StatusOK)
	view := postGrillStop(t, ts, sess.ID, http.StatusOK)

	if view.State != hubstore.GrillParked || view.ParkedReason != grillStoppedReason {
		t.Errorf("repeat stop = %s (%q), want the stopped session back", view.State, view.ParkedReason)
	}
	if notices := grillNoticeTexts(t, stores, sid); len(notices) != 1 {
		t.Errorf("notices = %q, want one", notices)
	}
	if killed != 1 {
		t.Errorf("runner signalled %d times, want once", killed)
	}

	live.Store(false)
	if got := grillStopError(t, ts, sess.ID, http.StatusConflict); got != "no turn is running" {
		t.Errorf("stop on the settled session = %q", got)
	}
}

// An auto-accept session never pauses on its own, so stopping it also hands the
// answering back to the user rather than letting the next question be taken for them.
func TestGrillStopTurnsOffAutoAccept(t *testing.T) {
	ts, stores, repo, srv := grillHookServer(t)
	srv.stopGrillTurn = func(int64) {}
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)

	view := postGrillStop(t, ts, sess.ID, http.StatusOK)
	if view.AutoAccept {
		t.Errorf("view = %+v, want auto-accept off", view)
	}
	want := []string{"Auto-accept recommendations turned off", "You stopped the agent mid-turn."}
	if got := grillNoticeTexts(t, stores, sid); !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}
}

func TestGrillAbandonKillsInFlightTurn(t *testing.T) {
	ts, _, repo, srv := grillHookServer(t)
	killed := 0
	srv.stopGrillTurn = func(int64) { killed++ }

	sess := createGrill(t, ts, repo, "COD-1")
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/abandon", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("abandon status = %d, want 200", res.StatusCode)
	}
	if killed != 1 {
		t.Errorf("runner signalled %d times, want once", killed)
	}
}

// The stop parks the session before the child dies, so the turn the kill ends — and a
// turn that ended naturally in the same moment — must not re-settle it, however the
// output it left behind reads. Only a session parked for another reason still walks up
// to stalled.
func TestGrillReconcileKeepsStopReason(t *testing.T) {
	tests := []struct {
		name       string
		parkReason string
		out        grillOutput
		runErr     error
		resultErr  bool
		wantState  string
		wantHint   string
	}{
		{
			name:      "killed child, routine rate-limit line and all",
			out:       grillOutput{stdout: []byte(grillRateLimitEvent)},
			runErr:    errors.New("signal: killed"),
			wantState: hubstore.GrillParked,
			wantHint:  grillStoppedReason,
		},
		{
			name:      "turn ended naturally as the stop landed",
			out:       grillOutput{stdout: []byte(`{"type":"result","session_id":"sid-one","is_error":false}`)},
			wantState: hubstore.GrillParked,
			wantHint:  grillStoppedReason,
		},
		{
			name:      "errored result raced the stop",
			resultErr: true,
			wantState: hubstore.GrillParked,
			wantHint:  grillStoppedReason,
		},
		{
			name:       "auth wall still stalls a crashed session",
			parkReason: grillCrashReason,
			out:        grillOutput{stderr: "API Error: 403 Request not allowed. Please run /login"},
			wantState:  hubstore.GrillStalled,
			wantHint:   "re-authentication",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, store, repo, _ := newGrillRunnerTest(t, grillStubScript)
			sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1"})
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			reason := tt.parkReason
			if reason == "" {
				reason = grillStoppedReason
			}
			if _, err := store.Transition(sess.ID, hubstore.GrillParked, reason); err != nil {
				t.Fatalf("park session: %v", err)
			}

			r.reconcile(sess.ID, claudeGrillAdapter{r: r}, tt.out, tt.runErr, tt.resultErr)

			got, _, _ := store.Session(sess.ID)
			if got.State != tt.wantState || !strings.Contains(got.ParkedReason, tt.wantHint) {
				t.Errorf("session = %s (%q), want %s containing %q",
					got.State, got.ParkedReason, tt.wantState, tt.wantHint)
			}
		})
	}
}

// The whole path with a real child: the stop kills it and everything it spawned, the
// park survives the turn's own reconcile, and the chain the killed turn minted keeps
// the session resumable.
func TestGrillStopKillsChild(t *testing.T) {
	r, store, repo, stubDir := newGrillRunnerTest(t, grillStubSleep)
	r.srv.startGrill = r.launch
	r.srv.grillTurnActive = r.active
	r.srv.stopGrillTurn = r.stop
	ts := httptest.NewServer(r.srv.Handler())
	t.Cleanup(ts.Close)

	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	r.launch(context.Background(), sess)
	waitForGrillStub(t, filepath.Join(stubDir, "started"))

	sid := strconv.FormatInt(sess.ID, 10)
	if view := postGrillStop(t, ts, sid, http.StatusOK); view.ParkedReason != grillStoppedReason {
		t.Fatalf("stopped view = %+v", view)
	}
	waitForGrillIdle(t, r, sess.ID)

	got, _, _ := store.Session(sess.ID)
	if got.State != hubstore.GrillParked || got.ParkedReason != grillStoppedReason {
		t.Errorf("session after the turn ended = %s (%q), want the stop reason", got.State, got.ParkedReason)
	}
	if got.SessionChain != "sid-stop" {
		t.Errorf("chain = %q, want the killed turn's chain", got.SessionChain)
	}
}

// The message that picks a stopped session back up resumes the same chain and reaches
// the agent as a redirect, not as an answer to the question it was cut off in.
func TestGrillStopSteersResumeTurn(t *testing.T) {
	r, store, repo, stubDir := newGrillRunnerTest(t, grillStubScript)
	r.srv.startGrill = r.launch
	r.srv.grillTurnActive = r.active
	r.srv.stopGrillTurn = r.stop
	ts := httptest.NewServer(r.srv.Handler())
	t.Cleanup(ts.Close)

	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	r.runTurn(context.Background(), sess)
	running, err := store.Transition(sess.ID, hubstore.GrillRunning, "")
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}

	sid := strconv.FormatInt(running.ID, 10)
	postGrillStop(t, ts, sid, http.StatusOK)
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sid+"/answer", GrillAnswerRequest{Text: "drop the schema thread"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d, want 200", res.StatusCode)
	}

	waitForGrillChain(t, store, sess.ID, "sid-two")

	resumeArgs := readNullArgs(t, filepath.Join(stubDir, "resume.args"))
	if !contains(resumeArgs, "--resume") || !contains(resumeArgs, "sid-one") {
		t.Errorf("steering turn must resume the same chain: %v", resumeArgs)
	}
	prompt := lastArg(resumeArgs)
	if !strings.Contains(prompt, "stopped your previous turn") || !strings.Contains(prompt, "drop the schema thread") {
		t.Errorf("resume prompt = %q, want the steering frame around the message", prompt)
	}
}

func waitForGrillStub(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stub never started: %s", path)
}

func waitForGrillIdle(t *testing.T, r *grillRunner, sid int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !r.active(sid) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("turn %d never ended", sid)
}
