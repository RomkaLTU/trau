package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/RomkaLTU/trau/internal/config"
)

// ProjectSetup is the configuration collected by the onboarding wizard and
// handed to Actions.SetupProject for persistence.
type ProjectSetup struct {
	Provider        string
	TrackerProvider string
	BaseBranch      string
	Team            string
	ReadyLabel      string
	QuarantineLabel string
	CreateLabels    bool
	EpicFlow        bool
	Timelog         bool
	RequireCI       bool
	ExpectedChecks  []string
	LinearAPIKey    string
	JiraBaseURL     string
	JiraEmail       string
	JiraAPIToken    string
}

// SetupResult reports what the setup step actually did.
type SetupResult struct {
	ConfigPath string
	LabelErr   error
}

// JiraCreds carries the REST credentials entered in the wizard so team
// detection enumerates projects as that Jira identity, rather than falling back
// to the shared Rovo MCP account (a different Atlassian identity).
type JiraCreds struct {
	BaseURL  string
	Email    string
	APIToken string
}

// DetectedTeam is one selectable project-management container surfaced by the
// wizard — a Linear team, a Jira project, or a GitHub repository slug. Key is
// the value stored in config; Name is the label shown to the user.
type DetectedTeam struct {
	Key  string
	Name string
}

// TeamDetection is the result of probing the chosen tracker for selectable
// containers. Label adapts the wording per provider ("team"/"project"/
// "repository"). AutoFill is set when there is a single obvious choice that the
// wizard should pre-fill rather than show in a picker (GitHub's current repo).
type TeamDetection struct {
	Label    string
	Teams    []DetectedTeam
	AutoFill bool
}

// CIDetection is the result of probing whether this repo gates PRs on CI. Gate
// is the recommended REQUIRE_CI value. Confident is set only when the answer
// came from an authoritative GitHub source (branch-protection or ruleset
// required checks) rather than the local-workflow guess, so the wizard can
// present it as auto-detected instead of a question. ExpectedChecks carries the
// required status-check names when GitHub exposed them, seeding EXPECTED_CHECKS.
// Source labels the winning signal for the description line: "branch-protection",
// "workflows" (local .github/workflows fallback), or "none".
type CIDetection struct {
	Gate           bool
	Confident      bool
	ExpectedChecks []string
	Source         string
}

// expectedChecksLabel renders the required checks for the description line,
// capping the list so a repo with many checks cannot overflow the form.
func (d CIDetection) expectedChecksLabel() string {
	const max = 3
	names := d.ExpectedChecks
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:max], ", ") + fmt.Sprintf(" +%d more", len(names)-max)
}

// OnboardingPrefill carries existing configuration so the onboarding wizard
// can default to the current values when it is re-run.
type OnboardingPrefill struct {
	Provider        string
	TrackerProvider string
	BaseBranch      string
	Team            string
	ReadyLabel      string
	QuarantineLabel string
	EpicFlow        bool
	Timelog         bool
	LinearAPIKey    string
	JiraBaseURL     string
	JiraEmail       string
	JiraAPIToken    string
}

// OnboardingActions is the narrow seam the onboarding wizard needs from the
// backend. The concrete implementation lives in cmd/trau/main.go.
type OnboardingActions interface {
	// RepoRoot returns the resolved target repo root, or "" when no repo was found.
	RepoRoot() string

	// OnboardingPrefill returns the current config values, used to default the
	// wizard when it is re-run.
	OnboardingPrefill() OnboardingPrefill

	// LinearAPIKeyConfigured reports whether a Linear API key is already present
	// in config/env. When true the linear readiness gate passes even if the
	// Linear MCP is not connected.
	LinearAPIKeyConfigured() bool

	// DetectTeams enumerates the selectable containers for the chosen tracker,
	// driving the PM tool through the chosen AI provider where needed (Linear,
	// Jira); GitHub is detected locally from the git remote. The jira creds
	// entered in the wizard are passed so Jira detection queries the REST API as
	// that identity instead of the shared Rovo MCP account. An error means the
	// wizard should fall back to manual entry.
	DetectTeams(ctx context.Context, trackerProvider, aiProvider string, jira JiraCreds) (TeamDetection, error)

	// SetupProject writes the project env file (and optionally creates Linear
	// labels) from the values collected in the wizard.
	SetupProject(ctx context.Context, setup ProjectSetup) (SetupResult, error)

	// DetectCI probes whether this repo gates PRs on CI, using the GitHub repo the
	// wizard already resolves from the git remote: it reads the branch-protection
	// and ruleset required checks for baseBranch, and falls back to the local
	// .github/workflows scan when gh is unavailable or the repo is unreachable. A
	// blank baseBranch lets detection use the repo's default branch. It never
	// errors — an undetectable repo returns the local-workflow guess.
	DetectCI(ctx context.Context, baseBranch string) CIDetection
}

