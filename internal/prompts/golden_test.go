package prompts

import (
	"os"
	"path/filepath"
	"testing"
)

// The testdata fixtures were dumped from the pre-refactor concatenation
// functions; these tests pin the registry rendering to byte-identical output.

const (
	goldenID        = "COD-123"
	goldenBranch    = "feature/COD-123-slice"
	goldenTicketCtx = "\n\n=== COD-123: Ticket title ===\nTicket body.\n=== end COD-123 ==="

	goldenHandoffPath    = "/tmp/handoff-COD-123.md"
	goldenVerdictPath    = "/tmp/verify-COD-123.json"
	goldenRubricPath     = "/tmp/rubric-COD-123.json"
	goldenBuildNotesPath = "/tmp/buildnotes-COD-123.md"

	rubricSchema = `{"ticket":"<ID>","acceptance_criteria":["..."],"non_goals":["..."],"required_tests":["..."],"ui_paths":["..."],"fail_conditions":["..."]}`
	lessonSchema = `{"lesson":"<one or two sentence durable takeaway>","tags":["<short-keyword>","..."]}`
)

func goldenRepairData(handoff, fails, rubricNote, lessonsNote, notesNote, ticketCtx string) RepairData {
	return RepairData{
		ID:            goldenID,
		Verdict:       goldenVerdictPath,
		Handoff:       handoff,
		Branch:        goldenBranch,
		Fails:         fails,
		RubricNote:    rubricNote,
		LessonsNote:   lessonsNote,
		NotesNote:     notesNote,
		CodeStyle:     Render("code_style", nil),
		TicketContext: ticketCtx,
	}
}

func TestRenderMatchesPreRefactorGoldens(t *testing.T) {
	rubricFragment := Render("rubric", RubricData{ID: goldenID, Path: goldenRubricPath, Schema: rubricSchema})
	buildNotesFragment := Render("build_notes", BuildNotesData{ID: goldenID, Path: goldenBuildNotesPath})
	codeStyle := Render("code_style", nil)

	buildData := func(skillsNote, note, ticketCtx string) BuildData {
		return BuildData{
			ID:            goldenID,
			Branch:        goldenBranch,
			SkillsNote:    skillsNote,
			Note:          note,
			CodeStyle:     codeStyle,
			BuildNotes:    buildNotesFragment,
			TicketContext: ticketCtx,
		}
	}
	verifyData := func(handoff, note, checks, rubricNote, lessonsNote, ticketCtx string) VerifyData {
		return VerifyData{
			ID:             goldenID,
			Handoff:        handoff,
			Verdict:        goldenVerdictPath,
			Note:           note,
			ChecksFragment: checks,
			RubricNote:     rubricNote,
			LessonsNote:    lessonsNote,
			TicketContext:  ticketCtx,
		}
	}

	cases := []struct {
		golden string
		name   string
		data   any
	}{
		{"preamble", "preamble", nil},
		{"explore_preamble", "explore_preamble", nil},
		{"code_style", "code_style", nil},
		{"skills_none", "skills", SkillsData{}},
		{"skills_installed", "skills", SkillsData{Installed: []string{"alpha", "beta"}}},
		{"skills_required", "skills", SkillsData{Installed: []string{"alpha", "beta", "gamma"}, Required: []string{"beta"}}},
		{"build_full", "build", buildData("SKILLS-NOTE.", " NOTE-FRAGMENT.", goldenTicketCtx)},
		{"build_empty", "build", buildData("", "", "")},
		{"handoff_ctx", "handoff", HandoffData{ID: goldenID, Handoff: goldenHandoffPath, Rubric: rubricFragment, TicketContext: goldenTicketCtx}},
		{"handoff_empty", "handoff", HandoffData{ID: goldenID, Handoff: goldenHandoffPath, Rubric: rubricFragment}},
		{"rubric", "rubric", RubricData{ID: goldenID, Path: goldenRubricPath, Schema: rubricSchema}},
		{"build_notes", "build_notes", BuildNotesData{ID: goldenID, Path: goldenBuildNotesPath}},
		{"verify_brief", "verify", verifyData(goldenHandoffPath, "NOTE.", " CHECKS-FRAGMENT.", " RUBRIC-NOTE.", " LESSONS-NOTE.", goldenTicketCtx)},
		{"verify_derive", "verify", verifyData("", "", "", "", "", "")},
		{"commit_squash", "commit", CommitData{ID: goldenID, RubricNote: " RUBRIC-NOTE.", Squash: true}},
		{"commit_split", "commit", CommitData{ID: goldenID}},
		{"repair_brief", "repair", goldenRepairData(goldenHandoffPath, "fail one\nfail two", " RUBRIC-NOTE.", " LESSONS-NOTE.", " NOTES-NOTE.", goldenTicketCtx)},
		{"repair_nobrief", "repair", goldenRepairData("", "boom", "", "", "", "")},
		{"bugfix_brief", "bugfix", goldenRepairData(goldenHandoffPath, "fail one\nfail two", " RUBRIC-NOTE.", " LESSONS-NOTE.", " NOTES-NOTE.", goldenTicketCtx)},
		{"bugfix_nobrief", "bugfix", goldenRepairData("", "boom", "", "", "", "")},
		{"push_repair", "push_repair", PushRepairData{ID: goldenID, HookOutput: "hook line one\nhook line two", NotesNote: " NOTES-NOTE.", CodeStyle: codeStyle}},
		{"resolve_conflicts", "resolve_conflicts", ResolveConflictsData{ID: goldenID, Base: "main", Branch: goldenBranch}},
		{"epic_repair", "epic_repair", EpicRepairData{EpicID: "COD-100", PRURL: "https://github.com/o/r/pull/5", Branch: "epic/COD-100"}},
		{"cleanup_notes", "cleanup", CleanupData{ID: goldenID, NotesNote: " NOTES-NOTE."}},
		{"cleanup_empty", "cleanup", CleanupData{ID: goldenID}},
		{"lint_fix", "lint_fix", LintFixData{ID: goldenID}},
		{"lessons_distill", "lessons_distill", LessonsDistillData{ID: goldenID, Result: "fixed", FailureType: "test", Evidence: "evidence line one\nevidence line two", Path: "/tmp/lesson-COD-123.json", Schema: lessonSchema}},
		{"timelog_estimate", "timelog_estimate", TimelogEstimateData{ID: goldenID, Files: 3, Additions: 120, Deletions: 40, Commits: 2, Path: "/tmp/timelog-COD-123.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			got := Render(tc.name, tc.data)
			if got != string(want) {
				t.Errorf("Render(%q) diverged from the pre-refactor output\n got: %q\nwant: %q", tc.name, got, want)
			}
		})
	}
}

func TestCatalog(t *testing.T) {
	cat := Catalog()
	if len(cat) != 19 {
		t.Fatalf("catalog has %d prompts, want 19", len(cat))
	}
	seen := map[string]bool{}
	for _, p := range cat {
		if seen[p.Name] {
			t.Errorf("duplicate prompt name %q", p.Name)
		}
		seen[p.Name] = true
		if p.Title == "" || p.Description == "" || p.Default == "" {
			t.Errorf("prompt %q has incomplete metadata", p.Name)
		}
	}
}
