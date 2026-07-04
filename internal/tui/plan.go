package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

// PlanOutcome is a planning round's result surfaced to the Plan screen. Status is
// the payload status; for a PRD, Title and Markdown carry the document; for
// questions, Questions carries them. SessionDir is the durable plan session the
// round ran under, so the next round can be answered against it. Note holds a
// graceful one-line message for a status the screen does not action.
type PlanOutcome struct {
	Status     string
	SessionDir string
	Title      string
	Markdown   string
	Questions  []PlanQuestion
	Note       string
}

type planStep int

const (
	planInput     planStep = iota // idea entry
	planRunning                   // round in flight
	planQuestions                 // answering a round of questions
	planPRD                       // PRD in a scrollable viewport
	planNote                      // graceful message (non-PRD result or error)
)

// planModel is the Plan screen: paste/type a raw idea (or a file path), run one
// planning round, and read the returned PRD in a scrollable viewport.
type planModel struct {
	styles  Styles
	actions Actions
	ctx     context.Context
	width   int
	height  int

	inited     bool
	step       planStep
	idea       textarea.Model
	viewport   viewport.Model
	pform      *planForm
	sessionDir string
	title      string
	note       string
	badIdea    bool
	cancelled  bool
}

type planDoneMsg struct {
	out PlanOutcome
	err error
}

type planFormSubmitMsg struct{}

type planFormCancelMsg struct{}

// planAccessibleDoneMsg carries the answers collected by the accessible runner
// after the TUI resumed from tea.Exec, or the error it failed with.
type planAccessibleDoneMsg struct {
	answers []PlanAnswer
	err     error
}

func newPlanModel(ctx context.Context, actions Actions, styles Styles, w, h int) planModel {
	ta := textarea.New()
	ta.Placeholder = "Paste or type a raw idea — or give a path to a file containing one…"
	ta.Focus()

	m := planModel{
		styles:   styles,
		actions:  actions,
		ctx:      ctx,
		inited:   true,
		step:     planInput,
		idea:     ta,
		viewport: viewport.New(),
	}
	m.relayout(w, h)
	return m
}

func (m *planModel) relayout(w, h int) {
	m.width, m.height = w, h
	if m.width < 20 {
		m.width = 20
	}
	if m.height < 8 {
		m.height = 8
	}
	bodyH := m.height - 4 // header(2) + hint(2)
	if bodyH < 4 {
		bodyH = 4
	}
	innerW := m.width - 2
	if innerW < 10 {
		innerW = 10
	}
	m.viewport.SetWidth(innerW)
	m.viewport.SetHeight(bodyH)

	taH := bodyH - 2 // one instruction line + a spacer above the box
	if taH < 3 {
		taH = 3
	}
	m.idea.SetWidth(innerW)
	m.idea.SetHeight(taH)
}

func (m planModel) Init() tea.Cmd { return textarea.Blink }

