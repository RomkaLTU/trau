package tui

import (
	"context"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

// PlanQuestion is one structured question a planning round asked, projected onto
// the TUI so the plan package's types never cross the Actions seam. Kind is one
// of "single", "multi", or "text".
type PlanQuestion struct {
	ID      string
	Header  string
	Text    string
	Kind    string
	Options []PlanOption
	Default string
}

// PlanOption is one selectable answer to a PlanQuestion.
type PlanOption struct {
	Label       string
	Description string
}

// PlanAnswer is the user's answer to one PlanQuestion: the chosen values (one for
// a single-select or free-text answer, several for a multi-select) and whether it
// took the question's stated default via skip rather than an explicit choice.
type PlanAnswer struct {
	ID       string
	Question string
	Values   []string
	Skipped  bool
}

// Sentinels for the escape options every option list carries: an "Other"
// free-text entry and a skip that records the question's default. The null byte
// keeps them from colliding with a real option label.
const (
	planOtherSentinel = "\x00other"
	planSkipSentinel  = "\x00skip"
)

// planForm renders a round of PlanQuestions as one huh form: a single-select,
// multi-select, or free-text field per question, each option list carrying an
// "Other" escape and a skip-to-default, following the onboarding wizard's
// embedded-form prior art.
type planForm struct {
	form     *huh.Form
	bindings []*qBinding
	textKeys map[string]bool
	firstKey string
}

// qBinding holds one question's huh-bound values across the Elm update loop:
// single and multi carry the picked option value(s); other carries the free text
// for an "Other" choice or for a free-text question.
type qBinding struct {
	q      PlanQuestion
	single string
	multi  []string
	other  string
}

// buildPlanGroups is the single source of the question fields, shared by the
// embedded interactive form and the accessible runner so both render the same
// questions. It returns the bindings alongside the groups, plus the keys of the
// free-text fields and the first field's key for navigation.
func buildPlanGroups(questions []PlanQuestion) (*planForm, []*huh.Group) {
	pf := &planForm{
		bindings: make([]*qBinding, len(questions)),
		textKeys: map[string]bool{},
	}
	var groups []*huh.Group
	for i := range questions {
		b := &qBinding{q: questions[i]}
		pf.bindings[i] = b
		if i == 0 {
			pf.firstKey = b.q.ID
		}
		groups = append(groups, pf.questionGroups(b)...)
	}
	return pf, groups
}

// newPlanForm builds the embedded interactive form for the Plan screen.
func newPlanForm(questions []PlanQuestion, width int) *planForm {
	pf, groups := buildPlanGroups(questions)
	form := huh.NewForm(groups...).
		WithTheme(huhTheme(theme)).
		WithShowHelp(false).
		WithWidth(width)
	form.SubmitCmd = func() tea.Msg { return planFormSubmitMsg{} }
	form.CancelCmd = func() tea.Msg { return planFormCancelMsg{} }
	pf.form = form
	return pf
}

// runAccessiblePlanForm collects answers to a round of questions through huh's
// accessible (screen-reader) prompts — the same fields the interactive form
// builds, matching the onboarding accessible flow. It reads and writes through
// the terminal the TUI released for it. Conditional "Other" inputs are always
// presented in this mode (huh's accessible runner does not honour hidden groups)
// and are ignored unless the matching option was chosen.
func runAccessiblePlanForm(ctx context.Context, questions []PlanQuestion, in io.Reader, out io.Writer) ([]PlanAnswer, error) {
	pf, groups := buildPlanGroups(questions)
	form := huh.NewForm(groups...).
		WithAccessible(true).
		WithTheme(huhTheme(theme)).
		WithInput(in).
		WithOutput(out)
	if err := form.RunWithContext(ctx); err != nil {
		return nil, err
	}
	return pf.answers(), nil
}

// accessiblePlanExec runs a round of plan questions through the accessible runner
// while the TUI has released the terminal via tea.Exec, capturing the answers for
// the exec callback.
type accessiblePlanExec struct {
	ctx       context.Context
	questions []PlanQuestion
	in        io.Reader
	out       io.Writer
	answers   []PlanAnswer
}

func (a *accessiblePlanExec) SetStdin(r io.Reader)  { a.in = r }
func (a *accessiblePlanExec) SetStdout(w io.Writer) { a.out = w }
func (a *accessiblePlanExec) SetStderr(io.Writer)   {}

func (a *accessiblePlanExec) Run() error {
	answers, err := runAccessiblePlanForm(a.ctx, a.questions, a.in, a.out)
	a.answers = answers
	return err
}

func (pf *planForm) questionGroups(b *qBinding) []*huh.Group {
	switch b.q.Kind {
	case "multi":
		return pf.multiGroups(b)
	case "text":
		return pf.textGroups(b)
	default:
		return pf.singleGroups(b)
	}
}

func (pf *planForm) singleGroups(b *qBinding) []*huh.Group {
	b.single = defaultSingle(b.q)
	sel := huh.NewGroup(
		huh.NewSelect[string]().
			Key(b.q.ID).
			Title(b.q.Text).
			Description(b.q.Header).
			Options(singleOptions(b.q)...).
			Value(&b.single),
	)
	return append([]*huh.Group{sel}, pf.otherGroup(b, func() bool { return b.single != planOtherSentinel }))
}

func (pf *planForm) multiGroups(b *qBinding) []*huh.Group {
	sel := huh.NewGroup(
		huh.NewMultiSelect[string]().
			Key(b.q.ID).
			Title(b.q.Text).
			Description(multiDescription(b.q)).
			Options(multiOptions(b.q)...).
			Value(&b.multi),
	)
	return append([]*huh.Group{sel}, pf.otherGroup(b, func() bool { return !contains(b.multi, planOtherSentinel) }))
}

func (pf *planForm) textGroups(b *qBinding) []*huh.Group {
	pf.textKeys[b.q.ID] = true
	input := huh.NewText().
		Key(b.q.ID).
		Title(b.q.Text).
		Description(b.q.Header).
		Placeholder(placeholderDefault(b.q)).
		Value(&b.other)
	return []*huh.Group{huh.NewGroup(input)}
}

// otherGroup is the conditional free-text follow-up for an "Other" choice, hidden
// until hide reports false.
func (pf *planForm) otherGroup(b *qBinding, hide func() bool) *huh.Group {
	key := b.q.ID + "_other"
	pf.textKeys[key] = true
	return huh.NewGroup(
		huh.NewInput().
			Key(key).
			Title("Other — please specify").
			Value(&b.other),
	).WithHideFunc(hide)
}

func singleOptions(q PlanQuestion) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(q.Options)+2)
	for _, o := range q.Options {
		opts = append(opts, huh.NewOption(o.Label, o.Label))
	}
	opts = append(opts, huh.NewOption("✎ Other…", planOtherSentinel))
	opts = append(opts, huh.NewOption(skipLabel(q), planSkipSentinel))
	return opts
}

