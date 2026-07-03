package tui

import (
	"fmt"
	"image/color"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/usage"
	"github.com/RomkaLTU/trau/internal/vterm"
)

const maxLogLines = 1000

const (
	leftPaneW = 32 // pipeline pane total width (incl. borders)
	hudH      = 4  // usage strip: title row + 2 content rows + bottom border
	headerH   = 2  // brand row + rule
	footerH   = 1
	panelGap  = 1 // vertical blank line between stacked regions

	// The running body splits into the span pane (left) and the queue rail
	// (right). The rail takes ~a third of the width, clamped, and is dropped
	// below railShowMin so a narrow terminal keeps the live tail full-width.
	railWMin    = 30
	railWMax    = 44
	railShowMin = 80
)

type viewState int

const (
	stateRunning viewState = iota
	stateSummary
)

// keyMap holds the running dashboard's action bindings. Key matching stays with
// bubbles' key.Binding; the footer legend and the ? overlay are both rendered
// from runningHelp so they can't drift from what this handles.
type keyMap struct {
	Quit   key.Binding
	Follow key.Binding
	Open   key.Binding
	Watch  key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Quit:   key.NewBinding(key.WithKeys("ctrl+c", "q"), key.WithHelp("q", "quit/stop")),
		Follow: key.NewBinding(key.WithKeys("f", "G"), key.WithHelp("f", "follow")),
		Open:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open PR")),
		Watch:  key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "watch agent")),
	}
}

// runningHelp is the live dashboard's key legend (the ? overlay). The footer
// itself is the dynamic runningHint, since the applicable recovery verbs depend
// on the selected rail row.
func (m model) runningHelp() screenHelp {
	return screenHelp{title: "Run", columns: []helpColumn{
		group("Queue",
			fk("↑↓", "select ticket"),
			fk("o", "open PR"),
			fk("l", "jump to logs"),
		),
		group("Live tail",
			fk("w", "watch agent"),
			fk("f", "follow tail"),
			xk("pgup/pgdn", "scroll"),
			xk("esc", "exit live view"),
		),
		group("Session",
			fk("q", "quit/stop"),
			xk("ctrl+c", "force quit"),
		),
	}}
}

// runningHint is the live footer legend: the queue verbs that apply to the
// selected rail row, plus watch and stop. A pending reset shows the confirm.
func (m model) runningHint() string {
	if m.streaming {
		return "esc exit watch · f follow · q stop"
	}
	sel, hasSel := m.selectedRow()
	parts := append([]string{"↑↓ select"}, queueVerbHints(sel, hasSel, true)...)
	parts = append(parts, "w watch", "q stop")
	return strings.Join(parts, " · ")
}

// summaryHelp is the recap screen's key legend. The recovery keys (resume,
// branch, reset) apply per selected row; the overlay lists them all so nothing
// stays hidden, while summaryHint keeps the footer to what the row supports.
func (m model) summaryHelp() screenHelp {
	return screenHelp{title: "Session complete", columns: []helpColumn{
		group("Navigate", fk("↑↓", "move")),
		group("Recover",
			fk("o", "open PR"),
			fk("l", "jump to logs"),
			fk("r", "resume ticket"),
			fk("b", "checkout branch"),
			fk("x", "reset ticket"),
		),
		group("Session", fk("esc/q", "close")),
	}}
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
	viewport  viewport.Model
	feed      []feedEntry
	following bool
	usage     usageStats
	win       usage.Window

	currentTicket string
	currentTitle  string
	ticketStarted time.Time
	ticketNum     int
	plannedTotal  int    // epic sub-issue count; 0 in queue mode
	binding       string // integration base branch, shown as run context
	banner        string
	bannerErr     bool

	// PR badge state for the current ticket, from pipeline pr_open/ci events.
	// ciState ∈ {"", "open", "pending", "failing", "green", "merged"}.
	prNum    int
	prURL    string
	ciState  string
	ciPollAt time.Time
	ciEvery  int

	onInterrupt func()
	stopping    bool
	paused      bool

	// live agent tail (w toggle): feeds streamPath's transcript into a virtual
	// terminal so Claude's full-screen TUI renders legibly
	streaming     bool
	streamPath    string
	streamCols    int
	streamRows    int
	streamOffset  int64
	stream        *vterm.Screen
	streamReading bool

	results []console.TicketResult

	// queue is the live attention-rail snapshot of every tracked ticket, refreshed
	// from the store as tickets start and finish. The recap draws from results
	// instead (see queueRows); the two feed the same shared component.
	queue []QueueRow

	summary console.SessionSummary
	// queueCursor selects a row in the attention queue (the recap, and the live
	// rail). It indexes the selectable (non-folded) rows in attention order.
	queueCursor int
	// recoveryNote is a transient line shown under the queue after a recovery key
	// (b/x) acts; confirmResetID, when non-empty, is the ticket awaiting a second
	// keypress to confirm a destructive reset.
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
	streamDataMsg struct {
		path   string
		offset int64
		data   []byte
	}
	// recoveryDoneMsg carries the outcome of a summary recovery action (b/x): note
	// is the line to surface; resetID, when set and err is nil, marks the ticket
	// that was reset so its summary row can reflect it.
	recoveryDoneMsg struct {
		note    string
		resetID string
		err     error
	}
)

