package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// providerSettingsModel is the execution-tuning panel: a per-provider view of
// the model and reasoning-effort dials, plus per-phase overrides. Provider tabs
// switch with ←→; every value is a provider-aware picker so the editor never
// accepts an effort the CLI would reject. There are no speed presets — tuning is
// the two independent dials, model and effort, optionally overridden per phase.
type providerSettingsModel struct {
	styles  Styles
	actions SettingsActions
	width   int
	height  int

	providers []ProviderTuning
	tab       int // index into providers

	rows   []provRow
	cursor int

	layers   []string
	layerIdx int

	step provStep
	edit provEditor

	saveErr  error
	savedMsg string
}

type provStep int

const (
	provBrowse provStep = iota
	provEdit
	provSaving
)

// provRowKind tags a browse row as a provider default dial or a per-phase row.
type provRowKind int

const (
	rowModel provRowKind = iota
	rowEffort
	rowPhase
)

type provRow struct {
	kind     provRowKind
	phaseIdx int // valid when kind == rowPhase
}

// provPicker is one option dial in the editor, bound to a config key.
type provPicker struct {
	label  string
	key    string
	values []string // values to save (e.g. "" for inherit)
	labels []string // display labels
	idx    int
}

// provEditor edits one browse row: one or two value pickers plus the write
// layer. focus walks the pickers then the layer control.
type provEditor struct {
	title   string
	rowDesc string
	pickers []provPicker
	focus   int // 0..len(pickers)-1 = picker; len(pickers) = layer
}

type provSaveDoneMsg struct {
	err     error
	summary string
}

func newProviderSettingsModel(actions SettingsActions, styles Styles, width, height int) providerSettingsModel {
	m := providerSettingsModel{
		styles:    styles,
		actions:   actions,
		width:     width,
		height:    height,
		providers: actions.ProviderTunings(),
		layers:    actions.ConfigLayers(),
	}
	m.layerIdx = defaultLayerIndex(m.layers)
	m.tab = m.activeTab()
	m.rebuildRows()
	return m
}

// activeTab returns the index of the loop's active provider so the panel opens
// on the provider that actually runs.
func (m providerSettingsModel) activeTab() int {
	for i, p := range m.providers {
		if p.Active {
			return i
		}
	}
	return 0
}

func defaultLayerIndex(layers []string) int {
	for i, l := range layers {
		if l == "project" {
			return i
		}
	}
	return 0
}

func (m *providerSettingsModel) rebuildRows() {
	m.rows = m.rows[:0]
	m.rows = append(m.rows, provRow{kind: rowModel}, provRow{kind: rowEffort})
	if m.tab < len(m.providers) {
		for i := range m.providers[m.tab].Phases {
			m.rows = append(m.rows, provRow{kind: rowPhase, phaseIdx: i})
		}
	}
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
}

func (m providerSettingsModel) Init() tea.Cmd { return nil }

// AtRoot reports whether the panel is in its browse view, so the hub can treat
// esc as "back to the settings hub" rather than "cancel edit".
func (m providerSettingsModel) AtRoot() bool { return m.step == provBrowse }

func (m providerSettingsModel) Update(msg tea.Msg) (providerSettingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case provSaveDoneMsg:
		m.step = provBrowse
		if msg.err != nil {
			m.saveErr = msg.err
		} else {
			m.saveErr = nil
			m.savedMsg = msg.summary
			m.providers = m.actions.ProviderTunings()
			m.rebuildRows()
		}
		return m, nil
	}
	return m, nil
}

func (m providerSettingsModel) handleKey(msg tea.KeyMsg) (providerSettingsModel, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.step {
	case provBrowse:
		return m.handleBrowseKey(msg)
	case provEdit:
		return m.handleEditKey(msg)
	}
	return m, nil
}