func multiOptions(q PlanQuestion) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(q.Options)+1)
	for _, o := range q.Options {
		opts = append(opts, huh.NewOption(o.Label, o.Label))
	}
	return append(opts, huh.NewOption("✎ Other…", planOtherSentinel))
}

func skipLabel(q PlanQuestion) string {
	if d := strings.TrimSpace(q.Default); d != "" {
		return "⤼ Skip — use default: " + d
	}
	return "⤼ Skip"
}

func multiDescription(q PlanQuestion) string {
	if d := strings.TrimSpace(q.Default); d != "" {
		return joinDot(q.Header, "select none to use default: "+d)
	}
	return q.Header
}

func placeholderDefault(q PlanQuestion) string {
	if d := strings.TrimSpace(q.Default); d != "" {
		return "Leave blank to use default: " + d
	}
	return ""
}

// defaultSingle pre-selects the option matching the stated default, else the
// first real option, so a bound value always maps to a listed choice.
func defaultSingle(q PlanQuestion) string {
	d := strings.TrimSpace(q.Default)
	for _, o := range q.Options {
		if o.Label == d {
			return o.Label
		}
	}
	if len(q.Options) > 0 {
		return q.Options[0].Label
	}
	return planSkipSentinel
}

// answers resolves every binding to its PlanAnswer.
func (pf *planForm) answers() []PlanAnswer {
	out := make([]PlanAnswer, 0, len(pf.bindings))
	for _, b := range pf.bindings {
		out = append(out, b.resolve())
	}
	return out
}

func (b *qBinding) resolve() PlanAnswer {
	a := PlanAnswer{ID: b.q.ID, Question: b.q.Text}
	switch b.q.Kind {
	case "multi":
		vals := b.multiValues()
		if len(vals) == 0 {
			a.Values, a.Skipped = defaultValues(b.q), true
		} else {
			a.Values = vals
		}
	case "text":
		if t := strings.TrimSpace(b.other); t != "" {
			a.Values = []string{t}
		} else {
			a.Values, a.Skipped = defaultValues(b.q), true
		}
	default:
		switch b.single {
		case planOtherSentinel:
			if t := strings.TrimSpace(b.other); t != "" {
				a.Values = []string{t}
			} else {
				a.Values, a.Skipped = defaultValues(b.q), true
			}
		case planSkipSentinel, "":
			a.Values, a.Skipped = defaultValues(b.q), true
		default:
			a.Values = []string{b.single}
		}
	}
	return a
}

// multiValues expands the multi-select, substituting the free text for the
// "Other" sentinel and dropping it when no text was entered.
func (b *qBinding) multiValues() []string {
	var vals []string
	for _, v := range b.multi {
		if v == planOtherSentinel {
			if t := strings.TrimSpace(b.other); t != "" {
				vals = append(vals, t)
			}
			continue
		}
		vals = append(vals, v)
	}
	return vals
}

func defaultValues(q PlanQuestion) []string {
	if d := strings.TrimSpace(q.Default); d != "" {
		return []string{d}
	}
	return nil
}

// editing reports whether a free-text field owns keystrokes, so back keys type
// into the field instead of navigating and the global overlays stay closed.
func (pf *planForm) editing() bool {
	f := pf.form.GetFocusedField()
	return f != nil && pf.textKeys[f.GetKey()]
}

func (pf *planForm) onFirstField() bool {
	f := pf.form.GetFocusedField()
	return f != nil && f.GetKey() == pf.firstKey
}

func contains(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}

func joinDot(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "  ·  " + b
	}
}
