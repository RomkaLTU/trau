package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ConfigItem is one resolved configuration key exposed to the settings editor.
// It mirrors config.ConfigItem but keeps the tui package decoupled from the
// config package internals.
type ConfigItem struct {
	Key      string
	Value    string
	Layer    string
	Advanced bool
	// Options enumerates the allowed values; when non-empty the editor offers
	// a picker instead of a free-text field.
	Options []string
	// Bool marks a 1/0 toggle key, rendered as an on/off switch.
	Bool bool
	// Description and Default explain the key and its fallback value.
	Description string
	Default     string
}

// ProviderTuningField is one resolved value plus the layer that supplied it.
// It mirrors config.ProviderTuningField.
type ProviderTuningField struct {
	Value string
	Layer string
}

// ProviderPhaseTuning is one phase's model/effort for a provider: the raw
// per-phase override (empty Value = inherit) plus the effective value that runs.
type ProviderPhaseTuning struct {
	Phase     string
	Model     ProviderTuningField
	Effort    ProviderTuningField
	EffModel  string
	EffEffort string
}

// ProviderTuning is the execution-tuning picture for one provider, consumed by
// the provider settings panel. It mirrors config.ProviderTuning.
type ProviderTuning struct {
	Name    string
	Active  bool
	Models  []string
	Efforts []string
	Model   ProviderTuningField
	Effort  ProviderTuningField
	Phases  []ProviderPhaseTuning
}

// SettingsActions is the narrow seam the settings editor needs from the
// backend. The concrete implementation lives in cmd/trau/main.go.
type SettingsActions interface {
	// ConfigItems returns every known config key with its effective value and
	// the layer that supplied it.
	ConfigItems() []ConfigItem

	// SaveConfigItem persists value for key to the named write-back layer.
	// layer is one of the strings returned by ConfigLayers.
	SaveConfigItem(key, value, layer string) error

	// ConfigLayers returns the writable layer names offered by the editor,
	// ordered from lowest to highest precedence.
	ConfigLayers() []string

	// ProviderTunings returns per-provider execution tuning (model/effort and
	// per-phase overrides) for the provider settings panel.
	ProviderTunings() []ProviderTuning
}

type settingsStep int

const (
	settingsList settingsStep = iota
	settingsEdit
	settingsSaving
)

// editKind selects which input widget the edit screen presents for a key.
type editKind int

const (
	editText   editKind = iota // free-text field
	editSelect                 // pick one of a fixed set of values
	editBool                   // on/off toggle stored as 1/0
)

type settingsModel struct {
	styles Styles

	actions  SettingsActions
	width    int
	height   int
	items    []ConfigItem
	filtered []ConfigItem
	cursor   int

	title string

	showAdvanced bool
	step         settingsStep

	editInput        textinput.Model
	editValueFocused bool
	editLayer        int
	editLayers       []string
	editKey          string

	editKind      editKind
	editOptions   []string // option values to save (e.g. "1"/"0" for bools)
	editOptLabels []string // option display labels (e.g. "on"/"off")
	editOptIdx    int

	saveErr  error
	savedMsg string
}

func newSettingsModel(actions SettingsActions, styles Styles, width, height int) settingsModel {
	ti := textinput.New()
	ti.Placeholder = "value"
	ti.CharLimit = 512
	ti.SetWidth(40)
	ti.Prompt = "Value: "

	m := settingsModel{
		styles:     styles,
		actions:    actions,
		width:      width,
		height:     height,
		items:      actions.ConfigItems(),
		title:      "Settings",
		editInput:  ti,
		editLayers: actions.ConfigLayers(),
	}
	m.rebuildFiltered()
	return m
}

func (m settingsModel) Init() tea.Cmd { return nil }

