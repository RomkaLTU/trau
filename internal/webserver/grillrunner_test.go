package webserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

func TestParseGrillStream(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		wantID  string
		wantErr bool
	}{
		{
			name:   "init and result",
			stream: `{"type":"system","subtype":"init","session_id":"aaa"}` + "\n" + `{"type":"result","subtype":"success","session_id":"aaa","is_error":false}`,
			wantID: "aaa",
		},
		{
			name:   "result only",
			stream: `{"type":"result","subtype":"success","session_id":"bbb"}`,
			wantID: "bbb",
		},
		{
			name:   "last id wins across turns",
			stream: `{"type":"system","subtype":"init","session_id":"old"}` + "\n" + `{"type":"result","session_id":"new"}`,
			wantID: "new",
		},
		{
			name:    "error result",
			stream:  `{"type":"result","subtype":"error","session_id":"ccc","is_error":true}`,
			wantID:  "ccc",
			wantErr: true,
		},
		{
			name:   "malformed lines skipped",
			stream: "not json\n\n" + `{"type":"result","session_id":"ddd"}` + "\ngarbage",
			wantID: "ddd",
		},
		{
			name:   "no session id",
			stream: `{"type":"system","subtype":"init"}`,
			wantID: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, gotErr := parseGrillStream([]byte(tt.stream))
			if id != tt.wantID {
				t.Errorf("session id = %q, want %q", id, tt.wantID)
			}
			if gotErr != tt.wantErr {
				t.Errorf("resultErr = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestGrillDeltaText(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "text delta",
			line: `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}`,
			want: "hi",
		},
		{
			name: "thinking delta",
			line: `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}}`,
		},
		{
			name: "tool input delta",
			line: `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}}`,
		},
		{
			name: "block start",
			line: `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text","text":""}}}`,
		},
		{
			name: "whole assistant message",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		},
		{name: "result", line: `{"type":"result","subtype":"success","is_error":false}`},
		{name: "not json", line: `warning: ignore me`},
		{name: "blank"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := grillDeltaText([]byte(tt.line)); got != tt.want {
				t.Errorf("grillDeltaText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGrillStallReason(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		stderr string
		want   string
	}{
		{name: "clean", stdout: `{"type":"result","is_error":false}`, want: ""},
		{name: "auth wall on stdout", stdout: "API Error: Please run /login", want: "re-authentication"},
		{name: "rate limit on stderr", stderr: "Error: 429 rate_limit exceeded", want: "rate limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grillStallReason([]byte(tt.stdout), tt.stderr)
			if tt.want == "" {
				if got != "" {
					t.Errorf("reason = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("reason = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func TestGrillTurnArgs(t *testing.T) {
	first := grillTurnArgs([]string{"--dangerously-skip-permissions"}, "sonnet", `{"mcp":1}`, "", "hello prompt")
	if contains(first, "--resume") {
		t.Errorf("first turn args should not resume: %v", first)
	}
	for _, want := range []string{"--dangerously-skip-permissions", "--model", "sonnet", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--strict-mcp-config", "--mcp-config", `{"mcp":1}`} {
		if !contains(first, want) {
			t.Errorf("first turn args missing %q: %v", want, first)
		}
	}
	if got := first[len(first)-2]; got != "-p" {
		t.Errorf("prompt flag = %q, want -p as penultimate arg", got)
	}
	if got := first[len(first)-1]; got != "hello prompt" {
		t.Errorf("prompt = %q, want it last", got)
	}

	resume := grillTurnArgs(nil, "", `{}`, "chain-1", "the answer")
	if !contains(resume, "--resume") || !contains(resume, "chain-1") {
		t.Errorf("resume args missing --resume chain-1: %v", resume)
	}
	if contains(resume, "--model") {
		t.Errorf("empty model should add no --model flag: %v", resume)
	}
	if got := resume[len(resume)-1]; got != "the answer" {
		t.Errorf("resume prompt = %q, want the answer", got)
	}
}

func TestGrillChildEnv(t *testing.T) {
	t.Setenv("TRAU_ACTIVE", "1")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("GRILL_KEEP_ME", "yes")
	env := grillChildEnv()
	for _, kv := range env {
		if strings.HasPrefix(kv, "TRAU_ACTIVE=") {
			t.Errorf("TRAU_ACTIVE should be stripped: %q", kv)
		}
		if strings.HasPrefix(kv, "CLAUDECODE=") {
			t.Errorf("CLAUDECODE should be stripped: %q", kv)
		}
	}
	if !containsPrefix(env, "GRILL_KEEP_ME=yes") {
		t.Errorf("unrelated env should pass through: %v", env)
	}
}

// TestGrillRunnerParkResumeRoundTrip drives a real stub claude binary through a
// first turn and a resume turn: the first turn parks (no outcome), the answer fires
// a resume turn that carries --resume <chain> and the answer, and the chain updates
// from each turn's result event.
func TestGrillRunnerParkResumeRoundTrip(t *testing.T) {
	r, store, repo, stubDir := newGrillRunnerTest(t, grillStubScript)

	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	r.runTurn(context.Background(), sess)

	got, _, _ := store.Session(sess.ID)
	if got.SessionChain != "sid-one" {
		t.Fatalf("chain after first turn = %q, want sid-one", got.SessionChain)
	}
	if got.State != hubstore.GrillParked {
		t.Fatalf("state after first turn = %q, want parked", got.State)
	}

	firstArgs := readNullArgs(t, filepath.Join(stubDir, "first.args"))
	if contains(firstArgs, "--resume") {
		t.Errorf("first turn must not resume: %v", firstArgs)
	}
	for _, want := range []string{"--model", "grill-test-model", "--output-format", "stream-json", "--verbose", "--strict-mcp-config"} {
		if !contains(firstArgs, want) {
			t.Errorf("first turn args missing %q", want)
		}
	}
	if prompt := lastArg(firstArgs); !strings.Contains(prompt, "COD-1") {
		t.Errorf("first prompt should name the issue, got %q", prompt)
	}
	assertStubEnv(t, filepath.Join(stubDir, "first.env"), repo.Root)

	// The user answers the parked session; that must fire a resume turn.
	if _, _, err := store.AppendMessage(sess.ID, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindAnswer, Payload: `{"text":"make it red"}`,
	}); err != nil {
		t.Fatalf("append answer: %v", err)
	}
	resumed, err := store.Transition(sess.ID, hubstore.GrillRunning, "")
	if err != nil {
		t.Fatalf("transition to running: %v", err)
	}

	r.runTurn(context.Background(), resumed)

	got, _, _ = store.Session(sess.ID)
	if got.SessionChain != "sid-two" {
		t.Fatalf("chain after resume turn = %q, want sid-two", got.SessionChain)
	}

	resumeArgs := readNullArgs(t, filepath.Join(stubDir, "resume.args"))
	if !contains(resumeArgs, "--resume") || !contains(resumeArgs, "sid-one") {
		t.Errorf("resume turn must carry --resume sid-one: %v", resumeArgs)
	}
	if prompt := lastArg(resumeArgs); prompt != "make it red" {
		t.Errorf("resume prompt = %q, want the user's answer", prompt)
	}
}

// TestGrillAnswerResumeThroughHandler drives a parked session's answer through the
// real HTTP handler and confirms the wired runner fires a --resume turn: the handler
// path, not a direct runTurn call, is what actually spawns the resume child.
func TestGrillAnswerResumeThroughHandler(t *testing.T) {
	r, store, repo, stubDir := newGrillRunnerTest(t, grillStubScript)
	r.srv.startGrill = r.launch
	ts := httptest.NewServer(r.srv.Handler())
	t.Cleanup(ts.Close)

	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	r.runTurn(context.Background(), sess)
	if got, _, _ := store.Session(sess.ID); got.State != hubstore.GrillParked {
		t.Fatalf("state after first turn = %q, want parked", got.State)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+strconv.FormatInt(sess.ID, 10)+"/answer", GrillAnswerRequest{Text: "make it red"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d, want 200", res.StatusCode)
	}

	waitForGrillChain(t, store, sess.ID, "sid-two")

	resumeArgs := readNullArgs(t, filepath.Join(stubDir, "resume.args"))
	if !contains(resumeArgs, "--resume") || !contains(resumeArgs, "sid-one") {
		t.Errorf("resume turn must carry --resume sid-one: %v", resumeArgs)
	}
	if prompt := lastArg(resumeArgs); prompt != "make it red" {
		t.Errorf("resume prompt = %q, want the user's answer", prompt)
	}
}

func TestGrillRunnerStallClassification(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		wantState  string
		wantReason string
	}{
		{name: "auth wall", script: grillStubAuth, wantState: hubstore.GrillStalled, wantReason: "re-authentication"},
		{name: "rate limit", script: grillStubRate, wantState: hubstore.GrillStalled, wantReason: "rate limit"},
		{name: "crash", script: grillStubCrash, wantState: hubstore.GrillParked, wantReason: "unexpectedly"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, store, repo, _ := newGrillRunnerTest(t, tt.script)
			sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-9"})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			r.runTurn(context.Background(), sess)
			got, _, _ := store.Session(sess.ID)
			if got.State != tt.wantState {
				t.Fatalf("state = %q, want %q", got.State, tt.wantState)
			}
			if !strings.Contains(got.ParkedReason, tt.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", got.ParkedReason, tt.wantReason)
			}
		})
	}
}

// TestGrillRunnerStreamsDeltas drives a stub whose reply arrives as partial-message
// events: the turn's text must reach subscribers numbered from one and in order,
// carrying the reply and nothing else, and all of it before the frame that settles
// the turn.
func TestGrillRunnerStreamsDeltas(t *testing.T) {
	r, store, repo, _ := newGrillRunnerTest(t, grillStubStream)
	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-2"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	sub, ch := r.srv.grillEvents.subscribe()
	defer r.srv.grillEvents.unsubscribe(sub)

	r.runTurn(context.Background(), sess)

	deltas := []GrillDeltaView{}
	settled := false
	for draining := true; draining; {
		select {
		case ev := <-ch:
			switch ev.Event {
			case "delta":
				if settled {
					t.Errorf("delta %+v arrived after the turn settled", ev.Payload)
				}
				deltas = append(deltas, ev.Payload.(GrillDeltaView))
			case "state", "message":
				settled = true
			}
		default:
			draining = false
		}
	}

	want := []GrillDeltaView{{Seq: 1, Text: "Let me "}, {Seq: 2, Text: "push back."}}
	if len(deltas) != len(want) {
		t.Fatalf("deltas = %+v, want %+v", deltas, want)
	}
	for i, w := range want {
		if deltas[i] != w {
			t.Errorf("delta %d = %+v, want %+v", i, deltas[i], w)
		}
	}
	if !settled {
		t.Error("turn published no settling frame")
	}
}

// newGrillRunnerTest builds a runner over a real store and a repo whose CLAUDE_BIN
// is a stub claude at script. HOME and CLAUDE_CONFIG_DIR are isolated to temp dirs
// so config resolution and the --resume transcript check never touch the real ones.
func newGrillRunnerTest(t *testing.T, script string) (*grillRunner, *hubstore.Grill, registry.Repo, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))

	stores := testStoresAt(t, home)
	repo := registry.Repo{Name: "acme", Root: filepath.Join(home, "acme"), RunsDir: filepath.Join(home, "acme", ".trau", "runs")}
	if err := os.MkdirAll(repo.Root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}

	stubDir := t.TempDir()
	t.Setenv("GRILL_STUB_DIR", stubDir)
	stub := filepath.Join(t.TempDir(), "claude-stub.sh")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	if err := os.WriteFile(config.ProjectConfigPath(repo.Root), []byte("CLAUDE_BIN="+stub+"\nGRILL_MODEL=grill-test-model\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	srv := New("test", "127.0.0.1", "", nil, false, stores)
	r := &grillRunner{srv: srv, baseCtx: context.Background(), baseURL: "http://127.0.0.1:1", inflight: map[int64]bool{}}
	return r, stores.Grill(), repo, stubDir
}

// waitForGrillChain blocks until the session's chain reaches want, the signal that
// an async resume turn spawned and updated the store, or fails at the deadline.
func waitForGrillChain(t *testing.T, store *hubstore.Grill, sid int64, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, _, _ := store.Session(sid); got.SessionChain == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session %d chain never reached %q", sid, want)
}

func readNullArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read args %s: %v", path, err)
	}
	parts := strings.Split(string(data), "\x00")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func assertStubEnv(t *testing.T, path, root string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env %s: %v", path, err)
	}
	env := string(data)
	if !strings.Contains(env, "TRAU_ACTIVE=\n") {
		t.Errorf("child saw a non-empty TRAU_ACTIVE: %q", env)
	}
	if !strings.Contains(env, "CLAUDECODE=\n") {
		t.Errorf("child saw a non-empty CLAUDECODE: %q", env)
	}
	for _, line := range strings.Split(env, "\n") {
		if pwd, ok := strings.CutPrefix(line, "PWD="); ok {
			if filepath.Base(pwd) != filepath.Base(root) {
				t.Errorf("child cwd = %q, want the repo root %q", pwd, root)
			}
		}
	}
}

func lastArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

func containsPrefix(ss []string, prefix string) bool {
	for _, s := range ss {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

const grillStubScript = `#!/bin/sh
which=first
sid=sid-one
for a in "$@"; do
  if [ "$a" = "--resume" ]; then which=resume; sid=sid-two; fi
done
: > "$GRILL_STUB_DIR/$which.args"
for a in "$@"; do printf '%s\000' "$a" >> "$GRILL_STUB_DIR/$which.args"; done
{
  printf 'TRAU_ACTIVE=%s\n' "$TRAU_ACTIVE"
  printf 'CLAUDECODE=%s\n' "$CLAUDECODE"
  printf 'PWD=%s\n' "$(pwd)"
} > "$GRILL_STUB_DIR/$which.env"
mkdir -p "$CLAUDE_CONFIG_DIR/projects/p"
: > "$CLAUDE_CONFIG_DIR/projects/p/$sid.jsonl"
printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$sid"
printf '{"type":"result","subtype":"success","session_id":"%s","is_error":false,"result":"ok"}\n' "$sid"
`

// grillStubStream writes one reply as partial-message events, salted with the deltas
// that are not the reply — a thinking delta, a tool-input delta — and closed by the
// whole assistant event that repeats the text the deltas already carried.
const grillStubStream = `#!/bin/sh
printf '{"type":"system","subtype":"init","session_id":"sid-stream"}\n'
printf '{"type":"stream_event","session_id":"sid-stream","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"weighing it"}}}\n'
printf '{"type":"stream_event","session_id":"sid-stream","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me "}}}\n'
printf '{"type":"stream_event","session_id":"sid-stream","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"push back."}}}\n'
printf '{"type":"stream_event","session_id":"sid-stream","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}}\n'
printf '{"type":"assistant","session_id":"sid-stream","message":{"content":[{"type":"text","text":"Let me push back."}]}}\n'
printf '{"type":"result","subtype":"success","session_id":"sid-stream","is_error":false}\n'
`

const grillStubAuth = `#!/bin/sh
echo "API Error: 403 Request not allowed. Please run /login"
exit 1
`

const grillStubRate = `#!/bin/sh
echo '{"type":"result","subtype":"error","session_id":"r1","is_error":true,"result":"Error 429: rate_limit exceeded"}'
exit 0
`

const grillStubCrash = `#!/bin/sh
echo "boom" 1>&2
exit 1
`
