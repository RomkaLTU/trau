package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create transcript dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// TestParseTranscriptSumsSubagentUsage guards the token-accounting requirement for
// the Explore opt-in: a run that spawns read-only subagents logs their assistant
// turns into the same session transcript with isSidechain=true, and those turns
// must be summed into the phase's usage total, not dropped.
func TestParseTranscriptSumsSubagentUsage(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}}`,
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-opus-5","usage":{"input_tokens":40,"output_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`,
	}, "\n")

	st, ok := parseTranscript(strings.NewReader(transcript))
	if !ok {
		t.Fatal("parseTranscript reported no usage-bearing lines")
	}
	if st.Turns != 2 {
		t.Errorf("Turns = %d, want 2 (main + subagent)", st.Turns)
	}
	if st.Usage.Input != 140 {
		t.Errorf("Input = %d, want 140 (100 main + 40 subagent)", st.Usage.Input)
	}
	if st.Usage.Output != 30 {
		t.Errorf("Output = %d, want 30 (20 main + 10 subagent)", st.Usage.Output)
	}
	if st.Usage.CacheRead != 7 {
		t.Errorf("CacheRead = %d, want 7", st.Usage.CacheRead)
	}
	if st.Usage.CacheCreation != 4 {
		t.Errorf("CacheCreation = %d, want 4", st.Usage.CacheCreation)
	}
	if st.SubagentTurns != 1 {
		t.Errorf("SubagentTurns = %d, want 1", st.SubagentTurns)
	}
	if st.Subagent != (Usage{Input: 40, Output: 10, CacheRead: 2, CacheCreation: 1}) {
		t.Errorf("Subagent = %+v, want the sidechain turn's counts alone", st.Subagent)
	}
	if st.Context != 108 {
		t.Errorf("Context = %d, want 108 — a subagent turn must not raise the orchestrator's high-water mark", st.Context)
	}
}

// TestParseTranscriptCountsSubagentDispatches: every dispatch the orchestrator
// issued is counted, the Explore ones are separated out, and a nested dispatch made
// from inside a subagent belongs to that subagent, not to the phase.
func TestParseTranscriptCountsSubagentDispatches(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"assistant","message":{"usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"tool_use","name":"Agent","input":{"subagent_type":"Explore"}},{"type":"tool_use","name":"Agent","input":{"subagent_type":"general-purpose"}}]}}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"tool_use","name":"Task","input":{"subagent_type":"Explore"}}]}}`,
		`{"type":"assistant","isSidechain":true,"message":{"usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"tool_use","name":"Agent","input":{"subagent_type":"Explore"}}]}}`,
	}, "\n")

	st, ok := parseTranscript(strings.NewReader(transcript))
	if !ok {
		t.Fatal("parseTranscript reported no usage-bearing lines")
	}
	if st.Dispatches != 3 {
		t.Errorf("Dispatches = %d, want 3 (two Agent, one Task; the sidechain's own does not count)", st.Dispatches)
	}
	if st.Explores != 2 {
		t.Errorf("Explores = %d, want 2", st.Explores)
	}
}

