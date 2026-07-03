package tui

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/sahilm/fuzzy"
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
}

type onboardStep int

const (
	onboardSystemCheck onboardStep = iota
	onboardWelcome
	onboardProviders
	onboardLinearAPIKey
	onboardJiraCreds
	onboardBaseBranch
	onboardLinearTeam
	onboardLabels
	onboardTimeTracking
	onboardCI
	onboardWrite
	onboardCreateLabels
	onboardDone
	onboardNoRepo
)

type checkStatus int

const (
	checkPending checkStatus = iota
	checkRunning
	checkDone
	checkFailed
	checkSkipped
)

// systemCheck is one readiness probe run at the very start of onboarding.
type systemCheck struct {
	name   string
	desc   string
	status checkStatus
	err    error
}

type onboardingModel struct {
	styles   Styles
	actions  OnboardingActions
	ctx      context.Context
	width    int
	height   int
	repoRoot string

	step onboardStep

	trackerCursor int
	trackers      []string

	providerCursor     int
	providers          []string
	providersPMFocused bool

	apiKey             textinput.Model
	apiKeyInputFocused bool

	jiraBaseURL     textinput.Model
	jiraEmail       textinput.Model
	jiraToken       textinput.Model
	jiraFieldCursor int // 0=base url, 1=email, 2=token

	baseBranch             textinput.Model
	baseBranchInputFocused bool

	branchingCursor  int
	branchingOptions []string
	epicFlow         bool

	team textinput.Model

	teamDetecting  bool
	teamDetected   bool   // detection finished (ok or failed) for teamProvider
	teamProvider   string // tracker the cached detection was run for
	teamDetectErr  error
	teamOptions    []DetectedTeam
	teamLabel      string // "team" | "project" | "repository"
	teamAutoFilled bool   // single obvious choice pre-filled (GitHub repo)
	teamManual     bool   // free-text fallback / manual entry active
	teamFilter     textinput.Model
	teamCursor     int
	teamSpin       spinner.Model

	labelsCursor int
	createLabels bool

	timelogCursor  int
	timelogOptions []string
	timelog        bool

	ciCursor   int
	ciOptions  []string
	requireCI  bool
	ciHasPRDet bool // a pull_request-triggered workflow was detected in the repo

	writing bool
	done    bool
	result  SetupResult
	errMsg  string

	systemChecks       []systemCheck
	systemCheckIndex   int
	systemCheckSpin    spinner.Model
	systemCheckBar     progress.Model
	systemCheckDone    bool
	systemCheckStarted bool
	mcp                *mcpProbe

	// scrollOffset is the first visible body line for steps whose content is
	// taller than the terminal (e.g. the config preview). It is reset whenever
	// the step changes and clamped to the content in View.
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
		styles:                 styles,
		actions:                actions,
		ctx:                    ctx,
		width:                  width,
		height:                 height,
		repoRoot:               actions.RepoRoot(),
		step:                   onboardSystemCheck,
		trackers:               []string{"linear", "jira", "github"},
		providers:              []string{"claude", "codex", "kimi"},
		branchingOptions:       []string{"Use epic branches for tickets with sub-issues", "Process every ticket standalone"},
		timelogOptions:         []string{"No — don't track time (default)", "Yes — log estimated dev time per ticket"},
		ciOptions:              []string{"Yes — wait for CI checks before merge (default)", "No — this repo has no PR CI; skip the gate"},
		epicFlow:               true,
		baseBranchInputFocused: true,
		providersPMFocused:     true,
		systemCheckSpin:        spin,
		systemCheckBar:         newSystemCheckBar(),
		mcp:                    newMCPProbe(),
	}
	m.apiKeyInputFocused = true
	m.resetSystemChecks()

	if m.repoRoot == "" {
		m.step = onboardNoRepo
	}

	m.ciHasPRDet = config.HasPullRequestCI(m.repoRoot)
	m.requireCI = m.ciHasPRDet
	if !m.ciHasPRDet {
		m.ciCursor = 1
	}

	ak := textinput.New()
	ak.Placeholder = "lin_api_..."
	ak.CharLimit = 256
	ak.SetWidth(40)
	ak.Prompt = "Linear API key: "
	ak.EchoMode = textinput.EchoPassword
	m.apiKey = ak

	jbu := textinput.New()
	jbu.Placeholder = "https://acme.atlassian.net"
	jbu.CharLimit = 200
	jbu.SetWidth(40)
	jbu.Prompt = "Base URL:  "
	m.jiraBaseURL = jbu

	je := textinput.New()
	je.Placeholder = "you@acme.com"
	je.CharLimit = 200
	je.SetWidth(40)
	je.Prompt = "Email:     "
	m.jiraEmail = je

	jt := textinput.New()
	jt.Placeholder = "classic API token"
	jt.CharLimit = 256
	jt.SetWidth(40)
	jt.Prompt = "API token: "
	jt.EchoMode = textinput.EchoPassword
	m.jiraToken = jt

	bb := textinput.New()
	bb.Placeholder = "main"
	bb.CharLimit = 64
	bb.SetWidth(30)
	bb.Prompt = "Base branch: "
	m.baseBranch = bb

	t := textinput.New()
	t.Placeholder = "your-team"
	t.CharLimit = 64
	t.SetWidth(30)
	t.Prompt = "Project/team: "
	m.team = t

	tf := textinput.New()
	tf.Placeholder = "type to search…"
	tf.CharLimit = 64
	tf.SetWidth(30)
	tf.Prompt = "Search: "
	m.teamFilter = tf

	tspin := spinner.New()
	tspin.Spinner = spinner.Dot
	tspin.Style = styles.Spinner
	m.teamSpin = tspin

	m.applyPrefill(prefill)
	return m
}

// applyPrefill sets the wizard's initial selections and input values from the
// current configuration so re-running onboarding keeps existing choices.
func (m *onboardingModel) applyPrefill(p OnboardingPrefill) {
	for i, t := range m.trackers {
		if t == p.TrackerProvider {
			m.trackerCursor = i
			break
		}
	}
	for i, name := range m.providers {
		if name == p.Provider {
			m.providerCursor = i
			break
		}
	}
	if p.BaseBranch != "" {
		m.baseBranch.SetValue(p.BaseBranch)
	}
	if p.Team != "" {
		m.team.SetValue(p.Team)
	}
	if p.EpicFlow {
		m.branchingCursor = 0
	} else {
		m.branchingCursor = 1
	}
	if p.Timelog {
		m.timelogCursor = 1
	} else {
		m.timelogCursor = 0
	}
	if p.LinearAPIKey != "" {
		m.apiKey.SetValue(p.LinearAPIKey)
	}
	if p.JiraBaseURL != "" {
		m.jiraBaseURL.SetValue(p.JiraBaseURL)
	}
	if p.JiraEmail != "" {
		m.jiraEmail.SetValue(p.JiraEmail)
	}
	if p.JiraAPIToken != "" {
		m.jiraToken.SetValue(p.JiraAPIToken)
	}
}

func (m onboardingModel) Init() tea.Cmd {
	if m.step == onboardSystemCheck {
		return m.systemCheckSpin.Tick
	}
	return textinput.Blink
}

