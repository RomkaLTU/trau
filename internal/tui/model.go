package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/state"
)

const maxLogLines = 1000

const (
	leftPaneW = 32 // pipeline pane total width (incl. borders)
	hudH      = 4  // usage strip: title row + 2 content rows + bottom border
	headerH   = 2  // brand row + rule
	footerH   = 1
	panelGap  = 1 // vertical blank line between stacked regions

	mockUsageLimit = 2_000_000 // placeholder provider token budget until a real source is wired
)

type viewState int

const (
	stateRunning viewState = iota
	stateSummary
)

type keyMap struct {
	Quit   key.Binding
	Help   key.Binding
	Follow key.Binding
	Open   key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Quit:   key.NewBinding(key.WithKeys("ctrl+c", "q"), key.WithHelp("q", "quit/stop")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Follow: key.NewBinding(key.WithKeys("f", "G"), key.WithHelp("f", "follow")),
		Open:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open PR")),
	}
}

// ShortHelp returns the short-form key bindings for the help footer.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Follow, k.Help, k.Quit}
}

// FullHelp returns the full key binding help page.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Follow, k.Open}, {k.Help, k.Quit}}
}

type model struct {
	styles Styles
	keys   keyMap
	state  viewState

	width   int
	height  int
	started time.Time

	steps     []phaseStep
	spin      spinner.Model
	progress  progress.Model
	viewport  viewport.Model
	feed      []feedEntry
	following bool
	help      help.Model
	usage     usageStats

	currentTicket string
	currentTitle  string
	ticketNum     int
	banner        string
	bannerErr     bool

	onInterrupt func()
	stopping    bool
	paused      bool

	results []console.TicketResult

	summary      console.SessionSummary
	summaryTable table.Model
	// recoveryNote is a transient line shown under the summary card after a
	// recovery key (b/x) acts; confirmResetID, when non-empty, is the ticket
	// awaiting a second keypress to confirm a destructive reset.
	recoveryNote   string
	confirmResetID string
}

type (
	logMsg        struct{ line string }
	eventMsg      struct{ ev event.Event }
	ticketMsg     struct{ id string }
	titleMsg      struct{ title string }
	phaseStartMsg struct{ phase string }
	ticketDoneMsg struct{ r console.TicketResult }
	loopDoneMsg   struct{ s console.SessionSummary }
	// recoveryDoneMsg carries the outcome of a summary recovery action (b/x): note
	// is the line to surface; resetID, when set and err is nil, marks the ticket
	// that was reset so its summary row can reflect it.
	recoveryDoneMsg struct {
		note    string
		resetID string
		err     error
	}
)

// feedEntry is one row of the activity feed: a timestamped, glyph-tagged line
// attributed to a pipeline phase. sub entries are indented continuation lines
// (failure reasons, detail) that hang under the preceding entry.
type feedEntry struct {
	ts     time.Time
	glyph  string
	gstyle lipgloss.Style
	phase  string
	text   string
	sub    bool
}

// usageStats accumulates the run's agent spend from agent_call events. limit and
// resets are placeholders (mockUsageLimit) until a real provider source is wired.
type usageStats struct {
	provider string
	in       int
	out      int
	total    int
	cost     float64
}

func initialModel(onInterrupt func()) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = DefaultStyles().Spinner

	vp := viewport.New(0, 0)
	vp.SetContent("")

	p := progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage())

	return model{
		styles:      DefaultStyles(),
		keys:        defaultKeyMap(),
		state:       stateRunning,
		started:     time.Now(),
		steps:       phaseSteps(),
		spin:        s,
		progress:    p,
		viewport:    vp,
		feed:        make([]feedEntry, 0, maxLogLines),
		following:   true,
		help:        help.New(),
		onInterrupt: onInterrupt,
	}
}

func (m model) Init() tea.Cmd {
	return m.spin.Tick
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m, cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.relayout()

	case logMsg:
		m.addLog(msg.line)

	case eventMsg:
		m.applyEvent(msg.ev)

	case ticketMsg:
		m.startTicket(msg.id)

	case titleMsg:
		m.currentTitle = msg.title

	case phaseStartMsg:
		m.steps = startPhase(m.steps, msg.phase, time.Now())

	case ticketDoneMsg:
		m.finishTicket(msg.r)

	case loopDoneMsg:
		return m.enterSummary(msg.s)

	case recoveryDoneMsg:
		m = m.applyRecovery(msg)
		return m, nil
	}

	var cmd tea.Cmd
	m.spin, cmd = m.spin.Update(msg)
	cmds = append(cmds, cmd)

	if m.state == stateSummary {
		m.summaryTable, cmd = m.summaryTable.Update(msg)
		cmds = append(cmds, cmd)
	} else {
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
		m.following = m.viewport.AtBottom()
	}

	return m, tea.Batch(cmds...)
}