// TestParseTranscriptSettlesSkillCallsByResult is the COD-1502 guard: Claude
// Code's Skill tool is exact-match only, so a name the model re-typed with spaces
// comes back an error and the phase runs without that skill. The transcript's
// tool_results decide — a rejected call is a failed attempt, never a load, even
// when its input is a near-miss the live snapper would happily settle, and a
// retry that succeeds counts as loaded regardless of what preceded it.
func TestParseTranscriptSettlesSkillCallsByResult(t *testing.T) {
	skillCall := func(id, name string) string {
		return `{"type":"assistant","message":{"usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"tool_use","id":"` + id + `","name":"Skill","input":{"skill":"` + name + `"}}]}}`
	}
	result := func(id string, isErr bool) string {
		return `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","is_error":` + map[bool]string{true: "true", false: "false"}[isErr] + `}]}}`
	}

	cases := []struct {
		name       string
		lines      []string
		wantLoaded []string
		wantFailed []string
	}{
		{
			name:       "successful call loads",
			lines:      []string{skillCall("t1", "tdd"), result("t1", false)},
			wantLoaded: []string{"tdd"},
		},
		{
			name:       "rejected call is an attempt, not a load",
			lines:      []string{skillCall("t1", "vercel- react- best- practices"), result("t1", true)},
			wantFailed: []string{"vercel- react- best- practices"},
		},
		{
			name:       "a near-miss the snapper would settle still fails",
			lines:      []string{skillCall("t1", "code- review"), result("t1", true)},
			wantFailed: []string{"code- review"},
		},
		{
			name: "one loads while another fails",
			lines: []string{
				skillCall("t1", "tdd"), result("t1", false),
				skillCall("t2", "vercel- react- best- practices"), result("t2", true),
			},
			wantLoaded: []string{"tdd"},
			wantFailed: []string{"vercel- react- best- practices"},
		},
		{
			name: "a successful retry loads the skill",
			lines: []string{
				skillCall("t1", "vercel- react- best- practices"), result("t1", true),
				skillCall("t2", "vercel-react-best-practices"), result("t2", false),
			},
			wantLoaded: []string{"vercel-react-best-practices"},
			wantFailed: []string{"vercel- react- best- practices"},
		},
		{
			name:       "a call whose result never landed still counts as loaded",
			lines:      []string{skillCall("t1", "tdd")},
			wantLoaded: []string{"tdd"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := parseTranscript(strings.NewReader(strings.Join(tc.lines, "\n")))
			if !ok {
				t.Fatal("parseTranscript reported no usage-bearing lines")
			}
			if !reflect.DeepEqual(st.Skills, tc.wantLoaded) {
				t.Errorf("Skills = %v, want %v", st.Skills, tc.wantLoaded)
			}
			if !reflect.DeepEqual(st.SkillsFailed, tc.wantFailed) {
				t.Errorf("SkillsFailed = %v, want %v", st.SkillsFailed, tc.wantFailed)
			}
		})
	}
}

// TestReadSessionStatsFoldsSubagentTranscripts is the subagent-spend guard: Claude
// Code writes a dispatched subagent's turns to its own child transcript under
// <session-id>/subagents/, so reading the parent alone undercounts the call. The
// combined totals must include the children while their share stays separable.
func TestReadSessionStatsFoldsSubagentTranscripts(t *testing.T) {
	const sessionID = "11111111-2222-3333-4444-555555555555"
	root := t.TempDir()
	projects := filepath.Join(root, "projects", "-Users-dev-repo")

	writeTranscript(t, filepath.Join(projects, sessionID+".jsonl"),
		`{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3},"content":[{"type":"tool_use","name":"Agent","input":{"subagent_type":"Explore"}}]}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":4}}}`,
	)
	writeTranscript(t, filepath.Join(projects, sessionID, "subagents", "agent-a1b2.jsonl"),
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-opus-5","usage":{"input_tokens":40,"output_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`,
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-opus-5","usage":{"input_tokens":6,"output_tokens":2}}}`,
	)

	st, ok := readSessionStats(root, sessionID)
	if !ok {
		t.Fatal("readSessionStats reported no usage-bearing lines")
	}
	if want := (Usage{Input: 156, Output: 36, CacheRead: 7, CacheCreation: 4}); st.Usage != want {
		t.Errorf("Usage = %+v, want %+v (parent plus child session)", st.Usage, want)
	}
	if want := (Usage{Input: 46, Output: 12, CacheRead: 2, CacheCreation: 1}); st.Subagent != want {
		t.Errorf("Subagent = %+v, want %+v", st.Subagent, want)
	}
	if st.Turns != 4 || st.SubagentTurns != 2 {
		t.Errorf("Turns/SubagentTurns = %d/%d, want 4/2", st.Turns, st.SubagentTurns)
	}
	if st.Dispatches != 1 || st.Explores != 1 {
		t.Errorf("Dispatches/Explores = %d/%d, want 1/1", st.Dispatches, st.Explores)
	}
	if st.Context != 108 {
		t.Errorf("Context = %d, want 108 — the orchestrator's own high-water mark", st.Context)
	}
	if st.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want claude-opus-5", st.Model)
	}
}
