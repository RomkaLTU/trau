package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"github.com/RomkaLTU/trau/internal/agent"
)

// This file is the animated system-readiness check — a bespoke, non-huh step
// kept as-is per COD-670: grouped probes with per-line status and actionable
// failure hints. It runs before the huh form and gates entry to it.

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

type systemCheckResultMsg struct {
	index  int
	status checkStatus
	err    error
}

type systemCheckAdvanceMsg struct{}

type systemCheckDoneMsg struct{}

type systemCheckAdvanceStepMsg struct{}

type skillsInstallDoneMsg struct{ err error }

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

// skillsInstallOffer returns the curated recommendations to offer for one-key
// install: only when the readiness pass found no skills and the detected
// project type has a pinned recommended set.
func (m onboardingModel) skillsInstallOffer() []agent.SkillRecommendation {
	if m.checkStatusFor("skills") == checkDone {
		return nil
	}
	r := agent.CheckSkillReadiness(m.repoRoot)
	if r.HasSkills {
		return nil
	}
	return r.Missing
}

func (m onboardingModel) installSkillsCmd() tea.Cmd {
	recs := m.skillsOffer
	root := m.repoRoot
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for _, rec := range recs {
			if err := agent.InstallSkill(ctx, root, rec); err != nil {
				return skillsInstallDoneMsg{err: err}
			}
		}
		return skillsInstallDoneMsg{}
	}
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
	if msg.String() == "i" && m.systemCheckDone && !m.skillsInstalling && len(m.skillsOffer) > 0 {
		m.skillsInstalling = true
		m.skillsInstallErr = ""
		return m, tea.Batch(m.systemCheckSpin.Tick, m.installSkillsCmd())
	}
	if msg.String() == "enter" {
		if m.skillsInstalling {
			return m, nil
		}
		if !m.systemCheckStarted {
			m.systemCheckStarted = true
			m.systemChecks[0].status = checkRunning
			return m, tea.Batch(m.systemCheckSpin.Tick, m.runSystemChecksCmd())
		}
		if m.systemCheckDone {
			if m.systemChecksPass() {
				m.phase = phaseWelcome
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
			switch {
			case m.skillsInstalling:
				rows = append(rows, s.Info.Render(m.systemCheckSpin.View()+" installing recommended skills…"))
			case len(m.skillsOffer) > 0:
				rows = append(rows, s.Info.Render("Press i to install the recommended skills for this project (npx skills add)."))
				if m.skillsInstallErr != "" {
					rows = append(rows, s.Error.Render("✗ "+m.skillsInstallErr))
				}
			}
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
