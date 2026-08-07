package pipeline

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/state"
)

// verifyLevelMarkers holds a phrase unique to each VERIFY_EFFORT level's verify
// fragment; "high" — the full adversarial hunt — maps to no fragment at all.
var verifyLevelMarkers = map[string]string{
	"low":    "Verification effort for this run is LOW",
	"medium": "Verification effort for this run is MEDIUM",
}

func TestVerifyEffortNoteMapsEveryLevel(t *testing.T) {
	for level, marker := range verifyLevelMarkers {
		note := verifyEffortNote(level)
		if !strings.HasPrefix(note, " ") {
			t.Errorf("verifyEffortNote(%q) = %q, want a leading space so it splices into the prompt", level, note)
		}
		if !strings.Contains(note, marker) {
			t.Errorf("verifyEffortNote(%q) = %q, want it to contain %q", level, note, marker)
		}
	}
	for _, level := range []string{"high", "", "off", "bogus"} {
		if note := verifyEffortNote(level); note != "" {
			t.Errorf("verifyEffortNote(%q) = %q, want empty", level, note)
		}
	}
}

// The blocking bar the fragment sets is the point of the key: the rubric contract
// blocks at every level, regressions in the slice's footprint block from medium up,
// and anything past the level's bar is a non-blocking note in the summary.
func TestVerifyEffortStatesTheBlockingBar(t *testing.T) {
	low := verifyEffortNote("low")
	mustContain(t, "low fragment", low,
		"the rubric is the entire job",
		"no regression sweep of the rest of the repo",
		"no code-smell audit",
		"non-blocking note")

	medium := verifyEffortNote("medium")
	mustContain(t, "medium fragment", medium,
		"check the slice's own footprint for regressions",
		"Do not hunt beyond the diff",
		"non-blocking note")
}

func TestVerifyEffortSplicesIntoTheVerifyPrompt(t *testing.T) {
	const id = "COD-1557"
	for level := range verifyLevelMarkers {
		got := verifyTail(prompts.Renderer{}, id, handoffPath(id), verifyPath(id), "", "", "", "", "", "", "", verifyEffortNote(level), "", false)
		for other, marker := range verifyLevelMarkers {
			want := other == level
			if has := strings.Contains(got, marker); has != want {
				t.Errorf("VERIFY_EFFORT=%s: verify prompt contains the %s fragment = %v, want %v", level, other, has, want)
			}
		}
	}
}

// High leaves the prompt byte-identical to a repo that never set the key.
func TestVerifyEffortHighLeavesThePromptUntouched(t *testing.T) {
	const id = "COD-1557"
	base := verifyTail(prompts.Renderer{}, id, handoffPath(id), verifyPath(id), "note", "", " CHECKS.", " RUBRIC.", " LESSONS.", "skills", "", "", "", false)
	for _, level := range []string{"high", "", "bogus"} {
		got := verifyTail(prompts.Renderer{}, id, handoffPath(id), verifyPath(id), "note", "", " CHECKS.", " RUBRIC.", " LESSONS.", "skills", "", verifyEffortNote(level), "", false)
		if got != base {
			t.Errorf("VERIFY_EFFORT=%s: verify prompt diverged from the untouched rendering\n got: %q\nwant: %q", level, got, base)
		}
	}
}

// The two fragments occupy different slots, so TEST_EFFORT=off's which-tests
// sentence and the verify-effort note must both survive together.
func TestVerifyEffortComposesWithTestEffortOff(t *testing.T) {
	const id = "COD-1557"
	for level := range verifyLevelMarkers {
		got := verifyTail(prompts.Renderer{}, id, handoffPath(id), verifyPath(id), "", "", "", "", "", "", verifyTestEffortNote("off"), verifyEffortNote(level), "", false)
		mustContain(t, "verify prompt at "+level+" with TEST_EFFORT=off", got,
			"run the existing tests that cover the changed code",
			verifyLevelMarkers[level])
	}
}

// Panel members render through the same verifyAttempt path, so each one
// receives the level's fragment without separate plumbing.
func TestVerifyEffortReachesEveryPanelMember(t *testing.T) {
	const id = "COD-1557001"
	calls := &promptLog{}
	panel := []Verifier{
		{Name: "claude", Provider: "claude", Runner: fakeRunner{calls: calls}},
		{Name: "codex", Provider: "codex", Runner: fakeRunner{calls: calls}},
	}
	t.Cleanup(func() { removeVerifyArtifacts(t, id, panel) })
	dir := t.TempDir()
	p := &Pipeline{
		State:        state.NewStore(dir),
		RunsDir:      dir,
		Runner:       fakeRunner{calls: calls},
		VerifyPanel:  panel,
		PanelPolicy:  "unanimous",
		VerifyEffort: "low",
	}

	if _, err := p.verifyAttempt(context.Background(), id, "verify", "brief", "", "", "", "", "", "", "", ""); err != nil {
		t.Fatalf("verifyAttempt: %v", err)
	}

	recorded := calls.all()
	if len(recorded) != len(panel) {
		t.Fatalf("recorded %d member prompts, want %d", len(recorded), len(panel))
	}
	for _, c := range recorded {
		mustContain(t, c.label+" prompt", c.prompt, verifyLevelMarkers["low"])
	}
}

// The mechanical gate is rubric-derived and cheap, so it stays on at every
// level — a missing required test blocks the run whatever VERIFY_EFFORT says.
func TestTestGateIgnoresVerifyEffort(t *testing.T) {
	for _, level := range []string{"low", "medium", "high"} {
		t.Run(level, func(t *testing.T) {
			id := "COD-1557" + level
			repo := t.TempDir()
			mustWrite(t, filepath.Join(repo, "present_test.go"), "package here\n\nimport \"testing\"\n\nfunc TestPresent(t *testing.T) {}\n")
			writeGateRubric(t, id, rubric{
				Ticket:        id,
				RequiredTests: []string{"missing_test.go exists", "TestPresent still passes"},
			})
			var buf bytes.Buffer
			p := newTestPipeline(t, &countingRunner{results: []error{nil}}, &fakeTracker{})
			p.RepoRoot = repo
			p.Events = event.New(&buf)
			p.VerifyEffort = level

			v, gated := p.testGate(context.Background(), id)
			if !gated || v.Pass {
				t.Fatalf("VERIFY_EFFORT=%s: gate passed a rubric naming a test that does not exist", level)
			}
			if fails := v.failureLines(); !strings.Contains(fails, "missing_test.go") {
				t.Errorf("failure lines %q do not name the missing target", fails)
			}
		})
	}
}
