package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// providerSettingsModel is the execution-tuning panel: a per-provider view of
// the model and reasoning-effort dials, plus per-phase overrides. Provider tabs
// switch with ←→. Every value is a typo-proof picker — model options are the
// documented values (Claude/Codex) or the aliases from the user's Kimi
// config.toml; effort is shown only for providers that expose a usable knob.
// There are no speed presets — tuning is the two independent dials, model and
// effort, optionally overridden per phase. Values not in a list are reachable
// through the raw "All settings" editor.
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
	values []string // values to save (e.g. "" for the inherit/default sentinel)
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

func (m providerSettingsModel) current() (ProviderTuning, bool) {
	if m.tab < 0 || m.tab >= len(m.providers) {
		return ProviderTuning{}, false
	}
	return m.providers[m.tab], true
}

func (m *providerSettingsModel) rebuildRows() {
	m.rows = m.rows[:0]
	p, ok := m.current()
	if !ok {
		return
	}
	m.rows = append(m.rows, provRow{kind: rowModel})
	if len(p.Efforts) > 0 {
		m.rows = append(m.rows, provRow{kind: rowEffort})
	}
	for i := range p.Phases {
		m.rows = append(m.rows, provRow{kind: rowPhase, phaseIdx: i})
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
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouseClick(msg)
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

func (m providerSettingsModel) handleKey(msg tea.KeyPressMsg) (providerSettingsModel, tea.Cmd) {
	if msg.String() == "ctrl+c" {
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

// handleMouseClick resolves a left click on the browse screen: a provider tab
// switches provider; a dial/phase row selects, and a click on the selected row
// opens its editor. Ignored on the edit screen, which has no rows.
func (m providerSettingsModel) handleMouseClick(msg tea.MouseClickMsg) (providerSettingsModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.step != provBrowse {
		return m, nil
	}
	if i, ok := clickedRow(msg, zoneProvTab, len(m.providers)); ok {
		if i != m.tab {
			m.tab = i
			m.cursor = 0
			m.savedMsg, m.saveErr = "", nil
			m.rebuildRows()
		}
		return m, nil
	}
	if i, ok := clickedRow(msg, zoneProvRow, len(m.rows)); ok {
		if i == m.cursor {
			return m.enterEdit()
		}
		m.cursor = i
	}
	return m, nil
}

func (m providerSettingsModel) handleBrowseKey(msg tea.KeyPressMsg) (providerSettingsModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return m, nil // handled by the hub as back
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "left", "h":
		if m.tab > 0 {
			m.tab--
			m.cursor = 0
			m.savedMsg, m.saveErr = "", nil
			m.rebuildRows()
		}
	case "right", "l":
		if m.tab < len(m.providers)-1 {
			m.tab++
			m.cursor = 0
			m.savedMsg, m.saveErr = "", nil
			m.rebuildRows()
		}
	case "enter", "e":
		return m.enterEdit()
	}
	return m, nil
}

func (m providerSettingsModel) enterEdit() (providerSettingsModel, tea.Cmd) {
	p, ok := m.current()
	if !ok || m.cursor >= len(m.rows) {
		return m, nil
	}
	row := m.rows[m.cursor]
	prefix := strings.ToUpper(p.Name) + "_"
	m.saveErr = nil
	m.savedMsg = ""

	switch row.kind {
	case rowModel:
		m.edit = provEditor{
			title:   "Default model — " + p.Name,
			rowDesc: modelDesc(p),
			pickers: []provPicker{pickerField("Model", prefix+"MODEL", p.Models, p.Model.Value, "(default)")},
		}
	case rowEffort:
		m.edit = provEditor{
			title:   "Default reasoning effort — " + p.Name,
			rowDesc: effortDesc(p.Name),
			pickers: []provPicker{pickerField("Effort", prefix+"EFFORT", p.Efforts, p.Effort.Value, "(default)")},
		}
	case rowPhase:
		ph := p.Phases[row.phaseIdx]
		pp := strings.ToUpper(ph.Phase)
		pickers := []provPicker{pickerField("Model", prefix+pp+"_MODEL", p.Models, ph.Model.Value, "(inherit)")}
		if len(p.Efforts) > 0 {
			pickers = append(pickers, pickerField("Effort", prefix+pp+"_EFFORT", p.Efforts, ph.Effort.Value, "(inherit)"))
		}
		m.edit = provEditor{
			title:   ph.Phase + " phase — " + p.Name,
			rowDesc: "Override the " + ph.Phase + " phase. (inherit) keeps the provider default.",
			pickers: pickers,
		}
	}
	m.edit.focus = 0
	m.step = provEdit
	return m, nil
}

func pickerField(label, key string, opts []string, value, sentinel string) provPicker {
	values, labels := optionsWith(opts, sentinel)
	return provPicker{
		label:  label,
		key:    key,
		values: values,
		labels: labels,
		idx:    optionIndex(values, value),
	}
}

func (m providerSettingsModel) handleEditKey(msg tea.KeyPressMsg) (providerSettingsModel, tea.Cmd) {
	layerFocus := len(m.edit.pickers)
	switch msg.String() {
	case "esc", "q":
		m.step = provBrowse
		return m, nil
	case "enter":
		return m.saveEdit()
	case "tab", "down", "j":
		if m.edit.focus < layerFocus {
			m.edit.focus++
		}
	case "shift+tab", "up", "k":
		if m.edit.focus > 0 {
			m.edit.focus--
		}
	case "left", "h":
		m.changeFocused(-1)
	case "right", "l":
		m.changeFocused(1)
	}
	return m, nil
}

func (m *providerSettingsModel) changeFocused(delta int) {
	if m.edit.focus == len(m.edit.pickers) {
		m.layerIdx = clampInt(m.layerIdx+delta, 0, len(m.layers)-1)
		return
	}
	p := &m.edit.pickers[m.edit.focus]
	p.idx = clampInt(p.idx+delta, 0, len(p.values)-1)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (m providerSettingsModel) saveEdit() (providerSettingsModel, tea.Cmd) {
	layer := m.layers[m.layerIdx]
	pairs := make([]provKV, 0, len(m.edit.pickers))
	for _, p := range m.edit.pickers {
		val := ""
		if p.idx >= 0 && p.idx < len(p.values) {
			val = p.values[p.idx]
		}
		pairs = append(pairs, provKV{key: p.key, value: val})
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
	}
	return ""
}

// modelDesc explains where a provider's model choices come from.
func modelDesc(p ProviderTuning) string {
	switch p.Name {
	case "kimi":
		if len(p.Models) == 0 {
			return "No model aliases found in ~/.kimi-code/config.toml — define one there, or set KIMI_MODEL via All settings."
		}
		return "Model aliases from your ~/.kimi-code/config.toml [models.<alias>]."
	case "claude":
		return "Claude model alias (resolves to the current version). Pin a full ID via All settings."
	case "codex":
		return "Codex model. For a model not listed, use All settings."
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
	header := []string{
		s.SummaryTitle.Render("Provider tuning"),
		"",
		m.renderTabs(),
		"",
	}
	if m.saveErr != nil {
		header = append(header, s.Error.Render("Error: "+m.saveErr.Error()), "")
	} else if m.savedMsg != "" {
		header = append(header, s.Success.Render("✓ "+m.savedMsg), "")
	}

	p, ok := m.current()
	if !ok {
		return m.renderCard(strings.Join(header, "\n"), "esc back")
	}
	hasEffort := len(p.Efforts) > 0

	// mid holds the selectable dial/phase rows plus their section labels; track
	// the focused row's line so the window can keep it in view.
	mid := []string{s.Subtle.Render("Defaults")}
	cursorLine := 0
	phaseHeaderShown := false
	for i, r := range m.rows {
		focused := i == m.cursor
		switch r.kind {
		case rowModel:
			if focused {
				cursorLine = len(mid)
			}
			mid = append(mid, markRow(zoneProvRow, i, m.renderValueRow(focused, "Model", fieldDisplay(p.Model, "(provider default)"))))
		case rowEffort:
			if focused {
				cursorLine = len(mid)
			}
			mid = append(mid, markRow(zoneProvRow, i, m.renderValueRow(focused, "Reasoning", fieldDisplay(p.Effort, "(provider default)"))))
		case rowPhase:
			if !phaseHeaderShown {
				mid = append(mid, "", s.Subtle.Render("Per-phase overrides"))
				phaseHeaderShown = true
			}
			if focused {
				cursorLine = len(mid)
			}
			mid = append(mid, markRow(zoneProvRow, i, m.renderPhaseRow(focused, p.Phases[r.phaseIdx], hasEffort)))
		}
	}

	footer := []string{"", s.Help.Render("Effective: " + m.effectiveRoutes(p, hasEffort))}
	if d := m.cursorDesc(p); d != "" {
		footer = append(footer, "", s.Help.Render(d))
	}

	midBudget := cardBodyBudget(m.height, 0) - len(header) - len(footer)
	mid = scrollToCursor(mid, cursorLine, midBudget)

	body := make([]string, 0, len(header)+len(mid)+len(footer))
	body = append(body, header...)
	body = append(body, mid...)
	body = append(body, footer...)

	return m.renderCard(strings.Join(body, "\n"), m.help().footer())
}

// help is the provider tuning panel's key legend per step: the single source
// for its footer and the ? overlay.
func (m providerSettingsModel) help() screenHelp {
	if m.step == provEdit {
		return screenHelp{title: "Edit provider", columns: []helpColumn{
			group("Navigate", fk("↑↓", "switch field"), xk("tab/⇧tab", "switch field")),
			group("Value", fk("←→", "change"), xk("h/l", "change")),
			group("Actions", fk("enter", "save"), fk("esc/q", "cancel")),
		}}
	}
	return screenHelp{title: "Provider settings", columns: []helpColumn{
		group("Navigate", fk("↑↓", "move"), fk("←→", "switch provider")),
		group("Actions", fk("enter", "edit"), xk("e", "edit"), fk("esc/q", "back")),
	}}
}

func (m providerSettingsModel) renderTabs() string {
	s := m.styles
	var parts []string
	for i, p := range m.providers {
		label := p.Name
		if p.Active {
			label = "● " + label
		}
		var tab string
		if i == m.tab {
			tab = s.Header.Render("[" + label + "]")
		} else {
			tab = s.Help.Render(" " + label + " ")
		}
		parts = append(parts, markRow(zoneProvTab, i, tab))
	}
	return strings.Join(parts, "  ")
}

func (m providerSettingsModel) renderValueRow(focused bool, label, value string) string {
	s := m.styles
	labelStyle := s.Subtle
	valStyle := lipgloss.NewStyle()
	if focused {
		labelStyle = s.Header
		valStyle = lipgloss.NewStyle().Foreground(theme.Brand)
	}
	return cursorMarker(s, focused) + labelStyle.Render(padRight(label, 12)) + "  " + valStyle.Render(value)
}

func (m providerSettingsModel) renderPhaseRow(focused bool, ph ProviderPhaseTuning, hasEffort bool) string {
	model := ph.EffModel
	if model == "" {
		model = "(default)"
	}
	value := model
	overridden := ph.Model.Value != ""
	if hasEffort {
		effort := ph.EffEffort
		if effort == "" {
			effort = "(default)"
		}
		value += " @ " + effort
		overridden = overridden || ph.Effort.Value != ""
	}
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
		return modelDesc(p)
	case rowPhase:
		return "• marks a phase with an explicit override. Enter to edit."
	}
	return ""
}

// effectiveRoutes renders the model (and effort, where supported) each phase
// will run under — the same resolution the loop applies — so precedence is
// legible at a glance.
func (m providerSettingsModel) effectiveRoutes(p ProviderTuning, hasEffort bool) string {
	parts := make([]string, 0, len(p.Phases))
	for _, ph := range p.Phases {
		model := ph.EffModel
		if model == "" {
			model = "default"
		}
		seg := ph.Phase + "→" + model
		if hasEffort && ph.EffEffort != "" {
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
	focusLine := 0
	for i, p := range m.edit.pickers {
		focused := m.edit.focus == i
		if focused {
			focusLine = len(rows)
		}
		rows = append(rows,
			cursorMarker(s, focused)+s.Subtle.Render(p.label+":"),
			"  "+radioRow(s, p.labels, p.idx),
		)
	}
	layerFocus := m.edit.focus == len(m.edit.pickers)
	if layerFocus {
		focusLine = len(rows) + 1 // the "Write to layer:" line, after the blank
	}
	rows = append(rows,
		"",
		cursorMarker(s, layerFocus)+s.Subtle.Render("Write to layer:"),
		"  "+radioRow(s, m.layers, m.layerIdx),
		"  "+s.Help.Render(layerHint(m.layers[m.layerIdx])),
	)

	rows = scrollToCursor(rows, focusLine, cardBodyBudget(m.height, 0))
	if m.step == provSaving {
		return m.renderCard(strings.Join(rows, "\n"), "saving…")
	}
	return m.renderCard(strings.Join(rows, "\n"), m.help().footer())
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

func (m providerSettingsModel) renderCard(body, hint string) string {
	return cardView(m.styles, m.width, m.height, body, hint)
}
