package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/notify"
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

	// StartPlan runs one planning round on the raw idea — literal multi-line text,
	// or a path to a file containing the idea — and returns the outcome for the Plan
	// screen to render. It is cancellable via ctx.
	StartPlan(ctx context.Context, idea string) (PlanOutcome, error)

	// AnswerPlan records the answers to the previous round's questions on the plan
	// session at dir, then runs the next planning round as a fresh process and
	// returns its outcome. It is cancellable via ctx.
	AnswerPlan(ctx context.Context, dir string, answers []PlanAnswer) (PlanOutcome, error)

	// RevisePlan records a free-text change request against the drafted PRD in the
	// plan session at dir and runs a fresh revision round, returning the revised PRD
	// outcome. It is cancellable via ctx.
	RevisePlan(ctx context.Context, dir, note string) (PlanOutcome, error)

	// ApprovePlan approves the drafted PRD in the plan session at dir, advancing the
	// checkpoint to prd_ready, then publishes it to the tracker as an epic when the
	// tracker supports it. The outcome reports the created epic, or that publish was
	// skipped and the plan stayed local. It is cancellable via ctx.
	ApprovePlan(ctx context.Context, dir string) (PublishOutcome, error)

	// SlicePlan runs the slice round on the published plan session at dir: a fresh
	// agent process drafts the epic's tracer-bullet child issues and returns them
	// for the review list. Drafts only — nothing is created or persisted until
	// CreateSlices. It is cancellable via ctx.
	SlicePlan(ctx context.Context, dir string) (PlanOutcome, error)

	// CreateSlices creates the reviewed slice drafts as children of the plan
	// session's published epic — in the reviewed order, each carrying the
	// configured ready label — and advances the checkpoint to sliced. It is
	// cancellable via ctx.
	CreateSlices(ctx context.Context, dir string, slices []PlanSlice) (SliceOutcome, error)

	// ListPlans returns every durable plan session for the Plan screen's list,
	// resumable ones first. It reads local session state directly, so it needs no
	// context and never blocks on the network.
	ListPlans() []PlanSession

	// ResumePlan re-enters the plan session at dir at the step its checkpoint
	// dictates — re-asking the pending questions, reopening the PRD for review,
	// re-drafting the slices, or a note for a step not yet wired — and returns the
	// outcome the Plan screen renders. It is cancellable via ctx.
	ResumePlan(ctx context.Context, dir string) (PlanOutcome, error)

	// AbortPlan marks the plan session at dir aborted — a terminal side-exit that
	// writes nothing to the tracker. It is cancellable via ctx.
	AbortPlan(ctx context.Context, dir string) error
}

// sessionReporter is the optional capability an Actions implementation exposes so
// the menu shell can report the transitions only it sees — back to idle on a
// return to a browse screen, and stopping the moment a run is interrupted. The
// run-driven grazing/working/parked states are reported by the loop and pipeline,
// not here. A non-implementing Actions makes these calls no-ops.
type sessionReporter interface {
	ReportIdle()
	ReportStopping()
}

func (m appModel) reportIdle() {
	if r, ok := m.actions.(sessionReporter); ok {
		r.ReportIdle()
	}
}

func (m appModel) reportStopping() {
	if r, ok := m.actions.(sessionReporter); ok {
		r.ReportStopping()
	}
}

