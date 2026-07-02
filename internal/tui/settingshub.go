package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// settingsHubModel is the Settings landing screen. It routes to two views:
// Providers (the curated execution-tuning panel) and All settings (the raw flat
// editor of every key, as an escape hatch).
type settingsHubModel struct {
	styles  Styles
	actions SettingsActions
	width   int
	height  int

	step   hubStep
	cursor int
	items  []hubItem

	all       settingsModel
	providers providerSettingsModel
}

type hubStep int

const (
	hubMenu hubStep = iota
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
		m.all, _ = m.all.Update(msg)
		m.providers, _ = m.providers.Update(msg)
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	switch m.step {
	case hubAll:
		m.all, cmd = m.all.Update(msg)
	case hubProviders:
		m.providers, cmd = m.providers.Update(msg)
	}
	return m, cmd
}

func (m settingsHubModel) handleKey(msg tea.KeyPressMsg) (settingsHubModel, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.step {
	case hubMenu:
		switch msg.String() {
		case "esc", "q":
			return m, nil // handled by the app as back
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			return m.openSection(m.items[m.cursor].step)
		}
		return m, nil

	case hubAll:
		if m.all.InList() && (msg.String() == "esc" || msg.String() == "q") {
			m.step = hubMenu
			return m, nil
		}
		var cmd tea.Cmd
		m.all, cmd = m.all.Update(msg)
		return m, cmd

	case hubProviders:
		if m.providers.AtRoot() && (msg.String() == "esc" || msg.String() == "q") {
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
	case hubAll:
		m.all = newSettingsModel(m.actions, m.styles, m.width, m.height)
		m.all.title = "All settings"
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
