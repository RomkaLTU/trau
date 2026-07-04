package tui

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
)

// TestPlanQuestionsAccessibleBranch checks that with accessible mode requested a
// questions outcome degrades to the accessible runner (via tea.Exec) instead of
// building the interactive full-screen form, matching onboarding parity.
func TestPlanQuestionsAccessibleBranch(t *testing.T) {
	t.Setenv("ACCESSIBLE", "1")
	fake := &planFake{}
	m := newPlanModel(context.Background(), fake, DefaultStyles(), 100, 40)
	m.step = planRunning

	out, _ := fake.StartPlan(context.Background(), "x")
	m, cmd := m.Update(planDoneMsg{out: out})

	if m.pform != nil {
		t.Error("accessible mode must not build the interactive question form")
	}
	if m.step == planQuestions {
		t.Error("accessible mode must not enter the interactive questions step")
	}
	if cmd == nil {
		t.Fatal("accessible mode should return the accessible exec command")
	}
	if m.sessionDir != "/plans/session-1" {
		t.Errorf("sessionDir = %q, want /plans/session-1", m.sessionDir)
	}
}

// TestAccessiblePlanExecCollectsAnswers drives the accessible runner end to end
// through scripted stdin: picking the first option for the single-select and
// leaving the free-text question blank to take its default.
func TestAccessiblePlanExecCollectsAnswers(t *testing.T) {
	questions := []PlanQuestion{
		{ID: "q1", Text: "who is the actor?", Kind: "single", Options: []PlanOption{{Label: "admins"}, {Label: "editors"}}, Default: "admins"},
		{ID: "q2", Text: "name it?", Kind: "text", Default: "Widgets"},
	}
	exec := &accessiblePlanExec{ctx: context.Background(), questions: questions}
	exec.SetStdin(strings.NewReader("1\n\n\n"))
	exec.SetStdout(io.Discard)

	if err := exec.Run(); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(exec.answers) != 2 {
		t.Fatalf("answers = %d, want 2", len(exec.answers))
	}
	if got := exec.answers[0]; got.ID != "q1" || got.Skipped || len(got.Values) != 1 || got.Values[0] != "admins" {
		t.Errorf("q1 answer = %+v, want explicit admins", got)
	}
	if got := exec.answers[1]; got.ID != "q2" || !got.Skipped || len(got.Values) != 1 || got.Values[0] != "Widgets" {
		t.Errorf("q2 answer = %+v, want skipped default Widgets", got)
	}
}