func (m planModel) Update(msg tea.Msg) (planModel, tea.Cmd) {
	// The shell fans WindowSizeMsg out to every sub-model, including this one before
	// it has ever been opened; its zero-value textarea/viewport would panic on
	// resize, so a not-yet-created screen ignores everything.
	if !m.inited {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.relayout(msg.Width, msg.Height)
		if m.pform != nil {
			m.pform.form = m.pform.form.WithWidth(m.formWidth())
		}
		return m, nil

	case planDoneMsg:
		if m.step != planRunning {
			return m, nil
		}
		if msg.err != nil {
			m.step, m.note = planNote, "✗ "+msg.err.Error()
			return m, nil
		}
		m.sessionDir = msg.out.SessionDir
		switch msg.out.Status {
		case "prd":
			m.step = planPRD
			m.title = msg.out.Title
			m.viewport.SetContent(m.prdBody(msg.out))
			m.viewport.GotoTop()
			return m, nil
		case "questions":
			if accessibleRequested() {
				return m, m.accessiblePlanCmd(msg.out.Questions)
			}
			m.pform = newPlanForm(msg.out.Questions, m.formWidth())
			m.step = planQuestions
			return m, m.pform.form.Init()
		default:
			m.step, m.note = planNote, planStatusNote(msg.out)
			return m, nil
		}

	case planFormSubmitMsg:
		if m.step != planQuestions {
			return m, nil
		}
		answers := m.pform.answers()
		m.step = planRunning
		return m, m.answerPlanCmd(answers)

	case planAccessibleDoneMsg:
		if m.step != planRunning {
			return m, nil
		}
		if msg.err != nil {
			m.step, m.note = planNote, "✗ "+msg.err.Error()
			return m, nil
		}
		return m, m.answerPlanCmd(msg.answers)

	case planFormCancelMsg:
		if m.step == planQuestions {
			m.step = planInput
			m.idea.Focus()
			return m, textarea.Blink
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	switch m.step {
	case planInput:
		m.idea, cmd = m.idea.Update(msg)
	case planQuestions:
		m, cmd = m.passToForm(msg)
	case planPRD:
		m.viewport, cmd = m.viewport.Update(msg)
	}
	return m, cmd
}

func (m planModel) handleKey(msg tea.KeyPressMsg) (planModel, tea.Cmd) {
	switch m.step {
	case planInput:
		switch msg.String() {
		case "esc":
			m.cancelled = true
			return m, nil
		case "ctrl+d":
			if strings.TrimSpace(m.idea.Value()) == "" {
				m.badIdea = true
				return m, nil
			}
			m.step, m.badIdea = planRunning, false
			return m, m.startPlanCmd()
		}
		m.badIdea = false
		var cmd tea.Cmd
		m.idea, cmd = m.idea.Update(msg)
		return m, cmd

	case planRunning:
		if isBack(msg) {
			m.step = planInput
			m.idea.Focus()
		}
		return m, nil

	case planQuestions:
		if m.isFormBackKey(msg) {
			if m.pform.onFirstField() {
				m.step = planInput
				m.idea.Focus()
				return m, textarea.Blink
			}
			return m.passToForm(planFormBackKey())
		}
		return m.passToForm(msg)

	case planPRD, planNote:
		switch msg.String() {
		case "esc", "q":
			m.cancelled = true
			return m, nil
		case "e":
			m.step = planInput
			m.idea.Focus()
			return m, textarea.Blink
		}
		if m.step == planPRD {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	return m, nil
}

func (m planModel) handleMouseClick(msg tea.MouseClickMsg) (planModel, tea.Cmd) {
	if m.step != planPRD {
		return m, nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m planModel) startPlanCmd() tea.Cmd {
	actions, ctx, idea := m.actions, m.ctx, m.idea.Value()
	return func() tea.Msg {
		out, err := actions.StartPlan(ctx, idea)
		return planDoneMsg{out: out, err: err}
	}
}

func (m planModel) answerPlanCmd(answers []PlanAnswer) tea.Cmd {
	actions, ctx, dir := m.actions, m.ctx, m.sessionDir
	return func() tea.Msg {
		out, err := actions.AnswerPlan(ctx, dir, answers)
		return planDoneMsg{out: out, err: err}
	}
}

// accessiblePlanCmd releases the terminal and runs the question round through
// huh's accessible prompts, returning the answers once the TUI resumes.
func (m planModel) accessiblePlanCmd(questions []PlanQuestion) tea.Cmd {
	exec := &accessiblePlanExec{ctx: m.ctx, questions: questions}
	return tea.Exec(exec, func(err error) tea.Msg {
		return planAccessibleDoneMsg{answers: exec.answers, err: err}
	})
}

// passToForm drives the embedded question form; pform is pointer-shared so its
// state survives the value-copy of planModel through the update loop.
func (m planModel) passToForm(msg tea.Msg) (planModel, tea.Cmd) {
	fm, cmd := m.pform.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.pform.form = f
	}
	return m, cmd
}

// isFormBackKey reports whether the key steps back in the question form: esc
// always, and ← / q only when a text field is not capturing the keystroke.
func (m planModel) isFormBackKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "esc":
		return true
	case "left", "q":
		return !m.pform.editing()
	}
	return false
}

// planFormBackKey is the shift+tab huh reads as "previous field/group".
func planFormBackKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
}

func (m planModel) formWidth() int {
	w := m.width - 4
	if w > 72 {
		w = 72
	}
	if w < 24 {
		w = 24
	}
	return w
}

// prdBody is the PRD viewport content: the title as a heading over the markdown.
func (m planModel) prdBody(out PlanOutcome) string {
	title := strings.TrimSpace(out.Title)
	if title == "" {
		return out.Markdown
	}
	return m.styles.SummaryTitle.Render(title) + "\n\n" + out.Markdown
}

// planStatusNote flags a result the Plan screen does not action gracefully — the
// slice round that would publish slices is a later slice.
func planStatusNote(out PlanOutcome) string {
	if out.Note != "" {
		return out.Note
	}
	switch out.Status {
	case "slices":
		return "The planner returned slices rather than a PRD. Slice publishing isn't wired up yet."
	default:
		return "The planner returned an unexpected result. Refine the idea and try again."
	}
}

func (m planModel) Cancelled() bool { return m.cancelled }

// editing reports whether a free-text field owns input, so the global ? and :
// overlays stay closed (both are valid characters in a raw idea or a typed
// answer): the idea textarea, or a text/Other field of the question form.
func (m planModel) editing() bool {
	switch m.step {
	case planInput:
		return true
	case planQuestions:
		return m.pform != nil && m.pform.editing()
	}
	return false
}

func (m planModel) view(spinner string) string {
	s := m.styles
	title := "plan"
	if m.step == planPRD && strings.TrimSpace(m.title) != "" {
		title = "plan · " + truncate(m.title, m.width-16)
	}
	header := s.Header.Render("⬡ trau") + "  " + s.SummaryTitle.Render(title)
	sep := s.Separator.Render(strings.Repeat("─", m.width))
	return header + "\n" + sep + "\n" + m.body(spinner) + "\n" + sep + "\n" + s.Help.Render(m.hint())
}

func (m planModel) body(spinner string) string {
	s := m.styles
	switch m.step {
	case planRunning:
		return spinner + " " + s.Subtle.Render("running a planning round — a fresh agent is reading the idea and your answers…")
	case planQuestions:
		if m.pform == nil {
			return ""
		}
		intro := s.Subtle.Render("The planner needs a few answers. Pick Other to type your own, or Skip to take the default.")
		return intro + "\n\n" + m.pform.form.View()
	case planPRD:
		return m.viewport.View()
	case planNote:
		return s.Subtle.Width(m.width - 2).Render(m.note)
	default:
		rows := []string{s.Subtle.Render("Describe the idea, or paste a file path. ctrl+d starts planning."), m.idea.View()}
		if m.badIdea {
			rows = append(rows, "", s.Error.Render("Type an idea (or a path to a file) first."))
		}
		return strings.Join(rows, "\n")
	}
}

func (m planModel) hint() string { return m.help().footer() }

// help is the Plan screen's key legend per step: the single source for its footer
// and the ? overlay.
func (m planModel) help() screenHelp {
	switch m.step {
	case planRunning:
		return screenHelp{title: "Plan", columns: []helpColumn{
			group("Session", fk("esc", "cancel")),
		}}
	case planQuestions:
		return screenHelp{title: "Plan · questions", columns: []helpColumn{
			group("Navigate", fk("↑↓", "move"), fk("tab", "next field")),
			group("Answer", fk("x", "toggle (multi)"), fk("enter", "select/submit")),
			group("Actions", fk("esc/←", "back")),
		}}
	case planPRD:
		return screenHelp{title: "Plan", columns: []helpColumn{
			group("Read PRD", fk("f/b/u/d", "scroll"), xk("g/G", "jump")),
			group("Actions", fk("e", "new idea"), fk("esc/q", "back")),
		}}
	case planNote:
		return screenHelp{title: "Plan", columns: []helpColumn{
			group("Actions", fk("e", "new idea"), fk("esc/q", "back")),
		}}
	default:
		return screenHelp{title: "Plan", columns: []helpColumn{
			group("Actions", fk("ctrl+d", "start planning"), fk("esc", "back")),
			group("Global", xk("ctrl+t", "toggle mouse (select text)")),
		}}
	}
}