// feedEntry is one row of the retained activity feed: a glyph-tagged line
// attributed to a pipeline phase. sub entries are indented continuation lines
// (failure reasons, detail) that hang under the preceding entry. The feed is the
// forensic tier and the source of the tail for non-agent phases.
type feedEntry struct {
	glyph  string
	gstyle lipgloss.Style
	phase  string
	text   string
	sub    bool
}

// usageStats accumulates the run's agent spend (tokens + cost) live from
// agent_call events. The provider rate-limit window that frames it in the HUD
// arrives separately as a usage_window event (see model.win) — this struct holds
// only the run totals, which are always real.
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

	vp := viewport.New()
	vp.SetContent("")

	return model{
		styles:      DefaultStyles(),
		keys:        defaultKeyMap(),
		state:       stateRunning,
		started:     time.Now(),
		steps:       phaseSteps(),
		spin:        s,
		viewport:    vp,
		feed:        make([]feedEntry, 0, maxLogLines),
		following:   true,
		onInterrupt: onInterrupt,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, tea.RequestBackgroundColor)
}

func (m model) restyled() model {
	m.styles = DefaultStyles()
	m.spin.Style = m.styles.Spinner
	return m
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m, cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}

	case tea.BackgroundColorMsg:
		setThemeBackground(msg.IsDark())
		m = m.restyled()

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

	case streamDataMsg:
		m.streamReading = false
		if msg.path == m.streamPath && m.stream != nil {
			m.stream.Write(msg.data)
			m.streamOffset = msg.offset
			m.refreshBody()
		}
	}

	var cmd tea.Cmd
	m.spin, cmd = m.spin.Update(msg)
	cmds = append(cmds, cmd)

	if _, ok := msg.(spinner.TickMsg); ok &&
		m.state == stateRunning && m.stream != nil && !m.streamReading {
		m.streamReading = true
		cmds = append(cmds, m.tailReadCmd())
	}

	// Advance the active phase's elapsed each tick. When a stream is live the
	// per-tick streamDataMsg already re-renders, so only drive it here for phases
	// with no stream (CI/merge), avoiding a second render per frame.
	if _, ok := msg.(spinner.TickMsg); ok &&
		m.state == stateRunning && m.stream == nil && activeIndex(m.steps) >= 0 {
		m.refreshBody()
	}

	if m.state != stateSummary {
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
		m.following = m.viewport.AtBottom()
	}

	return m, tea.Batch(cmds...)
}