// PublishOutcome reports what approving a PRD did with the tracker. Epic is the
// created epic identifier; Published is false when the tracker lacks the
// hierarchical-create capability and the plan stayed local at prd_ready.
type PublishOutcome struct {
	Epic      string
	Published bool
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
	Version string
	// Repo is the basename of the resolved repo root — the location label every
	// trau surface marks itself with. RepoPath is the full resolved root, shown
	// (~-abbreviated) only in the menu context block.
	Repo          string
	RepoPath      string
	Provider      string
	Model         string
	Base          string
	Prefix        string
	MaxIterations int
	AutoMerge     bool
	Notify        bool
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

// StatusRow is one ticket's saved state, rendered in the attention queue. Branch
// and FailureReason back the rail's recovery verbs (checkout / reason reveal);
// Updated dates the row so the rail can show its age.
type StatusRow struct {
	ID            string
	Title         string
	Phase         string
	PRURL         string
	Branch        string
	FailureReason string
	FailureClass  string
	Tokens        int
	Cost          float64
	// CostMetered is false when some phase logged tokens but no per-call dollar
	// cost (a kimi/codex subscription call), so Cost is a lower bound shown as
	// "n/a"/"+" rather than a measured "$0".
	CostMetered bool
	Updated     time.Time
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
	viewHandle
	viewRunLoop
	viewRunOnce
	viewRunning
	viewError
	viewSettings
	viewPlan
)

type menuAction int

const (
	actRun menuAction = iota
	actRunOnce
	actDryRun
	actPlan
	actStatus
	actLogs
	actReset
	actVersion
	actOnboarding
	actSettings
	actMore
	actBack
	actQuit
	actHandle
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
	// statusActionMsg carries the outcome of a Status-screen recovery verb (b/x):
	// note is the line to surface, and a successful reset drops the checkpoint so
	// the reloaded rows reflect it.
	statusActionMsg struct {
		note string
		err  error
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

	// attnRows are the needs-attention checkpoints backing the conditional top-of-
	// menu Handle item; handleRow is the ticket the Handle confirm screen acts on,
	// with handleConfirm arming its two-key destructive reset.
	attnRows      []QueueRow
	handleRow     QueueRow
	handleConfirm bool

	statusRows      []QueueRow
	statusCursor    int
	statusConfirmID string
	reset           textinput.Model
	spin            spinner.Model
	busy            bool
	result          string
	errMsg          string
	statusBusy      bool
	statusNote      string
	statusCancel    context.CancelFunc

	logs logsModel

	dash       model
	loopCancel context.CancelFunc

	onboard   onboardingModel
	loopSetup loopSetupModel
	runOnce   runOnceModel
	settings  settingsHubModel
	plan      planModel

	help    helpModel
	palette paletteModel

	mouseOff bool

	// focused tracks terminal focus so screens that fire desktop nudges (the Plan
	// screen) only do so while the user is away. The terminal starts focused.
	focused bool

	// notifier is the desktop notifier each fresh dashboard reports through
	// (nil = NOTIFY off); see internal/notify and model.notifier.
	notifier notify.Notifier
}

// baseMenuItems is the static main-menu action list. The Handle recovery item is
// prepended dynamically by withMenuItems when a checkpoint needs attention.
func baseMenuItems() []menuItem {
	return []menuItem{
		{actRun, "Run loop", "next ready ticket → PR"},
		{actRunOnce, "Run once", "pick one ticket to run"},
		{actDryRun, "Dry run", "preview the next ticket"},
		{actPlan, "Plan", "raw idea → PRD"},
		{actMore, "More…", "status · reset · version"},
		{actQuit, "Quit", ""},
	}
}

func newAppModel(ctx context.Context, actions Actions, renderer *TUI) appModel {
	items := baseMenuItems()
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
	setScreenRepo(info.Repo)

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
		focused:   true,
	}
	m = m.withMenuItems()
	if info.Notify {
		m.notifier = notify.OS()
	}
	if actions.OnboardingNeeded() {
		m.view = viewOnboarding
		m.onboard = newOnboardingModel(ctx, actions, m.styles, 0, 0)
	}
	return m
}

func (m appModel) Init() tea.Cmd { return tea.Batch(m.spin.Tick, tea.RequestBackgroundColor) }

func (m appModel) restyled() appModel {
	m.styles = DefaultStyles()
	m.spin.Style = m.styles.Spinner
	m.onboard.styles = m.styles
	m.settings = m.settings.restyled(m.styles)
	m.dash = m.dash.restyled()
	return m
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		setThemeBackground(msg.IsDark())
		return m.restyled(), nil

	case saveConfigDoneMsg:
		var cmd tea.Cmd
		m.settings, cmd = m.settings.Update(msg)
		if msg.err == nil {
			applyThemeFromItems(m.actions.ConfigItems())
			m = m.restyled()
		}
		return m, cmd

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.dash = applyDash(m.dash, msg)
		m.onboard, _ = m.onboard.Update(msg)
		m.loopSetup, _ = m.loopSetup.Update(msg)
		m.runOnce, _ = m.runOnce.Update(msg)
		m.settings, _ = m.settings.Update(msg)
		m.logs, _ = m.logs.Update(msg, m.actions.LogContent)
		m.plan, _ = m.plan.Update(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.FocusMsg, tea.BlurMsg:
		_, m.focused = msg.(tea.FocusMsg)
		m.plan.focused = m.focused
		// The away-recap only lives on the running dashboard, so only route focus
		// there. Elsewhere a blur/focus can't produce a recap the user would see, and
		// routing it would let one linger for the next time the dashboard is shown.
		if m.view != viewRunning {
			return m, nil
		}
		var cmd tea.Cmd
		m.dash, cmd = applyDashCmd(m.dash, msg)
		return m, cmd

	case tea.MouseClickMsg:
		return m.handleMouseClick(msg)

	case logMsg, eventMsg, ticketMsg, titleMsg, phaseStartMsg, ticketDoneMsg, loopDoneMsg, recoveryDoneMsg:
		var cmd tea.Cmd
		m.dash, cmd = applyDashCmd(m.dash, msg)
		// Refresh the rail snapshot on ticket boundaries so other tickets reflect
		// the store while the active one stays live-overlaid by the dash.
		switch msg.(type) {
		case ticketMsg, ticketDoneMsg:
			m.dash = m.dash.withQueue(m.buildQueueRows())
		}
		// The Plan screen tails its own agent, so feed it the transcript-path event
		// while it is open.
		if m.view == viewPlan {
			if _, ok := msg.(eventMsg); ok {
				var pcmd tea.Cmd
				m.plan, pcmd = m.plan.Update(msg)
				cmd = tea.Batch(cmd, pcmd)
			}
		}
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
		note, _ := reconcileNote(msg)
		m.statusNote = note
		m = m.loadStatusRows()
		return m, nil

	case statusActionMsg:
		m.statusNote = msg.note
		if m.view == viewStatus {
			m = m.loadStatusRows()
		}
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
		if m.view == viewPlan {
			m.plan, cmd = m.plan.Update(msg)
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	var cmd tea.Cmd
	switch m.view {
	case viewOnboarding:
		m.onboard, cmd = m.onboard.Update(msg)
		if m.onboard.Done() {
			m = m.toMenu()
		}
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
	case viewPlan:
		m.plan, cmd = m.plan.Update(msg)
	case viewRunning:
		m.dash, cmd = applyDashCmd(m.dash, msg)
	}
	return m, cmd
}

func (m appModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The ? help overlay is modal and global: while open it owns every key, and
	// it opens on any screen that isn't mid-text-entry (where ? is a literal
	// character). One interception here keeps behavior identical on every view.
	if m.help.active {
		lay := layoutHelp(m.styles, m.helpFor(), m.help.filter, m.width, m.height)
		var closed bool
		m.help, closed = m.help.update(msg, lay)
		if closed {
			m.help = helpModel{}
		}
		return m, nil
	}
	if msg.String() == "?" && !m.editing() && m.helpFor().hasKeys() {
		m.help = helpModel{active: true}
		return m, nil
	}

	// The command palette is the other global modal: ctrl+p opens it from every
	// screen, : opens it anywhere ? would (outside free-text entry). While open it
	// owns every key; enter runs the highlighted command and closes.
	if m.palette.active {
		lay := m.paletteLayoutNow()
		var chosen *paletteCommand
		var closed bool
		m.palette, chosen, closed = m.palette.update(msg, lay)
		if chosen != nil {
			run := chosen.run
			m.palette = paletteModel{}
			return run(m)
		}
		if closed {
			m.palette = paletteModel{}
		}
		return m, nil
	}
	if msg.String() == "ctrl+p" || (msg.String() == ":" && !m.editing()) {
		m.palette = paletteModel{active: true}
		// ctrl+p opens the palette from anywhere, including over the peek preview;
		// dismiss the preview so the two modals don't stack.
		if m.view == viewRunning {
			m.dash.peek = false
		}
		return m, nil
	}
	if msg.String() == "ctrl+t" {
		m.mouseOff = !m.mouseOff
		setMouseEnabled(!m.mouseOff)
		return m, nil
	}

	switch m.view {
	case viewOnboarding:
		var cmd tea.Cmd
		m.onboard, cmd = m.onboard.Update(msg)
		if m.onboard.Done() {
			m = m.toMenu()
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
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "q":
			return m.toMainMenu(), nil
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
		return m.afterLoopSetup(cmd)

	case viewRunOnce:
		var cmd tea.Cmd
		m.runOnce, cmd = m.runOnce.Update(msg)
		return m.afterRunOnce(cmd)

	case viewPlan:
		var cmd tea.Cmd
		m.plan, cmd = m.plan.Update(msg)
		return m.afterPlan(cmd)

	case viewStatus:
		return m.handleStatusKey(msg)

	case viewHandle:
		return m.handleHandleKey(msg)

	case viewLogs:
		if isBack(msg) {
			switch m.subReturn {
			case viewRunning:
				m.view = viewRunning
				return m, nil
			case viewStatus:
				m.view = viewStatus
				return m, nil
			}
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
		if m.settings.AtRoot() && isBack(msg) {
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
		case "q":
			if m.result != "" {
				return m.toMenu(), nil
			}
		case "enter":
			if m.busy || m.result != "" {
				return m, nil
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
			return m.handleQueueKey(msg, false)
		}
		return m.handleRunningKey(msg)
	}
	return m, nil
}

// handleRunningKey drives the live dashboard: the queue rail owns ↑↓ selection
// and the read-only verbs (o open, l logs); the mutating verbs are withheld live
// (queueVerbs) since they would disturb the running ticket, so they act only from
// the recap/Status. Everything else — watch, follow, page, exit stream — goes to
// the dash. q arms a two-key stop confirm; ctrl+c is the unguarded emergency stop.
// afterLoopSetup starts the run the loop-setup step resolved to, once its Update
// reports Done — the single dispatch both the keyboard and a click drive through.
func (m appModel) afterLoopSetup(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if !m.loopSetup.Done() {
		return m, cmd
	}
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

// afterRunOnce starts the ticket the run-once step resolved to, once Done.
func (m appModel) afterRunOnce(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if !m.runOnce.Done() {
		return m, cmd
	}
	if m.runOnce.Cancelled() {
		return m.toMenu(), nil
	}
	return m.startRunTicket(m.runOnce.Selected(), m.runOnce.Provider())
}

// afterPlan returns to the menu once the Plan screen backs out; otherwise it keeps
// the screen active and propagates its command (starting a round, scrolling).
func (m appModel) afterPlan(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if m.plan.Cancelled() {
		return m.toMenu(), nil
	}
	return m, cmd
}

func (m appModel) handleRunningKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// ctrl+c is the emergency stop and always wins, even mid-filter, so it can't be
	// swallowed by the filter input. It bypasses the two-key stop guard entirely.
	if msg.String() == "ctrl+c" {
		if m.loopCancel != nil {
			m.loopCancel()
		}
		m.reportStopping()
		m.dash = m.dash.disarmStop().markStopping()
		return m, nil
	}
	// A stop is armed (a prior q): the confirming second q cancels the run; esc or
	// any other key disarms. The disarming key is swallowed — it can't also act —
	// mirroring the reset guard.
	if m.dash.stopArmed() {
		m.dash = m.dash.disarmStop()
		if msg.String() == "q" {
			if m.loopCancel != nil {
				m.loopCancel()
			}
			m.reportStopping()
			m.dash = m.dash.markStopping()
		}
		return m, nil
	}
	m.dash = m.dash.clearToast().dismissRecap()
	// While the dash filter input is capturing, every other key belongs to it —
	// route straight to the dash so an action key (q, o, j) narrows the feed instead
	// of firing the action or moving the rail.
	if m.dash.filterActive() {
		var cmd tea.Cmd
		m.dash, cmd = applyDashCmd(m.dash, msg)
		return m, cmd
	}
	// The peek preview is modal: while open every key (including q) belongs to it,
	// so route straight to the dash before the q stop and rail nav below.
	if m.dash.peeking() {
		var cmd tea.Cmd
		m.dash, cmd = applyDashCmd(m.dash, msg)
		return m, cmd
	}
	if msg.String() == "q" {
		m.dash = m.dash.armStop()
		return m, nil
	}
	// When the rail isn't drawn — watch mode is full-screen, or the terminal is
	// too narrow to spare it — its keys go to the dash rather than acting on a
	// selection the user can't see.
	if !m.dash.railVisible() {
		var cmd tea.Cmd
		m.dash, cmd = applyDashCmd(m.dash, msg)
		return m, cmd
	}
	switch msg.String() {
	case "up", "k":
		m.dash = m.dash.movedQueueCursor(-1)
		return m, nil
	case "down", "j":
		m.dash = m.dash.movedQueueCursor(1)
		return m, nil
	case "o", "l":
		return m.handleQueueKey(msg, true)
	}
	var cmd tea.Cmd
	m.dash, cmd = applyDashCmd(m.dash, msg)
	return m, cmd
}

func (m appModel) toMenu() appModel {
	m.view = viewMenu
	if m.subReturn == viewMore {
		m.view = viewMore
	}
	m.result = ""
	m.busy = false
	m.reportIdle()
	m.info = m.actions.MenuInfo()
	return m.withMenuItems()
}

// toMainMenu returns to the top-level menu unconditionally (unlike toMenu, which
// honors a viewMore subReturn), refreshing the context line and the Handle item.
func (m appModel) toMainMenu() appModel {
	m.view = viewMenu
	m.reportIdle()
	m.info = m.actions.MenuInfo()
	return m.withMenuItems()
}

// withMenuItems rebuilds the main-menu list from the current checkpoints: the
// static actions, topped by a Handle recovery item whenever one or more tickets
// carry a classified failure. Called on every entry to the menu so the item
// appears and vanishes as tickets fault and recover; the cursor rests on it when
// present so recovery is one keypress away.
func (m appModel) withMenuItems() appModel {
	m.attnRows = m.attentionRows()
	items := baseMenuItems()
	if len(m.attnRows) > 0 {
		items = append([]menuItem{m.handleItem()}, items...)
		m.cursor = 0
	}
	m.items = items
	if m.cursor >= len(items) {
		m.cursor = len(items) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	return m
}

// attentionRows returns the checkpoints carrying a classified failure, in
// attention order (needs-human first, then by id), backing the Handle item.
func (m appModel) attentionRows() []QueueRow {
	var rows []QueueRow
	for _, r := range m.buildQueueRows() {
		if r.needsAttention() {
			rows = append(rows, r)
		}
	}
	sortQueue(rows)
	return rows
}

// handleItem builds the top-of-menu recovery entry: a single stuck ticket names
// its id and class; several collapse to a count. The class glyph and color are
// applied in menuRows so the row reads ⚠/⏸ like the queue rail.
func (m appModel) handleItem() menuItem {
	if len(m.attnRows) == 1 {
		r := m.attnRows[0]
		return menuItem{action: actHandle, title: "Handle " + r.ID + " — " + failureLabel(r.FailureClass)}
	}
	return menuItem{action: actHandle, title: fmt.Sprintf("Handle %d tickets — need attention", len(m.attnRows))}
}

// openHandle enters the Handle confirm screen for row, remembering where to
// return (the main menu, or Status when reached from a needs-attention row).
func (m appModel) openHandle(row QueueRow, ret appView) appModel {
	m.handleRow = row
	m.handleConfirm = false
	m.busy = false
	m.result = ""
	m.subReturn = ret
	m.view = viewHandle
	return m
}

// exitHandle leaves the Handle confirm, clearing its transient state and routing
// back to wherever it was opened from — reloading Status rows so a completed reset
// is reflected there.
func (m appModel) exitHandle() appModel {
	m.handleConfirm = false
	m.busy = false
	m.result = ""
	if m.subReturn == viewStatus {
		m.view = viewStatus
		m.statusConfirmID = ""
		return m.loadStatusRows()
	}
	return m.toMainMenu()
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

	case actPlan:
		m.plan = newPlanModel(m.baseCtx, m.actions, m.styles, m.width, m.height)
		m.plan.notifier = m.notifier
		m.plan.focused = m.focused
		m.view = viewPlan
		return m, m.plan.Init()

	case actMore:
		m.view = viewMore
		m.moreCursor = 0
		return m, nil

	case actBack:
		return m.toMainMenu(), nil

	case actStatus:
		m.statusCursor = 0
		m.statusConfirmID = ""
		m = m.loadStatusRows()
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

	case actHandle:
		if len(m.attnRows) == 1 {
			return m.openHandle(m.attnRows[0], viewMenu), nil
		}
		return m.selectAction(actStatus)

	case actQuit:
		return m, tea.Quit
	}
	return m, nil
}

func (m appModel) startRunLoop(epic string) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.loopCancel = cancel
	m.subReturn = viewMenu
	m.dash = freshDash(m.width, m.height, m.info.Base).withNotifier(m.notifier).withQueue(m.buildQueueRows())
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

// handleQueueKey drives the recovery verbs for the dash-backed attention queue —
// the session recap and, when live, the running rail. It acts on the selected
// row: o open PR, l jump to logs, r resume, b checkout branch, x reset (two-key
// confirm). live withholds the tree/loop-mutating verbs (queueVerbs) so mid-run
// actions can't disturb the running ticket; navigation and back stay uniform.
func (m appModel) handleQueueKey(msg tea.KeyPressMsg, live bool) (tea.Model, tea.Cmd) {
	if id := m.dash.pendingResetID(); id != "" {
		if msg.String() == "x" || msg.String() == "y" {
			m.dash = m.dash.clearResetConfirm()
			return m, m.resetFromSummaryCmd(m.baseCtx, id)
		}
		m.dash = m.dash.clearResetConfirm()
		return m, nil
	}

	sel, hasSel := m.dash.selectedRow()
	open, logs, resume, branch, reset := queueVerbs(sel, live)
	switch {
	case msg.String() == "o" && hasSel && open:
		return m, m.dash.openSelectedPR()
	case msg.String() == "l" && hasSel && logs:
		return m.openLogsFor(sel.ID)
	case msg.String() == "r" && hasSel && resume:
		return m.startRunTicket(sel.ID, "")
	case msg.String() == "b" && hasSel && branch:
		return m, m.checkoutFromSummaryCmd(m.baseCtx, sel.ID)
	case msg.String() == "x" && hasSel && reset:
		m.dash = m.dash.askResetConfirm(sel.ID)
		return m, nil
	case isBack(msg) && !live:
		return m.toMenu(), nil
	default:
		m.dash = applyDash(m.dash, msg)
		return m, nil
	}
}

// handleStatusKey drives the Status browse screen, which renders the same
// attention queue as the rail and recap. R reconciles against the tracker;
// o/l/r/b/x act on the selected checkpoint with the recap's semantics (the
// screen is never mid-run, so the full recovery set applies). A pending reset is
// guarded by the two-key confirm.
func (m appModel) handleStatusKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.statusConfirmID != "" {
		id := m.statusConfirmID
		m.statusConfirmID = ""
		if msg.String() == "x" || msg.String() == "y" {
			return m, m.statusResetCmd(m.baseCtx, id)
		}
		return m, nil
	}
	if m.statusBusy {
		if isBack(msg) {
			if m.statusCancel != nil {
				m.statusCancel()
				m.statusCancel = nil
			}
			m.statusBusy = false
		}
		return m, nil
	}
	if isBack(msg) {
		return m.toMenu(), nil
	}

	sel, hasSel := m.selectedStatusRow()
	open, logs, resume, branch, reset := queueVerbs(sel, false)
	switch {
	case msg.String() == "up" || msg.String() == "k":
		m.moveStatusCursor(-1)
		return m, nil
	case msg.String() == "down" || msg.String() == "j":
		m.moveStatusCursor(1)
		return m, nil
	case msg.String() == "enter" && hasSel && sel.needsAttention():
		return m.openHandle(sel, viewStatus), nil
	case msg.String() == "R":
		ctx, cancel := context.WithCancel(m.baseCtx)
		m.statusCancel = cancel
		m.statusBusy = true
		m.statusNote = ""
		return m, tea.Batch(m.spin.Tick, m.reconcileCmd(ctx))
	case msg.String() == "o" && hasSel && open:
		return m, openURLCmd(sel.PRURL)
	case msg.String() == "l" && hasSel && logs:
		return m.openLogsFor(sel.ID)
	case msg.String() == "r" && hasSel && resume:
		return m.startRunTicket(sel.ID, "")
	case msg.String() == "b" && hasSel && branch:
		return m, m.statusCheckoutCmd(m.baseCtx, sel.ID)
	case msg.String() == "x" && hasSel && reset:
		m.statusConfirmID = sel.ID
		m.statusNote = ""
		return m, nil
	}
	return m, nil
}

// handleHandleKey drives the Handle confirm — the guided recovery for one stuck
// ticket. Per class it offers resume (paused/faulted) and the destructive reset
// (faulted/quarantined); the reset arms a two-key confirm. A finished reset shows
// its result until dismissed. esc/q cancels back to wherever it was opened from.
func (m appModel) handleHandleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	if m.result != "" {
		if isBack(msg) || msg.String() == "enter" {
			return m.exitHandle(), nil
		}
		return m, nil
	}
	// A destructive reset is armed (a prior x): the confirming x/y runs it; any
	// other key disarms, mirroring the Status reset guard.
	if m.handleConfirm {
		m.handleConfirm = false
		if msg.String() == "x" || msg.String() == "y" {
			m.busy = true
			return m, tea.Batch(m.spin.Tick, m.resetCmd(m.handleRow.ID))
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		return m.exitHandle(), nil
	case "r":
		if m.handleRow.canResume() {
			return m.startRunTicket(m.handleRow.ID, "")
		}
	case "x":
		if m.handleRow.canReset() {
			m.handleConfirm = true
		}
	}
	return m, nil
}

// handleHint is the Handle confirm footer: the per-class verbs, or the two-key
// prompt while a destructive reset is armed.
func (m appModel) handleHint() string {
	if m.handleConfirm {
		return "⚠ reset " + m.handleRow.ID + "? x again to confirm · esc cancel"
	}
	var parts []string
	if m.handleRow.canResume() {
		parts = append(parts, "r resume")
	}
	if m.handleRow.canReset() {
		parts = append(parts, "x reset")
	}
	parts = append(parts, "esc cancel")
	return strings.Join(markVerbs(parts), " · ")
}

// moveStatusCursor shifts the Status selection by delta, clamped to the
// selectable rows.
func (m *appModel) moveStatusCursor(delta int) {
	active, _ := partitionQueue(m.statusRows, false)
	m.statusCursor += delta
	if m.statusCursor >= len(active) {
		m.statusCursor = len(active) - 1
	}
	if m.statusCursor < 0 {
		m.statusCursor = 0
	}
}

func (m appModel) statusResetCmd(ctx context.Context, id string) tea.Cmd {
	actions := m.actions
	return func() tea.Msg {
		if err := actions.Reset(ctx, id); err != nil {
			return statusActionMsg{note: "✗ reset failed: " + err.Error(), err: err}
		}
		return statusActionMsg{note: "✓ reset " + id + " — it can be picked again"}
	}
}

func (m appModel) statusCheckoutCmd(ctx context.Context, id string) tea.Cmd {
	actions := m.actions
	return func() tea.Msg {
		branch, err := actions.CheckoutBranch(ctx, id)
		if err != nil {
			return statusActionMsg{note: "✗ checkout failed: " + err.Error(), err: err}
		}
		return statusActionMsg{note: "✓ checked out " + branch + " — your WIP is here when you exit trau"}
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
	m.dash = freshDash(m.width, m.height, m.info.Base).withNotifier(m.notifier).withQueue(m.buildQueueRows())
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

// reconcileNote formats a reconcile outcome for the Status note or the rail
// banner, returning whether it was an error.
func reconcileNote(msg reconcileDoneMsg) (string, bool) {
	switch {
	case msg.err != nil:
		return "✗ reconcile failed: " + msg.err.Error(), true
	case len(msg.cleared) == 0:
		return "✓ nothing stale — all checkpoints match the tracker", false
	default:
		return fmt.Sprintf("✓ cleared %d stale checkpoint(s): %s", len(msg.cleared), strings.Join(msg.cleared, ", ")), false
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
	content := m.render()
	if m.mouseOff {
		content = overlayMouseOff(m.styles, content, m.width, m.height)
	}
	v := tea.NewView(zone.Scan(content))
	v.AltScreen = true
	// The tab reflects run state only while the dashboard is up; on the menu and
	// other screens the run isn't live (you can't leave a running dashboard without
	// stopping it), so the title rests at the repo mark rather than freezing on the
	// last run's summary.
	v.WindowTitle = brandMark()
	if m.view == viewRunning {
		v.WindowTitle = m.dash.ambientTitle()
	}
	v.ReportFocus = true
	if !m.mouseOff {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m appModel) render() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}
	base := m.renderScreen()
	if m.help.active {
		return compositeHelp(m.styles, base, m.helpFor(), m.help, m.width, m.height)
	}
	if m.palette.active {
		return compositePalette(m.styles, base, m.paletteMatches(m.palette.filter), m.palette, m.width, m.height)
	}
	return base
}

func (m appModel) renderScreen() string {
	switch m.view {
	case viewOnboarding:
		return m.onboard.View()
	case viewRunning:
		return m.dash.render()
	case viewStatus:
		queueW := m.width - 8
		if queueW < 24 {
			queueW = 24
		}
		bodyH := cardBodyBudget(m.height, 2) // title + a note/spinner row
		body := renderQueue(m.styles, spinnerGlyph(m.spin), m.statusRows, m.statusCursor, queueW, bodyH, false, zoneStatusRow)
		switch {
		case m.statusBusy:
			body += "\n\n" + m.spin.View() + " reconciling against the tracker…"
		case m.statusNote != "":
			body += "\n\n" + m.styles.Subtle.Render(m.statusNote)
		}
		sel, hasSel := m.selectedStatusRow()
		hint := queueHint(sel, hasSel, false, m.statusConfirmID)
		if m.statusBusy {
			hint = "reconciling… · esc/q back"
		}
		return m.renderCard("Status", body, hint)
	case viewLogs:
		return m.logs.View()
	case viewVersion:
		return m.renderCard("Version", "trau "+m.info.Version, leafHelp("Version").footer())
	case viewDryRun:
		return m.renderBusy("Dry run", "asking Linear for the next eligible ticket")
	case viewReset:
		return m.renderReset()
	case viewHandle:
		return m.renderHandle()
	case viewRunLoop:
		return m.renderCard("Run loop", m.loopSetup.body(m.spin.View()), m.loopSetup.hint())
	case viewRunOnce:
		return m.renderCard("Run once", m.runOnce.body(m.spin.View()), m.runOnce.hint())
	case viewPlan:
		return m.plan.view(m.spin.View())
	case viewMore:
		return m.renderMore()
	case viewSettings:
		return m.settings.View()
	case viewError:
		return m.renderCard("Error", m.styles.Error.Render(m.errMsg), leafHelp("Error").footer())
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

	rows := m.menuRows(m.items, m.cursor, zoneMenuRow)

	head := []string{header, tagline}
	if m.info.Resume.Active() {
		head = append(head, s.Warning.Render(truncate(m.info.Resume.Line(), menuCardW)))
	}
	head = append(head, context, "")
	body := strings.Join(head, "\n") + "\n" + strings.Join(rows, "\n")

	return cardView(s, m.width, m.height, body, menuHelp().footer())
}

func (m appModel) renderMore() string {
	s := m.styles

	header := joinEnds(
		s.SummaryTitle.Render("More"),
		s.Subtle.Render("v"+firstNonEmpty(m.info.Version, "dev")),
		menuCardW,
	)
	tagline := s.Subtle.Render("status · maintenance · build info")
	rows := m.menuRows(m.moreItems, m.moreCursor, zoneMoreRow)

	body := strings.Join([]string{header, tagline, ""}, "\n") +
		"\n" + strings.Join(rows, "\n")

	return cardView(s, m.width, m.height, body, moreHelp().footer())
}

func (m appModel) menuRows(items []menuItem, cursor int, zonePrefix string) []string {
	s := m.styles
	rows := make([]string, 0, len(items)+1)
	for i, it := range items {
		if it.action == actMore {
			rows = append(rows, s.Help.Render(strings.Repeat("─", menuCardW)))
		}
		var row string
		if it.action == actHandle {
			row = m.handleMenuRow(i == cursor, it)
		} else {
			row = listRow(s, i == cursor, it.title, it.desc, 14)
		}
		rows = append(rows, markRow(zonePrefix, i, row))
	}
	return rows
}

// handleMenuRow renders the prominent top-of-menu recovery entry with its class
// glyph colored like the rail (⚠ faulted/quarantined, ⏸ paused) and the label in
// the warning tone, brightening under the cursor.
func (m appModel) handleMenuRow(focused bool, it menuItem) string {
	s := m.styles
	class := ""
	if len(m.attnRows) == 1 {
		class = m.attnRows[0].FailureClass
	}
	glyph, glyphStyle := attentionGlyph(s, class)
	labelStyle := s.Warning
	if focused {
		labelStyle = s.Header
	}
	return cursorMarker(s, focused) + glyphStyle.Render(glyph) + " " + labelStyle.Render(it.title)
}

// contextLines is the at-a-glance MenuInfo. The ~-abbreviated repo path leads
// (menu only), then two rows so every field stays visible inside menuCardW even
// with a long model name: provider · model, then base · auto-merge · in-flight · done.
func (m appModel) contextLines() []string {
	var lines []string
	if p := abbreviateHome(m.info.RepoPath); p != "" {
		lines = append(lines, p)
	}
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

	return append(lines,
		strings.Join(top, " · "),
		strings.Join(bottom, " · "),
	)
}

// abbreviateHome rewrites a leading home directory as ~ for the menu path row.
func abbreviateHome(path string) string {
	if path == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

func (m appModel) renderCard(title, body, hint string) string {
	return titledCardView(m.styles, m.width, m.height, title, body, hint)
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
		hint = "esc/q back"
	default:
		body = "Enter the ticket ID to reset (e.g. " + exampleID(m.info.Prefix) + "):\n\n" + m.reset.View()
		hint = resetHelp().footer()
	}
	return m.renderCard("Reset ticket", body, hint)
}

// renderHandle draws the Handle confirm card: the ticket's class glyph, phase and
// failure reason, then the per-class verbs (or the reset progress/result).
func (m appModel) renderHandle() string {
	s := m.styles
	r := m.handleRow
	title := "Handle " + r.ID
	switch {
	case m.busy:
		return m.renderCard(title, m.spin.View()+" "+s.Subtle.Render("resetting…"), "working…")
	case m.result != "":
		return m.renderCard(title, m.result, "esc/q back")
	}
	glyph, glyphStyle := attentionGlyph(s, r.FailureClass)
	head := glyphStyle.Render(glyph) + " " + s.Header.Render(r.ID) + "  " + s.Subtle.Render(failureLabel(r.FailureClass))
	lines := []string{head, s.Subtle.Render("phase: " + prettyPhase(r.Phase))}
	if r.FailureReason != "" {
		lines = append(lines, "", s.Warning.Render(oneLine(r.FailureReason)))
	}
	return m.renderCard(title, strings.Join(lines, "\n"), m.handleHint())
}

// toQueueRows projects saved checkpoints onto the shared queue model, dating each
// row by time since its last checkpoint update.
func toQueueRows(src []StatusRow) []QueueRow {
	rows := make([]QueueRow, 0, len(src))
	for _, r := range src {
		var age time.Duration
		if !r.Updated.IsZero() {
			age = time.Since(r.Updated)
		}
		rows = append(rows, QueueRow{
			ID:            r.ID,
			Title:         r.Title,
			Phase:         r.Phase,
			PRURL:         r.PRURL,
			Branch:        r.Branch,
			FailureReason: r.FailureReason,
			FailureClass:  r.FailureClass,
			Tokens:        r.Tokens,
			Cost:          r.Cost,
			CostMetered:   r.CostMetered,
			Age:           age,
		})
	}
	return rows
}

// buildQueueRows reads the current checkpoints for the live rail snapshot.
func (m appModel) buildQueueRows() []QueueRow {
	return toQueueRows(m.actions.StatusRows())
}

// loadStatusRows refreshes the Status browse screen's rows, clamping the cursor
// into the new selectable set.
func (m appModel) loadStatusRows() appModel {
	m.statusRows = m.buildQueueRows()
	active, _ := partitionQueue(m.statusRows, false)
	if m.statusCursor >= len(active) {
		m.statusCursor = len(active) - 1
	}
	if m.statusCursor < 0 {
		m.statusCursor = 0
	}
	return m
}

// selectedStatusRow returns the queue row under the Status cursor.
func (m appModel) selectedStatusRow() (QueueRow, bool) {
	active, _ := partitionQueue(m.statusRows, false)
	if m.statusCursor < 0 || m.statusCursor >= len(active) {
		return QueueRow{}, false
	}
	return active[m.statusCursor], true
}

// isBack reports whether msg backs out one level under the shared key contract:
// esc everywhere, q on leaf screens. enter is never back — it acts on the
// selection (or does nothing where no action exists yet).
func isBack(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "esc", "q":
		return true
	}
	return false
}

// helpFor returns the current screen's key legend — the one declaration that
// drives both its footer and the ? overlay. Sub-model-backed screens delegate
// to the sub-model so its handled keys and the overlay can never drift apart.
func (m appModel) helpFor() screenHelp {
	switch m.view {
	case viewMenu:
		return menuHelp()
	case viewMore:
		return moreHelp()
	case viewStatus:
		return statusHelp()
	case viewLogs:
		return m.logs.help()
	case viewReset:
		return resetHelp()
	case viewHandle:
		return m.handleHelp()
	case viewVersion:
		return leafHelp("Version")
	case viewDryRun:
		return leafHelp("Dry run")
	case viewError:
		return leafHelp("Error")
	case viewRunLoop:
		return m.loopSetup.help()
	case viewRunOnce:
		return m.runOnce.help()
	case viewPlan:
		return m.plan.help()
	case viewSettings:
		return m.settings.help()
	case viewOnboarding:
		return m.onboard.help()
	case viewRunning:
		if m.dash.done() {
			return m.dash.summaryHelp()
		}
		return m.dash.runningHelp()
	}
	return screenHelp{}
}

// editing reports whether a dash modal owns input, so the global ? and : overlays
// stay closed rather than firing over a filter box or the peek preview. For
// free-text fields ? is also typed as a literal; ID/epic/branch fields (where ? is
// never valid) still open help.
func (m appModel) editing() bool {
	switch m.view {
	case viewOnboarding:
		return m.onboard.editing()
	case viewSettings:
		return m.settings.editing()
	case viewPlan:
		return m.plan.editing()
	case viewRunning:
		return m.dash.filterActive() || m.dash.peeking()
	}
	return false
}

func menuHelp() screenHelp {
	return screenHelp{title: "Menu", columns: []helpColumn{
		group("Navigate", fk("↑↓", "move"), xk("j/k", "move")),
		group("Actions", fk("enter", "select"), fk("q", "quit")),
		group("Global", xk("ctrl+p / :", "command palette"), xk("ctrl+t", "toggle mouse (select text)"), xk("⇧ drag", "select text (⌥ on iTerm2/Terminal.app)")),
	}}
}

func moreHelp() screenHelp {
	return screenHelp{title: "More", columns: []helpColumn{
		group("Navigate", fk("↑↓", "move"), xk("j/k", "move")),
		group("Actions", fk("enter", "select"), fk("esc/q", "back")),
	}}
}

func statusHelp() screenHelp {
	return screenHelp{title: "Status", columns: []helpColumn{
		group("Navigate", fk("↑↓", "move"), xk("j/k", "move")),
		group("Recover",
			fk("o", "open PR"),
			fk("l", "jump to logs"),
			fk("r", "resume"),
			fk("b", "checkout branch"),
			fk("x", "reset"),
			xk("enter", "handle (guided recovery)"),
		),
		group("Session", fk("R", "reconcile"), fk("esc/q", "back"), xk("ctrl+t", "toggle mouse (select text)"), xk("⇧ drag", "select text (⌥ on iTerm2/Terminal.app)")),
	}}
}

func resetHelp() screenHelp {
	return screenHelp{title: "Reset ticket", columns: []helpColumn{
		group("Actions", fk("enter", "confirm"), fk("esc", "back")),
	}}
}

// handleHelp is the Handle confirm's legend, listing only the verbs its ticket's
// class actually offers so the ? overlay matches the footer.
func (m appModel) handleHelp() screenHelp {
	var recover []helpKey
	if m.handleRow.canResume() {
		recover = append(recover, fk("r", "resume from checkpoint"))
	}
	if m.handleRow.canReset() {
		recover = append(recover, fk("x", "reset (destructive)"))
	}
	return screenHelp{title: "Handle " + m.handleRow.ID, columns: []helpColumn{
		group("Recover", recover...),
		group("Actions", fk("esc/q", "cancel")),
	}}
}

// leafHelp is the legend for a read-only card whose only key is back.
func leafHelp(title string) screenHelp {
	return screenHelp{title: title, columns: []helpColumn{
		group("Actions", fk("esc/q", "back")),
	}}
}

func freshDash(w, h int, binding string) model {
	d := initialModel(nil)
	d.binding = binding
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
