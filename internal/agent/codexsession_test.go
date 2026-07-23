package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rolloutFixture is a trimmed capture of a real interactive codex rollout: the
// session_meta header, the turn_context naming the model, and the token_count
// events whose total_token_usage accumulates across the session.
func rolloutFixture(cwd string) string {
	return strings.Join([]string{
		`{"timestamp":"2026-07-23T02:13:30.101Z","type":"session_meta","payload":{"session_id":"019f8c1b-0d46-7980-947b-8d24261f5b59","cwd":"` + cwd + `","originator":"codex_cli_rs","cli_version":"0.144.5"}}`,
		`{"timestamp":"2026-07-23T02:13:30.140Z","type":"turn_context","payload":{"cwd":"` + cwd + `","approval_policy":"never","model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2026-07-23T02:13:41.500Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-07-23T02:13:44.592Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10,"reasoning_output_tokens":5,"total_tokens":110},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10,"reasoning_output_tokens":5,"total_tokens":110},"model_context_window":258400}}}`,
		`{"timestamp":"2026-07-23T02:14:02.113Z","type":"response_item","payload":{"type":"function_call","name":"shell"}}`,
		`{"timestamp":"2026-07-23T02:14:11.004Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":350,"cached_input_tokens":200,"output_tokens":25,"reasoning_output_tokens":12,"total_tokens":375},"last_token_usage":{"input_tokens":250,"cached_input_tokens":160,"output_tokens":15,"reasoning_output_tokens":7,"total_tokens":265},"model_context_window":258400}}}`,
		`not json at all`,
		`{"timestamp":"2026-07-23T02:14:40.887Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":900,"cached_input_tokens":700,"output_tokens":40,"reasoning_output_tokens":20,"total_tokens":940},"last_token_usage":{"input_tokens":550,"cached_input_tokens":500,"output_tokens":15,"reasoning_output_tokens":8,"total_tokens":565},"model_context_window":258400}}}`,
		``,
	}, "\n")
}

// TestParseCodexRollout pins the renormalization: codex reports input_tokens
// INCLUDING cached and reports totals cumulatively, so the last event carries the
// whole call and its cached portion belongs in CacheRead, not Input.
func TestParseCodexRollout(t *testing.T) {
	st, ok := parseCodexRollout(strings.NewReader(rolloutFixture("/repo")))
	if !ok {
		t.Fatal("parseCodexRollout ok = false, want usage recovered")
	}
	want := Usage{Input: 200, Output: 40, CacheRead: 700, Reasoning: 20}
	if st.Usage != want {
		t.Errorf("Usage = %+v, want %+v", st.Usage, want)
	}
	if st.Turns != 3 {
		t.Errorf("Turns = %d, want one per token_count event (3)", st.Turns)
	}
	if st.Model != "gpt-5.6-sol" {
		t.Errorf("Model = %q, want gpt-5.6-sol", st.Model)
	}
	if st.Context != 550 {
		t.Errorf("Context = %d, want the 550-token high-water mark", st.Context)
	}

	if _, ok := parseCodexRollout(strings.NewReader(`{"type":"event_msg","payload":{"type":"task_started"}}`)); ok {
		t.Error("a rollout with no token_count event must report usage unrecovered")
	}
}

// TestCodexSessionStatsFromRollout walks the recovery path end to end: the
// workspace's own rollout is located, parsed and reported recovered, while a
// workspace codex never ran in is reported unrecovered rather than as a zero total.
func TestCodexSessionStatsFromRollout(t *testing.T) {
	sessions, repo := t.TempDir(), t.TempDir()
	day := filepath.Join(sessions, "2026", "07", "23")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(day, "rollout-2026-07-23T02-13-30-019f8c1b.jsonl")
	if err := os.WriteFile(rollout, []byte(rolloutFixture(repo)), 0o644); err != nil {
		t.Fatal(err)
	}

	since := time.Now().Add(-time.Minute)
	st, ok := readCodexSessionStats(sessions, repo, since)
	if !ok {
		t.Fatal("readCodexSessionStats ok = false, want this workspace's session")
	}
	if want := (Usage{Input: 200, Output: 40, CacheRead: 700, Reasoning: 20}); st.Usage != want {
		t.Errorf("Usage = %+v, want %+v", st.Usage, want)
	}
	if st.Model != "gpt-5.6-sol" || st.Turns != 3 {
		t.Errorf("model/turns = %q/%d, want gpt-5.6-sol/3", st.Model, st.Turns)
	}

	if _, ok := readCodexSessionStats(sessions, t.TempDir(), since); ok {
		t.Error("a workspace with no session of its own must report usage unrecovered")
	}
}

// TestFindCodexRolloutPicksThisCallsSession guards the identification rule:
// interactive codex never echoes its session id, so the rollout is matched by the
// workspace it recorded plus the call's start time. Another repo's session and
// this repo's previous phase must both be ignored.
func TestFindCodexRolloutPicksThisCallsSession(t *testing.T) {
	sessions := t.TempDir()
	repo, other := t.TempDir(), t.TempDir()
	day := filepath.Join(sessions, "2026", "07", "23")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Now().Add(-time.Hour)
	write := func(name, cwd string, mod time.Time) string {
		path := filepath.Join(day, name)
		if err := os.WriteFile(path, []byte(rolloutFixture(cwd)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
		return path
	}

	write("rollout-2026-07-23T01-00-00-aaa.jsonl", repo, base)                      // this repo, previous phase
	write("rollout-2026-07-23T02-30-00-bbb.jsonl", other, base.Add(40*time.Minute)) // another repo, concurrent
	want := write("rollout-2026-07-23T02-31-00-ccc.jsonl", repo, base.Add(45*time.Minute))

	since := base.Add(30 * time.Minute)
	got, ok := findCodexRollout(sessions, repo, since)
	if !ok {
		t.Fatal("findCodexRollout ok = false, want this call's rollout")
	}
	if got != want {
		t.Errorf("rollout = %s, want %s", filepath.Base(got), filepath.Base(want))
	}

	if _, ok := findCodexRollout(sessions, repo, base.Add(50*time.Minute)); ok {
		t.Error("a call that started after every rollout must find none")
	}
	if _, ok := findCodexRollout(sessions, t.TempDir(), since); ok {
		t.Error("a workspace with no session of its own must find none")
	}
}
