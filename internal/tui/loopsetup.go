package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type loopStep int

const (
	loopConfirm loopStep = iota
	loopLoading
	loopList
)

// loopSetupModel is the screen shown when the user selects "Run loop". It takes an
// optional epic: with one, it fetches the epic's sub-issues via an agent and shows
// the planned list before starting (loop scoped to that epic); left blank, it loops
// the team's ready queue as before. Either way nothing starts from a stray keypress.
// Outcome is read via Done/Cancelled/Epic.
type loopSetupModel struct {
	styles  Styles
	actions Actions
	ctx     context.Context
	width   int
	height  int
	info    MenuInfo

	step      loopStep
	input     textinput.Model
	epic      string
	subs      []SubIssue
	cursor    int
	loadErr   error
	badID     bool
	subsCache map[string][]SubIssue // epic id -> loaded sub-issues

	done      bool
	cancelled bool
	single    bool
	selected  string // single sub-issue ID to run instead of the loop

	// loopArmed guards the biggest blast radius in the app: a blank-input enter
	// starts the whole ready queue. The first such enter arms this; the confirming
	// second enter starts. Any other key (esc included) disarms.
	loopArmed bool
}

type subIssuesLoadedMsg struct {
	epic string
	subs []SubIssue
	err  error
}

func newLoopSetupModel(ctx context.Context, actions Actions, styles Styles, info MenuInfo, w, h int) loopSetupModel {
	ti := textinput.New()
	ti.Placeholder = exampleID(info.Prefix) + " (optional)"
	ti.CharLimit = 64
	ti.SetWidth(32)
	ti.Prompt = "› "
	ti.Focus()

	return loopSetupModel{
		styles:    styles,
		actions:   actions,
		ctx:       ctx,
		width:     w,
		height:    h,
		info:      info,
		step:      loopConfirm,
		input:     ti,
		subsCache: map[string][]SubIssue{},
	}
}

