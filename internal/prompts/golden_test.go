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

	goldenGrillTitle       = "Ticket title"
	goldenGrillBody        = "Ticket body.\n\n![shot](/tmp/trau-attachments-COD-123/shot.png)"
	goldenGrillAttachments = "\n--- Attachments ---\n/tmp/trau-attachments-COD-123/shot.png — shot.png (image/png, 2.0KB)\nThese are local copies of the ticket's files. Images may show UI states or error screenshots that matter for this task — read them.\n"
	goldenGrillFocus       = "Whether the collapse threshold should be configurable."
	goldenGrillQuestion    = "Which OAuth flow the desktop client should use."
)

func goldenRepairData(codeStyle, handoff, fails, rubricNote, lessonsNote, notesNote, ticketCtx string) RepairData {
	return RepairData{
		ID:            goldenID,
		Verdict:       goldenVerdictPath,
		Handoff:       handoff,
		Branch:        goldenBranch,
		Fails:         fails,
		RubricNote:    rubricNote,
		LessonsNote:   lessonsNote,
		NotesNote:     notesNote,
		CodeStyle:     codeStyle,
		TicketContext: ticketCtx,
	}
}

func grillIssue(title, body, attachments string) GrillIssueData {
	return GrillIssueData{ID: goldenID, Title: title, Body: body, Attachments: attachments}
}

func grillIssueFocused(focus string) GrillIssueData {
	data := grillIssue(goldenGrillTitle, goldenGrillBody, goldenGrillAttachments)
	data.Focus = focus
	return data
}

func grillResearchAnchored(focus string) GrillResearchData {
	return GrillResearchData{GrillIssueData: grillIssueFocused(focus)}
}

func grillIssueAutoAccept() GrillIssueData {
	data := grillIssueFocused(goldenGrillFocus)
	data.AutoAccept = true
	return data
}

type goldenCase struct {
	golden string
	name   string
	data   any
}

func assertGoldens(t *testing.T, cases []goldenCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			if got := Render(tc.name, tc.data); got != string(want) {
				t.Errorf("Render(%q) diverged from the golden\n got: %q\nwant: %q", tc.name, got, want)
			}
		})
	}
}

