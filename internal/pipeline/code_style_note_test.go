package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/prompts"
)

var codeStyleMarkers = []string{
	"Write it the way a senior engineer on this project would",
	"Skip the AI tells",
}

func TestCodeStyleNoteGatesEveryCodingPrompt(t *testing.T) {
	const id = "COD-1195"
	for _, on := range []bool{true, false} {
		p := &Pipeline{CodeStyleNote: on}
		note := p.codeStyleNote()
		phases := []namedPrompt{
			{"build", buildInstruction(p.prompts, id, "feature/x", "", "", note, "")},
			{"repair", repairInstruction(p.prompts, id, verifyPath(id), handoffPath(id), "feature/x", "boom", "", "", "", "", note, "")},
			{"bugfix", bugfixInstruction(p.prompts, id, verifyPath(id), handoffPath(id), "feature/x", "boom", "", "", "", "", note, "")},
			{"push-repair", pushRepairInstruction(p.prompts, id, "hook said no", "", note)},
		}
		for _, ph := range phases {
			for _, marker := range codeStyleMarkers {
				if got := strings.Contains(ph.got, marker); got != on {
					t.Errorf("CODE_STYLE_NOTE=%v: %s prompt contains %q = %v, want %v", on, ph.name, marker, got, on)
				}
			}
		}
	}
}

func TestCodeStyleNoteOffKeepsThePromptOverridable(t *testing.T) {
	if _, ok := prompts.Lookup("code_style"); !ok {
		t.Fatal("code_style left the prompt catalog")
	}
	r := prompts.Renderer{Overrides: map[string]string{"code_style": " Custom style fragment."}}

	off := &Pipeline{prompts: r}
	if note := off.codeStyleNote(); note != "" {
		t.Errorf("codeStyleNote() with the flag off = %q, want empty even with an override stored", note)
	}

	on := &Pipeline{prompts: r, CodeStyleNote: true}
	if note := on.codeStyleNote(); note != " Custom style fragment." {
		t.Errorf("codeStyleNote() = %q, want the stored override", note)
	}
}

func TestBuildPromptCarriesTheCodeStyleNote(t *testing.T) {
	calls := &promptLog{}
	p := newTestPipeline(t, fakeRunner{calls: calls}, &fakeTracker{})

	if err := p.Build(context.Background(), "COD-1195"); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, c := range calls.all() {
		if c.label == "build" {
			mustContain(t, "build prompt", c.prompt, codeStyleMarkers...)
			return
		}
	}
	t.Fatal("no build prompt captured")
}
