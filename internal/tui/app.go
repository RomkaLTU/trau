package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/RomkaLTU/trau/internal/console"
)

// Actions is the backend the menu shell drives. The main package wires a
// concrete implementation; keeping it an interface keeps the tui package free of
// the pipeline / tracker / agent wiring. Network-bound methods take a context so
// the shell can cancel them.
type Actions interface {
	OnboardingActions
	SettingsActions

	MenuInfo() MenuInfo

	StatusRows() []StatusRow

	// LogRuns returns every saved ticket run, ordered by most recent update first,
	// for the log inspector. The implementation is allowed to read the durable
	// state/logs directly; the TUI only renders what it returns.
	LogRuns() []LogRun
	// LogContent returns the concatenated phase logs for id, formatted for display.
	// Phases are ordered with the most recent first so the inspector shows latest
	// context at the top.
	LogContent(id string) string

	// Reconcile cross-checks in-flight/quarantined checkpoints against the tracker
	// and clears those whose issue is Done/Canceled, returning the cleared ids. It
	// backs the status screen's on-demand reconcile.
	Reconcile(ctx context.Context) (cleared []string, err error)

	DryRun(ctx context.Context) (id string, err error)

	Reset(ctx context.Context, id string) error

	// CheckoutBranch switches the target repo to ticket id's feature branch and
	// returns the branch name, so the summary's recovery keys can land the user on
	// an incomplete ticket's preserved WIP.
	CheckoutBranch(ctx context.Context, id string) (string, error)

	// RunLoop runs the autonomous loop. When epic is non-empty the loop is scoped
	// to that epic's sub-issues; otherwise it works the team's ready queue.
	RunLoop(ctx context.Context, epic string, r console.Renderer)

	// SubIssues returns the direct children of an epic, for the run-loop preview.
	SubIssues(ctx context.Context, id string) ([]SubIssue, error)

	// ListEligible returns the ready queue using the fast API lister, when the
	// tracker supports it. An empty result means nothing is eligible right now.
	ListEligible(ctx context.Context) ([]ListedTicket, error)

	// RunTicket runs a single chosen ticket through the pipeline — resuming its own
	// checkpoint when it has one — routing progress to r and closing with r.LoopDone.
	// A non-empty provider applies an ephemeral single-run override of the default
	// provider (Run once only); other callers pass "".
	RunTicket(ctx context.Context, id, provider string, r console.Renderer)

	// OnboardingNeeded reports whether the project is missing the setup required
	// to run the loop. When true, the menu shell starts in the onboarding wizard
	// instead of the hero-card menu.
	OnboardingNeeded() bool
}

// ListedTicket is one eligible ticket returned by a fast list operation.
type ListedTicket struct {
	ID    string
	Title string
	State string
}

// ProviderChoice is one selectable provider and the model it would run, in the
// fixed cycle order (claude → codex → kimi). The Run once screen renders and
// cycles these to pick an ephemeral per-run provider override.
type ProviderChoice struct {
	Name  string
	Model string
}

// MenuInfo is the at-a-glance context shown on the landing screen.
type MenuInfo struct {
	Version       string
	Provider      string
	Model         string
	Base          string
	Prefix        string
	MaxIterations int
	AutoMerge     bool
	InFlight      int
	Done          int
	Resume        ResumeTarget
	// Providers is the fixed-order set the Run once screen cycles for an
	// ephemeral provider override; Provider names the config default within it.
	Providers []ProviderChoice
}

// ResumeTarget names the in-flight ticket the next run will continue before it
// picks anything new, so the menu and run screens can surface it by name instead
// of as a bare in-flight count. A zero value (empty ID) means nothing is
// resumable. Phase is the human label of the phase the resume runs next
// (checkpoint built → "handoff").
type ResumeTarget struct {
	ID    string
	Phase string
	Title string
}

// Active reports whether there is a ticket to resume.
func (r ResumeTarget) Active() bool { return r.ID != "" }

// Line renders the one-line resume callout shared by every screen, e.g.
// ↻ COD-498 resumes from handoff — "Enrich message conversations…". The title is
// omitted when unknown.
func (r ResumeTarget) Line() string {
	s := "↻ " + r.ID + " resumes from " + firstNonEmpty(r.Phase, "where it left off")
	if t := strings.TrimSpace(r.Title); t != "" {
		s += " — " + t
	}
	return s
}

