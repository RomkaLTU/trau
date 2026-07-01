package pipeline

import (
	"strings"
	"testing"
)

// These pin the output-token constraints COD-640 added to the cleanup, commit,
// and handoff prompts. Verify is the sole authoritative test gate; if any of
// these prompts starts mandating a test run or a written report again, the
// per-ticket cost regresses (M4C-88: cleanup/commit each emitted ~115K output
// tokens narrating and re-running the suite).
func TestCleanupInstructionStaysLean(t *testing.T) {
	got := cleanupInstruction("COD-640")
	mustNotContain(t, "cleanupInstruction", got, "run only the tests", "run the tests")
	mustContain(t, "cleanupInstruction", got,
		"do NOT emit a JSON or prose report",
		"do NOT list, count, or justify",
		"no changes needed",
	)
}

func TestCommitInstructionStaysLean(t *testing.T) {
	squashed := commitInstruction("COD-640", "", true)
	mustContain(t, "commitInstruction(squash)", squashed,
		"Verify has already passed",
		"do NOT run tests",
		"do NOT emit a status report",
		"skip splitting entirely",
	)

	nonSquash := commitInstruction("COD-640", "", false)
	mustContain(t, "commitInstruction(merge)", nonSquash,
		"Verify has already passed",
		"make ONE commit",
	)
	mustNotContain(t, "commitInstruction(merge)", nonSquash, "skip splitting entirely")
}

func TestHandoffTailSkipsTestRun(t *testing.T) {
	got := handoffTail("COD-640")
	mustContain(t, "handoffTail", got, "Do NOT run the test suite")
}

func mustContain(t *testing.T, name, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("%s: missing %q", name, sub)
		}
	}
}

func mustNotContain(t *testing.T, name, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Errorf("%s: should not contain %q", name, sub)
		}
	}
}
