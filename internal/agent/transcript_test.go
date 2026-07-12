package agent

import (
	"strings"
	"testing"
)

// TestParseTranscriptSumsSubagentUsage guards the token-accounting requirement for
// the Explore opt-in: a run that spawns read-only subagents logs their assistant
// turns into the same session transcript with isSidechain=true, and those turns
// must be summed into the phase's usage total, not dropped.
func TestParseTranscriptSumsSubagentUsage(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}}`,
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-opus-4-8","usage":{"input_tokens":40,"output_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`,
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
}