func (m onboardingModel) Update(msg tea.Msg) (onboardingModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		if delta, ok := scrollKeyDelta(msg, m.bodyBudget()); ok {
			m.scrollOffset = m.clampScroll(m.scrollOffset + delta)
			return m, nil
		}
		prev := m.step
		nm, cmd := m.handleKey(msg)
		if nm.step != prev {
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

	case setupDoneMsg:
		return m.applySetupDone(msg), nil

	case teamsDetectedMsg:
		return m.applyTeamsDetected(msg), textinput.Blink

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
		if m.step == onboardSystemCheck && m.systemChecksPass() {
			m.step = onboardWelcome
		}
		return m, nil
	}

	var cmd tea.Cmd
	switch m.step {
	case onboardSystemCheck:
		m.systemCheckSpin, cmd = m.systemCheckSpin.Update(msg)
	case onboardLinearAPIKey:
		m.apiKey, cmd = m.apiKey.Update(msg)
	case onboardJiraCreds:
		cmd = m.updateJiraField(msg)
	case onboardBaseBranch:
		m.baseBranch, cmd = m.baseBranch.Update(msg)
	case onboardLinearTeam:
		switch {
		case m.teamDetecting:
			m.teamSpin, cmd = m.teamSpin.Update(msg)
		case m.teamManual, m.teamAutoFilled:
			m.team, cmd = m.team.Update(msg)
		default:
			m.teamFilter, cmd = m.teamFilter.Update(msg)
		}
	}
	return m, cmd
}

func (m onboardingModel) handleKey(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if msg.String() == "q" && !m.textEntryActive() {
		msg = tea.KeyPressMsg{Code: tea.KeyEsc}
	}

	switch m.step {
	case onboardSystemCheck:
		return m.handleSystemCheck(msg)
	case onboardWelcome:
		return m.handleWelcome(msg)
	case onboardProviders:
		return m.handleProviders(msg)
	case onboardLinearAPIKey:
		return m.handleLinearAPIKey(msg)
	case onboardJiraCreds:
		return m.handleJiraCreds(msg)
	case onboardBaseBranch:
		return m.handleBaseBranch(msg)
	case onboardLinearTeam:
		return m.handleLinearTeam(msg)
	case onboardLabels:
		return m.handleLabels(msg)
	case onboardTimeTracking:
		return m.handleTimeTracking(msg)
	case onboardCI:
		return m.handleCI(msg)
	case onboardWrite:
		return m.handleWrite(msg)
	case onboardCreateLabels:
		return m.handleCreateLabels(msg)
	case onboardDone:
		return m.handleDone(msg)
	case onboardNoRepo:
		return m.handleNoRepo(msg)
	}
	return m, nil
}

func (m onboardingModel) handleWelcome(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	if msg.String() == "esc" {
		m.step = onboardSystemCheck
		return m, nil
	}
	if msg.String() == "enter" {
		m.step = onboardProviders
		m.trackerCursor = m.firstEnabledOption(m.trackers)
		m.providerCursor = m.firstEnabledOption(m.providers)
	}
	return m, nil
}

func (m onboardingModel) handleProviders(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	if m.providersPMFocused {
		switch msg.String() {
		case "esc", "left":
			m.step = onboardWelcome
			return m, nil
		case "enter", "tab":
			m.providersPMFocused = false
			return m, nil
		case "up", "k":
			m.trackerCursor = m.moveCursor(m.trackers, m.trackerCursor, -1)
		case "down", "j":
			m.trackerCursor = m.moveCursor(m.trackers, m.trackerCursor, +1)
		}
		return m, nil
	}

	switch msg.String() {
	case "esc", "left":
		m.step = onboardWelcome
		return m, nil
	case "tab":
		m.providersPMFocused = true
		return m, nil
	case "enter":
		switch m.selectedTracker() {
		case "linear":
			m.step = onboardLinearAPIKey
			m.apiKeyInputFocused = true
			m.apiKey.Focus()
			return m, textinput.Blink
		case "jira":
			m.step = onboardJiraCreds
			m.jiraFieldCursor = 0
			m.focusJiraField()
			return m, textinput.Blink
		}
		m.step = onboardBaseBranch
		m.baseBranch.Focus()
		return m, textinput.Blink
	case "up", "k":
		m.providerCursor = m.moveCursor(m.providers, m.providerCursor, -1)
	case "down", "j":
		m.providerCursor = m.moveCursor(m.providers, m.providerCursor, +1)
	}
	return m, nil
}

// optionDisabled reports whether a provider/tracker option is known-unavailable
// — its readiness probe failed. A skipped probe (could not verify) leaves the
// option selectable, since there is no proof it is unusable.
func (m onboardingModel) optionDisabled(name string) bool {
	return m.checkStatusFor(name) == checkFailed
}

// firstEnabledOption returns the index of the first selectable option. The
// readiness gate guarantees at least one provider and one ticket system are
// ready, so 0 is only a fallback.
func (m onboardingModel) firstEnabledOption(list []string) int {
	for i, name := range list {
		if !m.optionDisabled(name) {
			return i
		}
	}
	return 0
}

// moveCursor steps cur by dir (±1), skipping disabled options. It stays put when
// no enabled option lies further in that direction.
func (m onboardingModel) moveCursor(list []string, cur, dir int) int {
	for i := cur + dir; i >= 0 && i < len(list); i += dir {
		if !m.optionDisabled(list[i]) {
			return i
		}
	}
	return cur
}

func (m onboardingModel) handleLinearAPIKey(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	if m.apiKeyInputFocused {
		switch msg.String() {
		case "esc", "left":
			m.step = onboardProviders
			m.apiKey.Blur()
			return m, nil
		case "enter", "tab":
			m.apiKeyInputFocused = false
			m.apiKey.Blur()
			m.baseBranch.Focus()
			m.step = onboardBaseBranch
			return m, textinput.Blink
		}
		switch msg.String() {
		case "o":
			return m, openURLCmd(linearAPIKeySettingsURL)
		}
		var cmd tea.Cmd
		m.apiKey, cmd = m.apiKey.Update(msg)
		return m, cmd
	}
	// Should not reach here; the step is input-focused by default.
	return m, nil
}

// handleJiraCreds drives the three-field Jira credential form (base URL, email,
// token). tab / ↑↓ move between fields, enter advances and then proceeds to the
// base-branch step from the last field, and esc / ← goes back to the provider
// picker. No single-letter shortcut is bound: every key must be typeable into a
// URL or email.
func (m onboardingModel) handleJiraCreds(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "left":
		m.blurJiraInputs()
		m.step = onboardProviders
		m.providersPMFocused = false
		return m, nil
	case "tab", "down", "enter":
		if m.jiraFieldCursor < 2 {
			m.jiraFieldCursor++
			m.focusJiraField()
			return m, textinput.Blink
		}
		m.blurJiraInputs()
		m.step = onboardBaseBranch
		m.baseBranch.Focus()
		return m, textinput.Blink
	case "shift+tab", "up":
		if m.jiraFieldCursor > 0 {
			m.jiraFieldCursor--
			m.focusJiraField()
		}
		return m, textinput.Blink
	}
	return m, m.updateJiraField(msg)
}

// focusJiraField focuses the input at jiraFieldCursor and blurs the rest.
func (m *onboardingModel) focusJiraField() {
	m.blurJiraInputs()
	switch m.jiraFieldCursor {
	case 0:
		m.jiraBaseURL.Focus()
	case 1:
		m.jiraEmail.Focus()
	case 2:
		m.jiraToken.Focus()
	}
}

func (m *onboardingModel) blurJiraInputs() {
	m.jiraBaseURL.Blur()
	m.jiraEmail.Blur()
	m.jiraToken.Blur()
}

// updateJiraField forwards a message to the focused Jira input only.
func (m *onboardingModel) updateJiraField(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.jiraFieldCursor {
	case 0:
		m.jiraBaseURL, cmd = m.jiraBaseURL.Update(msg)
	case 1:
		m.jiraEmail, cmd = m.jiraEmail.Update(msg)
	case 2:
		m.jiraToken, cmd = m.jiraToken.Update(msg)
	}
	return cmd
}

