package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

	step      runOnceStep
	input     textinput.Model
	eligible  []ListedTicket
	cursor    int
	loadErr   error
	badID     bool
	done      bool
	cancelled bool
	selected  string
}

type eligibleLoadedMsg struct {
	tickets []ListedTicket
	err     error
}

func newRunOnceModel(ctx context.Context, actions Actions, styles Styles, info MenuInfo, w, h int) runOnceModel {
	ti := textinput.New()
	ti.Placeholder = exampleID(info.Prefix)
	ti.CharLimit = 64
	ti.Width = 32
	ti.Prompt = "› "
	ti.Focus()

	return runOnceModel{
		styles:   styles,
		actions:  actions,
		ctx:      ctx,
		width:    w,
		height:   h,
		info:     info,
		step:     runOnceConfirm,
		input:    ti,
		eligible: nil,
	}
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

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.step == runOnceConfirm {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m runOnceModel) handleKey(msg tea.KeyMsg) (runOnceModel, tea.Cmd) {
	switch m.step {
	case runOnceConfirm:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled, m.done = true, true
			return m, nil
		case tea.KeyEnter:
			m.badID = false
			raw := strings.TrimSpace(m.input.Value())
			id := extractTicketID(raw, m.info.Prefix)
			if id == "" {
				m.badID = true
				return m, nil
			}
			m.selected = id
			m.done = true
			return m, nil
		}
		switch msg.String() {
		case "l":
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
		if msg.Type == tea.KeyCtrlC || msg.Type == tea.KeyEsc {
			m.step = runOnceConfirm
			m.input.Focus()
		}
		return m, nil

	case runOnceList:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.cancelled, m.done = true, true
		case tea.KeyEsc:
			m.step = runOnceConfirm
			m.eligible = nil
			m.cursor = 0
			m.input.Focus()
		case tea.KeyEnter:
			if m.cursor >= 0 && m.cursor < len(m.eligible) {
				m.selected = m.eligible[m.cursor].ID
				m.done = true
			}
		case tea.KeyUp, tea.KeyShiftTab:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown, tea.KeyTab:
			if m.cursor < len(m.eligible)-1 {
				m.cursor++
			}
		}
		switch msg.String() {
		case "o":
			if m.cursor >= 0 && m.cursor < len(m.eligible) {
				return m, openURLCmd(linearIssueURL(m.eligible[m.cursor].ID))
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
	rows := []string{
		s.Subtle.Render("Run a single ticket. Type an ID or load the ready queue:"),
		"",
		s.Subtle.Render("Issue ") + m.input.View(),
		s.Help.Render("type ID · 'l' load eligible tickets · enter run · esc back"),
	}
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
	if len(m.eligible) == 0 {
		return s.Subtle.Render("No eligible tickets right now.") + "\n\n" +
			s.Help.Render("esc back · 'r' refresh")
	}

	var rows []string
	rows = append(rows, s.Subtle.Render(fmt.Sprintf("Eligible tickets (%d):", len(m.eligible))))
	rows = append(rows, "")

	idW, titleW := m.listColumnWidths()
	for i, t := range m.eligible {
		marker := "  "
		idStyle := s.Subtle
		titleStyle := s.Subtle
		stateStyle := s.Help
		if i == m.cursor {
			marker = s.Info.Render("▸ ")
			idStyle = s.Header
			titleStyle = lipgloss.NewStyle().Foreground(colorBrand)
		}
		idStr := padRight(t.ID, idW)
		titleStr := truncate(t.Title, titleW)
		stateStr := truncate(firstNonEmpty(t.State, "—"), 10)
		rows = append(rows, marker+idStyle.Render(idStr)+"  "+titleStyle.Render(titleStr)+"  "+stateStyle.Render(stateStr))
	}
	return strings.Join(rows, "\n")
}

func (m runOnceModel) listColumnWidths() (idW, titleW int) {
	const gap = 8 // marker + padding + state column
	for _, t := range m.eligible {
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

func (m runOnceModel) summary() string {
	s := m.styles
	info := m.info

	agent := firstNonEmpty(info.Provider, "?")
	if info.Model != "" {
		agent += " · " + info.Model
	}
	parts := []string{agent}
	if info.Base != "" {
		parts = append(parts, "base "+info.Base)
	}
	return s.Help.Render(strings.Join(parts, " · ")) + "\n" +
		s.Help.Render(fmt.Sprintf("%d in-flight · %d done", info.InFlight, info.Done))
}

func (m runOnceModel) hint() string {
	switch m.step {
	case runOnceLoading:
		return "loading… · esc cancel"
	case runOnceList:
		return "↑↓ move · enter run selected · 'o' open · 'r' refresh · esc back"
	default:
		return "type an ID · 'l' load eligible · enter run · esc back"
	}
}