func TestRenderMatchesPreRefactorGoldens(t *testing.T) {
	rubricFragment := Render("rubric", RubricData{ID: goldenID, Path: goldenRubricPath, Schema: rubricSchema})
	buildNotesFragment := Render("build_notes", BuildNotesData{ID: goldenID, Path: goldenBuildNotesPath})
	codeStyle := Render("code_style", nil)

	buildData := func(codeStyle, skillsNote, note, ticketCtx string) BuildData {
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

	cases := []goldenCase{
		{"preamble", "preamble", nil},
		{"explore_preamble", "explore_preamble", nil},
		{"code_style", "code_style", nil},
		{"skills_none", "skills", SkillsData{}},
		{"skills_required", "skills", SkillsData{Installed: []string{"alpha", "beta", "gamma"}, Required: []string{"beta"}}},
		{"skills_menu", "skills", SkillsData{Installed: []string{"alpha", "beta", "gamma"}, Required: []string{"beta"}, Menu: []string{"alpha", "gamma"}}},
		{"skills_menu_empty", "skills", SkillsData{Installed: []string{"beta"}, Required: []string{"beta"}}},
		{"verify_skills_none", "verify_skills", SkillsData{}},
		{"verify_skills_required", "verify_skills", SkillsData{Installed: []string{"alpha", "beta", "tdd"}, Required: []string{"tdd", "browser-harness"}}},
		{"verify_skills_browser_only", "verify_skills", SkillsData{Required: []string{"browser-harness"}}},
		{"verify_skills_menu", "verify_skills", SkillsData{Installed: []string{"alpha", "beta", "tdd"}, Required: []string{"tdd"}, Menu: []string{"alpha", "beta"}}},
		{"verify_skills_menu_empty", "verify_skills", SkillsData{Installed: []string{"tdd"}, Required: []string{"tdd"}}},
		{"build_full", "build", buildData(codeStyle, "SKILLS-NOTE.", " NOTE-FRAGMENT.", goldenTicketCtx)},
		{"build_empty", "build", buildData(codeStyle, "", "", "")},
		{"build_nostyle", "build", buildData("", "SKILLS-NOTE.", " NOTE-FRAGMENT.", goldenTicketCtx)},
		{"handoff_ctx", "handoff", HandoffData{ID: goldenID, Handoff: goldenHandoffPath, Rubric: rubricFragment, TicketContext: goldenTicketCtx}},
		{"handoff_empty", "handoff", HandoffData{ID: goldenID, Handoff: goldenHandoffPath, Rubric: rubricFragment}},
		{"rubric", "rubric", RubricData{ID: goldenID, Path: goldenRubricPath, Schema: rubricSchema}},
		{"build_notes", "build_notes", BuildNotesData{ID: goldenID, Path: goldenBuildNotesPath}},
		{"verify_brief", "verify", verifyData(goldenHandoffPath, "NOTE.", " CHECKS-FRAGMENT.", " RUBRIC-NOTE.", " LESSONS-NOTE.", goldenTicketCtx)},
		{"verify_derive", "verify", verifyData("", "", "", "", "", "")},
		{"commit_squash", "commit", CommitData{ID: goldenID, RubricNote: " RUBRIC-NOTE.", Squash: true}},
		{"commit_split", "commit", CommitData{ID: goldenID}},
		{"repair_brief", "repair", goldenRepairData(codeStyle, goldenHandoffPath, "fail one\nfail two", " RUBRIC-NOTE.", " LESSONS-NOTE.", " NOTES-NOTE.", goldenTicketCtx)},
		{"repair_nobrief", "repair", goldenRepairData(codeStyle, "", "boom", "", "", "", "")},
		{"repair_nostyle", "repair", goldenRepairData("", goldenHandoffPath, "fail one\nfail two", " RUBRIC-NOTE.", " LESSONS-NOTE.", " NOTES-NOTE.", goldenTicketCtx)},
		{"bugfix_brief", "bugfix", goldenRepairData(codeStyle, goldenHandoffPath, "fail one\nfail two", " RUBRIC-NOTE.", " LESSONS-NOTE.", " NOTES-NOTE.", goldenTicketCtx)},
		{"bugfix_nobrief", "bugfix", goldenRepairData(codeStyle, "", "boom", "", "", "", "")},
		{"bugfix_nostyle", "bugfix", goldenRepairData("", goldenHandoffPath, "fail one\nfail two", " RUBRIC-NOTE.", " LESSONS-NOTE.", " NOTES-NOTE.", goldenTicketCtx)},
		{"push_repair", "push_repair", PushRepairData{ID: goldenID, HookOutput: "hook line one\nhook line two", NotesNote: " NOTES-NOTE.", CodeStyle: codeStyle}},
		{"push_repair_nostyle", "push_repair", PushRepairData{ID: goldenID, HookOutput: "hook line one\nhook line two", NotesNote: " NOTES-NOTE."}},
		{"resolve_conflicts", "resolve_conflicts", ResolveConflictsData{ID: goldenID, Base: "main", Branch: goldenBranch}},
		{"epic_repair", "epic_repair", EpicRepairData{EpicID: "COD-100", PRURL: "https://github.com/o/r/pull/5", Branch: "epic/COD-100"}},
		{"cleanup_notes", "cleanup", CleanupData{ID: goldenID, NotesNote: " NOTES-NOTE."}},
		{"cleanup_empty", "cleanup", CleanupData{ID: goldenID}},
		{"lint_fix", "lint_fix", LintFixData{ID: goldenID}},
		{"lessons_distill", "lessons_distill", LessonsDistillData{ID: goldenID, Result: "fixed", FailureType: "test", Evidence: "evidence line one\nevidence line two", Path: "/tmp/lesson-COD-123.json", Schema: lessonSchema}},
		{"timelog_estimate", "timelog_estimate", TimelogEstimateData{ID: goldenID, Files: 3, Additions: 120, Deletions: 40, Commits: 2, Path: "/tmp/timelog-COD-123.txt"}},
		{"grill_issue", "grill_issue", grillIssue(goldenGrillTitle, goldenGrillBody, goldenGrillAttachments)},
		{"grill_issue_focus", "grill_issue", grillIssueFocused(goldenGrillFocus)},
		{"grill_issue_bare", "grill_issue", grillIssue("", "(no description yet)", "")},
		{"grill_pregrill", "grill_pregrill", grillIssue(goldenGrillTitle, goldenGrillBody, goldenGrillAttachments)},
		{"grill_authoring_seed", "grill_authoring", GrillAuthoringData{Idea: "A dark-mode toggle in the toolbar."}},
		{"grill_authoring_empty", "grill_authoring", GrillAuthoringData{}},
		{"grill_research_issue", "grill_research", grillResearchAnchored(goldenGrillFocus)},
		{"grill_research_bare", "grill_research", GrillResearchData{GrillIssueData: grillIssue("", "(no description yet)", "")}},
		{"grill_research_idea", "grill_research", GrillResearchData{Idea: goldenGrillQuestion}},
		{"grill_research_empty", "grill_research", GrillResearchData{}},
		{"grill_issue_auto_accept", "grill_issue", grillIssueAutoAccept()},
		{"grill_authoring_auto_accept", "grill_authoring", GrillAuthoringData{Idea: "A dark-mode toggle in the toolbar.", AutoAccept: true}},
		{"grill_research_auto_accept", "grill_research", GrillResearchData{GrillIssueData: grillIssueAutoAccept()}},
	}
	assertGoldens(t, cases)
}

// These pin where the TEST_EFFORT fragment splices into each prompt. The
// default high level renders nothing and is pinned by the goldens above, which
// leave TestEffort empty.
func TestRenderTestEffortGoldens(t *testing.T) {
	const fragment = " TEST-EFFORT."
	codeStyle := Render("code_style", nil)

	repair := goldenRepairData(codeStyle, goldenHandoffPath, "fail one\nfail two", " RUBRIC-NOTE.", " LESSONS-NOTE.", " NOTES-NOTE.", goldenTicketCtx)
	repair.TestEffort = fragment

	assertGoldens(t, []goldenCase{
		{"build_test_effort", "build", BuildData{
			ID:            goldenID,
			Branch:        goldenBranch,
			SkillsNote:    "SKILLS-NOTE.",
			Note:          " NOTE-FRAGMENT.",
			TestEffort:    fragment,
			CodeStyle:     codeStyle,
			BuildNotes:    Render("build_notes", BuildNotesData{ID: goldenID, Path: goldenBuildNotesPath}),
			TicketContext: goldenTicketCtx,
		}},
		{"repair_test_effort", "repair", repair},
		{"bugfix_test_effort", "bugfix", repair},
		{"verify_test_effort", "verify", VerifyData{
			ID:             goldenID,
			Handoff:        goldenHandoffPath,
			Verdict:        goldenVerdictPath,
			Note:           "NOTE.",
			ChecksFragment: " CHECKS-FRAGMENT.",
			RubricNote:     " RUBRIC-NOTE.",
			LessonsNote:    " LESSONS-NOTE.",
			TestEffort:     "TEST-EFFORT.",
			TicketContext:  goldenTicketCtx,
		}},
	})
}

func TestCatalog(t *testing.T) {
	cat := Catalog()
	if len(cat) != 24 {
		t.Fatalf("catalog has %d prompts, want 24", len(cat))
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