func (m providerSettingsModel) handleBrowseKey(msg tea.KeyMsg) (providerSettingsModel, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc, msg.String() == "q":
		return m, nil // handled by the hub as back
	case msg.Type == tea.KeyUp, msg.String() == "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case msg.Type == tea.KeyDown, msg.String() == "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case msg.Type == tea.KeyLeft, msg.String() == "h":
		if m.tab > 0 {
			m.tab--
			m.cursor = 0
			m.savedMsg, m.saveErr = "", nil
			m.rebuildRows()
		}
	case msg.Type == tea.KeyRight, msg.String() == "l":
		if m.tab < len(m.providers)-1 {
			m.tab++
			m.cursor = 0
			m.savedMsg, m.saveErr = "", nil
			m.rebuildRows()
		}
	case msg.Type == tea.KeyEnter, msg.String() == "e":
		return m.enterEdit()
	}
	return m, nil
}

func (m providerSettingsModel) enterEdit() (providerSettingsModel, tea.Cmd) {
	if m.tab >= len(m.providers) || m.cursor >= len(m.rows) {
		return m, nil
	}
	p := m.providers[m.tab]
	row := m.rows[m.cursor]
	prefix := strings.ToUpper(p.Name) + "_"

	modelVals, modelLabels := optionsWith(p.Models, "(default)")
	effortVals, effortLabels := optionsWith(p.Efforts, "(default)")
	phModelVals, phModelLabels := optionsWith(p.Models, "(inherit)")
	phEffortVals, phEffortLabels := optionsWith(p.Efforts, "(inherit)")

	m.saveErr = nil
	m.savedMsg = ""

	switch row.kind {
	case rowModel:
		m.edit = provEditor{
			title:   "Default model — " + p.Name,
			rowDesc: "The model used when a phase has no per-phase override.",
			pickers: []provPicker{{
				label:  "Model",
				key:    prefix + "MODEL",
				values: modelVals,
				labels: modelLabels,
				idx:    optionIndex(modelVals, p.Model.Value),
			}},
		}
	case rowEffort:
		m.edit = provEditor{
			title:   "Default reasoning effort — " + p.Name,
			rowDesc: effortDesc(p.Name),
			pickers: []provPicker{{
				label:  "Effort",
				key:    prefix + "EFFORT",
				values: effortVals,
				labels: effortLabels,
				idx:    optionIndex(effortVals, p.Effort.Value),
			}},
		}
	case rowPhase:
		ph := p.Phases[row.phaseIdx]
		pp := strings.ToUpper(ph.Phase)
		m.edit = provEditor{
			title:   ph.Phase + " phase — " + p.Name,
			rowDesc: "Override model and/or effort for the " + ph.Phase + " phase. (inherit) uses the provider default.",
			pickers: []provPicker{
				{
					label:  "Model",
					key:    prefix + pp + "_MODEL",
					values: phModelVals,
					labels: phModelLabels,
					idx:    optionIndex(phModelVals, ph.Model.Value),
				},
				{
					label:  "Effort",
					key:    prefix + pp + "_EFFORT",
					values: phEffortVals,
					labels: phEffortLabels,
					idx:    optionIndex(phEffortVals, ph.Effort.Value),
				},
			},
		}
	}
	m.edit.focus = 0
	m.step = provEdit
	return m, nil
}

func (m providerSettingsModel) handleEditKey(msg tea.KeyMsg) (providerSettingsModel, tea.Cmd) {
	layerFocus := len(m.edit.pickers)
	switch {
	case msg.Type == tea.KeyEsc:
		m.step = provBrowse
		return m, nil
	case msg.Type == tea.KeyUp, msg.String() == "k":
		if m.edit.focus > 0 {
			m.edit.focus--
		}
	case msg.Type == tea.KeyDown, msg.String() == "j", msg.Type == tea.KeyTab:
		if m.edit.focus < layerFocus {
			m.edit.focus++
		}
	case msg.Type == tea.KeyLeft, msg.String() == "h":
		if m.edit.focus == layerFocus {
			if m.layerIdx > 0 {
				m.layerIdx--
			}
		} else {
			p := &m.edit.pickers[m.edit.focus]
			if p.idx > 0 {
				p.idx--
			}
		}
	case msg.Type == tea.KeyRight, msg.String() == "l":
		if m.edit.focus == layerFocus {
			if m.layerIdx < len(m.layers)-1 {
				m.layerIdx++
			}
		} else {
			p := &m.edit.pickers[m.edit.focus]
			if p.idx < len(p.values)-1 {
				p.idx++
			}
		}
	case msg.Type == tea.KeyEnter:
		return m.saveEdit()
	}
	return m, nil
}