// TestPlanFormResolveAnswers exercises the answer resolution for every question
// kind and escape: an explicit single/multi/text choice, the "Other" free-text
// escape, and skip-to-default (explicit for single, empty selection for multi,
// blank for text).
func TestPlanFormResolveAnswers(t *testing.T) {
	cases := []struct {
		name string
		bind qBinding
		want PlanAnswer
	}{
		{
			name: "single explicit",
			bind: qBinding{q: PlanQuestion{ID: "q", Text: "who?", Kind: "single", Default: "admins"}, single: "editors"},
			want: PlanAnswer{ID: "q", Question: "who?", Values: []string{"editors"}},
		},
		{
			name: "single skip records default",
			bind: qBinding{q: PlanQuestion{ID: "q", Text: "who?", Kind: "single", Default: "admins"}, single: planSkipSentinel},
			want: PlanAnswer{ID: "q", Question: "who?", Values: []string{"admins"}, Skipped: true},
		},
		{
			name: "single other free text",
			bind: qBinding{q: PlanQuestion{ID: "q", Text: "who?", Kind: "single", Default: "admins"}, single: planOtherSentinel, other: "  ops  "},
			want: PlanAnswer{ID: "q", Question: "who?", Values: []string{"ops"}},
		},
		{
			name: "single other blank falls back to default",
			bind: qBinding{q: PlanQuestion{ID: "q", Text: "who?", Kind: "single", Default: "admins"}, single: planOtherSentinel, other: "   "},
			want: PlanAnswer{ID: "q", Question: "who?", Values: []string{"admins"}, Skipped: true},
		},
		{
			name: "multi explicit",
			bind: qBinding{q: PlanQuestion{ID: "q", Text: "which?", Kind: "multi", Default: "a"}, multi: []string{"a", "b"}},
			want: PlanAnswer{ID: "q", Question: "which?", Values: []string{"a", "b"}},
		},
		{
			name: "multi with other",
			bind: qBinding{q: PlanQuestion{ID: "q", Text: "which?", Kind: "multi"}, multi: []string{"a", planOtherSentinel}, other: "custom"},
			want: PlanAnswer{ID: "q", Question: "which?", Values: []string{"a", "custom"}},
		},
		{
			name: "multi empty records default",
			bind: qBinding{q: PlanQuestion{ID: "q", Text: "which?", Kind: "multi", Default: "a"}},
			want: PlanAnswer{ID: "q", Question: "which?", Values: []string{"a"}, Skipped: true},
		},
		{
			name: "text explicit",
			bind: qBinding{q: PlanQuestion{ID: "q", Text: "name?", Kind: "text", Default: "Widgets"}, other: "Gizmos"},
			want: PlanAnswer{ID: "q", Question: "name?", Values: []string{"Gizmos"}},
		},
		{
			name: "text blank records default",
			bind: qBinding{q: PlanQuestion{ID: "q", Text: "name?", Kind: "text", Default: "Widgets"}, other: ""},
			want: PlanAnswer{ID: "q", Question: "name?", Values: []string{"Widgets"}, Skipped: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.bind
			if got := b.resolve(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("resolve() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSingleOptionsCarryEscapes checks every single-select list ends with an
// "Other" escape and a skip-to-default, and a multi-select carries the "Other"
// escape (skip on multi is selecting nothing).
func TestSingleOptionsCarryEscapes(t *testing.T) {
	q := PlanQuestion{Options: []PlanOption{{Label: "a"}, {Label: "b"}}, Default: "a"}

	single := singleOptions(q)
	if len(single) != 4 {
		t.Fatalf("single options = %d, want 4 (2 + Other + Skip)", len(single))
	}
	if single[2].Value != planOtherSentinel || single[3].Value != planSkipSentinel {
		t.Errorf("single escapes = %q,%q; want Other,Skip", single[2].Value, single[3].Value)
	}

	multi := multiOptions(q)
	if len(multi) != 3 || multi[2].Value != planOtherSentinel {
		t.Errorf("multi options = %d with last %q; want 3 ending in Other", len(multi), multi[len(multi)-1].Value)
	}
}

// TestBuildPlanGroups is the shared builder both the interactive and accessible
// forms use. It maps single/multi to two groups (field + conditional Other) and
// text to one, records the free-text keys the editing gate keys off, and pins the
// first field for back navigation.
func TestBuildPlanGroups(t *testing.T) {
	questions := []PlanQuestion{
		{ID: "q1", Text: "who?", Kind: "single", Options: []PlanOption{{Label: "a"}}},
		{ID: "q2", Text: "which?", Kind: "multi", Options: []PlanOption{{Label: "a"}}},
		{ID: "q3", Text: "name?", Kind: "text"},
	}
	pf, groups := buildPlanGroups(questions)

	if len(pf.bindings) != 3 {
		t.Fatalf("bindings = %d, want 3", len(pf.bindings))
	}
	if len(groups) != 5 {
		t.Errorf("groups = %d, want 5 (2 + 2 + 1)", len(groups))
	}
	if pf.firstKey != "q1" {
		t.Errorf("firstKey = %q, want q1", pf.firstKey)
	}
	for _, key := range []string{"q1_other", "q2_other", "q3"} {
		if !pf.textKeys[key] {
			t.Errorf("textKeys missing %q", key)
		}
	}
	if pf.textKeys["q1"] || pf.textKeys["q2"] {
		t.Error("select fields must not be marked as free-text")
	}
}

// TestDefaultSinglePreselectsDefault pre-selects the option matching the stated
// default, else the first option, so the bound value is always a real choice.
func TestDefaultSinglePreselectsDefault(t *testing.T) {
	q := PlanQuestion{Options: []PlanOption{{Label: "a"}, {Label: "b"}}}
	if got := defaultSingle(PlanQuestion{Options: q.Options, Default: "b"}); got != "b" {
		t.Errorf("defaultSingle with default b = %q, want b", got)
	}
	if got := defaultSingle(q); got != "a" {
		t.Errorf("defaultSingle without default = %q, want first option a", got)
	}
}
