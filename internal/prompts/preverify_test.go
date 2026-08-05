package prompts

import (
	"strings"
	"testing"
)

var preVerifyItems = []string{
	"1. Surface every failure visibly.",
	"2. Guard terminal and stale states.",
	"3. Assume two tabs and a double-click.",
	"4. Write the required tests and run them under -race.",
	"5. Match on full identifiers.",
	"6. Drive the new UI with the keyboard and with overlays open.",
}

func TestBuildPromptCarriesPreVerifyChecklist(t *testing.T) {
	got := Render("build", BuildData{ID: goldenID, Branch: goldenBranch})
	if !strings.Contains(got, "Pre-verify checklist") {
		t.Errorf("build prompt is missing the pre-verify checklist heading:\n%s", got)
	}
	for _, item := range preVerifyItems {
		if !strings.Contains(got, item) {
			t.Errorf("build prompt is missing checklist item %q", item)
		}
	}
}

func TestPreVerifyChecklistIsOverridable(t *testing.T) {
	r := Renderer{Overrides: map[string]string{"build": "Implement {{.ID}} on {{.Branch}}."}}
	got := r.Render("build", BuildData{ID: goldenID, Branch: goldenBranch})
	if strings.Contains(got, "Pre-verify checklist") {
		t.Errorf("override did not replace the checklist, so it is not part of the template:\n%s", got)
	}
}