func (m providerSettingsModel) saveEdit() (providerSettingsModel, tea.Cmd) {
	layer := m.layers[m.layerIdx]
	pairs := make([]provKV, 0, len(m.edit.pickers))
	for _, p := range m.edit.pickers {
		pairs = append(pairs, provKV{key: p.key, value: p.values[p.idx]})
	}
	m.step = provSaving
	return m, m.saveCmd(pairs, layer)
}

type provKV struct{ key, value string }

func (m providerSettingsModel) saveCmd(pairs []provKV, layer string) tea.Cmd {
	actions := m.actions
	keys := make([]string, 0, len(pairs))
	for _, p := range pairs {
		keys = append(keys, p.key)
	}
	return func() tea.Msg {
		for _, p := range pairs {
			if err := actions.SaveConfigItem(p.key, p.value, layer); err != nil {
				return provSaveDoneMsg{err: err}
			}
		}
		return provSaveDoneMsg{summary: strings.Join(keys, ", ") + " saved to " + layer}
	}
}

// optionsWith prefixes an inherit/default sentinel ("" value) onto a provider's
// option list so a picker can express "no override".
func optionsWith(opts []string, sentinel string) (values, labels []string) {
	values = make([]string, 0, len(opts)+1)
	labels = make([]string, 0, len(opts)+1)
	values = append(values, "")
	labels = append(labels, sentinel)
	values = append(values, opts...)
	labels = append(labels, opts...)
	return values, labels
}

func effortDesc(provider string) string {
	switch provider {
	case "claude":
		return "Claude --effort: low · medium · high · xhigh · max."
	case "codex":
		return "Codex model_reasoning_effort: minimal · low · medium · high · xhigh."
	case "kimi":
		return "Kimi KIMI_MODEL_THINKING_EFFORT: low · medium · high · xhigh · max."
	}
	return ""
}

func (m providerSettingsModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}
	if m.step == provEdit || m.step == provSaving {
		return m.renderEdit()
	}
	return m.renderBrowse()
}

func (m providerSettingsModel) renderBrowse() string {
	s := m.styles
	rows := []string{
		s.SummaryTitle.Render("Provider tuning"),
		"",
		m.renderTabs(),
		"",
	}
	if m.saveErr != nil {
		rows = append(rows, s.Error.Render("Error: "+m.saveErr.Error()), "")
	} else if m.savedMsg != "" {
		rows = append(rows, s.Success.Render("✓ "+m.savedMsg), "")
	}

	if m.tab >= len(m.providers) {
		return m.renderCard(strings.Join(rows, "\n"), "esc back")
	}
	p := m.providers[m.tab]

	rows = append(rows, s.Subtle.Render("Defaults"))
	rows = append(rows,
		m.renderValueRow(m.isCursor(rowDefaultsModelRow), "Model", fieldDisplay(p.Model, "(provider default)")),
		m.renderValueRow(m.isCursor(rowDefaultsEffortRow), "Reasoning", fieldDisplay(p.Effort, "(provider default)")),
		"",
		s.Subtle.Render("Per-phase overrides"),
	)
	for i, ph := range p.Phases {
		rowIdx := 2 + i
		rows = append(rows, m.renderPhaseRow(m.cursor == rowIdx, ph))
	}

	rows = append(rows,
		"",
		s.Help.Render("Effective: "+m.effectiveRoutes(p)),
	)
	if d := m.cursorDesc(p); d != "" {
		rows = append(rows, "", s.Help.Render(d))
	}

	hint := "↑↓ move · ←→ switch provider · enter edit · esc back"
	return m.renderCard(strings.Join(rows, "\n"), hint)
}

const (
	rowDefaultsModelRow  = 0
	rowDefaultsEffortRow = 1
)

func (m providerSettingsModel) isCursor(idx int) bool { return m.cursor == idx }

func (m providerSettingsModel) renderTabs() string {
	s := m.styles
	var parts []string
	for i, p := range m.providers {
		label := p.Name
		if p.Active {
			label = "● " + label
		}
		if i == m.tab {
			parts = append(parts, s.Header.Render("["+label+"]"))
		} else {
			parts = append(parts, s.Help.Render(" "+label+" "))
		}
	}
	return strings.Join(parts, "  ")
}