// StatusRow is one ticket's saved state, rendered in the in-TUI status table.
type StatusRow struct {
	ID     string
	Title  string
	Phase  string
	PRURL  string
	Tokens int
	Cost   float64
	// CostMetered is false when some phase logged tokens but no per-call dollar
	// cost (a kimi/codex subscription call), so Cost is a lower bound shown as
	// "n/a"/"+" rather than a measured "$0".
	CostMetered bool
}

type appView int

const (
	viewMenu appView = iota
	viewMore
	viewOnboarding
	viewStatus
	viewLogs
	viewVersion
	viewDryRun
	viewReset
	viewRunLoop
	viewRunOnce
	viewRunning
	viewError
	viewSettings
)

type menuAction int

const (
	actRun menuAction = iota
	actRunOnce
	actDryRun
	actStatus
	actLogs
	actReset
	actVersion
	actOnboarding
	actSettings
	actMore
	actBack
	actQuit
)

type menuItem struct {
	action menuAction
	title  string
	desc   string
}

type (
	dryRunDoneMsg struct {
		id  string
		err error
	}
	resetDoneMsg struct {
		id  string
		err error
	}
	reconcileDoneMsg struct {
		cleared []string
		err     error
	}
)

type appModel struct {
	styles   Styles
	actions  Actions
	renderer *TUI
	baseCtx  context.Context

	view   appView
	width  int
	height int

	items      []menuItem
	cursor     int
	moreItems  []menuItem
	moreCursor int
	subReturn  appView
	info       MenuInfo

	status       table.Model
	reset        textinput.Model
	spin         spinner.Model
	busy         bool
	result       string
	errMsg       string
	statusBusy   bool
	statusNote   string
	statusCancel context.CancelFunc

	logs logsModel

	dash       model
	loopCancel context.CancelFunc

	onboard   onboardingModel
	loopSetup loopSetupModel
	runOnce   runOnceModel
	settings  settingsHubModel
}

func newAppModel(ctx context.Context, actions Actions, renderer *TUI) appModel {
	items := []menuItem{
		{actRun, "Run loop", "next ready ticket → PR"},
		{actRunOnce, "Run once", "pick one ticket to run"},
		{actDryRun, "Dry run", "preview the next ticket"},
		{actMore, "More…", "status · reset · version"},
		{actQuit, "Quit", ""},
	}
	moreItems := []menuItem{
		{actStatus, "Status", "saved checkpoints + tokens"},
		{actLogs, "Logs", "inspect per-ticket phase logs"},
		{actReset, "Reset ticket", "re-queue a ticket"},
		{actVersion, "Version", "build info"},
		{actOnboarding, "Re-run onboarding", "change project settings"},
		{actSettings, "Settings", "edit .ini config"},
		{actBack, "Back", "to the main menu"},
	}

	info := actions.MenuInfo()

	ti := textinput.New()
	ti.Placeholder = exampleID(info.Prefix)
	ti.CharLimit = 32
	ti.SetWidth(30)
	ti.Prompt = "Ticket ID: "

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = DefaultStyles().Spinner

	m := appModel{
		styles:    DefaultStyles(),
		actions:   actions,
		renderer:  renderer,
		baseCtx:   ctx,
		view:      viewMenu,
		items:     items,
		moreItems: moreItems,
		info:      info,
		reset:     ti,
		spin:      s,
	}
	if actions.OnboardingNeeded() {
		m.view = viewOnboarding
		m.onboard = newOnboardingModel(ctx, actions, m.styles, 0, 0)
	}
	return m
}