func (m loopSetupModel) Update(msg tea.Msg) (loopSetupModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case subIssuesLoadedMsg:
		if m.step != loopLoading || msg.epic != m.epic {
			return m, nil
		}
		if msg.err != nil {
			m.loadErr = msg.err
			m.step = loopConfirm
			m.input.Focus()
			return m, nil
		}
		m.subs = msg.subs
		m.subsCache[msg.epic] = msg.subs
		m.cursor = 0
		m.step = loopList
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg:
		return m.handleMouseClick(msg)
	}

	if m.step == loopConfirm {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleMouseClick resolves a left click on the sub-issue list: a row selects, and
// a click on the already-selected row runs just that sub-issue (the s verb).
func (m loopSetupModel) handleMouseClick(msg tea.MouseClickMsg) (loopSetupModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.step != loopList {
		return m, nil
	}
	if i, ok := clickedRow(msg, zoneLoopRow, len(m.subs)); ok {
		if i == m.cursor {
			return m.handleKey(synthVerbKey("s"))
		}
		m.cursor = i
	}
	return m, nil
}

func (m loopSetupModel) handleKey(msg tea.KeyPressMsg) (loopSetupModel, tea.Cmd) {
	switch m.step {
	case loopConfirm:
		// A whole-ready-queue start is armed: the confirming second enter starts it;
		// esc or any other key disarms and is then handled normally below (so typing
		// an epic id both disarms and enters the id).
		if m.loopArmed {
			m.loopArmed = false
			if msg.String() == "enter" {
				m.epic = ""
				m.done = true
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled, m.done = true, true
			return m, nil
		case "enter":
			raw := strings.TrimSpace(m.input.Value())
			if raw == "" {
				m.loopArmed = true
				return m, nil
			}
			id := extractTicketID(raw, m.info.Prefix)
			if id == "" {
				m.badID = true
				return m, nil
			}
			m.epic = id
			m.badID = false
			m.loadErr = nil
			if cached, ok := m.subsCache[id]; ok {
				m.subs = cached
				m.cursor = 0
				m.step = loopList
				return m, nil
			}
			m.step = loopLoading
			return m, m.loadCmd(id)
		}
		m.badID = false
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case loopLoading:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.step = loopConfirm
			m.input.Focus()
		}
		return m, nil

	case loopList:
		switch msg.String() {
		case "ctrl+c":
			m.cancelled, m.done = true, true
		case "esc", "q":
			m.step = loopConfirm
			m.subs = nil
			m.cursor = 0
			m.input.Focus()
		case "enter":
			m.single = len(m.subs) == 0
			m.done = true
		case "up", "shift+tab":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "tab":
			if m.cursor < len(m.subs)-1 {
				m.cursor++
			}
		case "o":
			if m.cursor >= 0 && m.cursor < len(m.subs) {
				return m, openURLCmd(linearIssueURL(m.subs[m.cursor].ID))
			}
		case "r":
			delete(m.subsCache, m.epic)
			m.step = loopLoading
			return m, m.loadCmd(m.epic)
		case "s":
			if len(m.subs) > 0 && m.cursor >= 0 && m.cursor < len(m.subs) {
				m.selected = m.subs[m.cursor].ID
			} else {
				m.selected = m.epic
			}
			m.done = true
		}
		return m, nil
	}
	return m, nil
}

func (m loopSetupModel) loadCmd(epic string) tea.Cmd {
	actions, ctx := m.actions, m.ctx
	return func() tea.Msg {
		subs, err := actions.SubIssues(ctx, epic)
		return subIssuesLoadedMsg{epic: epic, subs: subs, err: err}
	}
}

func (m loopSetupModel) Done() bool      { return m.done }
func (m loopSetupModel) Cancelled() bool { return m.cancelled }
func (m loopSetupModel) Epic() string    { return m.epic }

// Single reports that the entered issue has no sub-issues, so it should run as a
// single ticket rather than as an epic loop.
func (m loopSetupModel) Single() bool { return m.single }

// Selected returns the ID of a single sub-issue chosen by the user, or "" when
// the user wants to run the whole loop/epic.
func (m loopSetupModel) Selected() string { return m.selected }

func (m loopSetupModel) body(spinnerView string) string {
	switch m.step {
	case loopLoading:
		return spinnerView + " " + m.styles.Subtle.Render("loading sub-issues of "+m.epic+"…") + "\n\n" + m.summary()
	case loopList:
		return m.renderList() + "\n\n" + m.summary()
	default:
		return m.renderConfirm()
	}
}

func (m loopSetupModel) renderConfirm() string {
	s := m.styles
	rows := []string{
		s.Subtle.Render("Run an epic, a single issue, or the whole ready queue:"),
		"",
		s.Subtle.Render("Issue ") + m.input.View(),
		s.Help.Render("epic → its sub-issues · issue → just that one · blank → ready queue"),
	}
	switch {
	case m.loopArmed:
		rows = append(rows, "", s.Warning.Render("⚠ start the loop over the whole ready queue — every ready ticket runs."))
	case m.badID:
		rows = append(rows, "", s.Error.Render("Couldn't read an epic ID — try "+exampleID(m.info.Prefix)+"."))
	case m.loadErr != nil:
		rows = append(rows, "", s.Warning.Render(truncate("Couldn't load sub-issues: "+m.loadErr.Error(), 48)))
	}
	rows = append(rows, "", m.summary())
	return strings.Join(rows, "\n")
}

func (m loopSetupModel) renderList() string {
	s := m.styles
	if len(m.subs) == 0 {
		return s.Subtle.Render(m.epic+" — no sub-issues") + "\n\n" +
			s.Help.Render("It will run as a single ticket.")
	}

	done := 0
	for _, sub := range m.subs {
		if sub.Done {
			done++
		}
	}

	var rows []string
	if done > 0 {
		rows = append(rows, s.Subtle.Render(fmt.Sprintf("%s — planned sub-issues · %d done · %d left", m.epic, done, len(m.subs)-done)))
	} else {
		rows = append(rows, s.Subtle.Render(fmt.Sprintf("%s — planned sub-issues (%d):", m.epic, len(m.subs))))
	}
	rows = append(rows, "")

	doneStyle := lipgloss.NewStyle().Foreground(theme.Faint)
	idW, titleW := m.subIssueColumnWidths()
	for i, sub := range m.subs {
		status := "  "
		idStyle := s.Subtle
		titleStyle := s.Subtle
		if sub.Done {
			status = s.Success.Render("✓ ")
			idStyle = doneStyle
			titleStyle = doneStyle
		}
		if i == m.cursor {
			idStyle = s.Header
			titleStyle = lipgloss.NewStyle().Foreground(theme.Brand)
		}
		idStr := padRight(sub.ID, idW)
		titleStr := truncate(sub.Title, titleW)
		if sub.HasChildren {
			titleStr += s.Subtle.Render("  ⊘ nested epic")
		}
		rows = append(rows, markRow(zoneLoopRow, i, cursorMarker(s, i == m.cursor)+status+idStyle.Render(idStr)+"  "+titleStyle.Render(titleStr)))
	}

	return strings.Join(rows, "\n")
}

func (m loopSetupModel) subIssueColumnWidths() (idW, titleW int) {
	const gap = 6 // marker + done-status slot + padding between columns
	for _, sub := range m.subs {
		if w := lipgloss.Width(sub.ID); w > idW {
			idW = w
		}
	}
	if idW < 8 {
		idW = 8
	}
	titleW = m.width - idW - gap - 4
	if titleW < 12 {
		titleW = 12
	}
	return idW, titleW
}

func (m loopSetupModel) summary() string {
	s := m.styles
	info := m.info

	agent := firstNonEmpty(info.Provider, "?")
	if info.Model != "" {
		agent += " · " + info.Model
	}
	merge := "auto-merge off"
	if info.AutoMerge {
		merge = "auto-merge on"
	}
	parts := []string{agent}
	if info.Base != "" {
		parts = append(parts, "base "+info.Base)
	}
	parts = append(parts, merge)

	out := s.Help.Render(strings.Join(parts, " · ")) + "\n" +
		s.Help.Render(fmt.Sprintf("%d in-flight · %d done", info.InFlight, info.Done))
	if info.Resume.Active() {
		out = s.Warning.Render(info.Resume.Line()) + "\n" +
			s.Help.Render("the loop resumes this first, then pulls the ready queue") + "\n" + out
	}
	return out
}

func (m loopSetupModel) hint() string {
	if m.step == loopLoading {
		return "loading… · esc/q cancel"
	}
	if m.loopArmed {
		return "⚠ start the loop over the whole ready queue? enter again to start · esc back"
	}
	return m.help().footer()
}

// help is the run-loop setup's key legend per step: the single source for its
// footer and the ? overlay.
func (m loopSetupModel) help() screenHelp {
	switch m.step {
	case loopLoading:
		return screenHelp{title: "Run loop", columns: []helpColumn{
			group("Session", fk("esc/q", "cancel")),
		}}
	case loopList:
		return screenHelp{title: "Run loop", columns: []helpColumn{
			group("Navigate", fk("↑↓", "move"), xk("tab/⇧tab", "move")),
			group("Actions",
				fk("enter", "start"),
				fk("s", "run selected"),
				fk("o", "open issue"),
				fk("r", "refresh"),
			),
			group("Session", fk("esc/q", "back")),
		}}
	default: // loopConfirm
		return screenHelp{title: "Run loop", columns: []helpColumn{
			group("Actions", fk("enter", "start ready queue (confirm)"), fk("type", "epic to preview")),
			group("Session", fk("esc", "back")),
		}}
	}
}

func linearIssueURL(id string) string {
	return "https://linear.app/issue/" + id
}

// exampleID renders a sample ticket id for placeholders and hints using the
// configured prefix (COD-123, ENG-123). An empty prefix falls back to COD.
func exampleID(prefix string) string {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if prefix == "" {
		prefix = "COD"
	}
	return prefix + "-123"
}

// extractTicketID accepts free-form input and returns the best-effort ticket
// identifier using the configured prefix.
func extractTicketID(input, prefix string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if prefix == "" {
		prefix = "COD"
	}
	upper := strings.ToUpper(input)
	re := regexp.MustCompile(`(` + regexp.QuoteMeta(prefix) + `)-?([0-9]+)`)
	if ms := re.FindStringSubmatch(upper); len(ms) == 3 {
		return ms[1] + "-" + ms[2]
	}
	return ""
}

func padRight(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}
