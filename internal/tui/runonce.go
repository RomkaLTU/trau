package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type runOnceStep int

const (
	runOnceConfirm runOnceStep = iota
	runOnceLoading
	runOnceList
)

// runOnceModel is the picker shown for "Run once". The user can type a ticket
// ID, or load the eligible ticket list via the fast API lister and pick from it.
type runOnceModel struct {
	styles  Styles
	actions Actions
	ctx     context.Context
	width   int
	height  int
	info    MenuInfo

	step        runOnceStep
	input       textinput.Model
	eligible    []ListedTicket
	cursor      int
	providerIdx int
	loadErr     error
	badID       bool
	done        bool
	cancelled   bool
	selected    string
}

type eligibleLoadedMsg struct {
	tickets []ListedTicket
	err     error
}

func newRunOnceModel(ctx context.Context, actions Actions, styles Styles, info MenuInfo, w, h int) runOnceModel {
	ti := textinput.New()
	ti.Placeholder = exampleID(info.Prefix)
	ti.CharLimit = 64
	ti.SetWidth(32)
	ti.Prompt = "› "
	ti.Focus()

	return runOnceModel{
		styles:      styles,
		actions:     actions,
		ctx:         ctx,
		width:       w,
		height:      h,
		info:        info,
		step:        runOnceConfirm,
		input:       ti,
		eligible:    nil,
		providerIdx: providerIndex(info.Providers, info.Provider),
	}
}

// providerIndex is the cycle start: the position of the config default within
// the fixed provider set, or 0 when it isn't found (empty set included).
func providerIndex(providers []ProviderChoice, dflt string) int {
	for i, p := range providers {
		if p.Name == dflt {
			return i
		}
	}
	return 0
}

func (m runOnceModel) Init() tea.Cmd { return textinput.Blink }

func (m runOnceModel) Update(msg tea.Msg) (runOnceModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case eligibleLoadedMsg:
		if m.step != runOnceLoading {
			return m, nil
		}
		if msg.err != nil {
			m.loadErr = msg.err
			m.step = runOnceConfirm
			m.input.Focus()
			return m, nil
		}
		m.eligible = msg.tickets
		m.cursor = 0
		m.step = runOnceList
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	if m.step == runOnceConfirm {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m runOnceModel) handleKey(msg tea.KeyPressMsg) (runOnceModel, tea.Cmd) {
	switch m.step {
	case runOnceConfirm:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled, m.done = true, true
			return m, nil
		case "shift+tab":
			m.providerIdx = cycleProvider(m.providerIdx, len(m.info.Providers))
			return m, nil
		case "enter":
			m.badID = false
			raw := strings.TrimSpace(m.input.Value())
			if raw == "" && m.info.Resume.Active() {
				m.selected = m.info.Resume.ID
				m.done = true
				return m, nil
			}
			id := extractTicketID(raw, m.info.Prefix)
			if id == "" {
				m.badID = true
				return m, nil
			}
			m.selected = id
			m.done = true
			return m, nil
		}
		if msg.String() == "l" {
			m.badID = false
			m.loadErr = nil
			m.step = runOnceLoading
			return m, m.loadEligibleCmd()
		}
		m.badID = false
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case runOnceLoading:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.step = runOnceConfirm
			m.input.Focus()
		}
		return m, nil

	case runOnceList:
		rows := m.listRows()
		switch msg.String() {
		case "ctrl+c":
			m.cancelled, m.done = true, true
		case "esc", "q":
			m.step = runOnceConfirm
			m.eligible = nil
			m.cursor = 0
			m.input.Focus()
		case "enter":
			if m.cursor >= 0 && m.cursor < len(rows) {
				m.selected = rows[m.cursor].ID
				m.done = true
			}
		case "shift+tab":
			m.providerIdx = cycleProvider(m.providerIdx, len(m.info.Providers))
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(rows)-1 {
				m.cursor++
			}
		case "o":
			if m.cursor >= 0 && m.cursor < len(rows) {
				return m, openURLCmd(linearIssueURL(rows[m.cursor].ID))
			}
		case "r":
			m.step = runOnceLoading
			return m, m.loadEligibleCmd()
		}
		return m, nil
	}
	return m, nil
}

func (m runOnceModel) loadEligibleCmd() tea.Cmd {
	actions, ctx := m.actions, m.ctx
	return func() tea.Msg {
		tickets, err := actions.ListEligible(ctx)
		return eligibleLoadedMsg{tickets: tickets, err: err}
	}
}

func (m runOnceModel) Done() bool       { return m.done }
func (m runOnceModel) Cancelled() bool  { return m.cancelled }
func (m runOnceModel) Selected() string { return m.selected }

// Provider returns the picked provider for an ephemeral single-run override, or
// "" when it matches the config default (so no override is applied).
func (m runOnceModel) Provider() string {
	name, _ := m.pickedProvider()
	if name == "" || name == m.info.Provider {
		return ""
	}
	return name
}

// pickedProvider is the currently-cycled provider name and its model, falling
// back to the MenuInfo default when the provider set is empty.
func (m runOnceModel) pickedProvider() (name, model string) {
	if m.providerIdx >= 0 && m.providerIdx < len(m.info.Providers) {
		p := m.info.Providers[m.providerIdx]
		return p.Name, p.Model
	}
	return m.info.Provider, m.info.Model
}