func (m appModel) Init() tea.Cmd { return m.spin.Tick }

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.dash = applyDash(m.dash, msg)
		m.onboard, _ = m.onboard.Update(msg)
		m.loopSetup, _ = m.loopSetup.Update(msg)
		m.runOnce, _ = m.runOnce.Update(msg)
		m.settings, _ = m.settings.Update(msg)
		m.logs, _ = m.logs.Update(msg, m.actions.LogContent)
		if m.view == viewStatus {
			cursor := m.status.Cursor()
			m.status = m.buildStatusTable()
			m.status.SetCursor(cursor)
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case logMsg, eventMsg, ticketMsg, titleMsg, phaseStartMsg, ticketDoneMsg, loopDoneMsg, recoveryDoneMsg:
		var cmd tea.Cmd
		m.dash, cmd = applyDashCmd(m.dash, msg)
		return m, cmd

	case dryRunDoneMsg:
		m.busy = false
		switch {
		case msg.err != nil:
			m.result = "Error: " + msg.err.Error()
		case msg.id == "":
			m.result = "Nothing eligible right now."
		default:
			m.result = "Next up: " + msg.id
		}
		return m, nil

	case resetDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.result = "Error: " + msg.err.Error()
		} else {
			m.result = "Reset " + msg.id + " — it can be picked again."
		}
		return m, nil

	case reconcileDoneMsg:
		m.statusBusy = false
		if m.statusCancel != nil {
			m.statusCancel()
			m.statusCancel = nil
		}
		if m.view != viewStatus {
			return m, nil
		}
		switch {
		case msg.err != nil:
			m.statusNote = "✗ reconcile failed: " + msg.err.Error()
		case len(msg.cleared) == 0:
			m.statusNote = "✓ nothing stale — all checkpoints match the tracker"
		default:
			m.statusNote = fmt.Sprintf("✓ cleared %d stale checkpoint(s): %s", len(msg.cleared), strings.Join(msg.cleared, ", "))
		}
		m.status = m.buildStatusTable()
		return m, nil

	case spinner.TickMsg:
		var cmds []tea.Cmd
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		cmds = append(cmds, cmd)
		if m.view == viewRunning {
			m.dash, cmd = applyDashCmd(m.dash, msg)
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	var cmd tea.Cmd
	switch m.view {
	case viewOnboarding:
		m.onboard, cmd = m.onboard.Update(msg)
		if m.onboard.Done() {
			m.view = viewMenu
			m.info = m.actions.MenuInfo()
		}
	case viewStatus:
		m.status, cmd = m.status.Update(msg)
	case viewLogs:
		m.logs, cmd = m.logs.Update(msg, m.actions.LogContent)
	case viewReset:
		m.reset, cmd = m.reset.Update(msg)
	case viewRunLoop:
		m.loopSetup, cmd = m.loopSetup.Update(msg)
	case viewRunOnce:
		m.runOnce, cmd = m.runOnce.Update(msg)
	case viewSettings:
		m.settings, cmd = m.settings.Update(msg)
	case viewRunning:
		m.dash, cmd = applyDashCmd(m.dash, msg)
	}
	return m, cmd
}