// onboardPhase is the wizard's outer state. The animated system check and the
// terminal screens are drawn by this model directly; the middle collection of
// steps is driven by an embedded huh form (see onboarding_form.go).
type onboardPhase int

const (
	phaseSystemCheck onboardPhase = iota
	phaseWelcome
	phaseForm
	phaseWriting
	phaseCreateLabels
	phaseDone
	phaseNoRepo
)

type onboardingModel struct {
	styles   Styles
	actions  OnboardingActions
	ctx      context.Context
	width    int
	height   int
	repoRoot string

	phase onboardPhase

	// form is the huh form driving the middle steps; fv holds its bound values
	// (pointer-shared across the value copies of this model).
	form *huh.Form
	fv   *formValues

	// system check
	systemChecks       []systemCheck
	systemCheckIndex   int
	systemCheckSpin    spinner.Model
	systemCheckBar     progress.Model
	systemCheckDone    bool
	systemCheckStarted bool
	mcp                *mcpProbe

	// ciHasPRDet records whether a pull_request-triggered workflow was detected
	// locally; it seeds the CI merge-gate default synchronously before the async
	// GitHub probe (ciDet) lands.
	ciHasPRDet bool

	// ciDet holds the async CI-gate probe result (branch-protection / rulesets,
	// falling back to local workflows); ciDetDone reports whether it has landed.
	// The probe is kicked off at Init and lands during the system-check screen,
	// well before the form is built, so newForm reads a settled value.
	ciDet     CIDetection
	ciDetDone bool

	writing bool
	result  SetupResult
	errMsg  string

	done bool

	// scrollOffset is the first visible body line for the system-check screen
	// when it is taller than the terminal. Reset on phase change, clamped in View.
	scrollOffset int
}

func newOnboardingModel(ctx context.Context, actions OnboardingActions, styles Styles, width, height int) onboardingModel {
	return newOnboardingModelWithPrefill(ctx, actions, styles, width, height, actions.OnboardingPrefill())
}

func newOnboardingModelWithPrefill(ctx context.Context, actions OnboardingActions, styles Styles, width, height int, prefill OnboardingPrefill) onboardingModel {
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = styles.Spinner

	m := onboardingModel{
		styles:          styles,
		actions:         actions,
		ctx:             ctx,
		width:           width,
		height:          height,
		repoRoot:        actions.RepoRoot(),
		phase:           phaseSystemCheck,
		systemCheckSpin: spin,
		systemCheckBar:  newSystemCheckBar(),
		mcp:             newMCPProbe(),
		fv:              &formValues{},
	}
	m.resetSystemChecks()
	if m.repoRoot == "" {
		m.phase = phaseNoRepo
	}
	m.ciHasPRDet = config.HasPullRequestCI(m.repoRoot)
	m.ciDet = CIDetection{Gate: m.ciHasPRDet, Source: ciWorkflowSource(m.ciHasPRDet)}
	m.applyPrefill(prefill)
	return m
}

// ciWorkflowSource labels the local-workflow fallback: a detected pull_request
// trigger reads as "workflows", nothing found as "none".
func ciWorkflowSource(hasPR bool) string {
	if hasPR {
		return "workflows"
	}
	return "none"
}

// applyPrefill seeds the form's bound values from the current configuration so
// re-running onboarding keeps existing choices.
func (m *onboardingModel) applyPrefill(p OnboardingPrefill) {
	fv := m.fv
	fv.tracker = firstNonEmpty(p.TrackerProvider, "linear")
	fv.aiProvider = firstNonEmpty(p.Provider, "claude")
	fv.baseBranch = p.BaseBranch
	fv.team = p.Team
	fv.teamManual = p.Team
	fv.prefillTeam = p.Team
	fv.epicFlow = p.EpicFlow
	fv.timelog = p.Timelog
	fv.requireCI = m.ciHasPRDet
	fv.linearKey = p.LinearAPIKey
	fv.jiraBase = p.JiraBaseURL
	fv.jiraEmail = p.JiraEmail
	fv.jiraToken = p.JiraAPIToken
}