// cycleProvider advances the provider index one step, wrapping around; a
// non-positive count leaves it unchanged.
func cycleProvider(idx, n int) int {
	if n <= 0 {
		return idx
	}
	return (idx + 1) % n
}

func (m runOnceModel) body(spinnerView string) string {
	switch m.step {
	case runOnceLoading:
		return spinnerView + " " + m.styles.Subtle.Render("loading eligible tickets…") + "\n\n" + m.summary()
	case runOnceList:
		return m.renderList() + "\n\n" + m.summary()
	default:
		return m.renderConfirm()
	}
}

func (m runOnceModel) renderConfirm() string {
	s := m.styles
	var rows []string
	help := "type ID · 'l' load eligible tickets · enter run · esc back"
	if m.info.Resume.Active() {
		rows = append(rows,
			s.Warning.Render(m.info.Resume.Line()),
			s.Help.Render("press enter to continue it — or type another ID below"),
			"")
		help = "enter resume · type ID to pick another · 'l' load queue · esc back"
	}
	rows = append(rows,
		s.Subtle.Render("Run a single ticket. Type an ID or load the ready queue:"),
		"",
		s.Subtle.Render("Issue ")+m.input.View(),
		s.Help.Render(help),
	)
	switch {
	case m.badID:
		rows = append(rows, "", s.Error.Render("Couldn't read a ticket ID — try "+exampleID(m.info.Prefix)+"."))
	case m.loadErr != nil:
		rows = append(rows, "", s.Warning.Render(truncate("Couldn't load eligible tickets: "+m.loadErr.Error(), 48)))
	}
	rows = append(rows, "", m.summary())
	return strings.Join(rows, "\n")
}

func (m runOnceModel) renderList() string {
	s := m.styles
	items := m.listRows()
	if len(items) == 0 {
		return s.Subtle.Render("No eligible tickets right now.") + "\n\n" +
			s.Help.Render("esc/q back · 'r' refresh")
	}

	header := fmt.Sprintf("Eligible tickets (%d):", len(items))
	if m.info.Resume.Active() {
		header = fmt.Sprintf("Resume or pick a ticket (%d):", len(items))
	}
	var rows []string
	rows = append(rows, s.Subtle.Render(header))
	rows = append(rows, "")

	idW, titleW := m.listColumnWidths()
	for i, t := range items {
		focused := i == m.cursor
		idStyle := s.Subtle
		titleStyle := s.Subtle
		stateStyle := s.Help
		if focused {
			idStyle = s.Header
			titleStyle = lipgloss.NewStyle().Foreground(theme.Brand)
		}
		idStr := padRight(t.ID, idW)
		titleStr := truncate(t.Title, titleW)
		stateStr := truncate(firstNonEmpty(t.State, "—"), 12)
		rows = append(rows, cursorMarker(s, focused)+idStyle.Render(idStr)+"  "+titleStyle.Render(titleStr)+"  "+stateStyle.Render(stateStr))
	}
	return strings.Join(rows, "\n")
}

// listRows is the run-once list: the resumable ticket (if any) pinned first and
// labeled as a resume, then the eligible ready queue with the resume de-duped out.
// The cursor indexes into this combined slice, so resume sits at index 0 and is
// pre-selected when the list opens.
func (m runOnceModel) listRows() []ListedTicket {
	var rows []ListedTicket
	if r := m.info.Resume; r.Active() {
		rows = append(rows, ListedTicket{ID: r.ID, Title: r.Title, State: "↻ resume"})
	}
	for _, t := range m.eligible {
		if t.ID == m.info.Resume.ID {
			continue
		}
		rows = append(rows, t)
	}
	return rows
}

func (m runOnceModel) listColumnWidths() (idW, titleW int) {
	const gap = 8 // marker + padding + state column
	for _, t := range m.listRows() {
		if w := lipgloss.Width(t.ID); w > idW {
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

// canSwitchProvider reports whether there is more than one provider to cycle,
// so the marker/hint only advertise the toggle when it does something.
func (m runOnceModel) canSwitchProvider() bool { return len(m.info.Providers) > 1 }

func (m runOnceModel) summary() string {
	s := m.styles
	info := m.info

	name, model := m.pickedProvider()
	agent := firstNonEmpty(name, "?")
	if model != "" {
		agent += " · " + model
	}
	parts := []string{agent}
	if info.Base != "" {
		parts = append(parts, "base "+info.Base)
	}
	line := s.Help.Render(strings.Join(parts, " · "))
	if m.canSwitchProvider() {
		line += "  " + s.Subtle.Render("⇥ switch provider")
	}
	if name != "" && info.Provider != "" && name != info.Provider {
		line += " " + s.Help.Render("(default: "+info.Provider+")")
	}
	return line + "\n" +
		s.Help.Render(fmt.Sprintf("%d in-flight · %d done", info.InFlight, info.Done))
}

func (m runOnceModel) hint() string {
	sw := ""
	if m.canSwitchProvider() {
		sw = " · ⇥ switch provider"
	}
	switch m.step {
	case runOnceLoading:
		return "loading… · esc/q cancel"
	case runOnceList:
		return "↑↓/jk move · enter run" + sw + " · 'o' open · 'r' refresh · esc/q back"
	default:
		if m.info.Resume.Active() {
			return "enter resume " + m.info.Resume.ID + " · type an ID" + sw + " · 'l' load · esc back"
		}
		return "type an ID" + sw + " · 'l' load eligible · enter run · esc back"
	}
}