func (m appModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.view {
	case viewOnboarding:
		var cmd tea.Cmd
		m.onboard, cmd = m.onboard.Update(msg)
		if m.onboard.Done() {
			m.view = viewMenu
			m.info = m.actions.MenuInfo()
		}
		return m, cmd

	case viewMenu:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "enter":
			return m.selectAction(m.items[m.cursor].action)
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		}
		return m, nil

	case viewMore:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc", "b":
			m.view = viewMenu
			m.info = m.actions.MenuInfo()
			return m, nil
		case "enter":
			return m.selectAction(m.moreItems[m.moreCursor].action)
		case "up", "k":
			if m.moreCursor > 0 {
				m.moreCursor--
			}
		case "down", "j":
			if m.moreCursor < len(m.moreItems)-1 {
				m.moreCursor++
			}
		}
		return m, nil

	case viewRunLoop:
		var cmd tea.Cmd
		m.loopSetup, cmd = m.loopSetup.Update(msg)
		if m.loopSetup.Done() {
			switch {
			case m.loopSetup.Cancelled():
				return m.toMenu(), nil
			case m.loopSetup.Selected() != "":
				return m.startRunTicket(m.loopSetup.Selected(), "")
			case m.loopSetup.Single():
				return m.startRunTicket(m.loopSetup.Epic(), "")
			default:
				return m.startRunLoop(m.loopSetup.Epic())
			}
		}
		return m, cmd

	case viewRunOnce:
		var cmd tea.Cmd
		m.runOnce, cmd = m.runOnce.Update(msg)
		if m.runOnce.Done() {
			if m.runOnce.Cancelled() {
				return m.toMenu(), nil
			}
			return m.startRunTicket(m.runOnce.Selected(), m.runOnce.Provider())
		}
		return m, cmd

	case viewStatus:
		if isBack(msg) {
			if m.statusCancel != nil {
				m.statusCancel()
				m.statusCancel = nil
			}
			m.statusBusy = false
			return m.toMenu(), nil
		}
		if m.statusBusy {
			return m, nil
		}
		if msg.String() == "r" {
			ctx, cancel := context.WithCancel(m.baseCtx)
			m.statusCancel = cancel
			m.statusBusy = true
			m.statusNote = ""
			return m, tea.Batch(m.spin.Tick, m.reconcileCmd(ctx))
		}
		var cmd tea.Cmd
		m.status, cmd = m.status.Update(msg)
		return m, cmd

	case viewLogs:
		if isBack(msg) {
			return m.toMenu(), nil
		}
		var cmd tea.Cmd
		m.logs, cmd = m.logs.Update(msg, m.actions.LogContent)
		return m, cmd

	case viewVersion, viewError:
		if isBack(msg) {
			return m.toMenu(), nil
		}
		return m, nil

	case viewSettings:
		if m.settings.AtRoot() && (msg.String() == "esc" || msg.String() == "q") {
			return m.toMenu(), nil
		}
		var cmd tea.Cmd
		m.settings, cmd = m.settings.Update(msg)
		return m, cmd

	case viewDryRun:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if !m.busy && isBack(msg) {
			return m.toMenu(), nil
		}
		return m, nil

	case viewReset:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.busy {
				return m, nil
			}
			return m.toMenu(), nil
		case "enter":
			if m.busy {
				return m, nil
			}
			if m.result != "" {
				return m.toMenu(), nil
			}
			id := strings.TrimSpace(m.reset.Value())
			if id == "" {
				return m, nil
			}
			m.busy = true
			return m, tea.Batch(m.spin.Tick, m.resetCmd(id))
		}
		var cmd tea.Cmd
		m.reset, cmd = m.reset.Update(msg)
		return m, cmd

	case viewRunning:
		if m.dash.done() {
			return m.handleSummaryKey(msg)
		}

		if msg.String() == "q" || msg.String() == "ctrl+c" {
			if m.loopCancel != nil {
				m.loopCancel()
			}
			m.dash = m.dash.markStopping()
			return m, nil
		}
		var cmd tea.Cmd
		m.dash, cmd = applyDashCmd(m.dash, msg)
		return m, cmd
	}
	return m, nil
}

func (m appModel) toMenu() appModel {
	m.view = viewMenu
	if m.subReturn == viewMore {
		m.view = viewMore
	}
	m.result = ""
	m.busy = false
	m.info = m.actions.MenuInfo()
	return m
}

func (m appModel) selectAction(a menuAction) (tea.Model, tea.Cmd) {
	m.subReturn = m.view
	switch a {
	case actRun:
		m.loopSetup = newLoopSetupModel(m.baseCtx, m.actions, m.styles, m.info, m.width, m.height)
		m.view = viewRunLoop
		return m, textinput.Blink

	case actRunOnce:
		m.runOnce = newRunOnceModel(m.baseCtx, m.actions, m.styles, m.info, m.width, m.height)
		m.view = viewRunOnce
		return m, textinput.Blink

	case actMore:
		m.view = viewMore
		m.moreCursor = 0
		return m, nil

	case actBack:
		m.view = viewMenu
		m.info = m.actions.MenuInfo()
		return m, nil

	case actStatus:
		m.status = m.buildStatusTable()
		m.statusBusy = false
		m.statusNote = ""
		m.view = viewStatus
		return m, nil

	case actLogs:
		m.logs = newLogsModel(m.styles, m.actions.LogRuns(), m.width, m.height, m.actions.LogContent)
		m.view = viewLogs
		return m, nil

	case actVersion:
		m.view = viewVersion
		return m, nil

	case actOnboarding:
		m.onboard = newOnboardingModel(m.baseCtx, m.actions, m.styles, m.width, m.height)
		m.view = viewOnboarding
		return m, textinput.Blink

	case actSettings:
		m.settings = newSettingsHubModel(m.actions, m.styles, m.width, m.height)
		m.view = viewSettings
		return m, nil

	case actDryRun:
		m.busy = true
		m.result = ""
		m.view = viewDryRun
		return m, tea.Batch(m.spin.Tick, m.dryRunCmd(m.baseCtx))

	case actReset:
		m.reset.SetValue("")
		m.reset.Placeholder = exampleID(m.info.Prefix)
		m.reset.Focus()
		m.result = ""
		m.busy = false
		m.view = viewReset
		return m, textinput.Blink

	case actQuit:
		return m, tea.Quit
	}
	return m, nil
}