func (m onboardingModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.phase == phaseSystemCheck {
		cmds = append(cmds, m.systemCheckSpin.Tick)
	}
	if m.repoRoot != "" {
		cmds = append(cmds, m.detectCICmd())
	}
	return tea.Batch(cmds...)
}

type ciDetectedMsg struct{ det CIDetection }

// detectCICmd probes the CI merge gate off the main loop so the gh calls never
// block the UI; the result lands as a ciDetectedMsg.
func (m onboardingModel) detectCICmd() tea.Cmd {
	actions, ctx, base := m.actions, m.ctx, m.fv.baseBranch
	return func() tea.Msg {
		return ciDetectedMsg{det: actions.DetectCI(ctx, base)}
	}
}

func (m onboardingModel) Update(msg tea.Msg) (onboardingModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.form != nil {
			m.form = m.form.WithWidth(m.formWidth())
		}
		return m, nil
	case setupDoneMsg:
		return m.applySetupDone(msg), nil
	case ciDetectedMsg:
		m.ciDet = msg.det
		m.ciDetDone = true
		m.ciHasPRDet = msg.det.Gate
		m.fv.requireCI = msg.det.Gate
		m.fv.expectedChecks = msg.det.ExpectedChecks
		return m, nil
	case formCompletedMsg:
		m.phase = phaseWriting
		m.writing = true
		m.errMsg = ""
		return m, m.writeConfigCmd()
	case formAbortedMsg:
		m.done = true
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}

	switch m.phase {
	case phaseSystemCheck:
		return m.updateSystemCheckPhase(msg)
	case phaseWelcome:
		return m.updateWelcome(msg)
	case phaseForm:
		return m.updateForm(msg)
	default:
		return m.updateTerminal(msg)
	}
}

func (m onboardingModel) updateSystemCheckPhase(msg tea.Msg) (onboardingModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if delta, ok := scrollKeyDelta(msg, m.bodyBudget()); ok {
			m.scrollOffset = m.clampScroll(m.scrollOffset + delta)
			return m, nil
		}
		if msg.String() == "q" {
			msg = tea.KeyPressMsg{Code: tea.KeyEsc}
		}
		prev := m.phase
		nm, cmd := m.handleSystemCheck(msg)
		if nm.phase != prev {
			nm.scrollOffset = 0
		}
		return nm, cmd
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scrollOffset = m.clampScroll(m.scrollOffset - 3)
		case tea.MouseWheelDown:
			m.scrollOffset = m.clampScroll(m.scrollOffset + 3)
		}
		return m, nil
	case progress.FrameMsg:
		var cmd tea.Cmd
		m.systemCheckBar, cmd = m.systemCheckBar.Update(msg)
		return m, cmd
	case systemCheckResultMsg:
		m = m.applySystemCheckResult(msg)
		return m, tea.Batch(m.nextSystemCheckCmd(), m.systemCheckBar.SetPercent(m.systemCheckProgress()))
	case systemCheckAdvanceMsg:
		return m.advanceSystemCheck(), m.nextSystemCheckCmd()
	case systemCheckDoneMsg:
		m.systemCheckDone = true
		cmd := m.systemCheckBar.SetPercent(1.0)
		if m.systemChecksPass() {
			return m, tea.Batch(cmd, tea.Tick(900*time.Millisecond, func(time.Time) tea.Msg {
				return systemCheckAdvanceStepMsg{}
			}))
		}
		return m, cmd
	case systemCheckAdvanceStepMsg:
		if m.phase == phaseSystemCheck && m.systemChecksPass() {
			m.phase = phaseWelcome
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.systemCheckSpin, cmd = m.systemCheckSpin.Update(msg)
	return m, cmd
}

func (m onboardingModel) updateWelcome(msg tea.Msg) (onboardingModel, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "enter":
		m.phase = phaseForm
		if m.form == nil {
			m.form = m.newForm()
		}
		return m, m.form.Init()
	case "esc", "q", "left":
		m.phase = phaseSystemCheck
		return m, nil
	}
	return m, nil
}

