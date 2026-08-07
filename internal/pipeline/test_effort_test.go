package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/prompts"
)

// levelMarkers holds a phrase unique to each TEST_EFFORT level's
// build/repair/bugfix fragment; "high" maps to no fragment at all.
var levelMarkers = map[string]string{
	"off":    "Do NOT create or extend any tests for this slice",
	"low":    "Write only critical tests",
	"medium": "Write focused tests",
}

func TestTestEffortNoteMapsEveryLevel(t *testing.T) {
	for level, marker := range levelMarkers {
		note := testEffortNote(level)
		if !strings.HasPrefix(note, " ") {
			t.Errorf("testEffortNote(%q) = %q, want a leading space so it splices into the prompt", level, note)
		}
		if !strings.Contains(note, marker) {
			t.Errorf("testEffortNote(%q) = %q, want it to contain %q", level, note, marker)
		}
	}
	for _, level := range []string{"high", "", "bogus"} {
		if note := testEffortNote(level); note != "" {
			t.Errorf("testEffortNote(%q) = %q, want empty", level, note)
		}
	}
}

func TestTestEffortConstrainsEveryCodingPrompt(t *testing.T) {
	const id = "COD-1211"
	for _, level := range []string{"off", "low", "medium", "high"} {
		p := &Pipeline{TestEffort: level}
		note := testEffortNote(p.TestEffort)
		phases := []namedPrompt{
			{"build", buildInstruction(p.prompts, id, "feature/x", "", "", note, "", "")},
			{"repair", repairInstruction(p.prompts, id, verifyPath(id), handoffPath(id), "feature/x", "boom", "", "", "", "", note, "", "")},
			{"bugfix", bugfixInstruction(p.prompts, id, verifyPath(id), handoffPath(id), "feature/x", "boom", "", "", "", "", note, "", "")},
		}
		for _, ph := range phases {
			for other, marker := range levelMarkers {
				want := other == level
				if got := strings.Contains(ph.got, marker); got != want {
					t.Errorf("TEST_EFFORT=%s: %s prompt contains the %s fragment = %v, want %v", level, ph.name, other, got, want)
				}
			}
		}
	}
}

func TestVerifyTestEffortOnlyRewordsTheOffLevel(t *testing.T) {
	const id = "COD-1211"
	const defaultWording = "Run only the tests relevant to this slice (the new or changed test files for this ticket)"

	for _, level := range []string{"low", "medium", "high", ""} {
		if note := verifyTestEffortNote(level); note != "" {
			t.Errorf("verifyTestEffortNote(%q) = %q, want empty", level, note)
		}
		got := verifyTail(prompts.Renderer{}, id, handoffPath(id), verifyPath(id), "", "", "", "", "", "", verifyTestEffortNote(level), "", "", false)
		mustContain(t, "verify prompt at "+level, got, defaultWording)
	}

	off := verifyTail(prompts.Renderer{}, id, handoffPath(id), verifyPath(id), "", "", "", "", "", "", verifyTestEffortNote("off"), "", "", false)
	mustNotContain(t, "verify prompt at off", off, defaultWording)
	mustContain(t, "verify prompt at off", off,
		"run the existing tests that cover the changed code",
		"do NOT treat the absence of new or changed tests as a failure")
}

// TEST_EFFORT governs how many tests get written, never whether the UI is driven:
// the browser instruction must survive the off-level rewording untouched.
func TestTestEffortLeavesBrowserVerifyAlone(t *testing.T) {
	const id = "COD-1211"
	browser := browserNote("always", "http://localhost:3000")

	for _, level := range []string{"off", "low", "medium", "high"} {
		got := verifyTail(prompts.Renderer{}, id, handoffPath(id), verifyPath(id), browser, "", "", "", "", "", verifyTestEffortNote(level), "", "", false)
		mustContain(t, "verify prompt at "+level, got, browser)
	}
}

func TestBuildPromptCarriesTheTestEffortFragment(t *testing.T) {
	calls := &promptLog{}
	p := newTestPipeline(t, fakeRunner{calls: calls}, &fakeTracker{})
	p.TestEffort = "low"

	if err := p.Build(context.Background(), "COD-1211"); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, c := range calls.all() {
		if c.label == "build" {
			mustContain(t, "build prompt", c.prompt, levelMarkers["low"])
			return
		}
	}
	t.Fatal("no build prompt captured")
}