func (m appModel) startRunLoop(epic string) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.loopCancel = cancel
	m.subReturn = viewMenu
	m.dash = freshDash(m.width, m.height)
	m.view = viewRunning
	return m, tea.Batch(m.dash.Init(), m.runLoopCmd(ctx, epic))
}

func (m appModel) runLoopCmd(ctx context.Context, epic string) tea.Cmd {
	actions, r := m.actions, m.renderer
	return func() tea.Msg {
		actions.RunLoop(ctx, epic, r)
		return nil
	}
}

// handleSummaryKey drives the recap screen's recovery keys: r resumes the selected
// ticket, b checks out its branch, x resets it (guarded by a two-key confirm).
// Navigation and "o open PR" stay delegated to the dash; enter/q/esc return to the
// menu. Recovery keys appear only for rows where they apply.
func (m appModel) handleSummaryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if id := m.dash.pendingResetID(); id != "" {
		if msg.String() == "x" || msg.String() == "y" {
			m.dash = m.dash.clearResetConfirm()
			return m, m.resetFromSummaryCmd(m.baseCtx, id)
		}
		m.dash = m.dash.clearResetConfirm()
		return m, nil
	}

	sel, hasSel := m.dash.selectedResult()
	switch {
	case msg.String() == "o":
		m.dash = applyDash(m.dash, msg)
		return m, nil
	case msg.String() == "r" && hasSel && recoverable(sel):
		return m.startRunTicket(sel.ID, "")
	case msg.String() == "b" && hasSel && sel.Branch != "":
		return m, m.checkoutFromSummaryCmd(m.baseCtx, sel.ID)
	case msg.String() == "x" && hasSel && recoverable(sel):
		m.dash = m.dash.askResetConfirm(sel.ID)
		return m, nil
	case isBack(msg):
		return m.toMenu(), nil
	default:
		m.dash = applyDash(m.dash, msg)
		return m, nil
	}
}

func (m appModel) checkoutFromSummaryCmd(ctx context.Context, id string) tea.Cmd {
	actions := m.actions
	return func() tea.Msg {
		branch, err := actions.CheckoutBranch(ctx, id)
		if err != nil {
			return recoveryDoneMsg{note: "✗ checkout failed: " + err.Error(), err: err}
		}
		return recoveryDoneMsg{note: "✓ checked out " + branch + " — your WIP is here when you exit trau"}
	}
}

func (m appModel) resetFromSummaryCmd(ctx context.Context, id string) tea.Cmd {
	actions := m.actions
	return func() tea.Msg {
		if err := actions.Reset(ctx, id); err != nil {
			return recoveryDoneMsg{note: "✗ reset failed: " + err.Error(), err: err}
		}
		return recoveryDoneMsg{note: "✓ reset " + id + " — it will be picked again on the next run", resetID: id}
	}
}

func (m appModel) startRunTicket(id, provider string) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.loopCancel = cancel
	m.subReturn = viewMenu
	m.dash = freshDash(m.width, m.height)
	m.view = viewRunning
	return m, tea.Batch(m.dash.Init(), m.runTicketCmd(ctx, id, provider))
}

func (m appModel) runTicketCmd(ctx context.Context, id, provider string) tea.Cmd {
	actions, r := m.actions, m.renderer
	return func() tea.Msg {
		actions.RunTicket(ctx, id, provider, r)
		return nil
	}
}

func (m appModel) dryRunCmd(ctx context.Context) tea.Cmd {
	actions := m.actions
	return func() tea.Msg {
		id, err := actions.DryRun(ctx)
		return dryRunDoneMsg{id: id, err: err}
	}
}