func (m model) handleKey(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	if m.state == stateSummary {
		switch {
		case key.Matches(msg, m.keys.Quit), msg.String() == "esc":
			return m, tea.Quit, true
		case key.Matches(msg, m.keys.Open):
			return m, m.openSelectedPR(), true
		case msg.String() == "up" || msg.String() == "k":
			m.moveQueueCursor(-1)
			return m, nil, true
		case msg.String() == "down" || msg.String() == "j":
			m.moveQueueCursor(1)
			return m, nil, true
		}
		return m, nil, false
	}

	switch {
	case key.Matches(msg, m.keys.Quit):

		if m.stopping && msg.String() == "ctrl+c" {
			return m, tea.Quit, true
		}
		if m.onInterrupt != nil {
			m.onInterrupt()
		}
		m.stopping = true
		m.banner = "⏹ stopping after this phase… (ctrl+c again to force quit)"
		m.bannerErr = false
		return m, nil, true
	case key.Matches(msg, m.keys.Follow):
		m.following = true
		m.viewport.GotoBottom()
		return m, nil, true
	case key.Matches(msg, m.keys.Watch):
		if m.streaming {
			m.streaming = false
			m.refreshBody()
			return m, nil, true
		}
		m.streaming = true
		if m.stream == nil && m.streamPath != "" {
			m.startStream()
			m.streamReading = true
			return m, m.tailReadCmd(), true
		}
		return m, nil, true
	case msg.String() == "esc":
		if m.streaming {
			m.streaming = false
			m.refreshBody()
			return m, nil, true
		}
	}
	return m, nil, false
}

// startStream opens a fresh virtual terminal for the active transcript, read from
// the top so the current screen reconstructs in full.
func (m *model) startStream() {
	if m.stream != nil {
		m.stream.Close()
	}
	m.stream = vterm.New(m.streamCols, m.streamRows)
	m.streamOffset = 0
}

// stopStream tears down the tail emulator (between tickets), leaving the loop
// untouched. The w view is hidden by clearing m.streaming, not by tearing this
// down, so the active phase's tail keeps updating whether or not it is expanded.
func (m *model) stopStream() {
	m.streaming = false
	if m.stream != nil {
		m.stream.Close()
		m.stream = nil
	}
	m.streamOffset = 0
}

func (m *model) relayout() {
	d := m.dims()
	m.viewport.SetWidth(d.vpW)
	m.viewport.SetHeight(d.vpH)
	m.refreshBody()
}

type dims struct {
	bodyH, bodyW, spanW, railW, vpW, vpH int
}

// dims derives the running view's regions. The body row splits into the span pane
// (spanW) and the queue rail (railW, 0 when the terminal is too narrow to spare
// it). Vertical budget, top to bottom: header(2) + gap + body(bodyH) + gap +
// usage HUD(hudH) + gap + footer(fh).
func (m model) dims() dims {
	bodyH := m.height - headerH - hudH - footerH - 3*panelGap
	if bodyH < 6 {
		bodyH = 6
	}
	bodyW := m.width
	if bodyW < 24 {
		bodyW = 24
	}
	railW := 0
	spanW := bodyW
	if bodyW >= railShowMin {
		railW = bodyW * 34 / 100
		if railW < railWMin {
			railW = railWMin
		}
		if railW > railWMax {
			railW = railWMax
		}
		spanW = bodyW - railW - 1 // 1-col gap between the panes
	}
	vpW := spanW - 4 // pane borders + a padding cell each side
	if vpW < 12 {
		vpW = 12
	}
	vpH := bodyH - 2 // top + bottom border
	if vpH < 3 {
		vpH = 3
	}
	return dims{bodyH: bodyH, bodyW: bodyW, spanW: spanW, railW: railW, vpW: vpW, vpH: vpH}
}

func LiveAgentSize(termW, termH int) (cols, rows int) {
	bodyH := termH - headerH - hudH - footerH - 3*panelGap
	if bodyH < 6 {
		bodyH = 6
	}
	rightW := termW - leftPaneW
	if rightW < 24 {
		rightW = 24
	}
	cols = rightW - 4
	if cols < 12 {
		cols = 12
	}
	rows = bodyH - 2
	if rows < 3 {
		rows = 3
	}
	return cols, rows
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
		m.appendFeed(feedEntry{glyph: "↳", gstyle: m.styles.Subtle, text: text, sub: true})
	} else {
		m.appendFeed(feedEntry{glyph: glyph, gstyle: style, phase: m.activePhase(), text: text})
	}
	if c, ok := parseChildSpan(line); ok {
		if idx := activeIndex(m.steps); idx >= 0 {
			m.steps[idx].subs = upsertChildSpan(m.steps[idx].subs, c)
		}
	}
	m.refreshBody()
}