// updateForm drives the embedded huh form, translating the trau back-navigation
// contract (esc / q-when-not-editing / ←) onto huh's shift+tab, and bouncing out
// to the welcome screen from the first field.
func (m onboardingModel) updateForm(msg tea.Msg) (onboardingModel, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		// 'o' opens the Linear key settings URL, but only while the field is empty
		// so a key containing 'o' can still be typed.
		if k.String() == "o" && m.focusedKey() == keyLinearKey && strings.TrimSpace(m.fv.linearKey) == "" {
			return m, openURLCmd(linearAPIKeySettingsURL)
		}
		if m.isBackKey(k) {
			if m.onFirstFormField() {
				m.phase = phaseWelcome
				return m, nil
			}
			return m.passToForm(onboardBackKey())
		}
	}
	return m.passToForm(msg)
}

func (m onboardingModel) passToForm(msg tea.Msg) (onboardingModel, tea.Cmd) {
	fm, cmd := m.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.form = f
	}
	if m.form.State == huh.StateAborted {
		m.done = true
	}
	return m, cmd
}

func (m onboardingModel) updateTerminal(msg tea.Msg) (onboardingModel, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch m.phase {
	case phaseWriting:
		if m.writing {
			return m, nil
		}
		switch k.String() {
		case "enter":
			m.writing = true
			m.errMsg = ""
			return m, m.writeConfigCmd()
		case "esc", "q":
			m.done = true
		}
		return m, nil
	case phaseCreateLabels:
		if k.String() == "enter" || k.String() == "esc" || k.String() == "q" {
			m.phase = phaseDone
		}
		return m, nil
	case phaseDone, phaseNoRepo:
		if k.String() == "enter" || k.String() == "esc" || k.String() == "q" {
			m.done = true
		}
		return m, nil
	}
	return m, nil
}

// isBackKey reports whether the key means "step back" in the form: esc always,
// and ← / q only when a text field is not capturing the keystroke.
func (m onboardingModel) isBackKey(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "esc":
		return true
	case "left", "q":
		return !m.editing()
	}
	return false
}

func (m onboardingModel) onFirstFormField() bool {
	return m.focusedKey() == keyTracker
}

// handleMouseClick lets a left click advance the bespoke screens (mirroring
// enter). The huh form has no mouse layer, so clicks within it are ignored;
// footer-verb clicks are routed by the app shell before this is reached.
func (m onboardingModel) handleMouseClick(msg tea.MouseClickMsg) (onboardingModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	switch m.phase {
	case phaseSystemCheck:
		return m.handleSystemCheck(enter)
	case phaseWelcome:
		return m.updateWelcome(enter)
	case phaseWriting, phaseCreateLabels, phaseDone, phaseNoRepo:
		return m.updateTerminal(enter)
	}
	return m, nil
}

// onboardBackKey is the shift+tab huh reads as "previous field/group"; the trau
// back keys are translated to it so huh owns step-by-step reverse navigation.
func onboardBackKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
}

func (m onboardingModel) focusedKey() string {
	if m.phase != phaseForm || m.form == nil {
		return ""
	}
	if f := m.form.GetFocusedField(); f != nil {
		return f.GetKey()
	}
	return ""
}

// editing reports whether a free-text field owns keystrokes, so q / ← type into
// the field instead of navigating back and ? is typed literally.
func (m onboardingModel) editing() bool {
	switch m.focusedKey() {
	case keyLinearKey, keyJiraBase, keyJiraEmail, keyJiraToken, keyBaseBranch, keyTeam, keyTeamManual:
		return true
	}
	return false
}

type setupDoneMsg struct {
	result SetupResult
	err    error
}

func (m onboardingModel) applySetupDone(msg setupDoneMsg) onboardingModel {
	m.writing = false
	if msg.err != nil {
		m.errMsg = msg.err.Error()
		m.phase = phaseWriting
		return m
	}
	m.result = msg.result
	if m.wantsCreateLabels() {
		m.phase = phaseCreateLabels
	} else {
		m.phase = phaseDone
	}
	return m
}

func (m onboardingModel) Done() bool { return m.done }

// --- framing ---

const trauWordmark = `  _______   _____               _    _
 |__   __| |  __ \      /\     | |  | |
    | |    | |__) |    /  \    | |  | |
    | |    |  _  /    / /\ \   | |  | |
    | |    | | \ \   / ____ \  | |__| |
    |_|    |_|  \_\ /_/    \_\  \____/`

func (m onboardingModel) brandHeader() string {
	if m.height > 0 && m.height < 18 {
		return lipgloss.NewStyle().Bold(true).Foreground(theme.Brand).Render("T R A U")
	}
	mark := lipgloss.NewStyle().Bold(true).Foreground(theme.Brand).Render(trauWordmark)
	tag := lipgloss.NewStyle().Foreground(theme.Subtle).Render("autonomous ticket loop")
	return lipgloss.JoinVertical(lipgloss.Center, mark, tag)
}