func (m onboardingModel) handleBaseBranch(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	if m.baseBranchInputFocused {
		switch msg.String() {
		case "esc", "left":
			switch m.selectedTracker() {
			case "linear":
				m.step = onboardLinearAPIKey
				m.baseBranch.Blur()
				m.apiKeyInputFocused = true
				m.apiKey.Focus()
				return m, textinput.Blink
			case "jira":
				m.step = onboardJiraCreds
				m.baseBranch.Blur()
				m.focusJiraField()
				return m, textinput.Blink
			}
			m.step = onboardProviders
			return m, nil
		case "enter":
			if strings.TrimSpace(m.baseBranch.Value()) == "" {
				m.baseBranch.SetValue("main")
			}
			m.baseBranchInputFocused = false
			return m, nil
		case "tab":
			m.baseBranchInputFocused = false
			return m, nil
		}
		var cmd tea.Cmd
		m.baseBranch, cmd = m.baseBranch.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "esc", "left", "tab":
		m.baseBranchInputFocused = true
		return m, nil
	case "enter":
		m.epicFlow = m.branchingCursor == 0
		m.step = onboardLinearTeam
		return m.enterTeamStep()
	case "up", "k":
		if m.branchingCursor > 0 {
			m.branchingCursor--
		}
	case "down", "j":
		if m.branchingCursor < len(m.branchingOptions)-1 {
			m.branchingCursor++
		}
	}
	return m, nil
}

// teamsDetectedMsg carries the result of the background team/project probe.
type teamsDetectedMsg struct {
	detection TeamDetection
	err       error
}

// enterTeamStep kicks off (or reuses) detection for the selected tracker. The
// result is cached per tracker so navigating back and forth does not re-run the
// agent call; switching the tracker selection invalidates the cache.
func (m onboardingModel) enterTeamStep() (onboardingModel, tea.Cmd) {
	provider := m.selectedTracker()
	if m.teamDetected && m.teamProvider == provider {
		return m.focusTeamStep()
	}
	m.teamProvider = provider
	m.teamDetecting = true
	m.teamDetected = false
	m.teamDetectErr = nil
	m.teamManual = false
	m.teamAutoFilled = false
	m.teamOptions = nil
	m.teamCursor = 0
	m.teamFilter.SetValue("")
	return m, tea.Batch(m.teamSpin.Tick, m.detectTeamsCmd())
}

// focusTeamStep restores input focus when re-entering an already-detected step.
func (m onboardingModel) focusTeamStep() (onboardingModel, tea.Cmd) {
	if m.teamManual || m.teamAutoFilled {
		m.team.Focus()
	} else {
		m.teamFilter.Focus()
	}
	return m, textinput.Blink
}

func (m onboardingModel) detectTeamsCmd() tea.Cmd {
	ctx := m.ctx
	actions := m.actions
	trackerProvider := m.selectedTracker()
	aiProvider := m.selectedProvider()
	jira := JiraCreds{
		BaseURL:  strings.TrimSpace(m.jiraBaseURL.Value()),
		Email:    strings.TrimSpace(m.jiraEmail.Value()),
		APIToken: strings.TrimSpace(m.jiraToken.Value()),
	}
	return func() tea.Msg {
		det, err := actions.DetectTeams(ctx, trackerProvider, aiProvider, jira)
		return teamsDetectedMsg{detection: det, err: err}
	}
}

// jiraRESTConfigured reports whether the wizard collected a full set of Jira
// REST credentials, so detection uses the direct API (that identity) rather than
// the shared Rovo MCP.
func (m onboardingModel) jiraRESTConfigured() bool {
	return strings.TrimSpace(m.jiraBaseURL.Value()) != "" &&
		strings.TrimSpace(m.jiraEmail.Value()) != "" &&
		strings.TrimSpace(m.jiraToken.Value()) != ""
}

func (m onboardingModel) applyTeamsDetected(msg teamsDetectedMsg) onboardingModel {
	m.teamDetecting = false
	m.teamDetected = true
	m.teamLabel = msg.detection.Label

	if msg.err != nil || len(msg.detection.Teams) == 0 {
		m.teamDetectErr = msg.err
		m.teamManual = true
		m.team.Focus()
		return m
	}
	if msg.detection.AutoFill {
		m.teamAutoFilled = true
		m.team.SetValue(msg.detection.Teams[0].Key)
		m.team.Focus()
		m.team.CursorEnd()
		return m
	}
	m.teamOptions = msg.detection.Teams
	m.teamCursor = 0
	m.teamFilter.Focus()
	return m
}

func (m onboardingModel) handleLinearTeam(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	if m.teamDetecting {
		if msg.String() == "esc" {
			m.step = onboardBaseBranch
			m.baseBranchInputFocused = false
		}
		return m, nil
	}
	if m.teamManual || m.teamAutoFilled {
		return m.handleTeamManual(msg)
	}
	return m.handleTeamList(msg)
}

func (m onboardingModel) handleTeamManual(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "left":
		m.step = onboardBaseBranch
		m.baseBranchInputFocused = false
		return m, nil
	case "enter":
		if strings.TrimSpace(m.team.Value()) == "" {
			return m, nil
		}
		m.step = onboardLabels
		return m, nil
	}
	var cmd tea.Cmd
	m.team, cmd = m.team.Update(msg)
	return m, cmd
}

func (m onboardingModel) handleTeamList(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	filtered := m.filteredTeams()
	switch msg.String() {
	case "esc", "left":
		m.step = onboardBaseBranch
		m.baseBranchInputFocused = false
		return m, nil
	case "up":
		if m.teamCursor > 0 {
			m.teamCursor--
		}
		return m, nil
	case "down":
		if m.teamCursor < len(filtered)-1 {
			m.teamCursor++
		}
		return m, nil
	case "enter":
		if m.teamCursor >= 0 && m.teamCursor < len(filtered) {
			m.team.SetValue(filtered[m.teamCursor].Key)
			m.step = onboardLabels
		}
		return m, nil
	case "ctrl+t":
		return m.switchToManual()
	}
	var cmd tea.Cmd
	m.teamFilter, cmd = m.teamFilter.Update(msg)
	if n := len(m.filteredTeams()); m.teamCursor >= n {
		m.teamCursor = max(0, n-1)
	}
	return m, cmd
}

func (m onboardingModel) switchToManual() (onboardingModel, tea.Cmd) {
	m.teamManual = true
	m.teamFilter.Blur()
	m.team.Focus()
	return m, textinput.Blink
}

// filteredTeams ranks the detected options against the search box with a fuzzy
// match over "key name"; an empty query returns every option in detection order.
func (m onboardingModel) filteredTeams() []DetectedTeam {
	q := strings.TrimSpace(m.teamFilter.Value())
	if q == "" {
		return m.teamOptions
	}
	hay := make([]string, len(m.teamOptions))
	for i, t := range m.teamOptions {
		hay[i] = t.Key + " " + t.Name
	}
	matches := fuzzy.Find(q, hay)
	out := make([]DetectedTeam, 0, len(matches))
	for _, mt := range matches {
		out = append(out, m.teamOptions[mt.Index])
	}
	return out
}

// entityLabel is the per-provider noun for the selectable container, falling
// back to a provider-derived default before detection has reported one.
func (m onboardingModel) entityLabel() string {
	if m.teamLabel != "" {
		return m.teamLabel
	}
	switch m.selectedTracker() {
	case "jira":
		return "project"
	case "github":
		return "repository"
	default:
		return "team"
	}
}