func (m *model) appendFeed(e feedEntry) {
	m.feed = append(m.feed, e)
	if len(m.feed) > maxLogLines {
		m.feed = m.feed[len(m.feed)-maxLogLines:]
	}
}

// refreshBody re-renders the span pane into the viewport. It is a no-op while the
// w live view is up (the viewport is hidden behind renderStream then), so the
// full-screen tail render isn't computed for a pane no one can see.
func (m *model) refreshBody() {
	if m.streaming {
		return
	}
	m.viewport.SetContent(m.renderSpanList(m.viewport.Width()))
	if m.following {
		m.viewport.GotoBottom()
	}
}

// tailReadCmd reads the next transcript delta from the current path/offset.
func (m model) tailReadCmd() tea.Cmd {
	path, offset := m.streamPath, m.streamOffset
	return func() tea.Msg { return readTail(path, offset) }
}

// readTail returns the raw bytes appended to path since offset, for the emulator.
func readTail(path string, offset int64) streamDataMsg {
	if path == "" {
		return streamDataMsg{}
	}
	f, err := os.Open(path)
	if err != nil {
		return streamDataMsg{path: path, offset: offset}
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err == nil && fi.Size() < offset {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return streamDataMsg{path: path, offset: offset}
	}
	data, _ := io.ReadAll(f)
	if len(data) == 0 {
		return streamDataMsg{path: path, offset: offset}
	}
	return streamDataMsg{path: path, offset: offset + int64(len(data)), data: data}
}

// renderStream is the live pane body, or a placeholder when no transcript is
// active. Watch mode owns the full body, so lines fit the full-width inner box
// (bodyW-4), not the rail-reduced span width.
func (m model) renderStream(d dims) string {
	if m.stream == nil {
		return m.styles.Subtle.Render("live agent view") + "\n" +
			m.styles.Help.Render("waiting for the next agent phase…")
	}
	w, h := d.bodyW-4, d.bodyH-2
	lines := m.stream.Lines()
	if h > 0 && len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], w, "")
	}
	return strings.Join(lines, "\n")
}

// activePhase is the label of the currently-running pipeline step, used as the
// feed's phase column. Empty between tickets / before the first phase.
func (m model) activePhase() string {
	if idx := activeIndex(m.steps); idx >= 0 {
		return m.steps[idx].label
	}
	return ""
}

// streamLabel names the live-view panel: the active phase's model tag if known,
// else the usage provider, else "agent".
func (m model) streamLabel() string {
	if idx := activeIndex(m.steps); idx >= 0 && m.steps[idx].tag != "" {
		return m.steps[idx].tag
	}
	if m.usage.provider != "" {
		return m.usage.provider
	}
	return "agent"
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
	if ev.Kind == event.KindAgentStart {
		if p := strField(ev.Fields, "transcript_path"); p != "" && p != m.streamPath {
			m.streamPath = p
			m.streamCols = intField(ev.Fields, "cols")
			m.streamRows = intField(ev.Fields, "rows")
			if idx := activeIndex(m.steps); idx >= 0 {
				m.steps[idx].transcript = p
			}
			m.startStream()
		}
		return
	}
	if ev.Kind == usage.EventKind {
		m.win = usage.WindowFromFields(ev.Fields)
		return
	}
	switch ev.Kind {
	case "pr_open":
		m.prNum = intField(ev.Fields, "number")
		m.prURL = strField(ev.Fields, "url")
		if m.ciState == "" {
			m.ciState = "open"
		}
		return
	case "ci":
		m.ciState = strField(ev.Fields, "state")
		if m.ciState == "pending" {
			m.ciEvery = intField(ev.Fields, "poll_secs")
			m.ciPollAt = time.Now()
		}
		return
	case "tickets":
		if t := intField(ev.Fields, "total"); t > 0 {
			m.plannedTotal = t
		}
		return
	}
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

func (m *model) startTicket(id string) {
	m.currentTicket = id
	m.currentTitle = ""
	m.ticketStarted = time.Now()
	m.ticketNum++
	m.paused = false
	m.prNum = 0
	m.prURL = ""
	m.ciState = ""
	m.ciEvery = 0
	m.steps = phaseSteps()
	m.streamPath = ""
	m.stopStream()
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
		m.clampQueueCursor()
	}
	return m
}

