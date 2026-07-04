package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/notify"
)

// planQuestionsNotify is the desktop nudge fired when a planning question round
// arrives while the terminal is unfocused, so a long round never silently stalls.
const planQuestionsNotify = "planning needs answers — a question round is waiting"

// planSlicesNotify is the same nudge for the slice round: drafted slices are
// waiting for review.
const planSlicesNotify = "planning drafted slices — a review is waiting"

// PlanOutcome is a planning round's result surfaced to the Plan screen. Status is
// the payload status; for a PRD, Title and Markdown carry the document; for
// questions, Questions carries them; for slices, Slices carries the drafts and
// Epic the published epic they would be created under. SessionDir is the durable
// plan session the round ran under, so the next round can be answered against it.
// Note holds a graceful one-line message for a status the screen does not action.
type PlanOutcome struct {
	Status     string
	SessionDir string
	Epic       string
	Title      string
	Markdown   string
	Questions  []PlanQuestion
	Slices     []PlanSlice
	Note       string
}

// PlanSession is one durable plan session projected onto the Plan screen's list:
// where it lives, its checkpoint phase, and the labels a row shows. Resumable is
// false for a terminal (sliced or aborted) session, which lists for inspection or
// cleanup rather than resume.
type PlanSession struct {
	Dir       string
	Phase     string
	Title     string
	Idea      string
	Updated   string
	Resumable bool
}

type planStep int

const (
	planInput     planStep = iota // idea entry
	planRunning                   // round in flight
	planQuestions                 // answering a round of questions
	planPRD                       // PRD in a scrollable viewport
	planRevise                    // entering a free-text change request
	planSlices                    // reviewing drafted slices before anything is created
	planCreating                  // creating the confirmed slices as children of the epic
	planNote                      // graceful message (approval, non-PRD result, or error)
	planList                      // choosing a saved session to resume, abort, or inspect
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
	changeNote textarea.Model
	viewport   viewport.Model
	pform      *planForm
	slices     sliceReview
	sessions   []PlanSession
	listCursor int
	sessionDir string
	title      string
	note       string
	badIdea    bool
	badNote    bool
	cancelled  bool

	// stream is the w-attach live tail of the planning agent during a round;
	// notifier posts the desktop nudge when a question round lands while the
	// terminal is unfocused (focused tracks that, fed from the app shell).
	stream   liveStream
	notifier notify.Notifier
	focused  bool
}

type planDoneMsg struct {
	out PlanOutcome
	err error
}

type planFormSubmitMsg struct{}

type planFormCancelMsg struct{}

type planApprovedMsg struct {
	out PublishOutcome
	err error
}

type planAbortDoneMsg struct{ err error }

type planSlicesDoneMsg struct {
	out SliceOutcome
	err error
}

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

	cn := textarea.New()
	cn.Placeholder = "Describe the changes you want — the planner will revise the PRD…"

	m := planModel{
		styles:     styles,
		actions:    actions,
		ctx:        ctx,
		inited:     true,
		step:       planInput,
		idea:       ta,
		changeNote: cn,
		viewport:   viewport.New(),
		focused:    true,
	}
	m.sessions = actions.ListPlans()
	if len(m.sessions) > 0 {
		m.step = planList
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
	m.changeNote.SetWidth(innerW)
	m.changeNote.SetHeight(taH)
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
		m.stream.reset()
		if msg.err != nil {
			m.step, m.note = planNote, planErrNote(msg.err)
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
			var cmds []tea.Cmd
			if !m.focused {
				cmds = append(cmds, notifyCmd(m.notifier, "trau", planQuestionsNotify))
			}
			if accessibleRequested() {
				cmds = append(cmds, m.accessiblePlanCmd(msg.out.Questions))
				return m, tea.Batch(cmds...)
			}
			m.pform = newPlanForm(msg.out.Questions, m.formWidth())
			m.step = planQuestions
			cmds = append(cmds, m.pform.form.Init())
			return m, tea.Batch(cmds...)
		case "slices":
			m.slices = newSliceReview(msg.out.Epic, msg.out.Slices)
			m.step = planSlices
			if !m.focused {
				return m, notifyCmd(m.notifier, "trau", planSlicesNotify)
			}
			return m, nil
		default:
			m.step, m.note = planNote, planStatusNote(msg.out)
			return m, nil
		}

	case eventMsg:
		if m.step == planRunning && msg.ev.Kind == event.KindAgentStart {
			if p := strField(msg.ev.Fields, "transcript_path"); p != "" {
				m.stream.setPath(p, intField(msg.ev.Fields, "cols"), intField(msg.ev.Fields, "rows"))
				return m, m.stream.pump()
			}
		}
		return m, nil

	case streamDataMsg:
		m.stream.write(msg)
		return m, nil

	case spinner.TickMsg:
		return m, m.stream.pump()

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

	case planApprovedMsg:
		if m.step != planPRD {
			return m, nil
		}
		if msg.err != nil {
			m.step, m.note = planNote, "✗ "+msg.err.Error()
			return m, nil
		}
		if msg.out.Published {
			m.step = planRunning
			return m, m.slicePlanCmd()
		}
		m.step, m.note = planNote, planPublishNote(msg.out)
		return m, nil

	case planSlicesDoneMsg:
		if m.step != planCreating {
			return m, nil
		}
		if msg.err != nil {
			m.step = planSlices
			m.slices.err = msg.err.Error()
			m.slices.created = msg.out.Children
			return m, nil
		}
		m.step, m.note = planNote, sliceCreatedNote(msg.out)
		return m, nil

	case planAbortDoneMsg:
		if msg.err != nil {
			m.step, m.note = planNote, "✗ "+msg.err.Error()
			return m, nil
		}
		m.sessions = m.actions.ListPlans()
		if m.listCursor >= len(m.sessions) {
			m.listCursor = len(m.sessions) - 1
		}
		m.step = planList
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
	case planRevise:
		m.changeNote, cmd = m.changeNote.Update(msg)
	case planSlices:
		if m.slices.editing {
			m.slices.input, cmd = m.slices.input.Update(msg)
		}
	}
	return m, cmd
}