// textEntryActive reports whether a focused text input owns keystrokes, so 'q'
// must type into the field instead of navigating back (the user is typing a
// branch, team, or search query).
func (m onboardingModel) textEntryActive() bool {
	switch m.step {
	case onboardLinearAPIKey:
		return m.apiKeyInputFocused
	case onboardJiraCreds:
		return true
	case onboardBaseBranch:
		return m.baseBranchInputFocused
	case onboardLinearTeam:
		return !m.teamDetecting
	}
	return false
}

func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func pluralLabel(label string) string {
	if label == "repository" {
		return "repositories"
	}
	return label + "s"
}

// titleTracker returns the display name of a tracker provider for headings.
func titleTracker(provider string) string {
	switch provider {
	case "jira":
		return "Jira"
	case "github":
		return "GitHub"
	default:
		return "Linear"
	}
}

// labelCreationSupported reports whether Trau can pre-create the routing labels
// for a tracker. Jira labels are freeform strings created implicitly when first
// applied, so there is nothing to create; Linear and GitHub expose label APIs.
func labelCreationSupported(provider string) bool {
	return provider != "jira"
}

// labelStepOptions returns the choices shown on the labels step. Trackers that
// can create labels offer to do so now; Jira (freeform labels) shows a single
// acknowledgement since no creation is needed.
func (m onboardingModel) labelStepOptions() []string {
	if labelCreationSupported(m.selectedTracker()) {
		return []string{"Create the labels in " + titleTracker(m.selectedTracker()) + " now", "I'll create the labels myself"}
	}
	return []string{"Continue"}
}

func (m onboardingModel) handleLabels(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	opts := m.labelStepOptions()
	if m.labelsCursor >= len(opts) {
		m.labelsCursor = len(opts) - 1
	}
	if m.labelsCursor < 0 {
		m.labelsCursor = 0
	}
	switch msg.String() {
	case "esc", "left":
		m.step = onboardLinearTeam
		return m, nil
	case "enter":
		m.createLabels = labelCreationSupported(m.selectedTracker()) && m.labelsCursor == 0
		m.step = onboardTimeTracking
	case "up", "k":
		if m.labelsCursor > 0 {
			m.labelsCursor--
		}
	case "down", "j":
		if m.labelsCursor < len(opts)-1 {
			m.labelsCursor++
		}
	}
	return m, nil
}

func (m onboardingModel) handleTimeTracking(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "left":
		m.step = onboardLabels
		return m, nil
	case "enter":
		m.timelog = m.timelogCursor == 1
		m.step = onboardCI
	case "up", "k":
		if m.timelogCursor > 0 {
			m.timelogCursor--
		}
	case "down", "j":
		if m.timelogCursor < len(m.timelogOptions)-1 {
			m.timelogCursor++
		}
	}
	return m, nil
}

func (m onboardingModel) handleCI(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "left":
		m.step = onboardTimeTracking
		return m, nil
	case "enter":
		m.requireCI = m.ciCursor == 0
		m.step = onboardWrite
	case "up", "k":
		if m.ciCursor > 0 {
			m.ciCursor--
		}
	case "down", "j":
		if m.ciCursor < len(m.ciOptions)-1 {
			m.ciCursor++
		}
	}
	return m, nil
}

func (m onboardingModel) handleWrite(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "left":
		if m.writing {
			return m, nil
		}
		m.step = onboardCI
		return m, nil
	case "enter":
		if m.writing {
			return m, nil
		}
		m.writing = true
		m.errMsg = ""
		return m, m.writeConfigCmd()
	}
	return m, nil
}

func (m onboardingModel) handleCreateLabels(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.step = onboardDone
	}
	return m, nil
}

func (m onboardingModel) handleDone(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	if msg.String() == "enter" || msg.String() == "esc" {
		m.done = true
	}
	return m, nil
}

func (m onboardingModel) handleNoRepo(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	if msg.String() == "enter" || msg.String() == "esc" {
		m.done = true
	}
	return m, nil
}

type setupDoneMsg struct {
	result SetupResult
	err    error
}

type systemCheckResultMsg struct {
	index  int
	status checkStatus
	err    error
}

type systemCheckAdvanceMsg struct{}

type systemCheckDoneMsg struct{}

type systemCheckAdvanceStepMsg struct{}

func (m *onboardingModel) resetSystemChecks() {
	m.systemChecks = []systemCheck{
		{name: "git", desc: "version control"},
		{name: "gh", desc: "GitHub CLI"},
		{name: "github-auth", desc: "GitHub authentication"},
		{name: "claude", desc: "Claude Code provider"},
		{name: "codex", desc: "Codex provider"},
		{name: "kimi", desc: "Kimi provider"},
		{name: "skills", desc: "skills"},
		{name: "linear", desc: "Linear API or MCP"},
		{name: "jira", desc: "Jira / Atlassian MCP"},
		{name: "github", desc: "GitHub issues (gh / MCP)"},
	}
	m.systemCheckIndex = 0
	m.systemCheckDone = false
	m.systemCheckStarted = false
	if m.mcp != nil {
		m.mcp.reset()
	}
}

func newSystemCheckBar() progress.Model {
	return progress.New(
		progress.WithColors(theme.Brand, theme.Accent),
		progress.WithWidth(38),
		progress.WithoutPercentage(),
	)
}

func (m onboardingModel) systemCheckProgress() float64 {
	total := len(m.systemChecks)
	if total == 0 {
		return 0
	}
	done := m.systemCheckIndex
	if done > total {
		done = total
	}
	return float64(done) / float64(total)
}

func (m onboardingModel) runSystemChecksCmd() tea.Cmd {
	return func() tea.Msg {
		return systemCheckAdvanceMsg{}
	}
}

func (m onboardingModel) nextSystemCheckCmd() tea.Cmd {
	if m.systemCheckIndex >= len(m.systemChecks) {
		return func() tea.Msg {
			return systemCheckDoneMsg{}
		}
	}
	idx := m.systemCheckIndex
	name := m.systemChecks[idx].name
	probe := m.mcp
	ghReady := m.checkStatusFor("github-auth") == checkDone
	linearAPIReady := m.actions.LinearAPIKeyConfigured()
	prefillProvider := m.actions.OnboardingPrefill().Provider
	repoRoot := m.repoRoot
	return tea.Tick(420*time.Millisecond, func(time.Time) tea.Msg {
		status, err := runSystemCheck(name, probe, ghReady, linearAPIReady, prefillProvider, repoRoot)
		return systemCheckResultMsg{index: idx, status: status, err: err}
	})
}

func (m onboardingModel) checkStatusFor(name string) checkStatus {
	for _, c := range m.systemChecks {
		if c.name == name {
			return c.status
		}
	}
	return checkPending
}

func runSystemCheck(name string, probe *mcpProbe, ghReady, linearAPIReady bool, prefillProvider, repoRoot string) (checkStatus, error) {
	switch name {
	case "git":
		_, err := exec.LookPath("git")
		if err != nil {
			return checkFailed, err
		}
		return checkDone, nil
	case "gh":
		_, err := exec.LookPath("gh")
		if err != nil {
			return checkFailed, err
		}
		return checkDone, nil
	case "github-auth":
		if _, err := exec.LookPath("gh"); err != nil {
			return checkFailed, err
		}
		cmd := exec.Command("gh", "auth", "status")
		if err := cmd.Run(); err != nil {
			return checkFailed, err
		}
		return checkDone, nil
	case "claude", "codex", "kimi":
		_, err := exec.LookPath(name)
		if err != nil {
			return checkFailed, err
		}
		return checkDone, nil
	case "skills":
		return runSkillsCheck(prefillProvider, repoRoot)
	case "linear", "jira", "github":
		return runTrackerCheck(name, probe, ghReady, linearAPIReady)
	}
	return checkFailed, fmt.Errorf("unknown check %q", name)
}