func (m appModel) resetCmd(id string) tea.Cmd {
	actions, ctx := m.actions, m.baseCtx
	return func() tea.Msg {
		err := actions.Reset(ctx, id)
		return resetDoneMsg{id: id, err: err}
	}
}

func (m appModel) reconcileCmd(ctx context.Context) tea.Cmd {
	actions := m.actions
	return func() tea.Msg {
		cleared, err := actions.Reconcile(ctx)
		return reconcileDoneMsg{cleared: cleared, err: err}
	}
}

func (m appModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m appModel) render() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}
	switch m.view {
	case viewOnboarding:
		return m.onboard.View()
	case viewRunning:
		return m.dash.render()
	case viewStatus:
		body := m.status.View()
		switch {
		case m.statusBusy:
			body += "\n\n" + m.spin.View() + " reconciling against the tracker…"
		case m.statusNote != "":
			body += "\n\n" + m.statusNote
		}
		hint := "↑↓ scroll · r reconcile · esc/q back"
		if m.statusBusy {
			hint = "reconciling… · esc to cancel"
		}
		return m.renderCard("Status", body, hint)
	case viewLogs:
		return m.logs.View()
	case viewVersion:
		return m.renderCard("Version", "trau "+m.info.Version, "esc/q back")
	case viewDryRun:
		return m.renderBusy("Dry run", "asking Linear for the next eligible ticket")
	case viewReset:
		return m.renderReset()
	case viewRunLoop:
		return m.renderCard("Run loop", m.loopSetup.body(m.spin.View()), m.loopSetup.hint())
	case viewRunOnce:
		return m.renderCard("Run once", m.runOnce.body(m.spin.View()), m.runOnce.hint())
	case viewMore:
		return m.renderMore()
	case viewSettings:
		return m.settings.View()
	case viewError:
		return m.renderCard("Error", m.styles.Error.Render(m.errMsg), "esc/q back")
	default:
		return m.renderMenu()
	}
}

const menuCardW = 50

func (m appModel) renderMenu() string {
	s := m.styles

	header := joinEnds(
		s.SummaryTitle.Render("trau"),
		s.Subtle.Render("v"+firstNonEmpty(m.info.Version, "dev")),
		menuCardW,
	)
	tagline := s.Subtle.Render("autonomous ticket loop")
	var contextRows []string
	for _, line := range m.contextLines() {
		contextRows = append(contextRows, s.Help.Render(truncate(line, menuCardW)))
	}
	context := strings.Join(contextRows, "\n")

	rows := m.menuRows(m.items, m.cursor)

	head := []string{header, tagline}
	if m.info.Resume.Active() {
		head = append(head, s.Warning.Render(truncate(m.info.Resume.Line(), menuCardW)))
	}
	head = append(head, context, "")
	body := strings.Join(head, "\n") + "\n" + strings.Join(rows, "\n")
	card := s.SummaryCard.Render(body)
	hint := s.Help.Render("↑↓ move · enter select · q quit")

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, card, hint))
}

func (m appModel) renderMore() string {
	s := m.styles

	header := joinEnds(
		s.SummaryTitle.Render("More"),
		s.Subtle.Render("v"+firstNonEmpty(m.info.Version, "dev")),
		menuCardW,
	)
	tagline := s.Subtle.Render("status · maintenance · build info")
	rows := m.menuRows(m.moreItems, m.moreCursor)

	body := strings.Join([]string{header, tagline, ""}, "\n") +
		"\n" + strings.Join(rows, "\n")
	card := s.SummaryCard.Render(body)
	hint := s.Help.Render("↑↓ move · enter select · esc back")

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, card, hint))
}

func (m appModel) menuRows(items []menuItem, cursor int) []string {
	s := m.styles
	rows := make([]string, 0, len(items)+1)
	for i, it := range items {
		if it.action == actMore {
			rows = append(rows, s.Help.Render(strings.Repeat("─", menuCardW)))
		}
		marker := "  "
		labelStyle := lipgloss.NewStyle().Foreground(colorSubtle)
		descStyle := s.Help
		if i == cursor {
			marker = s.Info.Render("▸ ")
			labelStyle = s.Header
			descStyle = s.Subtle
		}
		pad := 14 - len([]rune(it.title))
		if pad < 1 {
			pad = 1
		}
		row := marker + labelStyle.Render(it.title)
		if it.desc != "" {
			row += strings.Repeat(" ", pad) + descStyle.Render(it.desc)
		}
		rows = append(rows, row)
	}
	return rows
}