func (m model) handleKey(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	if m.state == stateSummary {
		switch {
		case key.Matches(msg, m.keys.Quit), msg.Type == tea.KeyEnter, msg.Type == tea.KeyEsc:
			return m, tea.Quit, true
		case key.Matches(msg, m.keys.Open):
			return m, m.openSelectedPR(), true
		}
		return m, nil, false
	}

	switch {
	case key.Matches(msg, m.keys.Quit):

		if m.stopping && msg.Type == tea.KeyCtrlC {
			return m, tea.Quit, true
		}
		if m.onInterrupt != nil {
			m.onInterrupt()
		}
		m.stopping = true
		m.banner = "⏹ stopping after this phase… (ctrl+c again to force quit)"
		m.bannerErr = false
		return m, nil, true
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.relayout()
		return m, nil, true
	case key.Matches(msg, m.keys.Follow):
		m.following = true
		m.viewport.GotoBottom()
		return m, nil, true
	}
	return m, nil, false
}

func (m *model) relayout() {
	d := m.dims()
	m.viewport.Width = d.vpW
	m.viewport.Height = d.vpH
	m.progress.Width = d.leftW - 9 // inner text width less room for " 100%"
	m.help.Width = m.width
	m.refreshFeed()
}

type dims struct {
	bodyH, leftW, rightW, vpW, vpH int
}

// dims derives the running view's regions. Vertical budget, top to bottom:
// header(2) + gap + body(bodyH) + gap + usage HUD(hudH) + gap + footer(fh).
func (m model) dims() dims {
	fh := footerH
	if m.help.ShowAll {
		fh += 2
	}
	bodyH := m.height - headerH - hudH - fh - 3*panelGap
	if bodyH < 6 {
		bodyH = 6
	}

	leftW := leftPaneW
	rightW := m.width - leftW
	if rightW < 24 {
		rightW = 24
	}
	vpW := rightW - 4 // 2 borders + 1 padding each side
	if vpW < 12 {
		vpW = 12
	}
	vpH := bodyH - 2 // top + bottom border
	if vpH < 3 {
		vpH = 3
	}
	return dims{bodyH: bodyH, leftW: leftW, rightW: rightW, vpW: vpW, vpH: vpH}
}

// addLog turns one raw pipeline line into an activity-feed entry. Continuation
// lines (↳) hang under the previous entry as detail; a "self-heal attempt N/M"
// line also lights up a sub-step under the active pipeline phase.
func (m *model) addLog(line string) {
	glyph, style, text, isSub := m.classifyLine(line)
	if glyph == "⏸" && strings.HasPrefix(text, "paused") {
		m.paused = true
	}
	if isSub {
		m.appendFeed(feedEntry{ts: time.Now(), glyph: "↳", gstyle: m.styles.Subtle, text: text, sub: true})
	} else {
		m.appendFeed(feedEntry{ts: time.Now(), glyph: glyph, gstyle: style, phase: m.activePhase(), text: text})
	}
	if a, b, ok := parseAttempt(line); ok {
		if idx := activeIndex(m.steps); idx >= 0 {
			m.steps[idx].subs = []string{fmt.Sprintf("self-heal %d/%d", a, b)}
		}
	}
	m.refreshFeed()
}

func (m *model) appendFeed(e feedEntry) {
	m.feed = append(m.feed, e)
	if len(m.feed) > maxLogLines {
		m.feed = m.feed[len(m.feed)-maxLogLines:]
	}
}

func (m *model) refreshFeed() {
	m.viewport.SetContent(m.renderFeed(m.viewport.Width))
	if m.following {
		m.viewport.GotoBottom()
	}
}

// activePhase is the label of the currently-running pipeline step, used as the
// feed's phase column. Empty between tickets / before the first phase.
func (m model) activePhase() string {
	if idx := activeIndex(m.steps); idx >= 0 {
		return m.steps[idx].label
	}
	return ""
}