func runSkillsCheck(prefillProvider, repoRoot string) (checkStatus, error) {
	r := agent.CheckSkillReadiness(repoRoot)
	if r.HasSkills {
		return checkDone, nil
	}
	msg := agent.MissingSkillsMessage(r)
	if msg == "" {
		msg = "no skills found — required if you select kimi or codex"
	}
	if prefillProvider == "kimi" || prefillProvider == "codex" {
		return checkFailed, fmt.Errorf("%s", msg)
	}
	return checkSkipped, fmt.Errorf("%s", msg)
}

// runTrackerCheck reports whether a ticket-management backend is reachable.
// Linear accepts a configured LINEAR_API_KEY as an alternative to the MCP, so
// a user without the Linear MCP can still proceed. GitHub additionally accepts
// an authenticated gh CLI. When the claude CLI is absent we cannot probe MCPs
// and report skipped rather than failed, so a codex/kimi user is never wrongly
// blocked.
func runTrackerCheck(name string, probe *mcpProbe, ghReady, linearAPIReady bool) (checkStatus, error) {
	res := probe.result()
	if name == "github" && ghReady {
		return checkDone, nil
	}
	if name == "linear" && linearAPIReady {
		return checkDone, nil
	}
	if !res.available {
		return checkSkipped, nil
	}
	if res.connected[name] {
		return checkDone, nil
	}
	if name == "linear" {
		return checkSkipped, fmt.Errorf("linear MCP not connected — add one in Settings or enter a Linear API key in the next step")
	}
	return checkFailed, fmt.Errorf("%s MCP not connected", name)
}

// mcpProbe memoises a single `claude mcp list` invocation. The command
// health-checks every configured server (slow when unreachable servers time
// out), so it must run at most once per readiness pass. The pointer is shared
// across value copies of onboardingModel, so the sync.Once survives the Elm
// update loop.
type mcpProbe struct {
	once sync.Once
	res  mcpResult
}

type mcpResult struct {
	available bool            // claude CLI present and the listing ran
	connected map[string]bool // tracker name -> MCP reported as connected
}

func newMCPProbe() *mcpProbe { return &mcpProbe{} }

// reset clears the cache so a re-check re-probes the MCP servers.
func (p *mcpProbe) reset() { *p = mcpProbe{} }

func (p *mcpProbe) result() mcpResult {
	p.once.Do(func() {
		p.res = probeMCPServers()
	})
	return p.res
}

func probeMCPServers() mcpResult {
	res := mcpResult{connected: map[string]bool{}}
	if _, err := exec.LookPath("claude"); err != nil {
		return res
	}
	out, err := exec.Command("claude", "mcp", "list").CombinedOutput()
	if err != nil {
		return res
	}
	res.available = true
	for _, line := range strings.Split(strings.ToLower(string(out)), "\n") {
		if !strings.Contains(line, "✔") {
			continue
		}
		switch {
		case strings.Contains(line, "linear"):
			res.connected["linear"] = true
		case strings.Contains(line, "atlassian"), strings.Contains(line, "jira"), strings.Contains(line, "rovo"):
			res.connected["jira"] = true
		case strings.Contains(line, "github"):
			res.connected["github"] = true
		}
	}
	return res
}

func (m onboardingModel) applySystemCheckResult(msg systemCheckResultMsg) onboardingModel {
	if msg.index < 0 || msg.index >= len(m.systemChecks) || msg.index != m.systemCheckIndex {
		return m
	}
	m.systemChecks[msg.index].status = msg.status
	m.systemChecks[msg.index].err = msg.err
	m.systemCheckIndex++
	if m.systemCheckIndex < len(m.systemChecks) {
		m.systemChecks[m.systemCheckIndex].status = checkRunning
	}
	return m
}

func (m onboardingModel) advanceSystemCheck() onboardingModel {
	if m.systemCheckIndex < len(m.systemChecks) {
		m.systemChecks[m.systemCheckIndex].status = checkRunning
	}
	return m
}

func (m onboardingModel) handleSystemCheck(msg tea.KeyPressMsg) (onboardingModel, tea.Cmd) {
	if msg.String() == "esc" {
		m.done = true
		return m, nil
	}
	if msg.String() == "enter" {
		if !m.systemCheckStarted {
			m.systemCheckStarted = true
			m.systemChecks[0].status = checkRunning
			return m, tea.Batch(m.systemCheckSpin.Tick, m.runSystemChecksCmd())
		}
		if m.systemCheckDone {
			if m.systemChecksPass() {
				m.step = onboardWelcome
			} else {
				m.resetSystemChecks()
				m.systemCheckBar = newSystemCheckBar()
				m.systemChecks[0].status = checkRunning
				m.systemCheckStarted = true
				return m, tea.Batch(m.systemCheckSpin.Tick, m.runSystemChecksCmd())
			}
		}
	}
	return m, nil
}

func (m onboardingModel) systemChecksPass() bool {
	for _, c := range m.systemChecks {
		switch c.name {
		case "git", "gh", "github-auth":
			if c.status != checkDone {
				return false
			}
		case "skills":
			if c.status == checkFailed {
				return false
			}
		}
	}
	return m.anyProviderReady() && m.anyTrackerReady()
}

func (m onboardingModel) anyProviderReady() bool {
	for _, c := range m.systemChecks {
		if isProviderCheck(c.name) && c.status == checkDone {
			return true
		}
	}
	return false
}

// anyTrackerReady reports whether at least one ticket-management backend is
// usable. A connected MCP (or authenticated gh for GitHub) satisfies it. When
// every tracker probe was skipped — the claude CLI is absent, so we could not
// verify — we do not block: the chosen provider may still have the MCP wired up.
func (m onboardingModel) anyTrackerReady() bool {
	sawTracker, allSkipped := false, true
	for _, c := range m.systemChecks {
		if !isTrackerCheck(c.name) {
			continue
		}
		sawTracker = true
		if c.status == checkDone {
			return true
		}
		if c.status != checkSkipped {
			allSkipped = false
		}
	}
	return sawTracker && allSkipped
}

func (m onboardingModel) writeConfigCmd() tea.Cmd {
	setup := ProjectSetup{
		Provider:        m.selectedProvider(),
		TrackerProvider: m.selectedTracker(),
		BaseBranch:      strings.TrimSpace(m.baseBranch.Value()),
		Team:            strings.TrimSpace(m.team.Value()),
		ReadyLabel:      "ready-for-agent",
		QuarantineLabel: "needs-human",
		CreateLabels:    m.createLabels,
		EpicFlow:        m.epicFlow,
		Timelog:         m.timelog,
		RequireCI:       m.requireCI,
		LinearAPIKey:    strings.TrimSpace(m.apiKey.Value()),
		JiraBaseURL:     strings.TrimSpace(m.jiraBaseURL.Value()),
		JiraEmail:       strings.TrimSpace(m.jiraEmail.Value()),
		JiraAPIToken:    strings.TrimSpace(m.jiraToken.Value()),
	}
	return func() tea.Msg {
		res, err := m.actions.SetupProject(m.ctx, setup)
		return setupDoneMsg{result: res, err: err}
	}
}

func (m onboardingModel) applySetupDone(msg setupDoneMsg) onboardingModel {
	m.writing = false
	if msg.err != nil {
		m.errMsg = msg.err.Error()
		return m
	}
	m.result = msg.result
	if m.createLabels {
		m.step = onboardCreateLabels
	} else {
		m.step = onboardDone
	}
	return m
}

func (m onboardingModel) selectedTracker() string {
	if m.trackerCursor < 0 || m.trackerCursor >= len(m.trackers) {
		return "linear"
	}
	return m.trackers[m.trackerCursor]
}