// moveQueueCursor shifts the queue selection by delta, clamped to the selectable
// rows.
func (m *model) moveQueueCursor(delta int) {
	m.queueCursor += delta
	m.clampQueueCursor()
}

// movedQueueCursor is the value-returning form for the app shell, which drives
// the live rail's selection.
func (m model) movedQueueCursor(delta int) model {
	m.moveQueueCursor(delta)
	return m
}

// withQueue replaces the live rail snapshot (the app shell refreshes it from the
// store as tickets start and finish).
func (m model) withQueue(rows []QueueRow) model {
	m.queue = rows
	m.clampQueueCursor()
	return m
}

// clampQueueCursor keeps the cursor within the selectable rows (0 when empty).
func (m *model) clampQueueCursor() {
	n := m.selectableCount()
	if m.queueCursor >= n {
		m.queueCursor = n - 1
	}
	if m.queueCursor < 0 {
		m.queueCursor = 0
	}
}

func (m *model) finishTicket(r console.TicketResult) {
	m.steps = finalize(m.steps, r.Phase != state.Quarantined, time.Now())
	if idx := failedIndex(m.steps); idx >= 0 {
		m.steps[idx].tailSnapshot = m.liveTail(idx, tailWindow)
	}
	m.refreshBody()
	m.results = append(m.results, r)
	if r.Phase != state.Merged && !m.stopping {
		label, kind := statusLabel(r.Phase)
		m.banner = fmt.Sprintf("%s %s — %s", statusGlyph(kind), r.ID, label)
		m.bannerErr = kind == statusBad
	}
}

func (m model) done() bool { return m.state == stateSummary }

// railVisible reports whether the queue rail is currently drawn — false while
// watching the full-screen stream or on a terminal too narrow to spare the rail
// — so the app shell only routes rail keys when there is a rail to act on.
func (m model) railVisible() bool { return !m.streaming && m.dims().railW > 0 }

func (m model) markStopping() model {
	m.stopping = true
	m.banner = "⏹ stopping after this phase…"
	m.bannerErr = false
	return m
}

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// render is the dashboard's screen content, shared by its own View and the app
// shell's running view.
func (m model) render() string {
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

	// Watch mode takes the whole body for the live agent stream — no rail.
	if m.streaming {
		pane := titledPanel(m.styles, "Live · "+m.streamLabel(), m.renderStream(d), d.bodyW, d.bodyH)
		return m.assembleRunning(pane)
	}

	title := fmt.Sprintf("Pipeline %d/%d", doneSteps(m.steps), len(m.steps))
	spanPane := titledPanel(m.styles, title, m.viewport.View(), d.spanW, d.bodyH)
	if d.railW == 0 {
		return m.assembleRunning(spanPane)
	}
	rail := titledPanel(m.styles, m.railTitle(), m.renderRail(d), d.railW, d.bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, spanPane, " ", rail)
	return m.assembleRunning(body)
}

// assembleRunning stacks the run-view regions around the pre-rendered body row.
func (m model) assembleRunning(body string) string {
	gap := strings.Repeat("\n", panelGap)
	return m.renderHeader() + gap +
		body + gap +
		m.renderHud(m.width) + gap +
		m.renderFooter()
}

// railTitle names the queue rail, tagged with the count of tickets that need a
// glance (needs-human + in-flight + ready).
func (m model) railTitle() string {
	if n := m.selectableCount(); n > 0 {
		return fmt.Sprintf("Queue · %d", n)
	}
	return "Queue"
}

// renderRail draws the attention queue into the right pane through the shared
// component, sized to the rail's inner box.
func (m model) renderRail(d dims) string {
	return renderQueue(m.styles, m.spinFrame(), m.liveQueueRows(), m.queueCursor, d.railW-4, d.bodyH-2, true)
}