// bodyBudget is how many body lines fit under the brand header and above the
// hint on the current terminal.
func (m onboardingModel) bodyBudget() int {
	return cardBodyBudget(m.height, lipgloss.Height(m.brandHeader()))
}

// formWidth is the inner width handed to the huh form, sized to sit comfortably
// inside the shared card.
func (m onboardingModel) formWidth() int {
	w := m.width - 12
	if w > 72 {
		w = 72
	}
	if w < 24 {
		w = 24
	}
	return w
}

// scrollKeyDelta maps the explicit scroll keys to a body-line delta. The system
// check owns ↑↓ for nothing, but keeps pgup/pgdn/wheel scroll for tall content.
func scrollKeyDelta(msg tea.KeyPressMsg, page int) (int, bool) {
	step := page - 1
	if step < 1 {
		step = 1
	}
	switch msg.String() {
	case "pgdown":
		return step, true
	case "pgup":
		return -step, true
	}
	return 0, false
}

func (m onboardingModel) clampScroll(off int) int {
	if off < 0 {
		return 0
	}
	maxOff := strings.Count(m.phaseBody(), "\n") + 1 - m.bodyBudget()
	if maxOff < 0 {
		maxOff = 0
	}
	if off > maxOff {
		off = maxOff
	}
	return off
}

func (m onboardingModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}
	body := m.phaseBody()
	hint := m.hint()
	if m.phase == phaseSystemCheck {
		lines := strings.Split(body, "\n")
		win, _, overflow := windowAt(lines, m.scrollOffset, m.bodyBudget())
		body = strings.Join(win, "\n")
		if overflow {
			hint += " · pgup/pgdn/wheel scroll"
		}
	}
	return centerScreen(m.width, m.height,
		m.brandHeader(), cardBox(m.styles, m.width, body), hintBar(m.styles, hint))
}

func (m onboardingModel) phaseBody() string {
	switch m.phase {
	case phaseSystemCheck:
		return m.renderSystemCheck()
	case phaseWelcome:
		return m.renderWelcome()
	case phaseForm:
		if m.form == nil {
			return ""
		}
		switch m.focusedKey() {
		case keyWrite:
			return m.renderWritePreview() + "\n\n" + m.form.View()
		case keyTeam, keyTeamManual:
			if e := m.fv.teamErrText(); e != "" {
				warn := m.styles.Warning.Render("Couldn't detect automatically — " + e)
				return warn + "\n\n" + m.form.View()
			}
		}
		return m.form.View()
	case phaseWriting:
		return m.renderWriting()
	case phaseCreateLabels:
		return m.renderCreateLabels()
	case phaseDone:
		return m.renderDone()
	case phaseNoRepo:
		return m.renderNoRepo()
	}
	return ""
}

func (m onboardingModel) hint() string {
	switch m.phase {
	case phaseSystemCheck:
		if m.systemCheckStarted && !m.systemCheckDone {
			return "checking dependencies…"
		}
	case phaseWriting:
		if m.writing {
			return "working…"
		}
	}
	return m.help().footer()
}

// help is the wizard's per-step key legend: the single source for its footer and
// the ? overlay.
func (m onboardingModel) help() screenHelp {
	nav := group("Navigate", fk("↑↓", "move"))
	switch m.phase {
	case phaseSystemCheck:
		action := "start check"
		if m.systemCheckDone {
			action = "continue/re-check"
		}
		return screenHelp{title: "System check", columns: []helpColumn{
			group("Actions", fk("enter", action), fk("esc/q", "back")),
		}}
	case phaseWelcome:
		return screenHelp{title: "Welcome", columns: []helpColumn{
			group("Actions", fk("enter", "continue"), fk("esc/q", "back")),
		}}
	case phaseForm:
		return m.formHelp(nav)
	case phaseWriting:
		if m.writing {
			return screenHelp{title: "Write config"}
		}
		return screenHelp{title: "Write config", columns: []helpColumn{
			group("Actions", fk("enter", "retry"), fk("esc/q", "cancel")),
		}}
	case phaseCreateLabels, phaseDone:
		return screenHelp{title: "Onboarding", columns: []helpColumn{
			group("Actions", fk("enter/esc/q", "continue")),
		}}
	case phaseNoRepo:
		return screenHelp{title: "No repository", columns: []helpColumn{
			group("Actions", fk("enter/esc/q", "exit")),
		}}
	}
	return screenHelp{title: "Onboarding"}
}

