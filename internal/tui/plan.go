package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// PlanOutcome is a planning round's result surfaced to the Plan screen. Status is
// the payload status; for a PRD, Title and Markdown carry the document. For the
// other statuses — not yet actioned in this slice — Note holds a graceful one-line
// message so the screen can flag rather than crash on them.
type PlanOutcome struct {
	Status   string
	Title    string
	Markdown string
	Note     string
}

type planStep int

const (
	planInput   planStep = iota // idea entry
	planRunning                 // round in flight
	planPRD                     // PRD in a scrollable viewport
	planNote                    // graceful message (non-PRD result or error)
)

// planModel is the Plan screen: paste/type a raw idea (or a file path), run one
// planning round, and read the returned PRD in a scrollable viewport.
type planModel struct {
	styles  Styles
	actions Actions
	ctx     context.Context
	width   int
	height  int

	inited    bool
	step      planStep
	idea      textarea.Model
	viewport  viewport.Model
	title     string
	note      string
	badIdea   bool
	cancelled bool
}

type planDoneMsg struct {
	out PlanOutcome
	err error
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
		return m, nil

	case planDoneMsg:
		if m.step != planRunning {
			return m, nil
		}
		if msg.err != nil {
			m.step, m.note = planNote, "✗ "+msg.err.Error()
			return m, nil
		}
		if msg.out.Status == "prd" {
			m.step = planPRD
			m.title = msg.out.Title
			m.viewport.SetContent(m.prdBody(msg.out))
			m.viewport.GotoTop()
			return m, nil
		}
		m.step, m.note = planNote, planStatusNote(msg.out)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	switch m.step {
	case planInput:
		m.idea, cmd = m.idea.Update(msg)
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

// prdBody is the PRD viewport content: the title as a heading over the markdown.
func (m planModel) prdBody(out PlanOutcome) string {
	title := strings.TrimSpace(out.Title)
	if title == "" {
		return out.Markdown
	}
	return m.styles.SummaryTitle.Render(title) + "\n\n" + out.Markdown
}

// planStatusNote flags a non-PRD result gracefully — the question and slice rounds
// that would act on these statuses are later slices.
func planStatusNote(out PlanOutcome) string {
	if out.Note != "" {
		return out.Note
	}
	switch out.Status {
	case "questions":
		return "The planner returned questions. Interactive question rounds aren't wired up yet — refine the idea and try again."
	case "slices":
		return "The planner returned slices rather than a PRD. Slice publishing isn't wired up yet."
	default:
		return "The planner returned an unexpected result. Refine the idea and try again."
	}
}

func (m planModel) Cancelled() bool { return m.cancelled }

// editing reports whether the idea textarea owns input, so the global ? and :
// overlays stay closed (both are valid characters in a raw idea).
func (m planModel) editing() bool { return m.step == planInput }

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
		return spinner + " " + s.Subtle.Render("running a planning round — a fresh agent is drafting your PRD…")
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