// contextLines is the at-a-glance MenuInfo, split across two rows so every
// field stays visible inside menuCardW even with a long model name. Row 1 is
// provider · model; row 2 is base · auto-merge · in-flight · done.
func (m appModel) contextLines() []string {
	top := []string{firstNonEmpty(m.info.Provider, "?")}
	if m.info.Model != "" {
		top = append(top, m.info.Model)
	}

	merge := "auto-merge off"
	if m.info.AutoMerge {
		merge = "auto-merge on"
	}
	bottom := make([]string, 0, 4)
	if m.info.Base != "" {
		bottom = append(bottom, m.info.Base)
	}
	bottom = append(bottom,
		merge,
		fmt.Sprintf("%d in-flight", m.info.InFlight),
		fmt.Sprintf("%d done", m.info.Done),
	)

	return []string{
		strings.Join(top, " · "),
		strings.Join(bottom, " · "),
	}
}

func (m appModel) renderCard(title, body, hint string) string {
	card := m.styles.SummaryCard.MaxWidth(m.width).Render(
		m.styles.SummaryTitle.Render(title) + "\n\n" + body,
	)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, card, m.styles.Help.Render(hint)))
}

func (m appModel) renderBusy(title, caption string) string {
	var body string
	hint := "esc/q back"
	if m.busy {
		body = m.spin.View() + " " + m.styles.Subtle.Render(caption+"…")
		hint = "working…"
	} else {
		body = m.result
	}
	return m.renderCard(title, body, hint)
}

func (m appModel) renderReset() string {
	var body, hint string
	switch {
	case m.busy:
		body = m.spin.View() + " " + m.styles.Subtle.Render("resetting…")
		hint = "working…"
	case m.result != "":
		body = m.result
		hint = "enter/esc back"
	default:
		body = "Enter the ticket ID to reset (e.g. " + exampleID(m.info.Prefix) + "):\n\n" + m.reset.View()
		hint = "enter confirm · esc back"
	}
	return m.renderCard("Reset ticket", body, hint)
}

func (m appModel) buildStatusTable() table.Model {
	rows := m.actions.StatusRows()
	idW, phaseW, tokW, costW := 9, 12, 10, 9
	titleW := m.width - (idW + phaseW + tokW + costW) - 18
	if titleW < 12 {
		titleW = 12
	}
	cols := []table.Column{
		{Title: "ID", Width: idW},
		{Title: "Title", Width: titleW},
		{Title: "Phase", Width: phaseW},
		{Title: "Tokens", Width: tokW},
		{Title: "Cost", Width: costW},
	}
	var trows []table.Row
	for _, r := range rows {
		phase := r.Phase
		if phase == "" {
			phase = "?"
		}
		trows = append(trows, table.Row{
			r.ID,
			truncate(firstNonEmpty(r.Title, "—"), titleW),
			phase,
			fmtTokens(r.Tokens),
			fmtCostCell(r.Cost, r.CostMetered),
		})
	}
	h := len(trows) + 1
	if h < 2 {
		h = 2
	}
	if h > 16 {
		h = 16
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(trows),
		table.WithFocused(true),
		table.WithHeight(h),
	)
	st := table.DefaultStyles()
	st.Header = st.Header.Bold(true).Foreground(colorSubtle).
		BorderBottom(true).BorderForeground(colorFaint)
	st.Selected = st.Selected.Foreground(lipgloss.Color("#FFFFFF")).
		Background(colorBrand).Bold(false)
	t.SetStyles(st)
	return t
}

func isBack(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "esc", "enter", "q", "b":
		return true
	}
	return false
}

func freshDash(w, h int) model {
	d := initialModel(nil)
	if w > 0 && h > 0 {
		d = applyDash(d, tea.WindowSizeMsg{Width: w, Height: h})
	}
	return d
}

func applyDash(d model, msg tea.Msg) model {
	nm, _ := d.Update(msg)
	return nm.(model)
}

func applyDashCmd(d model, msg tea.Msg) (model, tea.Cmd) {
	nm, cmd := d.Update(msg)
	return nm.(model), cmd
}
