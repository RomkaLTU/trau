package pipeline

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/RomkaLTU/trau/internal/prompts"
)

// rubric is the structured acceptance contract for a ticket: the handoff phase
// distills the ticket, PRD, and diff into explicit, checkable criteria so verify
// grades against a first-class artifact rather than re-reading prose. It is a
// companion to the QA brief (handoff.md), written in the same cold handoff
// process and persisted through the hub so it survives a reboot and a resume.
type rubric struct {
	Ticket             string   `json:"ticket"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	NonGoals           []string `json:"non_goals"`
	RequiredTests      []string `json:"required_tests"`
	UIPaths            []string `json:"ui_paths,omitempty"`
	FailConditions     []string `json:"fail_conditions"`
}

// rubricSchema is the JSON skeleton shown to the handoff agent so every rubric
// shares one shape the downstream phases can rely on.
const rubricSchema = `{"ticket":"<ID>","acceptance_criteria":["..."],"non_goals":["..."],"required_tests":["..."],"ui_paths":["..."],"fail_conditions":["..."]}`

func rubricPath(id string) string { return "/tmp/rubric-" + id + ".json" }

// rubricInstruction is appended to the handoff prompt: it asks the same agent
// that wrote the QA brief to also emit the structured rubric, populated from the
// ticket rather than invented, to exactly rubricPath(id).
func rubricInstruction(r prompts.Renderer, id string) string {
	return r.Render("rubric", prompts.RubricData{ID: id, Path: rubricPath(id), Schema: rubricSchema})
}

// readRubric parses the rubric at path. ok is false when the file is absent or
// not valid JSON, so a missing or corrupt rubric reads as "no rubric" rather
// than an error — the consuming phases degrade to the prose brief.
func readRubric(path string) (r rubric, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rubric{}, false
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return rubric{}, false
	}
	return r, true
}

// rubricValid reports whether a parsed rubric carries enough to be useful: at
// least one acceptance criterion. An empty rubric is treated like a missing one.
func rubricValid(r rubric) bool {
	for _, c := range r.AcceptanceCriteria {
		if strings.TrimSpace(c) != "" {
			return true
		}
	}
	return false
}

// persistRubric stores the /tmp rubric through the hub so it survives a reboot
// and a later resume. Best-effort and silent: a missing or unparseable rubric
// simply isn't stored (the loop never blocks on it).
func (p *Pipeline) persistRubric(id string) {
	data, err := os.ReadFile(rubricPath(id))
	if err != nil || len(data) == 0 {
		return
	}
	p.putArtifact(id, artifactRubric, string(data))
}

// restoreRubric copies the durable rubric back to /tmp when /tmp lost it (wiped
// on reboot), so a resume grades against the exact rubric the handoff produced
// instead of regenerating a fresh one. Best-effort: it leaves /tmp untouched when
// a non-empty copy is already there or the hub holds none.
func (p *Pipeline) restoreRubric(id string) {
	if fi, err := os.Stat(rubricPath(id)); err == nil && fi.Size() > 0 {
		return
	}
	content, ok := p.getArtifact(id, artifactRubric)
	if !ok || content == "" {
		return
	}
	_ = os.WriteFile(rubricPath(id), []byte(content), 0o644)
}

// activeRubric returns the /tmp rubric path when a valid rubric is on disk for
// id (restoring the durable copy first), or ("", false) when none is present or
// it is corrupt/empty. Phase prompts use the path to point the agent at the
// rubric and omit the reference entirely when ok is false.
func (p *Pipeline) activeRubric(id string) (path string, ok bool) {
	p.restoreRubric(id)
	r, parsed := readRubric(rubricPath(id))
	if !parsed || !rubricValid(r) {
		return "", false
	}
	return rubricPath(id), true
}

// verifyRubricNote tells the cold verifier to grade against the structured
// rubric. Empty when path is "" (no valid rubric — verify falls back to the
// prose brief), so an absent rubric never injects a dangling file reference.
func verifyRubricNote(path string) string {
	if path == "" {
		return ""
	}
	return " A structured acceptance rubric for this slice is at " + path +
		" (JSON: acceptance_criteria, non_goals, required_tests, ui_paths, fail_conditions). Grade every acceptance_criteria entry against the code on disk, run the required_tests, exercise the ui_paths when present, and fail the verdict if any fail_conditions hold or any non_goals were implemented. Treat the rubric and the QA brief together as the contract."
}

// repairRubricNote points a repair/bugfix pass at the rubric so the fix targets
// the acceptance criteria without drifting into non-goals. Empty when path is "".
func repairRubricNote(path string) string {
	if path == "" {
		return ""
	}
	return " The acceptance rubric for this slice is at " + path +
		"; your fix must satisfy its acceptance_criteria and required_tests and must not implement anything listed under non_goals."
}

// commitRubricNote points the commit phase at the rubric's non-goals so it
// keeps the staged change in scope. Empty when path is "".
func commitRubricNote(path string) string {
	if path == "" {
		return ""
	}
	return " The acceptance rubric for this slice is at " + path +
		"; its non_goals mark what is out of scope — do not stage files that implement a non-goal or are unrelated to its acceptance_criteria."
}