// renderHeader lays out the run-level context row. The left core and right cluster
// always show; the title, then the binding, yield first to keep it legible at 80 cols.
func (m model) renderHeader() string {
	left := m.styles.Header.Render("⬡ trau")
	if c := m.ticketCounter(); c != "" {
		left += "  " + m.styles.Subtle.Render(c)
	}
	if m.currentTicket != "" {
		left += "  " + chip(m.currentTicket, theme.Info)
	}

	state, sc := m.stateChip()
	right := chip(state, sc)
	if badge := m.prBadge(); badge != "" {
		right += "  " + badge
	}
	right += "  " + m.styles.Subtle.Render("⏱ "+fmtDur(time.Since(m.started)))

	// Binding yields before the title; the -4 keeps a 2-col gap to the right cluster
	// so joinEnds never drops it.
	avail := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if b := strings.TrimSpace(m.binding); b != "" {
		tag := m.styles.Help.Render("⎇ " + b)
		if lipgloss.Width(tag)+2 <= avail {
			left += "  " + tag
			avail -= lipgloss.Width(tag) + 2
		}
	}
	if m.currentTitle != "" && avail >= 8 {
		left += "  " + m.styles.Subtle.Render(truncate(m.currentTitle, avail))
	}

	top := joinEnds(left, right, m.width)
	return top + "\n" + m.styles.Separator.Render(strings.Repeat("─", maxInt(m.width, 1)))
}

// ticketCounter is "ticket n/N" for an epic set, "ticket n" in queue mode.
func (m model) ticketCounter() string {
	if m.ticketNum == 0 {
		return ""
	}
	if m.plannedTotal > 0 {
		return fmt.Sprintf("ticket %d/%d", m.ticketNum, m.plannedTotal)
	}
	return fmt.Sprintf("ticket %d", m.ticketNum)
}

// prBadge is the PR chip, colored by CI verdict: blue open, yellow pending,
// red failing, green green, purple merged. Empty until a PR exists.
func (m model) prBadge() string {
	if m.prNum == 0 {
		return ""
	}
	c := theme.Info
	switch m.ciState {
	case "pending":
		c = theme.Warning
	case "failing":
		c = theme.Error
	case "green":
		c = theme.Success
	case "merged":
		c = theme.Accent
	}
	return chip(fmt.Sprintf("PR #%d", m.prNum), c)
}

// stateChip reflects the loop's real state — it does not claim "paused" for a
// rate limit, since the engine still proceeds today. While CI checks are pending
// it surfaces the wait explicitly with a countdown to the next poll.
func (m model) stateChip() (string, color.Color) {
	switch {
	case m.paused:
		return "paused", theme.Error
	case m.stopping:
		return "stopping", theme.Warning
	case m.ciState == "pending":
		if s := m.ciCountdown(); s != "" {
			return "CI " + s, theme.Warning
		}
		return "CI", theme.Warning
	default:
		return "running", theme.Success
	}
}

// ciCountdown is the time to the next CI poll ("next 24s"), ticking on each spinner
// frame. Empty when the cadence is unknown.
func (m model) ciCountdown() string {
	if m.ciEvery <= 0 || m.ciPollAt.IsZero() {
		return ""
	}
	remain := time.Duration(m.ciEvery)*time.Second - time.Since(m.ciPollAt)
	if remain < 0 {
		remain = 0
	}
	secs := int(remain / time.Second)
	if remain%time.Second > 0 {
		secs++
	}
	return fmt.Sprintf("next %ds", secs)
}

// renderHud draws the Usage strip. Row 1 frames the run against the provider's
// real rate-limit window when one was probed (a utilization bar + reset hint), or
// a prepaid balance, or — when no window source is available — token totals with
// no bar, never a fabricated denominator. Row 2 is always the live run totals. A
// rate-limit pause overrides row 1 with the banner regardless of window state.
func (m model) renderHud(w int) string {
	prov := m.usage.provider
	if prov == "" {
		prov = m.win.Provider
	}
	if prov == "" {
		prov = "agent"
	}
	row1 := m.styles.Subtle.Render(pad(prov, 7)) + " " + m.hudWindow()
	row2 := m.styles.Help.Render("tokens ") +
		m.styles.Subtle.Render(fmt.Sprintf("in %s · out %s · %s total", fmtTokens(m.usage.in), fmtTokens(m.usage.out), fmtTokens(m.usage.total))) +
		m.styles.Help.Render("    cost ") + m.styles.Success.Render(fmt.Sprintf("$%.2f", m.usage.cost)) +
		m.styles.Help.Render("  this run")
	return titledPanel(m.styles, "Usage", row1+"\n"+row2, w, hudH)
}