func (m onboardingModel) selectedProvider() string {
	if m.providerCursor < 0 || m.providerCursor >= len(m.providers) {
		return "claude"
	}
	return m.providers[m.providerCursor]
}

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

// stepBody renders the current step's card body — the region that scrolls when
// it is taller than the terminal.
func (m onboardingModel) stepBody() string {
	switch m.step {
	case onboardSystemCheck:
		return m.renderSystemCheck()
	case onboardWelcome:
		return m.renderWelcome()
	case onboardProviders:
		return m.renderProviders()
	case onboardLinearAPIKey:
		return m.renderLinearAPIKey()
	case onboardJiraCreds:
		return m.renderJiraCreds()
	case onboardBaseBranch:
		return m.renderBaseBranch()
	case onboardLinearTeam:
		return m.renderLinearTeam()
	case onboardLabels:
		return m.renderLabels()
	case onboardTimeTracking:
		return m.renderTimeTracking()
	case onboardCI:
		return m.renderCI()
	case onboardWrite:
		return m.renderWrite()
	case onboardCreateLabels:
		return m.renderCreateLabels()
	case onboardDone:
		return m.renderDone()
	case onboardNoRepo:
		return m.renderNoRepo()
	}
	return ""
}

// bodyBudget is how many body lines fit under the brand header and above the
// hint on the current terminal.
func (m onboardingModel) bodyBudget() int {
	return cardBodyBudget(m.height, lipgloss.Height(m.brandHeader()))
}

// scrollKeyDelta maps the explicit scroll keys to a body-line delta. Onboarding
// steps own ↑↓ for navigation, so scrolling uses pgup/pgdn (a near-full page).
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

// clampScroll holds an offset within [0, maxScroll] for the current step's body.
func (m onboardingModel) clampScroll(off int) int {
	if off < 0 {
		return 0
	}
	maxOff := strings.Count(m.stepBody(), "\n") + 1 - m.bodyBudget()
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
	lines := strings.Split(m.stepBody(), "\n")
	win, _, overflow := windowAt(lines, m.scrollOffset, m.bodyBudget())
	hint := m.hint()
	if overflow {
		hint += " · pgup/pgdn/wheel scroll"
	}
	return centerScreen(m.width, m.height,
		m.brandHeader(), cardBox(m.styles, m.width, strings.Join(win, "\n")), hintBar(m.styles, hint))
}

func (m onboardingModel) renderSystemCheck() string {
	s := m.styles
	total := len(m.systemChecks)
	var rows []string
	rows = append(rows, s.SummaryTitle.Render("System readiness check"))
	rows = append(rows, "")
	rows = append(rows, s.Subtle.Render("Trau needs git, the GitHub CLI, one AI provider, and one ticket system."))
	rows = append(rows, "")

	if m.systemCheckStarted {
		rows = append(rows, m.systemCheckBar.View())
		if !m.systemCheckDone {
			label := fmt.Sprintf("Checking %d of %d…", min(m.systemCheckIndex+1, total), total)
			if isTrackerCheck(m.currentCheckName()) {
				label += " (probing MCP servers can take a few seconds)"
			}
			rows = append(rows, s.Info.Render(label))
		}
		rows = append(rows, "")
	}

	rows = append(rows, s.Header.Render("REQUIRED"))
	for i, c := range m.systemChecks {
		if isRequiredCheck(c.name) {
			rows = append(rows, m.renderCheckLine(i, c))
		}
	}
	rows = append(rows, "")
	rows = append(rows, s.Header.Render("AI PROVIDERS")+s.Subtle.Render("  · need at least one"))
	for i, c := range m.systemChecks {
		if isProviderCheck(c.name) {
			rows = append(rows, m.renderCheckLine(i, c))
		}
	}
	rows = append(rows, "")
	rows = append(rows, s.Header.Render("TICKET MANAGEMENT")+s.Subtle.Render("  · need at least one"))
	for i, c := range m.systemChecks {
		if isTrackerCheck(c.name) {
			rows = append(rows, m.renderCheckLine(i, c))
		}
	}

	if !m.systemCheckStarted {
		rows = append(rows, "")
		rows = append(rows, s.Info.Render("Press enter to run the check."))
	} else if m.systemCheckDone {
		rows = append(rows, "")
		if m.systemChecksPass() {
			rows = append(rows, s.Success.Render("✓ All set — continuing…"))
		} else {
			rows = append(rows, s.Error.Render("✗ Some required tools are missing."))
			rows = append(rows, s.Subtle.Render("Install them, then press enter to re-check."))
		}
	}

	return strings.Join(rows, "\n")
}

func (m onboardingModel) renderCheckLine(idx int, c systemCheck) string {
	s := m.styles
	const nameW = 13
	name := c.name
	if len(name) < nameW {
		name += strings.Repeat(" ", nameW-len(name))
	}

	switch c.status {
	case checkRunning:
		return m.systemCheckSpin.View() + " " + s.Header.Bold(true).Render(name) + " " + s.Info.Render("checking…")
	case checkDone:
		return s.Success.Render("✓") + " " + s.Success.Render(name) + " " + s.Subtle.Render(c.desc)
	case checkSkipped:
		hint := "not verified — install claude to probe MCPs"
		if c.err != nil {
			hint = c.err.Error()
		}
		return s.Subtle.Render("–") + " " + s.Subtle.Render(name) + " " + s.Subtle.Render(hint)
	case checkFailed:
		if isProviderCheck(c.name) && m.anyProviderReady() {
			return s.Warning.Render("⚠") + " " + s.Subtle.Render(name) + " " + s.Subtle.Render("optional — another provider is ready")
		}
		if isTrackerCheck(c.name) && m.anyTrackerReady() {
			return s.Warning.Render("⚠") + " " + s.Subtle.Render(name) + " " + s.Subtle.Render("optional — another ticket system is ready")
		}
		return s.Error.Render("✗") + " " + s.Error.Render(name) + " " + s.Error.Render(checkFailureHint(c.name, c.err))
	default:
		return s.Subtle.Render("·") + " " + s.Subtle.Render(name) + " " + s.Subtle.Render(c.desc)
	}
}

func isProviderCheck(name string) bool {
	switch name {
	case "claude", "codex", "kimi":
		return true
	}
	return false
}

func isTrackerCheck(name string) bool {
	switch name {
	case "linear", "jira", "github":
		return true
	}
	return false
}

func isRequiredCheck(name string) bool {
	return !isProviderCheck(name) && !isTrackerCheck(name)
}

func (m onboardingModel) currentCheckName() string {
	if m.systemCheckIndex < 0 || m.systemCheckIndex >= len(m.systemChecks) {
		return ""
	}
	return m.systemChecks[m.systemCheckIndex].name
}

func checkFailureHint(name string, err error) string {
	switch name {
	case "git":
		return "install git"
	case "gh":
		return "install the GitHub CLI"
	case "github-auth":
		return "run `gh auth login`"
	case "claude", "codex", "kimi":
		return fmt.Sprintf("install %s or pick a different provider", name)
	case "skills":
		return "install skills with `npx skills add <skill>` or add them to .agents/skills/ (see https://skills.sh)"
	case "linear":
		return "connect the Linear MCP or enter a Linear API key"
	case "jira":
		return "connect the Atlassian/Jira MCP (claude mcp add)"
	case "github":
		return "run `gh auth login` or connect the GitHub MCP"
	}
	if err != nil {
		return err.Error()
	}
	return "failed"
}

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

