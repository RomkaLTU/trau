package webserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
)

func TestCodexGrillArgs(t *testing.T) {
	mcpArgs := []string{"-c", `mcp_servers.trau-grill.url="http://127.0.0.1:1/api/v1/grill/7/mcp"`}
	first := codexGrillArgs([]string{"--foo"}, "work", "gpt-5.6-sol", "high", hubstore.GrillModeInterview, mcpArgs, "", "hello prompt")
	if first[0] != "exec" {
		t.Fatalf("first arg = %q, want exec", first[0])
	}
	if contains(first, "resume") {
		t.Errorf("first turn args should not resume: %v", first)
	}
	for _, want := range []string{"--foo", "--json", "--profile", "work", "--model", "gpt-5.6-sol", "model_reasoning_effort=high", `mcp_servers.trau-grill.url="http://127.0.0.1:1/api/v1/grill/7/mcp"`} {
		if !contains(first, want) {
			t.Errorf("first turn args missing %q: %v", want, first)
		}
	}
	if got := lastArg(first); got != "hello prompt" {
		t.Errorf("prompt = %q, want it last", got)
	}

	resume := codexGrillArgs(nil, "", "", "", hubstore.GrillModeInterview, nil, "codex-sid-1", "the answer")
	if !contains(resume, "resume") || !contains(resume, "codex-sid-1") {
		t.Errorf("resume args missing native resume id: %v", resume)
	}
	if contains(resume, "--model") {
		t.Errorf("empty model should add no --model flag: %v", resume)
	}
	if got := lastArg(resume); got != "the answer" {
		t.Errorf("resume prompt = %q, want the answer", got)
	}
}