func (m planModel) handleKey(msg tea.KeyPressMsg) (planModel, tea.Cmd) {
	switch m.step {
	case planList:
		return m.handleListKey(msg)

	case planInput:
		switch msg.String() {
		case "esc":
			if len(m.sessions) > 0 {
				m.step = planList
				return m, nil
			}
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
		switch msg.String() {
		case "w":
			if m.stream.toggle() {
				return m, m.stream.pump()
			}
			return m, nil
		case "esc", "q":
			if m.stream.attached {
				m.stream.attached = false
				return m, nil
			}
		}
		if isBack(msg) {
			m.stream.reset()
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
			switch msg.String() {
			case "a":
				return m, m.approvePlanCmd()
			case "r":
				m.step, m.badNote = planRevise, false
				m.changeNote.Reset()
				m.changeNote.Focus()
				return m, textarea.Blink
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		return m, nil

	case planRevise:
		switch msg.String() {
		case "esc":
			m.step, m.badNote = planPRD, false
			return m, nil
		case "ctrl+d":
			if strings.TrimSpace(m.changeNote.Value()) == "" {
				m.badNote = true
				return m, nil
			}
			m.step, m.badNote = planRunning, false
			return m, m.revisePlanCmd()
		}
		m.badNote = false
		var cmd tea.Cmd
		m.changeNote, cmd = m.changeNote.Update(msg)
		return m, cmd

	case planSlices:
		return m.handleSlicesKey(msg)
	}
	return m, nil
}

// handleSlicesKey drives the slice review list: move the cursor, reorder, drop,
// retitle, confirm the creation, or cancel with nothing created. While the inline
// title editor is open it owns every key except its enter/esc exits.
func (m planModel) handleSlicesKey(msg tea.KeyPressMsg) (planModel, tea.Cmd) {
	if m.slices.editing {
		switch msg.String() {
		case "enter":
			m.slices.commitEdit()
			return m, nil
		case "esc":
			m.slices.cancelEdit()
			return m, nil
		}
		var cmd tea.Cmd
		m.slices.input, cmd = m.slices.input.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "esc", "q":
		m.sessions = m.actions.ListPlans()
		m.listCursor = 0
		m.step = planList
		return m, nil
	case "up", "k":
		m.slices.move(-1)
	case "down", "j":
		m.slices.move(1)
	case "shift+up", "K":
		m.slices.moveRow(-1)
	case "shift+down", "J":
		m.slices.moveRow(1)
	case "e", "enter":
		m.slices.startEdit()
		return m, textinput.Blink
	case "x":
		m.slices.toggleDrop()
	case "c":
		kept := m.slices.kept()
		if len(kept) == 0 {
			m.slices.err = "Nothing to create — every slice is dropped."
			return m, nil
		}
		m.step = planCreating
		return m, m.createSlicesCmd(kept)
	}
	return m, nil
}

// handleListKey drives the saved-session list: move the cursor, resume or abort
// the selected session, start a fresh idea, or back out to the menu.
func (m planModel) handleListKey(msg tea.KeyPressMsg) (planModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.cancelled = true
		return m, nil
	case "up", "k":
		if m.listCursor > 0 {
			m.listCursor--
		}
		return m, nil
	case "down", "j":
		if m.listCursor < len(m.sessions)-1 {
			m.listCursor++
		}
		return m, nil
	case "n":
		m.step = planInput
		m.idea.Reset()
		m.idea.Focus()
		return m, textarea.Blink
	case "enter":
		sess := m.selectedSession()
		if sess == nil {
			return m, nil
		}
		m.sessionDir = sess.Dir
		m.step = planRunning
		return m, m.resumePlanCmd(sess.Dir)
	case "x":
		sess := m.selectedSession()
		if sess == nil || !sess.Resumable {
			return m, nil
		}
		return m, m.abortPlanCmd(sess.Dir)
	}
	return m, nil
}

func (m planModel) selectedSession() *PlanSession {
	if m.listCursor < 0 || m.listCursor >= len(m.sessions) {
		return nil
	}
	return &m.sessions[m.listCursor]
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

func (m planModel) revisePlanCmd() tea.Cmd {
	actions, ctx, dir, note := m.actions, m.ctx, m.sessionDir, m.changeNote.Value()
	return func() tea.Msg {
		out, err := actions.RevisePlan(ctx, dir, note)
		return planDoneMsg{out: out, err: err}
	}
}

func (m planModel) approvePlanCmd() tea.Cmd {
	actions, ctx, dir := m.actions, m.ctx, m.sessionDir
	return func() tea.Msg {
		out, err := actions.ApprovePlan(ctx, dir)
		return planApprovedMsg{out: out, err: err}
	}
}

// planPublishNote is the message shown when approving a PRD could not publish it:
// the tracker lacks the capability, so the plan stays local at prd_ready. A
// published approval flows straight into the slice round instead of a note.
func planPublishNote(out PublishOutcome) string {
	if out.Published {
		return "✓ PRD approved and published as epic " + out.Epic + " — checkpoint advanced to published."
	}
	return "✓ PRD approved. This tracker can't publish plans, so it stays local at prd_ready."
}

// sliceCreatedNote is the message shown after a confirmed review created the
// epic's children, closing the plan session.
func sliceCreatedNote(out SliceOutcome) string {
	if !out.Created {
		return "Nothing created — this tracker can't create child issues, so the plan stays at published."
	}
	return fmt.Sprintf("✓ Created %d child issues under epic %s — checkpoint advanced to sliced. trau %s builds them.", len(out.Children), out.Epic, out.Epic)
}

func (m planModel) slicePlanCmd() tea.Cmd {
	actions, ctx, dir := m.actions, m.ctx, m.sessionDir
	return func() tea.Msg {
		out, err := actions.SlicePlan(ctx, dir)
		return planDoneMsg{out: out, err: err}
	}
}

func (m planModel) createSlicesCmd(slices []PlanSlice) tea.Cmd {
	actions, ctx, dir := m.actions, m.ctx, m.sessionDir
	return func() tea.Msg {
		out, err := actions.CreateSlices(ctx, dir, slices)
		return planSlicesDoneMsg{out: out, err: err}
	}
}

func (m planModel) resumePlanCmd(dir string) tea.Cmd {
	actions, ctx := m.actions, m.ctx
	return func() tea.Msg {
		out, err := actions.ResumePlan(ctx, dir)
		return planDoneMsg{out: out, err: err}
	}
}

func (m planModel) abortPlanCmd(dir string) tea.Cmd {
	actions, ctx := m.actions, m.ctx
	return func() tea.Msg {
		return planAbortDoneMsg{err: actions.AbortPlan(ctx, dir)}
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

// planErrNote frames a failed round: a blameless provider pause reads as a paused
// glyph and stays resumable, everything else as a plain error.
func planErrNote(err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, "planning paused:") {
		return "⏸ " + strings.TrimPrefix(msg, "planning paused: ") + " — resume this session once the provider recovers."
	}
	return "✗ " + msg
}

// planStatusNote flags a result the Plan screen does not action gracefully.
func planStatusNote(out PlanOutcome) string {
	if out.Note != "" {
		return out.Note
	}
	return "The planner returned an unexpected result. Refine the idea and try again."
}

func (m planModel) Cancelled() bool { return m.cancelled }

// editing reports whether a free-text field owns input, so the global ? and :
// overlays stay closed (both are valid characters in a raw idea or a typed
// answer): the idea textarea, a text/Other field of the question form, or the
// slice review's inline title editor.
func (m planModel) editing() bool {
	switch m.step {
	case planInput, planRevise:
		return true
	case planQuestions:
		return m.pform != nil && m.pform.editing()
	case planSlices:
		return m.slices.editing
	}
	return false
}

func (m planModel) view(spinner string) string {
	s := m.styles
	title := "plan"
	switch {
	case m.step == planPRD && strings.TrimSpace(m.title) != "":
		title = "plan · " + truncate(m.title, m.width-16)
	case (m.step == planSlices || m.step == planCreating) && m.slices.epic != "":
		title = "plan · slices · " + m.slices.epic
	}
	header := s.Header.Render("⬡ trau") + "  " + s.SummaryTitle.Render(title)
	sep := s.Separator.Render(strings.Repeat("─", m.width))
	return header + "\n" + sep + "\n" + m.body(spinner) + "\n" + sep + "\n" + s.Help.Render(m.hint())
}

func (m planModel) body(spinner string) string {
	s := m.styles
	switch m.step {
	case planList:
		return m.listBody()
	case planRunning:
		if m.stream.attached {
			if body := m.stream.view(m.width-2, m.height-4); body != "" {
				return body
			}
			return s.Subtle.Render("◉ attached — waiting for the planning agent…  w detaches")
		}
		return spinner + " " + s.Subtle.Render("running a planning round — a fresh agent is reading the idea and your answers…  w attaches the live agent view")
	case planQuestions:
		if m.pform == nil {
			return ""
		}
		intro := s.Subtle.Render("The planner needs a few answers. Pick Other to type your own, or Skip to take the default.")
		return intro + "\n\n" + m.pform.form.View()
	case planPRD:
		return m.viewport.View()
	case planSlices:
		return m.slices.view(s, m.width)
	case planCreating:
		return spinner + " " + s.Subtle.Render(fmt.Sprintf("creating %d child issues under epic %s…", len(m.slices.kept()), m.slices.epic))
	case planRevise:
		rows := []string{s.Subtle.Render("Describe the changes you want. ctrl+d revises the PRD; esc keeps it."), m.changeNote.View()}
		if m.badNote {
			rows = append(rows, "", s.Error.Render("Type the changes you want first."))
		}
		return strings.Join(rows, "\n")
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

// listBody renders the saved-session list: each row is its checkpoint state
// (the phase) beside the PRD title, or the idea's first line before one exists.
func (m planModel) listBody() string {
	s := m.styles
	intro := s.Subtle.Render("Resume an in-flight plan session, abort one, or start a new idea.")
	rows := make([]string, 0, len(m.sessions))
	for i, sess := range m.sessions {
		label := strings.TrimSpace(sess.Title)
		if label == "" {
			label = sess.Idea
		}
		rows = append(rows, listRow(s, i == m.listCursor, sess.Phase, truncate(label, m.width-20), 12))
	}
	return intro + "\n\n" + strings.Join(rows, "\n")
}

func (m planModel) hint() string { return m.help().footer() }

// help is the Plan screen's key legend per step: the single source for its footer
// and the ? overlay.
func (m planModel) help() screenHelp {
	switch m.step {
	case planList:
		return screenHelp{title: "Plan · sessions", columns: []helpColumn{
			group("Navigate", fk("↑↓", "move")),
			group("Session", fk("enter", "resume"), fk("x", "abort"), fk("n", "new idea")),
			group("Actions", fk("esc/q", "back")),
		}}
	case planRunning:
		return screenHelp{title: "Plan", columns: []helpColumn{
			group("Live", fk("w", "attach agent view")),
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
			group("Review", fk("a", "approve"), fk("r", "request changes")),
			group("Read PRD", fk("f/b/u/d", "scroll"), xk("g/G", "jump")),
			group("Actions", fk("e", "new idea"), fk("esc/q", "back")),
		}}
	case planRevise:
		return screenHelp{title: "Plan · request changes", columns: []helpColumn{
			group("Actions", fk("ctrl+d", "revise PRD"), fk("esc", "cancel")),
		}}
	case planSlices:
		if m.slices.editing {
			return screenHelp{title: "Plan · slices", columns: []helpColumn{
				group("Edit title", fk("enter", "apply"), fk("esc", "keep old")),
			}}
		}
		return screenHelp{title: "Plan · slices", columns: []helpColumn{
			group("Navigate", fk("↑↓", "move"), fk("K/J", "reorder")),
			group("Slice", fk("e/enter", "edit title"), fk("x", "drop/keep")),
			group("Actions", fk("c", "create children"), fk("esc/q", "cancel")),
		}}
	case planCreating:
		return screenHelp{title: "Plan · slices"}
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