func (m onboardingModel) renderProviders() string {
	var rows []string
	rows = append(rows, m.styles.SummaryTitle.Render("Choose providers"))
	rows = append(rows, "")
	rows = append(rows, m.styles.Subtle.Render("Project management:"))
	for i, t := range m.trackers {
		rows = append(rows, m.renderOptionRow(t, m.providersPMFocused && i == m.trackerCursor))
	}
	rows = append(rows, "")
	rows = append(rows, m.styles.Subtle.Render("AI agent:"))
	for i, p := range m.providers {
		rows = append(rows, m.renderOptionRow(p, !m.providersPMFocused && i == m.providerCursor))
	}
	if !m.providersPMFocused {
		if warn := m.providerSkillWarning(m.selectedProvider()); warn != "" {
			rows = append(rows, "")
			rows = append(rows, m.styles.Warning.Render("⚠ "+warn))
		}
	}
	return strings.Join(rows, "\n")
}

// providerSkillWarning returns a warning message when the selected AI provider
// expects repo skills but none were found.
func (m onboardingModel) providerSkillWarning(provider string) string {
	if !providerNeedsSkills(provider) {
		return ""
	}
	r := agent.CheckSkillReadiness(m.repoRoot)
	if r.HasSkills {
		return ""
	}
	msg := agent.MissingSkillsMessage(r)
	if msg == "" {
		return fmt.Sprintf("%s expects skills in this repo, but none were found. Add skills to .agents/skills/ before running the loop.", provider)
	}
	return msg
}

func providerNeedsSkills(name string) bool {
	reg := agent.DefaultRegistry()
	if spec, ok := reg.Lookup(name); ok {
		return spec.NeedsSkills
	}
	return false
}

func (m onboardingModel) renderOptionRow(name string, selected bool) string {
	if m.optionDisabled(name) {
		dim := m.styles.Subtle.Strikethrough(true)
		return "  " + dim.Render(name) + "  " + m.styles.Subtle.Render(m.disabledReason(name))
	}
	return listRow(m.styles, selected, name, "", 0)
}

func (m onboardingModel) disabledReason(name string) string {
	if isTrackerCheck(name) {
		return "not connected — see readiness check"
	}
	return "not installed — see readiness check"
}

const linearAPIKeySettingsURL = "https://linear.app/settings/account/security"

func (m onboardingModel) renderLinearAPIKey() string {
	s := m.styles
	var rows []string
	rows = append(rows, s.SummaryTitle.Render("Linear API key"))
	rows = append(rows, "")
	rows = append(rows, "Enter a Linear personal API key to enable fast direct API calls.")
	rows = append(rows, s.Subtle.Render("Create one in Linear under Settings > Account > Security & Access > API."))
	rows = append(rows, "")
	rows = append(rows, s.Subtle.Render("Generate key: "+linearAPIKeySettingsURL+" (press 'o' to open)"))
	rows = append(rows, s.Subtle.Render("Leave blank to keep using the Linear MCP for all operations."))
	rows = append(rows, "")
	rows = append(rows, m.apiKey.View())
	return strings.Join(rows, "\n")
}

const jiraTokenSettingsURL = "https://id.atlassian.com/manage-profile/security/api-tokens"

func (m onboardingModel) renderJiraCreds() string {
	s := m.styles
	var rows []string
	rows = append(rows, s.SummaryTitle.Render("Jira REST credentials"))
	rows = append(rows, "")
	rows = append(rows, "Enter a classic Jira API token to enable fast direct REST calls.")
	rows = append(rows, s.Subtle.Render("Per-repo credentials let two repos use two separate Jira accounts."))
	rows = append(rows, s.Subtle.Render("Leave blank to use the Atlassian (Rovo) MCP for all Jira operations."))
	rows = append(rows, "")
	rows = append(rows, s.Subtle.Render("Generate a classic token: "+jiraTokenSettingsURL))
	rows = append(rows, "")
	rows = append(rows, m.jiraBaseURL.View())
	rows = append(rows, m.jiraEmail.View())
	rows = append(rows, m.jiraToken.View())
	return strings.Join(rows, "\n")
}

func (m onboardingModel) renderBaseBranch() string {
	var rows []string
	rows = append(rows, m.styles.SummaryTitle.Render("Base branch & branching strategy"))
	rows = append(rows, "")
	rows = append(rows, "Default branch for standalone tickets:")
	rows = append(rows, m.baseBranch.View())
	rows = append(rows, "")
	rows = append(rows, "When a ticket has sub-issues:")
	for i, opt := range m.branchingOptions {
		rows = append(rows, listRow(m.styles, !m.baseBranchInputFocused && i == m.branchingCursor, opt, "", 0))
	}
	return strings.Join(rows, "\n")
}

func (m onboardingModel) renderLinearTeam() string {
	s := m.styles
	title, desc := m.teamPrompt()
	label := m.entityLabel()

	if m.teamDetecting {
		detail := "Driving the " + m.selectedTracker() + " MCP through " + m.selectedProvider() + " — this can take a few seconds."
		if m.selectedTracker() == "jira" && m.jiraRESTConfigured() {
			detail = "Querying the Jira REST API with the credentials you entered…"
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			s.SummaryTitle.Render(title),
			"",
			m.teamSpin.View()+" "+s.Info.Render("Detecting your "+pluralLabel(label)+"…"),
			"",
			s.Subtle.Render(detail),
		)
	}

	if m.teamAutoFilled {
		return lipgloss.JoinVertical(lipgloss.Left,
			s.SummaryTitle.Render(title),
			"",
			s.Subtle.Render("Detected from the git remote — edit if this isn't right:"),
			"",
			m.team.View(),
		)
	}

	if m.teamManual {
		rows := []string{s.SummaryTitle.Render(title), ""}
		if m.teamDetectErr != nil {
			rows = append(rows, s.Warning.Render("Couldn't detect "+pluralLabel(label)+" automatically — enter it manually."))
			rows = append(rows, s.Subtle.Render(m.teamDetectErr.Error()), "")
		}
		rows = append(rows, desc, "", m.team.View())
		return strings.Join(rows, "\n")
	}

	filtered := m.filteredTeams()
	rows := []string{
		s.SummaryTitle.Render(title),
		"",
		s.Subtle.Render(fmt.Sprintf("Select your %s (%d found):", label, len(m.teamOptions))),
		"",
		m.teamFilter.View(),
		"",
	}
	const maxRows = 8
	if len(filtered) == 0 {
		rows = append(rows, s.Subtle.Render("No matches."))
	}
	for i, t := range filtered {
		if i >= maxRows {
			rows = append(rows, s.Subtle.Render(fmt.Sprintf("  …and %d more — keep typing to narrow.", len(filtered)-maxRows)))
			break
		}
		rows = append(rows, m.renderTeamRow(t, i == m.teamCursor))
	}
	return strings.Join(rows, "\n")
}

func (m onboardingModel) renderTeamRow(t DetectedTeam, selected bool) string {
	s := m.styles
	name := ""
	if t.Name != "" && t.Name != t.Key {
		name = "  " + s.Subtle.Render(t.Name)
	}
	return listRow(s, selected, t.Key, "", 0) + name
}

func (m onboardingModel) teamPrompt() (title, desc string) {
	switch m.selectedTracker() {
	case "jira":
		return "Jira project", "Enter your Jira project key (e.g. PROJ)."
	case "github":
		return "GitHub repository", "Enter the repository slug (e.g. owner/repo)."
	default:
		return "Linear team", "Enter your Linear team name or key (used to find ready tickets)."
	}
}