// classifyLine maps a raw pipeline line to a feed glyph, color, cleaned text, and
// whether it is an indented continuation. For phase-start lines (▸) the model tag
// after " · " is kept as the text since the phase column already names the step.
func (m model) classifyLine(line string) (glyph string, style lipgloss.Style, text string, sub bool) {
	t := strings.TrimLeft(line, " ")
	switch {
	case strings.HasPrefix(t, "↳"):
		return "↳", m.styles.Subtle, strings.TrimSpace(strings.TrimPrefix(t, "↳")), true
	case strings.HasPrefix(t, "▸"), strings.HasPrefix(t, "▶"):
		body := strings.TrimSpace(t[len("▸"):])
		if i := strings.Index(body, " · "); i >= 0 {
			body = strings.TrimSpace(body[i+len(" · "):])
		}
		return "▸", m.styles.Info, body, false
	case strings.HasPrefix(t, "✓"), strings.HasPrefix(t, "✔"):
		return "✓", m.styles.Success, strings.TrimSpace(t[len("✓"):]), false
	case strings.HasPrefix(t, "✗"):
		return "✗", m.styles.Error, strings.TrimSpace(t[len("✗"):]), false
	case strings.HasPrefix(t, "⚠"):
		return "⚠", m.styles.Warning, strings.TrimSpace(t[len("⚠"):]), false
	case strings.HasPrefix(t, "⏸"):
		return "⏸", m.styles.BannerErr, strings.TrimSpace(t[len("⏸"):]), false
	case strings.HasPrefix(t, "↻"):
		return "↻", m.styles.Subtle, strings.TrimSpace(t[len("↻"):]), false
	case strings.HasPrefix(t, "→"), strings.HasPrefix(t, "PR "):
		return "→", m.styles.Info, strings.TrimSpace(strings.TrimPrefix(t, "→")), false
	case strings.HasPrefix(t, "==="):
		return "", m.styles.Header, strings.TrimSpace(strings.Trim(t, "= ")), false
	default:
		return "·", m.styles.Subtle, t, false
	}
}

func (m *model) applyEvent(ev event.Event) {
	if ev.Kind != "agent_call" {
		return
	}
	if p := strField(ev.Fields, "provider"); p != "" {
		m.usage.provider = p
	}
	m.usage.in += intField(ev.Fields, "input_tokens")
	m.usage.out += intField(ev.Fields, "output_tokens")
	m.usage.total += intField(ev.Fields, "total_tokens")
	m.usage.cost += floatField(ev.Fields, "cost_usd")

	idx := activeIndex(m.steps)
	if idx < 0 {
		return
	}
	if tag := modelTag(ev.Fields); tag != "" {
		m.steps[idx].tag = tag
	}
}

// parseAttempt extracts the N/M from a "self-heal attempt N/M" line.
func parseAttempt(line string) (a, b int, ok bool) {
	i := strings.Index(line, "self-heal attempt ")
	if i < 0 {
		return 0, 0, false
	}
	rest := line[i+len("self-heal attempt "):]
	if _, err := fmt.Sscanf(rest, "%d/%d", &a, &b); err != nil {
		return 0, 0, false
	}
	return a, b, true
}

func (m *model) startTicket(id string) {
	m.currentTicket = id
	m.currentTitle = ""
	m.ticketNum++
	m.paused = false
	m.steps = phaseSteps()
	if !m.stopping {
		m.banner = ""
	}
}

// pendingResetID returns the ticket awaiting a reset confirmation, or "".
func (m model) pendingResetID() string { return m.confirmResetID }

// askResetConfirm arms the two-keypress guard before a destructive reset.
func (m model) askResetConfirm(id string) model {
	m.confirmResetID = id
	m.recoveryNote = ""
	return m
}

// clearResetConfirm cancels a pending reset confirmation.
func (m model) clearResetConfirm() model {
	m.confirmResetID = ""
	return m
}

// applyRecovery folds a recovery action's outcome into the recap: it shows the
// note and, on a successful reset, relabels that ticket's row so the recap
// reflects it will be re-picked.
func (m model) applyRecovery(msg recoveryDoneMsg) model {
	m.confirmResetID = ""
	m.recoveryNote = msg.note
	if msg.err == nil && msg.resetID != "" {
		for i := range m.results {
			if m.results[i].ID == msg.resetID {
				m.results[i].Phase = phaseReset
			}
		}
		m.summaryTable = m.makeSummaryTable()
	}
	return m
}

