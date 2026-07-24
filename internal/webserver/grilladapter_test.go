package webserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
)

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

// TestGrillRunnerKimiTurn drives a session pinned to kimi through a first turn and a
// native --session resume: the child runs under a per-session KIMI_CODE_HOME whose
// mcp.json exposes only this session's grill endpoint, the chain updates from the
// stream's resume hint, and the answer resumes by that id.
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

// kimiStubScript is a stand-in kimi CLI: it records its args and KIMI_CODE_HOME,
// seeds a session directory under the (symlinked) home so the resume gate sees it,
// and prints a kimi-shaped stream — an assistant message and a session.resume_hint.
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
