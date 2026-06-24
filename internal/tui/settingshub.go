package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// settingsHubModel is the Settings landing screen. It routes to three views:
// General (non-provider config), Providers (the execution-tuning panel), and
// All settings (the raw flat editor as an escape hatch).
type settingsHubModel struct {
	styles  Styles
	actions SettingsActions
	width   int
	height  int

	step   hubStep
	cursor int
	items  []hubItem

	general   settingsModel
	all       settingsModel
	providers providerSettingsModel
}

type hubStep int

const (
	hubMenu hubStep = iota
	hubGeneral
	hubProviders
	hubAll
)

type hubItem struct {
	title string
	desc  string
	step  hubStep
}

func newSettingsHubModel(actions SettingsActions, styles Styles, width, height int) settingsHubModel {
	return settingsHubModel{
		styles:  styles,
		actions: actions,
		width:   width,
		height:  height,
		step:    hubMenu,
		items: []hubItem{
			{"General", "trackers · branch · CI · merge", hubGeneral},
			{"Providers", "model · reasoning effort · per-phase", hubProviders},
			{"All settings", "every key, raw", hubAll},
		},
	}
}

func (m settingsHubModel) Init() tea.Cmd { return nil }

// AtRoot reports whether the hub is on its landing menu, so the app shell can
// treat esc/q as "back to the More menu".
func (m settingsHubModel) AtRoot() bool { return m.step == hubMenu }

func (m settingsHubModel) Update(msg tea.Msg) (settingsHubModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.general, _ = m.general.Update(msg)
		m.all, _ = m.all.Update(msg)
		m.providers, _ = m.providers.Update(msg)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	switch m.step {
	case hubGeneral:
		m.general, cmd = m.general.Update(msg)
	case hubAll:
		m.all, cmd = m.all.Update(msg)
	case hubProviders:
		m.providers, cmd = m.providers.Update(msg)
	}
	return m, cmd
}

func (m settingsHubModel) handleKey(msg tea.KeyMsg) (settingsHubModel, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.step {
	case hubMenu:
		switch {
		case msg.Type == tea.KeyEsc, msg.String() == "q":
			return m, nil // handled by the app as back
		case msg.Type == tea.KeyUp, msg.String() == "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case msg.Type == tea.KeyDown, msg.String() == "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case msg.Type == tea.KeyEnter:
			return m.openSection(m.items[m.cursor].step)
		}
		return m, nil

	case hubGeneral:
		if m.general.InList() && (msg.Type == tea.KeyEsc || msg.String() == "q") {
			m.step = hubMenu
			return m, nil
		}
		var cmd tea.Cmd
		m.general, cmd = m.general.Update(msg)
		return m, cmd

	case hubAll:
		if m.all.InList() && (msg.Type == tea.KeyEsc || msg.String() == "q") {
			m.step = hubMenu
			return m, nil
		}
		var cmd tea.Cmd
		m.all, cmd = m.all.Update(msg)
		return m, cmd

	case hubProviders:
		if m.providers.AtRoot() && (msg.Type == tea.KeyEsc || msg.String() == "q") {
			m.step = hubMenu
			return m, nil
		}
		var cmd tea.Cmd
		m.providers, cmd = m.providers.Update(msg)
		return m, cmd
	}
	return m, nil
}

// openSection builds the chosen sub-view fresh so it reflects the latest config,
// then switches to it.
func (m settingsHubModel) openSection(step hubStep) (settingsHubModel, tea.Cmd) {
	switch step {
	case hubGeneral:
		m.general = newSettingsModelCategory(m.actions, m.styles, m.width, m.height, categoryGeneral, "General settings")
	case hubAll:
		m.all = newSettingsModelCategory(m.actions, m.styles, m.width, m.height, categoryAll, "All settings")
	case hubProviders:
		m.providers = newProviderSettingsModel(m.actions, m.styles, m.width, m.height)
	}
	m.step = step
	return m, nil
}

func (m settingsHubModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}
	switch m.step {
	case hubGeneral:
		return m.general.View()
	case hubAll:
		return m.all.View()
	case hubProviders:
		return m.providers.View()
	default:
		return m.renderMenu()
	}
}

func (m settingsHubModel) renderMenu() string {
	s := m.styles
	rows := []string{
		s.SummaryTitle.Render("Settings"),
		"",
		s.Subtle.Render("Choose what to configure."),
		"",
	}
	for i, it := range m.items {
		marker := "  "
		titleStyle := s.Subtle
		descStyle := s.Help
		if i == m.cursor {
			marker = s.Info.Render("▸ ")
			titleStyle = s.Header
			descStyle = s.Subtle
		}
		rows = append(rows, marker+titleStyle.Render(padRight(it.title, 14))+"  "+descStyle.Render(it.desc))
	}
	body := strings.Join(rows, "\n")
	card := s.SummaryCard.Render(body)
	hint := s.Help.Render("↑↓ move · enter select · esc/q back")
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, card, hint))
}