func (m *model) finishTicket(r console.TicketResult) {
	m.steps = finalize(m.steps, r.Phase != state.Quarantined, time.Now())
	m.results = append(m.results, r)
	if r.Phase != state.Merged && !m.stopping {
		label, kind := statusLabel(r.Phase)
		m.banner = fmt.Sprintf("%s %s — %s", statusGlyph(kind), r.ID, label)
		m.bannerErr = kind == statusBad
	}
}

func (m model) done() bool { return m.state == stateSummary }

func (m model) markStopping() model {
	m.stopping = true
	m.banner = "⏹ stopping after this phase…"
	m.bannerErr = false
	return m
}

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}
	if m.state == stateSummary {
		return m.renderSummary()
	}
	return m.renderRunning()
}

func (m model) renderRunning() string {
	d := m.dims()

	left := m.renderStepper(m.spin.View(), d.leftW-4)
	left += "\n\n" + m.progress.ViewAs(completedFraction(m.steps)) +
		" " + m.styles.Subtle.Render(fmt.Sprintf("%3d%%", int(completedFraction(m.steps)*100+0.5)))
	leftBox := titledPanel(m.styles, "Pipeline", left, d.leftW, d.bodyH)

	rightBox := titledPanel(m.styles, "Activity", m.viewport.View(), d.rightW, d.bodyH)

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)
	gap := strings.Repeat("\n", panelGap)
	return m.renderHeader() + gap +
		body + gap +
		m.renderHud(m.width) + gap +
		m.renderFooter()
}

func (m model) renderHeader() string {
	left := m.styles.Header.Render("⬡ trau")
	if m.currentTicket != "" {
		left += "  " + chip(m.currentTicket, colorInfo)
	}
	if m.currentTitle != "" {
		left += " " + m.styles.Subtle.Render(truncate(m.currentTitle, 44))
	}
	state, sc := m.stateChip()
	right := chip(state, sc) + "  " + m.styles.Subtle.Render("⏱ "+fmtDur(time.Since(m.started)))
	top := joinEnds(left, right, m.width)
	return top + "\n" + m.styles.Separator.Render(strings.Repeat("─", maxInt(m.width, 1)))
}

// stateChip reflects the loop's real state — it does not claim "paused" for a
// rate limit, since the engine still proceeds today.
func (m model) stateChip() (string, lipgloss.Color) {
	switch {
	case m.paused:
		return "paused", colorError
	case m.stopping:
		return "stopping", colorWarning
	default:
		return "running", colorSuccess
	}
}

func (m model) renderHud(w int) string {
	prov := m.usage.provider
	if prov == "" {
		prov = "agent"
	}
	used := m.usage.total
	frac := float64(used) / float64(mockUsageLimit)
	if m.paused {
		frac = 1
	}
	pct := int(frac*100 + 0.5)
	if pct > 100 {
		pct = 100
	}
	limitTag := m.styles.Help.Render("  (limit mocked)")
	if m.paused {
		limitTag = "  " + m.styles.BannerErr.Render("rate limited")
	}
	barW := 28
	row1 := m.styles.Subtle.Render(pad(prov, 7)) + " " + usageBar(frac, barW) + " " +
		m.styles.Subtle.Render(fmt.Sprintf("%3d%%", pct)) + "   " +
		m.styles.Subtle.Render(fmt.Sprintf("%s / %s tokens", fmtTokens(used), fmtTokens(mockUsageLimit))) +
		limitTag
	row2 := m.styles.Help.Render("tokens ") +
		m.styles.Subtle.Render(fmt.Sprintf("in %s · out %s · %s total", fmtTokens(m.usage.in), fmtTokens(m.usage.out), fmtTokens(m.usage.total))) +
		m.styles.Help.Render("    cost ") + m.styles.Success.Render(fmt.Sprintf("$%.2f", m.usage.cost)) +
		m.styles.Help.Render("  this run")
	return titledPanel(m.styles, "Usage", row1+"\n"+row2, w, hudH)
}