func (m onboardingModel) formHelp(nav helpColumn) screenHelp {
	switch m.focusedKey() {
	case keyTracker, keyAIProvider:
		return screenHelp{title: "Providers", columns: []helpColumn{
			nav, group("Actions", fk("enter/tab", "next"), fk("esc/q/←", "back")),
		}}
	case keyLinearKey:
		return screenHelp{title: "Linear API key", columns: []helpColumn{
			group("Actions", fk("enter/tab", "next"), fk("o", "open key settings"), fk("esc/←", "back")),
		}}
	case keyJiraBase, keyJiraEmail, keyJiraToken:
		return screenHelp{title: "Jira credentials", columns: []helpColumn{
			group("Navigate", fk("tab/↑↓", "move")),
			group("Actions", fk("enter", "next"), fk("esc/←", "back")),
		}}
	case keyBaseBranch, keyBranching:
		return screenHelp{title: "Base branch", columns: []helpColumn{
			nav, group("Actions", fk("enter/tab", "next"), fk("esc/←", "back")),
		}}
	case keyTeam:
		return screenHelp{title: "Team", columns: []helpColumn{
			group("Search", fk("type", "to search")),
			nav,
			group("Actions", fk("enter", "select"), fk("esc/←", "back")),
		}}
	case keyTeamManual:
		return screenHelp{title: "Team", columns: []helpColumn{
			group("Actions", fk("enter", "confirm"), fk("esc/←", "back")),
		}}
	case keyLabels, keyTimelog, keyCI:
		return screenHelp{title: "Options", columns: []helpColumn{
			nav, group("Actions", fk("enter", "select"), fk("esc/q/←", "back")),
		}}
	case keyWrite:
		return screenHelp{title: "Write config", columns: []helpColumn{
			group("Actions", fk("enter", "write"), fk("esc/q/←", "back")),
		}}
	}
	return screenHelp{title: "Onboarding", columns: []helpColumn{
		nav, group("Actions", fk("enter", "next"), fk("esc", "back")),
	}}
}

// --- terminal screens ---

func (m onboardingModel) renderWelcome() string {
	s := m.styles
	title := s.SummaryTitle.Render("Welcome to trau")
	intro := "Trau runs an autonomous ticket loop for your repo:"
	pipeline := "build → handoff → verify → commit → PR → CI → merge"
	labels := "Tickets labelled ready-for-agent are picked automatically; " +
		"tickets that fail are moved to needs-human for you to review."
	return lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		intro,
		"",
		s.Info.Render(pipeline),
		"",
		labels,
		"",
		"Let's set up your project.",
	)
}

func (m onboardingModel) renderWriting() string {
	path := filepath.Join(m.repoRoot, config.ProjectConfigName)
	if m.writing {
		return lipgloss.JoinVertical(lipgloss.Left,
			m.styles.SummaryTitle.Render("Writing config"),
			"",
			"Saving "+path+"…",
		)
	}
	rows := []string{m.styles.SummaryTitle.Render("Write config"), ""}
	if m.errMsg != "" {
		rows = append(rows,
			m.styles.Error.Render("Error: "+m.errMsg),
			"",
			"Press enter to retry, or esc to cancel.",
		)
	}
	return strings.Join(rows, "\n")
}

func (m onboardingModel) renderCreateLabels() string {
	status := "Creating labels…"
	if m.result.LabelErr != nil {
		status = m.styles.Error.Render("Could not create labels: " + m.result.LabelErr.Error())
	} else if m.result.ConfigPath != "" {
		status = m.styles.Success.Render("Labels created successfully.")
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.styles.SummaryTitle.Render(titleTracker(m.fv.tracker)+" labels"),
		"",
		status,
		"",
		"Press enter to continue.",
	)
}

func (m onboardingModel) renderDone() string {
	msg := "Setup complete. Press enter to open the menu."
	if m.result.ConfigPath != "" {
		msg = "Wrote " + m.result.ConfigPath + ".\n\n" + msg
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.styles.SummaryTitle.Render("All set"),
		"",
		msg,
	)
}

func (m onboardingModel) renderNoRepo() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		m.styles.SummaryTitle.Render("No git repo found"),
		"",
		"Trau needs to know which repository to work on.",
		"",
		"Run trau from inside a git repo, or pass --repo <path>.",
		"",
		"Press enter or esc to exit.",
	)
}