func TestCodexGrillArgsPerMode(t *testing.T) {
	mcpURL := `mcp_servers.trau-grill.url="http://127.0.0.1:1/api/v1/grill/7/mcp"`
	interview := []string{
		"exec", "--foo", "--json",
		"--profile", "work",
		"--model", "gpt-5.6-sol",
		"-c", "model_reasoning_effort=high",
		"-c", mcpURL,
		"hello prompt",
	}
	research := []string{
		"exec", "--foo", "--json",
		"--profile", "work",
		"--model", "gpt-5.6-sol",
		"-c", "model_reasoning_effort=high",
		"-c", "tools.web_search=true",
		"-c", mcpURL,
		"hello prompt",
	}
	tests := []struct {
		name string
		mode string
		want []string
	}{
		{name: "legacy row", want: interview},
		{name: "interview", mode: hubstore.GrillModeInterview, want: interview},
		{name: "research", mode: hubstore.GrillModeResearch, want: research},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codexGrillArgs([]string{"--foo"}, "work", "gpt-5.6-sol", "high", tt.mode, []string{"-c", mcpURL}, "", "hello prompt")
			if !slices.Equal(got, tt.want) {
				t.Errorf("codexGrillArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCodexGrillDeltaText(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "agent message", line: `{"type":"item.completed","item":{"type":"agent_message","text":"push back"}}`, want: "push back"},
		{name: "tool call", line: `{"type":"item.completed","item":{"type":"tool_call","text":"ignored"}}`},
		{name: "turn completed", line: `{"type":"turn.completed","usage":{"input_tokens":1}}`},
		{name: "not json", line: `warning: ignore me`},
		{name: "blank"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexGrillDeltaText([]byte(tt.line)); got != tt.want {
				t.Errorf("codexGrillDeltaText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCodexGrillActivity(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "command started",
			line: `{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"rg  -n  grill\n  internal","status":"in_progress"}}`,
			want: `{"seq":0,"kind":"tool","name":"shell","detail":"rg -n grill internal"}`,
		},
		{
			name: "command completed",
			line: `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"rg -n grill","exit_code":0,"status":"completed"}}`,
			want: `{"seq":0,"kind":"result","ok":true}`,
		},
		{
			name: "command failed",
			line: `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"rg -n grill","exit_code":2,"status":"failed"}}`,
			want: `{"seq":0,"kind":"result","ok":false}`,
		},
		{
			name: "web search started",
			line: `{"type":"item.started","item":{"id":"item_2","type":"web_search","query":"sse frame contracts"}}`,
			want: `{"seq":0,"kind":"tool","name":"web_search","detail":"sse frame contracts"}`,
		},
		{name: "agent message", line: `{"type":"item.completed","item":{"type":"agent_message","text":"push back"}}`},
		{name: "reasoning", line: `{"type":"item.started","item":{"id":"item_3","type":"reasoning"}}`},
		{name: "turn completed", line: `{"type":"turn.completed","usage":{"input_tokens":1}}`},
		{name: "not json", line: `warning: ignore me`},
		{name: "blank"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activityJSON(t, codexGrillActivity([]byte(tt.line))); got != tt.want {
				t.Errorf("codexGrillActivity() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGrillActivityDetail(t *testing.T) {
	if got := grillActivityDetail("  go   test\n  ./...  "); got != "go test ./..." {
		t.Errorf("grillActivityDetail() = %q, want the command on one line", got)
	}
	got := grillActivityDetail(strings.Repeat("x", grillActivityDetailMax+20))
	if len([]rune(got)) != grillActivityDetailMax+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("grillActivityDetail(long) = %q, want it cut at %d runes", got, grillActivityDetailMax)
	}
}

func TestParseCodexGrillStream(t *testing.T) {
	stream := `{"type":"thread.started","thread_id":"codex-sid-1"}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":1}}`
	if id, gotErr := parseCodexGrillStream([]byte(stream)); id != "codex-sid-1" || gotErr {
		t.Errorf("parseCodexGrillStream() = (%q, %v), want (codex-sid-1, false)", id, gotErr)
	}

	multi := `{"type":"thread.started","thread_id":"old"}` + "\n" +
		`{"type":"thread.started","thread_id":"new"}`
	if id, _ := parseCodexGrillStream([]byte(multi)); id != "new" {
		t.Errorf("session id = %q, want new (last thread wins)", id)
	}

	failed := `{"type":"thread.started","thread_id":"codex-sid-1"}` + "\n" +
		`{"type":"turn.failed","error":"nope"}`
	if _, gotErr := parseCodexGrillStream([]byte(failed)); !gotErr {
		t.Error("turn.failed should mark the result as errored")
	}
}

func TestGrillRunnerCodexTurn(t *testing.T) {
	t.Setenv("TRAU_ACTIVE", "1")
	r, store, repo, stubDir := newGrillRunnerTest(t, grillStubScript)
	r.srv.token = "grill-token"

	codexStub := filepath.Join(t.TempDir(), "codex-stub.sh")
	if err := os.WriteFile(codexStub, []byte(codexStubScript), 0o755); err != nil {
		t.Fatalf("write codex stub: %v", err)
	}
	cfg := strings.Join([]string{
		"CODEX_BIN=" + codexStub,
		"CODEX_FLAGS=--dangerously-bypass-approvals-and-sandbox",
		"CODEX_PROFILE=work",
		"CODEX_MODEL=codex-test-model",
		"CODEX_EFFORT=high",
		"",
	}, "\n")
	if err := os.WriteFile(config.ProjectConfigPath(repo.Root), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1", Provider: "codex"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	r.runTurn(context.Background(), sess)

	got, _, _ := store.Session(sess.ID)
	if got.SessionChain != "codex-sid-one" {
		t.Fatalf("chain after first turn = %q, want codex-sid-one", got.SessionChain)
	}
	firstArgs := readNullArgs(t, filepath.Join(stubDir, "codex.first.args"))
	if contains(firstArgs, "resume") {
		t.Errorf("first turn must not resume: %v", firstArgs)
	}
	for _, want := range []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--json", "--profile", "work", "--model", "codex-test-model", "model_reasoning_effort=high"} {
		if !contains(firstArgs, want) {
			t.Errorf("first turn args missing %q: %v", want, firstArgs)
		}
	}
	assertArgContains(t, firstArgs, fmt.Sprintf("/grill/%d/mcp", sess.ID))
	assertArgContains(t, firstArgs, `mcp_servers.trau-grill.bearer_token_env_var="`+codexGrillMCPTokenEnv+`"`)
	if prompt := lastArg(firstArgs); !strings.Contains(prompt, "COD-1") {
		t.Errorf("first prompt should name the issue, got %q", prompt)
	}
	assertCodexStubEnv(t, filepath.Join(stubDir, "codex.first.env"), repo.Root, "grill-token")

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
	if got.SessionChain != "codex-sid-two" {
		t.Fatalf("chain after resume turn = %q, want codex-sid-two", got.SessionChain)
	}
	resumeArgs := readNullArgs(t, filepath.Join(stubDir, "codex.resume.args"))
	if !contains(resumeArgs, "resume") || !contains(resumeArgs, "codex-sid-one") {
		t.Errorf("resume turn must carry native resume codex-sid-one: %v", resumeArgs)
	}
	assertArgContains(t, resumeArgs, fmt.Sprintf("/grill/%d/mcp", sess.ID))
	if prompt := lastArg(resumeArgs); prompt != "make it red" {
		t.Errorf("resume prompt = %q, want the user's answer", prompt)
	}
}

func TestKimiGrillArgs(t *testing.T) {
	first := kimiGrillArgs([]string{"--foo"}, "k3", "", "hello prompt")
	if contains(first, "--session") {
		t.Errorf("first turn args should not resume: %v", first)
	}
	for _, want := range []string{"--foo", "--model", "k3", "--output-format", "stream-json"} {
		if !contains(first, want) {
			t.Errorf("first turn args missing %q: %v", want, first)
		}
	}
	if got := first[len(first)-2]; got != "-p" {
		t.Errorf("prompt flag = %q, want -p as penultimate arg", got)
	}
	if got := lastArg(first); got != "hello prompt" {
		t.Errorf("prompt = %q, want it last", got)
	}

	resume := kimiGrillArgs(nil, "", "kimi-sid-1", "the answer")
	if !contains(resume, "--session") || !contains(resume, "kimi-sid-1") {
		t.Errorf("resume args missing --session kimi-sid-1: %v", resume)
	}
	if contains(resume, "--model") {
		t.Errorf("empty model should add no --model flag: %v", resume)
	}
	if got := lastArg(resume); got != "the answer" {
		t.Errorf("resume prompt = %q, want the answer", got)
	}
}

func TestKimiGrillDeltaText(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "assistant message", line: `{"role":"assistant","content":"push back"}`, want: "push back"},
		{name: "meta resume hint", line: `{"role":"meta","type":"session.resume_hint","session_id":"s1","content":"To resume this session"}`},
		{name: "empty content", line: `{"role":"assistant","content":""}`},
		{name: "not json", line: `warning: ignore me`},
		{name: "blank"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kimiGrillDeltaText([]byte(tt.line)); got != tt.want {
				t.Errorf("kimiGrillDeltaText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKimiGrillActivity(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "tool line",
			line: `{"role":"tool","name":"read_file","content":"package webserver"}`,
			want: `{"seq":0,"kind":"tool","name":"read_file"}`,
		},
		{name: "unnamed tool", line: `{"role":"tool","content":"package webserver"}`},
		{name: "assistant message", line: `{"role":"assistant","content":"push back"}`},
		{name: "meta resume hint", line: `{"role":"meta","type":"session.resume_hint","session_id":"s1"}`},
		{name: "not json", line: `warning: ignore me`},
		{name: "blank"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activityJSON(t, kimiGrillActivity([]byte(tt.line))); got != tt.want {
				t.Errorf("kimiGrillActivity() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestParseKimiGrillStream(t *testing.T) {
	stream := `{"role":"assistant","content":"hi"}` + "\n" +
		`{"role":"meta","type":"session.resume_hint","session_id":"kimi-sid-1"}`
	if id, gotErr := parseKimiGrillStream([]byte(stream)); id != "kimi-sid-1" || gotErr {
		t.Errorf("parseKimiGrillStream() = (%q, %v), want (kimi-sid-1, false)", id, gotErr)
	}

	multi := `{"role":"meta","type":"session.resume_hint","session_id":"old"}` + "\n" +
		`{"role":"meta","type":"session.resume_hint","session_id":"new"}`
	if id, _ := parseKimiGrillStream([]byte(multi)); id != "new" {
		t.Errorf("session id = %q, want new (last hint wins)", id)
	}

	if id, _ := parseKimiGrillStream([]byte(`{"role":"assistant","content":"no id here"}`)); id != "" {
		t.Errorf("session id = %q, want empty when no resume hint", id)
	}
}

func TestGrillRunnerKimiTurn(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	r, store, repo, stubDir := newGrillRunnerTest(t, grillStubScript)

	realKimiHome := filepath.Join(os.Getenv("HOME"), ".kimi-code")
	if err := os.MkdirAll(filepath.Join(realKimiHome, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir kimi home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realKimiHome, "config.toml"), []byte("default_model = \"k3\"\n"), 0o644); err != nil {
		t.Fatalf("write kimi config: %v", err)
	}

	kimiStub := filepath.Join(t.TempDir(), "kimi-stub.sh")
	if err := os.WriteFile(kimiStub, []byte(kimiStubScript), 0o755); err != nil {
		t.Fatalf("write kimi stub: %v", err)
	}
	if err := os.WriteFile(config.ProjectConfigPath(repo.Root), []byte("KIMI_BIN="+kimiStub+"\nKIMI_MODEL=kimi-test-model\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1", Provider: "kimi"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	r.runTurn(context.Background(), sess)

	got, _, _ := store.Session(sess.ID)
	if got.SessionChain != "kimi-sid-one" {
		t.Fatalf("chain after first turn = %q, want kimi-sid-one", got.SessionChain)
	}

	firstArgs := readNullArgs(t, filepath.Join(stubDir, "kimi.first.args"))
	if contains(firstArgs, "--session") {
		t.Errorf("first turn must not resume: %v", firstArgs)
	}
	for _, want := range []string{"--model", "kimi-test-model", "--output-format", "stream-json"} {
		if !contains(firstArgs, want) {
			t.Errorf("first turn args missing %q: %v", want, firstArgs)
		}
	}
	if prompt := lastArg(firstArgs); !strings.Contains(prompt, "COD-1") {
		t.Errorf("first prompt should name the issue, got %q", prompt)
	}

	overlay := kimiGrillHomeEnv(t, filepath.Join(stubDir, "kimi.first.env"))
	if overlay == "" {
		t.Fatal("child saw no KIMI_CODE_HOME")
	}
	mcp, err := os.ReadFile(filepath.Join(overlay, "mcp.json"))
	if err != nil {
		t.Fatalf("read overlay mcp.json: %v", err)
	}
	if !strings.Contains(string(mcp), "trau-grill") || !strings.Contains(string(mcp), fmt.Sprintf("/grill/%d/mcp", sess.ID)) {
		t.Errorf("overlay mcp.json missing the scoped grill server: %s", mcp)
	}
	if link, err := os.Readlink(filepath.Join(overlay, "config.toml")); err != nil || link != filepath.Join(realKimiHome, "config.toml") {
		t.Errorf("overlay config.toml = %q (err %v), want a symlink to the real kimi home", link, err)
	}

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
	if got.SessionChain != "kimi-sid-two" {
		t.Fatalf("chain after resume turn = %q, want kimi-sid-two", got.SessionChain)
	}
	resumeArgs := readNullArgs(t, filepath.Join(stubDir, "kimi.resume.args"))
	if !contains(resumeArgs, "--session") || !contains(resumeArgs, "kimi-sid-one") {
		t.Errorf("resume turn must carry --session kimi-sid-one: %v", resumeArgs)
	}
	if prompt := lastArg(resumeArgs); prompt != "make it red" {
		t.Errorf("resume prompt = %q, want the user's answer", prompt)
	}
}

func kimiGrillHomeEnv(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "KIMI_CODE_HOME="); ok {
			return v
		}
	}
	return ""
}

func assertArgContains(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if strings.Contains(a, want) {
			return
		}
	}
	t.Fatalf("args = %v, want an arg containing %q", args, want)
}

func assertCodexStubEnv(t *testing.T, path, root, token string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env %s: %v", path, err)
	}
	env := string(data)
	if !strings.Contains(env, "TRAU_ACTIVE=\n") {
		t.Errorf("child saw a non-empty TRAU_ACTIVE: %q", env)
	}
	if !strings.Contains(env, codexGrillMCPTokenEnv+"="+token+"\n") {
		t.Errorf("child did not see the MCP bearer token env: %q", env)
	}
	for _, line := range strings.Split(env, "\n") {
		if pwd, ok := strings.CutPrefix(line, "PWD="); ok {
			if filepath.Base(pwd) != filepath.Base(root) {
				t.Errorf("child cwd = %q, want the repo root %q", pwd, root)
			}
		}
	}
}

const codexStubScript = `#!/bin/sh
if [ "$1" = "exec" ] && [ "$2" = "--help" ]; then
  printf 'Usage: codex exec [OPTIONS] [PROMPT]\nCommands:\n  resume\nOptions:\n  --json\n  -c, --config <key=value>\n'
  exit 0
fi
if [ "$1" = "mcp" ] && [ "$2" = "add" ] && [ "$3" = "--help" ]; then
  printf 'Options:\n  --url <URL>\n'
  exit 0
fi
which=first
sid=codex-sid-one
for a in "$@"; do
  if [ "$a" = "resume" ]; then which=resume; sid=codex-sid-two; fi
done
: > "$GRILL_STUB_DIR/codex.$which.args"
for a in "$@"; do printf '%s\000' "$a" >> "$GRILL_STUB_DIR/codex.$which.args"; done
{
  printf 'TRAU_ACTIVE=%s\n' "$TRAU_ACTIVE"
  printf 'TRAU_GRILL_MCP_TOKEN=%s\n' "$TRAU_GRILL_MCP_TOKEN"
  printf 'PWD=%s\n' "$(pwd)"
} > "$GRILL_STUB_DIR/codex.$which.env"
mkdir -p "$HOME/.codex/sessions/2026/01/02"
: > "$HOME/.codex/sessions/2026/01/02/rollout-2026-01-02T00-00-00-$sid.jsonl"
printf '{"type":"thread.started","thread_id":"%s"}\n' "$sid"
printf '{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Let me push back."}}\n'
printf '{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}\n'
`

const kimiStubScript = `#!/bin/sh
which=first
sid=kimi-sid-one
for a in "$@"; do
  if [ "$a" = "--session" ]; then which=resume; sid=kimi-sid-two; fi
done
: > "$GRILL_STUB_DIR/kimi.$which.args"
for a in "$@"; do printf '%s\000' "$a" >> "$GRILL_STUB_DIR/kimi.$which.args"; done
{
  printf 'KIMI_CODE_HOME=%s\n' "$KIMI_CODE_HOME"
  printf 'TRAU_ACTIVE=%s\n' "$TRAU_ACTIVE"
  printf 'PWD=%s\n' "$(pwd)"
} > "$GRILL_STUB_DIR/kimi.$which.env"
mkdir -p "$KIMI_CODE_HOME/sessions/ws/$sid"
printf '{"role":"assistant","content":"Let me push back."}\n'
printf '{"role":"meta","type":"session.resume_hint","session_id":"%s","command":"kimi -r %s"}\n' "$sid" "$sid"
`