func (m onboardingModel) renderLabels() string {
	opts := m.labelStepOptions()
	cursor := m.labelsCursor
	if cursor >= len(opts) {
		cursor = len(opts) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	var rows []string
	rows = append(rows, m.styles.SummaryTitle.Render(titleTracker(m.selectedTracker())+" labels"))
	rows = append(rows, "")
	rows = append(rows, "Trau uses two labels to route tickets:")
	rows = append(rows, "  • ready-for-agent  → tickets Trau should pick up")
	rows = append(rows, "  • needs-human      → tickets that failed and need a human")
	rows = append(rows, "")
	if labelCreationSupported(m.selectedTracker()) {
		rows = append(rows, "Defaults are ready-for-agent and needs-human.")
	} else {
		rows = append(rows, m.styles.Subtle.Render("Jira labels are freeform — Trau applies these automatically as tickets"))
		rows = append(rows, m.styles.Subtle.Render("move, so there's nothing to create. Label a ticket ready-for-agent for"))
		rows = append(rows, m.styles.Subtle.Render("Trau to pick it up."))
	}
	rows = append(rows, "")
	for i, opt := range opts {
		rows = append(rows, listRow(m.styles, i == cursor, opt, "", 0))
	}
	return strings.Join(rows, "\n")
}

func (m onboardingModel) renderTimeTracking() string {
	var rows []string
	rows = append(rows, m.styles.SummaryTitle.Render("Time tracking (optional)"))
	rows = append(rows, "")
	rows = append(rows, "Track estimated dev time per ticket?")
	rows = append(rows, m.styles.Subtle.Render("After a ticket merges, trau writes a per-ticket effort estimate to"))
	rows = append(rows, m.styles.Subtle.Render(".dev-flow/time/<TICKET>.json, a format other time-tracking tools can read."))
	rows = append(rows, m.styles.Subtle.Render("Off by default; the number is an estimate of human effort, not agent time."))
	rows = append(rows, "")
	for i, opt := range m.timelogOptions {
		rows = append(rows, listRow(m.styles, i == m.timelogCursor, opt, "", 0))
	}
	return strings.Join(rows, "\n")
}

func (m onboardingModel) renderCI() string {
	var rows []string
	rows = append(rows, m.styles.SummaryTitle.Render("CI merge gate"))
	rows = append(rows, "")
	if m.ciHasPRDet {
		rows = append(rows, m.styles.Subtle.Render("Detected a pull_request-triggered workflow in .github/workflows."))
	} else {
		rows = append(rows, m.styles.Subtle.Render("No pull_request-triggered workflow found in .github/workflows — PRs in"))
		rows = append(rows, m.styles.Subtle.Render("this repo would get zero checks, which the gate reads as never-green."))
	}
	rows = append(rows, m.styles.Subtle.Render("Skip the gate only if this repo has no PR CI (detection misses non-GitHub"))
	rows = append(rows, m.styles.Subtle.Render("CI). Change later in Settings or via REQUIRE_CI in .trau.ini."))
	rows = append(rows, "")
	for i, opt := range m.ciOptions {
		rows = append(rows, listRow(m.styles, i == m.ciCursor, opt, "", 0))
	}
	return strings.Join(rows, "\n")
}

func (m onboardingModel) renderWrite() string {
	if m.writing {
		return lipgloss.JoinVertical(lipgloss.Left,
			m.styles.SummaryTitle.Render("Writing config"),
			"",
			"Saving "+filepath.Join(m.repoRoot, config.ProjectConfigName)+"…",
		)
	}
	path := filepath.Join(m.repoRoot, config.ProjectConfigName)
	var rows []string
	rows = append(rows, m.styles.SummaryTitle.Render("Ready to write config"))
	rows = append(rows, "")
	rows = append(rows, "Path: "+path)
	rows = append(rows, "")
	rows = append(rows, "Values:")
	rows = append(rows, "  TRACKER_PROVIDER="+m.selectedTracker())
	rows = append(rows, "  LINEAR_TEAM="+strings.TrimSpace(m.team.Value()))
	if m.selectedTracker() == "linear" {
		if key := strings.TrimSpace(m.apiKey.Value()); key != "" {
			rows = append(rows, "  LINEAR_API_KEY="+maskAPIKey(key))
		} else {
			rows = append(rows, "  LINEAR_API_KEY=(blank — will use MCP)")
		}
	}
	if m.selectedTracker() == "jira" {
		if v := strings.TrimSpace(m.jiraBaseURL.Value()); v != "" {
			rows = append(rows, "  JIRA_BASE_URL="+v)
		}
		if v := strings.TrimSpace(m.jiraEmail.Value()); v != "" {
			rows = append(rows, "  JIRA_EMAIL="+v)
		}
		if tok := strings.TrimSpace(m.jiraToken.Value()); tok != "" {
			rows = append(rows, "  JIRA_API_TOKEN="+maskAPIKey(tok))
		} else {
			rows = append(rows, "  JIRA_API_TOKEN=(blank — will use MCP)")
		}
	}
	rows = append(rows, "  BASE_BRANCH="+strings.TrimSpace(m.baseBranch.Value()))
	rows = append(rows, "  PROVIDER="+m.selectedProvider())
	rows = append(rows, "  READY_LABEL=ready-for-agent")
	rows = append(rows, "  QUARANTINE_LABEL=needs-human")
	if m.epicFlow {
		rows = append(rows, "  EPIC_FLOW=1")
	} else {
		rows = append(rows, "  EPIC_FLOW=0")
	}
	if m.timelog {
		rows = append(rows, "  TIMELOG_ENABLED=1")
	} else {
		rows = append(rows, "  TIMELOG_ENABLED=0")
	}
	if labelCreationSupported(m.selectedTracker()) {
		if m.createLabels {
			rows = append(rows, "  Create labels in "+titleTracker(m.selectedTracker())+": yes")
		} else {
			rows = append(rows, "  Create labels in "+titleTracker(m.selectedTracker())+": no")
		}
	}
	if m.errMsg != "" {
		rows = append(rows, "")
		rows = append(rows, m.styles.Error.Render("Error: "+m.errMsg))
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
		m.styles.SummaryTitle.Render(titleTracker(m.selectedTracker())+" labels"),
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

func (m onboardingModel) hint() string {
	switch m.step {
	case onboardSystemCheck:
		if !m.systemCheckStarted {
			return "enter start check · esc/q back"
		}
		if m.systemCheckDone {
			return "enter continue/re-check · esc/q back"
		}
		return "checking dependencies…"
	case onboardWelcome:
		return "enter continue · esc/q back"
	case onboardProviders:
		if m.providersPMFocused {
			return "↑↓ move · enter/tab next · esc/q/← back"
		}
		return "↑↓ move · enter select · tab back · esc/q/← back"
	case onboardLinearAPIKey:
		return "enter/tab next · 'o' open key settings · esc/← back"
	case onboardJiraCreds:
		return "tab/↑↓ move · enter next · esc/← back"
	case onboardLabels:
		return "↑↓ move · enter select · esc/q/← back"
	case onboardTimeTracking:
		return "↑↓ move · enter select · esc/q/← back"
	case onboardCI:
		return "↑↓ move · enter select · esc/q/← back"
	case onboardBaseBranch:
		if m.baseBranchInputFocused {
			return "enter/tab next · esc/← back"
		}
		return "↑↓ move · enter select · esc/q/tab back"
	case onboardLinearTeam:
		switch {
		case m.teamDetecting:
			return "detecting… · esc/q back"
		case m.teamManual, m.teamAutoFilled:
			return "enter confirm · esc/← back"
		default:
			return "type to search · ↑↓ move · enter select · ctrl+t manual · esc/← back"
		}
	case onboardWrite:
		if m.writing {
			return "working…"
		}
		return "enter write · esc/q/← back"
	case onboardCreateLabels, onboardDone:
		return "enter/esc/q continue"
	case onboardNoRepo:
		return "enter/esc/q exit"
	}
	return ""
}

func (m onboardingModel) Done() bool { return m.done }