func (m settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case saveConfigDoneMsg:
		m.step = settingsList
		if msg.err != nil {
			m.saveErr = msg.err
		} else {
			m.savedMsg = msg.key + " saved to " + msg.layer
			m.items = m.actions.ConfigItems()
			m.rebuildFiltered()
		}
		return m, nil
	}

	if m.step == settingsEdit && m.editValueFocused && m.editKind == editText {
		var cmd tea.Cmd
		m.editInput, cmd = m.editInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m settingsModel) handleKey(msg tea.KeyPressMsg) (settingsModel, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}

	switch m.step {
	case settingsList:
		return m.handleListKey(msg)
	case settingsEdit:
		return m.handleEditKey(msg)
	case settingsSaving:
		return m, nil
	}
	return m, nil
}

func (m settingsModel) handleListKey(msg tea.KeyPressMsg) (settingsModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return m, nil // handled by app as back
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
	case "a":
		m.showAdvanced = !m.showAdvanced
		m.rebuildFiltered()
	case "enter", "e":
		if len(m.filtered) == 0 {
			return m, nil
		}
		return m.enterEdit()
	}
	return m, nil
}

func (m settingsModel) handleEditKey(msg tea.KeyPressMsg) (settingsModel, tea.Cmd) {
	key := msg.String()
	textFocused := m.editValueFocused && m.editKind == editText
	switch {
	case key == "esc", key == "q" && !textFocused:
		m.step = settingsList
		m.saveErr = nil
		m.savedMsg = ""
		return m, nil
	case key == "tab":
		m.editValueFocused = !m.editValueFocused
		if m.editValueFocused && m.editKind == editText {
			m.editInput.Focus()
		} else {
			m.editInput.Blur()
		}
		return m, nil
	case m.editValueFocused && m.editKind != editText && (key == "left" || key == "h"):
		if m.editOptIdx > 0 {
			m.editOptIdx--
		}
		return m, nil
	case m.editValueFocused && m.editKind != editText && (key == "right" || key == "l"):
		if m.editOptIdx < len(m.editOptions)-1 {
			m.editOptIdx++
		}
		return m, nil
	case m.editValueFocused && m.editKind == editBool && key == "space":
		m.editOptIdx = 1 - m.editOptIdx
		return m, nil
	case !m.editValueFocused && (key == "left" || key == "h"):
		if m.editLayer > 0 {
			m.editLayer--
		}
	case !m.editValueFocused && (key == "right" || key == "l"):
		if m.editLayer < len(m.editLayers)-1 {
			m.editLayer++
		}
	case key == "up", key == "k":
		if !m.editValueFocused {
			m.editValueFocused = true
			if m.editKind == editText {
				m.editInput.Focus()
			}
		}
	case key == "down", key == "j":
		if m.editValueFocused {
			m.editValueFocused = false
			m.editInput.Blur()
		}
	case key == "enter":
		return m.saveEdit()
	}

	if m.editValueFocused && m.editKind == editText {
		var cmd tea.Cmd
		m.editInput, cmd = m.editInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m settingsModel) enterEdit() (settingsModel, tea.Cmd) {
	item := m.filtered[m.cursor]
	m.editKey = item.Key
	m.editLayer = m.defaultLayer(item.Layer)
	m.editValueFocused = true
	m.saveErr = nil
	m.savedMsg = ""
	m.step = settingsEdit

	switch {
	case item.Bool:
		m.editKind = editBool
		m.editOptions = []string{"1", "0"}
		m.editOptLabels = []string{"on", "off"}
		m.editOptIdx = optionIndex(m.editOptions, item.Value)
		m.editInput.Blur()
		return m, nil
	case len(item.Options) > 0:
		m.editKind = editSelect
		m.editOptions = item.Options
		m.editOptLabels = item.Options
		m.editOptIdx = optionIndex(m.editOptions, item.Value)
		m.editInput.Blur()
		return m, nil
	default:
		m.editKind = editText
		m.editInput.SetValue(item.Value)
		m.editInput.Focus()
		return m, textinput.Blink
	}
}

// optionIndex returns the index of value in opts, or 0 when not found so the
// picker always starts on a valid option.
func optionIndex(opts []string, value string) int {
	for i, o := range opts {
		if o == value {
			return i
		}
	}
	return 0
}

func (m settingsModel) defaultLayer(layer string) int {
	for i, l := range m.editLayers {
		if l == layer {
			return i
		}
	}
	for i, l := range m.editLayers {
		if l == "project" {
			return i
		}
	}
	return 0
}

func (m settingsModel) saveEdit() (settingsModel, tea.Cmd) {
	var value string
	if m.editKind == editText {
		value = strings.TrimSpace(m.editInput.Value())
	} else if m.editOptIdx >= 0 && m.editOptIdx < len(m.editOptions) {
		value = m.editOptions[m.editOptIdx]
	}
	layer := m.editLayers[m.editLayer]
	m.step = settingsSaving
	return m, m.saveCmd(m.editKey, value, layer)
}

func (m settingsModel) saveCmd(key, value, layer string) tea.Cmd {
	actions := m.actions
	return func() tea.Msg {
		err := actions.SaveConfigItem(key, value, layer)
		return saveConfigDoneMsg{key: key, value: value, layer: layer, err: err}
	}
}

type saveConfigDoneMsg struct {
	key   string
	value string
	layer string
	err   error
}

func (m *settingsModel) rebuildFiltered() {
	m.filtered = m.filtered[:0]
	for _, it := range m.items {
		if it.Advanced && !m.showAdvanced {
			continue
		}
		m.filtered = append(m.filtered, it)
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m settingsModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}
	switch m.step {
	case settingsEdit:
		return m.renderEdit()
	case settingsSaving:
		return m.renderSaving()
	default:
		return m.renderList()
	}
}

func (m settingsModel) renderList() string {
	s := m.styles
	title := m.title
	if title == "" {
		title = "Settings"
	}
	header := []string{
		s.SummaryTitle.Render(title),
		"",
		s.Subtle.Render("Effective config values and the layer that supplies each one."),
		"",
	}
	if m.saveErr != nil {
		header = append(header, s.Error.Render("Error: "+m.saveErr.Error()), "")
	} else if m.savedMsg != "" {
		header = append(header, s.Success.Render("✓ "+m.savedMsg), "")
	}

	keyW, layerW := m.listColumnWidths()
	list := make([]string, 0, len(m.filtered))
	for i, it := range m.filtered {
		focused := i == m.cursor
		keyStyle := s.Subtle
		valStyle := lipgloss.NewStyle()
		layerStyle := s.Help
		if focused {
			keyStyle = s.Header
			valStyle = lipgloss.NewStyle().Foreground(theme.Brand)
			layerStyle = s.Subtle
		}
		row := cursorMarker(s, focused) +
			keyStyle.Render(padRight(it.Key, keyW)) + "  " +
			valStyle.Render(truncate(displayValue(it, it.Value), layerW*2)) + "  " +
			layerStyle.Render("("+it.Layer+")")
		list = append(list, row)
	}

	// The focused key's description is a fixed footer so it stays visible however
	// far the list scrolls.
	var footer []string
	if len(m.filtered) == 0 {
		footer = append(footer, s.Subtle.Render("No settings to show."))
	} else if m.cursor < len(m.filtered) {
		if d := m.filtered[m.cursor].Description; d != "" {
			footer = append(footer, "", s.Help.Render(d))
		}
	}

	// Scroll the list to follow the cursor; the header and description stay put.
	listBudget := cardBodyBudget(m.height, 0) - len(header) - len(footer)
	list = scrollToCursor(list, m.cursor, listBudget)

	body := make([]string, 0, len(header)+len(list)+len(footer))
	body = append(body, header...)
	body = append(body, list...)
	body = append(body, footer...)

	hint := "↑↓ move · enter/e edit · a toggle advanced · esc/q back"
	return m.renderCard(strings.Join(body, "\n"), hint)
}

func (m settingsModel) renderEdit() string {
	s := m.styles
	item := m.filtered[m.cursor]

	valueView := m.editInput.View()
	if m.editKind != editText {
		valueView = radioRow(s, m.editOptLabels, m.editOptIdx)
	}

	rows := []string{
		s.SummaryTitle.Render("Edit " + item.Key),
		"",
	}
	if item.Description != "" {
		rows = append(rows, s.Subtle.Render(item.Description), "")
	}
	valueLine := len(rows)
	rows = append(rows, cursorMarker(s, m.editValueFocused)+valueView)
	if item.Default != "" {
		rows = append(rows, "  "+s.Help.Render("default: "+displayValue(item, item.Default)))
	}
	layerLine := len(rows) + 1 // the "Write to layer:" line, after the blank
	rows = append(rows,
		"",
		cursorMarker(s, !m.editValueFocused)+s.Subtle.Render("Write to layer:"),
		"  "+radioRow(s, m.editLayers, m.editLayer),
		"  "+s.Help.Render(layerHint(m.editLayers[m.editLayer])),
	)
	if item.Advanced {
		rows = append(rows, "", s.Warning.Render("⚠ Advanced setting — edit with care."))
	}

	focusLine := layerLine
	if m.editValueFocused {
		focusLine = valueLine
	}
	rows = scrollToCursor(rows, focusLine, cardBodyBudget(m.height, 0))
	return m.renderCard(strings.Join(rows, "\n"), m.editHint())
}

// displayValue formats a raw config value for humans: booleans become on/off
// and an empty value reads as a dash rather than blank space.
func displayValue(item ConfigItem, val string) string {
	if item.Bool {
		return boolLabel(val)
	}
	if val == "" {
		return "—"
	}
	return val
}

func boolLabel(v string) string {
	switch v {
	case "1":
		return "on"
	case "0":
		return "off"
	default:
		return v
	}
}

// layerHint describes where the named write layer persists values.
func layerHint(layer string) string {
	switch layer {
	case "project":
		return "→ <repo>/.trau.ini — shared with the repo"
	case "user":
		return "→ ~/.trau.ini — personal, all your projects"
	case "local":
		return "→ ./trau.ini — current directory only"
	default:
		return ""
	}
}

func (m settingsModel) editHint() string {
	switch m.editKind {
	case editBool:
		return "tab/↑↓ switch focus · ←→/space toggle · enter save · esc/q cancel"
	case editSelect:
		return "tab/↑↓ switch focus · ←→/hl change value & layer · enter save · esc/q cancel"
	default:
		if m.editValueFocused {
			return "tab/↑↓ switch focus · ←→/hl pick layer · enter save · esc cancel"
		}
		return "tab/↑↓ switch focus · ←→/hl pick layer · enter save · esc/q cancel"
	}
}

func (m settingsModel) renderSaving() string {
	return m.renderCard(
		m.styles.SummaryTitle.Render("Saving…")+"\n\n"+m.styles.Subtle.Render(m.editKey),
		"working…",
	)
}

func (m settingsModel) renderCard(body, hint string) string {
	return cardView(m.styles, m.width, m.height, body, hint)
}

func (m settingsModel) listColumnWidths() (keyW, layerW int) {
	for _, it := range m.filtered {
		if w := lipgloss.Width(it.Key); w > keyW {
			keyW = w
		}
		if w := lipgloss.Width(it.Layer); w > layerW {
			layerW = w
		}
	}
	if keyW < 12 {
		keyW = 12
	}
	if layerW < 8 {
		layerW = 8
	}
	return keyW, layerW
}

// InList reports whether the editor is in the settings list (not editing),
// so the app shell can interpret esc/q as "back to menu".
func (m settingsModel) InList() bool { return m.step == settingsList }