// hudWindow renders row 1's window segment after the provider label. A pause wins
// over everything (red full bar + banner). Otherwise: a utilization window shows a
// threshold-colored bar + percent + which window + reset hint; a balance shows the
// prepaid figure; and the no-window state shows the run's token total with an
// explicit "no provider window" note instead of a misleading bar.
func (m model) hudWindow() string {
	const barW = 28
	if m.paused {
		return usageBar(1, barW) + " " + m.styles.Subtle.Render("100%") + "   " +
			m.styles.BannerErr.Render("rate limited")
	}
	switch {
	case m.win.Available && m.win.HasUtilization:
		frac := m.win.Utilization / 100
		pct := int(frac*100 + 0.5)
		seg := usageBar(frac, barW) + " " + m.styles.Subtle.Render(fmt.Sprintf("%3d%%", pct))
		if lbl := strings.TrimSpace(m.win.Label); lbl != "" {
			seg += "   " + m.styles.Help.Render(lbl)
		}
		if hint := resetHint(m.win, time.Now()); hint != "" {
			seg += " " + m.styles.Subtle.Render(hint)
		}
		return seg
	case m.win.Available && m.win.HasBalance:
		return m.styles.Help.Render("balance ") +
			m.styles.Success.Render(fmt.Sprintf("$%.2f", m.win.BalanceUSD))
	default:
		return m.styles.Subtle.Render(fmt.Sprintf("%s tokens", fmtTokens(m.usage.total))) + "   " +
			m.styles.Help.Render("(no provider window)")
	}
}

// resetHint formats a window's reset as a countdown ("resets in 2h 14m") when the
// remaining time is known, falling back to a wall-clock time ("resets 18:40") when
// only an advisory reset instant is set. Empty when no reset is known.
func resetHint(w usage.Window, now time.Time) string {
	if d, ok := w.Remaining(now); ok {
		return "resets in " + fmtCountdown(d)
	}
	if w.HasReset {
		return "resets " + w.ResetAt.Local().Format("15:04")
	}
	return ""
}

// fmtCountdown renders a coarse human countdown: days+hours past a day (weekly
// windows), hours+minutes past an hour, whole minutes under an hour, and "<1m"
// below a minute.
func fmtCountdown(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		h := int((d % (24 * time.Hour)) / time.Hour)
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, h)
	case d >= time.Hour:
		h := int(d / time.Hour)
		mnt := int((d % time.Hour) / time.Minute)
		if mnt == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, mnt)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return "<1m"
	}
}

func (m model) renderFooter() string {
	if m.banner != "" {
		style := m.styles.Banner
		if m.bannerErr {
			style = m.styles.BannerErr
		}
		return style.Render(truncate(m.banner, m.width))
	}
	// The running ticket counter lives in the header now; the footer keeps only the
	// cumulative merged tally (not shown up top) beside the key help.
	done := 0
	for i := range m.results {
		if m.results[i].Phase == state.Merged {
			done++
		}
	}
	left := ""
	if done > 0 {
		left = m.styles.Footer.Render(fmt.Sprintf("%d merged", done))
	}
	return joinEnds(left, m.styles.Help.Render(m.runningHint()), m.width)
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

// spinnerGlyph is the spinner's current frame stripped of styling, for animating
// a live row in the shared queue renderer from either the dash or the app shell.
func spinnerGlyph(s spinner.Model) string {
	return strings.TrimSpace(ansi.Strip(s.View()))
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

func chip(label string, bg color.Color) string {
	return lipgloss.NewStyle().Bold(true).Foreground(theme.Ink).Background(bg).Padding(0, 1).Render(label)
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
	c := theme.Success
	switch {
	case frac >= 0.9:
		c = theme.Error
	case frac >= 0.7:
		c = theme.Warning
	}
	full := lipgloss.NewStyle().Foreground(c).Render(strings.Repeat("█", on))
	empty := lipgloss.NewStyle().Foreground(theme.Surface).Render(strings.Repeat("░", w-on))
	return full + empty
}

// titledPanel, the tiled dashboard/logs container, now lives in the shared
// component kit (see kit.go).