func (m providerSettingsModel) renderValueRow(focused bool, label, value string) string {
	s := m.styles
	marker := "  "
	labelStyle := s.Subtle
	valStyle := lipgloss.NewStyle()
	if focused {
		marker = s.Info.Render("▸ ")
		labelStyle = s.Header
		valStyle = lipgloss.NewStyle().Foreground(colorBrand)
	}
	return marker + labelStyle.Render(padRight(label, 12)) + "  " + valStyle.Render(value)
}

func (m providerSettingsModel) renderPhaseRow(focused bool, ph ProviderPhaseTuning) string {
	model := ph.EffModel
	if model == "" {
		model = "(default)"
	}
	effort := ph.EffEffort
	if effort == "" {
		effort = "(default)"
	}
	value := model + " @ " + effort
	overridden := ph.Model.Value != "" || ph.Effort.Value != ""
	if overridden {
		value += "  " + m.styles.Info.Render("•")
	}
	return m.renderValueRow(focused, ph.Phase, value)
}

func (m providerSettingsModel) cursorDesc(p ProviderTuning) string {
	if m.cursor >= len(m.rows) {
		return ""
	}
	switch m.rows[m.cursor].kind {
	case rowEffort:
		return effortDesc(p.Name)
	case rowModel:
		return "Default model for " + p.Name + " when a phase sets no override."
	case rowPhase:
		return "• marks a phase with an explicit override. Enter to edit."
	}
	return ""
}

// effectiveRoutes renders the model@effort each phase will run under, the same
// resolution the loop applies, so the precedence is legible at a glance.
func (m providerSettingsModel) effectiveRoutes(p ProviderTuning) string {
	parts := make([]string, 0, len(p.Phases))
	for _, ph := range p.Phases {
		model := ph.EffModel
		if model == "" {
			model = "default"
		}
		seg := ph.Phase + "→" + model
		if ph.EffEffort != "" {
			seg += "@" + ph.EffEffort
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, " · ")
}

func (m providerSettingsModel) renderEdit() string {
	s := m.styles
	rows := []string{
		s.SummaryTitle.Render(m.edit.title),
		"",
	}
	if m.edit.rowDesc != "" {
		rows = append(rows, s.Subtle.Render(m.edit.rowDesc), "")
	}
	for i, p := range m.edit.pickers {
		focused := m.edit.focus == i
		rows = append(rows,
			focusMark(s, focused)+s.Subtle.Render(p.label+":"),
			"  "+radioRow(s, p.labels, p.idx),
		)
	}
	layerFocus := m.edit.focus == len(m.edit.pickers)
	rows = append(rows,
		"",
		focusMark(s, layerFocus)+s.Subtle.Render("Write to layer:"),
		"  "+radioRow(s, m.layers, m.layerIdx),
		"  "+s.Help.Render(layerHint(m.layers[m.layerIdx])),
	)
	if m.step == provSaving {
		return m.renderCard(strings.Join(rows, "\n"), "saving…")
	}
	hint := "↑↓ switch field · ←→ change · enter save · esc cancel"
	return m.renderCard(strings.Join(rows, "\n"), hint)
}

// fieldDisplay renders a resolved field value with its source layer, or an empty
// placeholder when unset.
func fieldDisplay(f ProviderTuningField, empty string) string {
	if f.Value == "" {
		return empty
	}
	layer := f.Layer
	if layer == "" || layer == "default" {
		return f.Value
	}
	return f.Value + "  (" + layer + ")"
}

func focusMark(s Styles, focused bool) string {
	if focused {
		return s.Info.Render("▸ ")
	}
	return "  "
}

func radioRow(s Styles, labels []string, idx int) string {
	var parts []string
	for i, label := range labels {
		if i == idx {
			parts = append(parts, s.Header.Render("● "+label))
		} else {
			parts = append(parts, s.Help.Render("○ "+label))
		}
	}
	return strings.Join(parts, "   ")
}

func (m providerSettingsModel) renderCard(body, hint string) string {
	card := m.styles.SummaryCard.MaxWidth(m.width - 4).Render(body)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, card, m.styles.Help.Render(hint)))
}