func (m model) renderFooter() string {
	if m.banner != "" {
		style := m.styles.Banner
		if m.bannerErr {
			style = m.styles.BannerErr
		}
		return style.Render(truncate(m.banner, m.width))
	}
	tickets := m.ticketNum
	done := 0
	for i := range m.results {
		if m.results[i].Phase == state.Merged {
			done++
		}
	}
	stats := fmt.Sprintf("ticket %d · %d merged", tickets, done)
	left := m.styles.Footer.Render(stats)
	right := m.help.View(m.keys)
	return joinEnds(left, right, m.width)
}

// renderFeed lays out the activity feed for a panel of inner text width w.
func (m model) renderFeed(w int) string {
	if w < 12 {
		w = 12
	}
	var b strings.Builder
	for i := range m.feed {
		e := m.feed[i]
		if e.sub {
			indent := "            ↳ "
			b.WriteString(m.styles.Help.Render(indent) + m.styles.Subtle.Render(truncate(e.text, w-lipgloss.Width(indent))))
		} else {
			ts := m.styles.Help.Render(e.ts.Format("15:04:05"))
			gl := e.gstyle.Render(pad(e.glyph, 1))
			ph := m.styles.Help.Render(pad(e.phase, 8))
			head := ts + "  " + gl + " " + ph + " "
			b.WriteString(head + truncate(e.text, w-lipgloss.Width(head)))
		}
		if i < len(m.feed)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func modelTag(fields map[string]any) string {
	model := shortModel(strField(fields, "model"))
	effort := strField(fields, "effort")
	switch {
	case model != "" && effort != "":
		return model + " @" + effort
	case model != "":
		return model
	case effort != "":
		return "@" + effort
	default:
		return ""
	}
}

func shortModel(model string) string {
	return strings.TrimPrefix(model, "claude-")
}

func strField(f map[string]any, k string) string {
	s, _ := f[k].(string)
	return s
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds()+0.5)) + "s"
	default:
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int((d%time.Minute)/time.Second))
	}
}

func joinEnds(left, right string, width int) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	gap := width - lw - rw
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func truncate(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > width {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func intField(f map[string]any, k string) int {
	switch v := f[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func floatField(f map[string]any, k string) float64 {
	switch v := f[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

// pad right-pads s with spaces to display width w (ANSI-aware), truncating if longer.
func pad(s string, w int) string {
	cur := lipgloss.Width(s)
	if cur == w {
		return s
	}
	if cur > w {
		return truncate(s, w)
	}
	return s + strings.Repeat(" ", w-cur)
}

func chip(label string, bg lipgloss.Color) string {
	return lipgloss.NewStyle().Bold(true).Foreground(colorInk).Background(bg).Padding(0, 1).Render(label)
}

// usageBar renders a threshold-colored fill bar (green → amber → red) of width w.
func usageBar(frac float64, w int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	on := int(frac*float64(w) + 0.5)
	c := colorSuccess
	switch {
	case frac >= 0.9:
		c = colorError
	case frac >= 0.7:
		c = colorWarning
	}
	full := lipgloss.NewStyle().Foreground(c).Render(strings.Repeat("█", on))
	empty := lipgloss.NewStyle().Foreground(colorBarOff).Render(strings.Repeat("░", w-on))
	return full + empty
}

// titledPanel draws a rounded box of total width w and height h with the title
// woven into the top border. body is pre-rendered (and may carry ANSI); its lines
// are padded/truncated to the inner width and the block to h-2 rows.
func titledPanel(s Styles, title, body string, w, h int) string {
	if w < 6 {
		w = 6
	}
	textW := w - 4
	innerH := h - 2
	if innerH < 1 {
		innerH = 1
	}
	fill := w - 5 - lipgloss.Width(title)
	if fill < 0 {
		fill = 0
	}
	border := s.Separator
	top := border.Render("╭─ ") + s.PaneTitle.Render(title) + border.Render(" "+strings.Repeat("─", fill)+"╮")
	bottom := border.Render("╰" + strings.Repeat("─", w-2) + "╯")
	bar := border.Render("│")

	lines := strings.Split(body, "\n")
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	lines = lines[:innerH]

	out := make([]string, 0, innerH+2)
	out = append(out, top)
	for _, ln := range lines {
		out = append(out, bar+" "+pad(ln, textW)+" "+bar)
	}
	out = append(out, bottom)
	return strings.Join(out, "\n")
}
