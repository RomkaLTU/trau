package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type fakeOnboardActions struct {
	repoRoot string
	prefill  OnboardingPrefill
	teams    []DetectedTeam
	gotSetup ProjectSetup
}

func (f *fakeOnboardActions) RepoRoot() string                     { return f.repoRoot }
func (f *fakeOnboardActions) OnboardingPrefill() OnboardingPrefill { return f.prefill }
func (f *fakeOnboardActions) LinearAPIKeyConfigured() bool         { return false }

func (f *fakeOnboardActions) DetectTeams(context.Context, string, string, JiraCreds) (TeamDetection, error) {
	return TeamDetection{Label: "project", Teams: f.teams}, nil
}

func (f *fakeOnboardActions) SetupProject(_ context.Context, s ProjectSetup) (SetupResult, error) {
	f.gotSetup = s
	return SetupResult{ConfigPath: "/tmp/.trau.ini"}, nil
}

func typeRunes(m onboardingModel, s string) onboardingModel {
	for _, r := range s {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func pressKey(m onboardingModel, code rune) onboardingModel {
	return stepModel(m, tea.KeyPressMsg{Code: code})
}

// stepModel applies msg then drives the resulting command tree the way the tea
// runtime would, so huh's msg-driven navigation (updateFieldMsg to populate
// options, nextField → nextGroup to advance) actually completes in a test.
func stepModel(m onboardingModel, msg tea.Msg) onboardingModel {
	next, cmd := m.Update(msg)
	return driveCmds(next, cmd)
}

// driveCmds runs a command tree breadth-first, unwrapping huh/tea batch and
// sequence messages (both []tea.Cmd) and feeding leaf messages back through the
// model. Window-size probes are skipped (the form is sized via WithWidth) and a
// step cap keeps self-renewing blink/tick commands from looping forever.
func driveCmds(m onboardingModel, cmd tea.Cmd) onboardingModel {
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 400; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := runCmd(c)
		if msg == nil {
			continue
		}
		if cmds, ok := asCmdSlice(msg); ok {
			queue = append(queue, cmds...)
			continue
		}
		if _, ok := msg.(tea.WindowSizeMsg); ok {
			continue
		}
		var next tea.Cmd
		m, next = m.Update(msg)
		if next != nil {
			queue = append(queue, next)
		}
	}
	return m
}

// runCmd executes a command but abandons it if it blocks — huh's blink/spinner
// ticks are tea.Tick sleeps that would otherwise stall (and self-renew) the
// synchronous drive loop. Navigation and option messages return immediately.
func runCmd(c tea.Cmd) tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() { done <- c() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(20 * time.Millisecond):
		return nil
	}
}

// asCmdSlice recognises tea.BatchMsg and huh's unexported sequenceMsg, both of
// which are []tea.Cmd underneath.
func asCmdSlice(msg tea.Msg) ([]tea.Cmd, bool) {
	rv := reflect.ValueOf(msg)
	if rv.Kind() != reflect.Slice {
		return nil, false
	}
	out := make([]tea.Cmd, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		c, ok := rv.Index(i).Interface().(tea.Cmd)
		if !ok {
			return nil, false
		}
		out = append(out, c)
	}
	return out, true
}

// formModel returns a model parked on the huh form with group 0 active, so a
// test can drive the middle steps without walking the async system check.
func formModel(fake *fakeOnboardActions, tracker string) onboardingModel {
	m := newOnboardingModelWithPrefill(context.Background(), fake, DefaultStyles(), 80, 40, OnboardingPrefill{})
	if tracker != "" {
		m.fv.tracker = tracker
	}
	m.phase = phaseForm
	m.form = m.newForm()
	return driveCmds(m, m.form.Init())
}

// TestOnboardingScrollsSystemCheck is the AC2 regression: a step taller than a
// short terminal stays fully reachable. pgdown and the wheel move the offset and
// the view never exceeds the terminal.
func TestOnboardingScrollsSystemCheck(t *testing.T) {
	fake := &fakeOnboardActions{repoRoot: t.TempDir()}
	m := newOnboardingModelWithPrefill(context.Background(), fake, DefaultStyles(), 60, 12, OnboardingPrefill{})

	if h := lipgloss.Height(m.View()); h > 12 {
		t.Fatalf("system check is %d rows on a 12-row terminal — content clipped", h)
	}
	total := strings.Count(m.phaseBody(), "\n") + 1
	if total <= m.bodyBudget() {
		t.Fatalf("precondition: system check (%d lines) should overflow the %d-line budget", total, m.bodyBudget())
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.scrollOffset == 0 {
		t.Fatalf("pgdown did not scroll; offset still 0")
	}
	down := m.scrollOffset

	m, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.scrollOffset >= down {
		t.Errorf("wheel-up did not scroll back: %d ≥ %d", m.scrollOffset, down)
	}

	for i := 0; i < 20; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if h := lipgloss.Height(m.View()); h > 12 {
		t.Errorf("view is %d rows after scrolling to the end — clipped", h)
	}
}

// TestOnboardingWriteConfigMapping is the byte-identical-config guard: the same
// inputs produce the same ProjectSetup handed to SetupProject. Blank base branch
// defaults to main; jira never opts into label creation; the manual-entry
// sentinel resolves to the typed team.
func TestOnboardingWriteConfigMapping(t *testing.T) {
	fake := &fakeOnboardActions{repoRoot: t.TempDir()}
	m := newOnboardingModelWithPrefill(context.Background(), fake, DefaultStyles(), 80, 40, OnboardingPrefill{})
	m.fv.tracker = "jira"
	m.fv.aiProvider = "codex"
	m.fv.baseBranch = "   "
	m.fv.jiraBase = "https://acme.atlassian.net"
	m.fv.jiraEmail = "me@acme.com"
	m.fv.jiraToken = "s3cr3t-token"
	m.fv.epicFlow = true
	m.fv.timelog = true
	m.fv.requireCI = false
	m.fv.labels = "create" // jira ignores this
	m.fv.team = teamManualSentinel
	m.fv.teamManual = "  PROJ  "

	msg := m.writeConfigCmd()()
	if _, ok := msg.(setupDoneMsg); !ok {
		t.Fatalf("writeConfigCmd msg = %T, want setupDoneMsg", msg)
	}
	got := fake.gotSetup
	checks := []struct {
		name, got, want string
	}{
		{"TrackerProvider", got.TrackerProvider, "jira"},
		{"Provider", got.Provider, "codex"},
		{"BaseBranch", got.BaseBranch, "main"},
		{"Team", got.Team, "PROJ"},
		{"ReadyLabel", got.ReadyLabel, "ready-for-agent"},
		{"QuarantineLabel", got.QuarantineLabel, "needs-human"},
		{"JiraBaseURL", got.JiraBaseURL, "https://acme.atlassian.net"},
		{"JiraEmail", got.JiraEmail, "me@acme.com"},
		{"JiraAPIToken", got.JiraAPIToken, "s3cr3t-token"},
		{"LinearAPIKey", got.LinearAPIKey, ""},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if got.CreateLabels {
		t.Error("jira must not opt into CreateLabels (labels are freeform)")
	}
	if !got.EpicFlow || !got.Timelog || got.RequireCI {
		t.Errorf("bool mapping wrong: EpicFlow=%v Timelog=%v RequireCI=%v", got.EpicFlow, got.Timelog, got.RequireCI)
	}
}

// TestOnboardingLinearCreateLabels: linear + create picks CreateLabels and the
// entered key flows through trimmed; a detected team key resolves directly.
func TestOnboardingLinearCreateLabels(t *testing.T) {
	fake := &fakeOnboardActions{repoRoot: t.TempDir()}
	m := newOnboardingModelWithPrefill(context.Background(), fake, DefaultStyles(), 80, 40, OnboardingPrefill{})
	m.fv.tracker = "linear"
	m.fv.aiProvider = "claude"
	m.fv.baseBranch = "dev"
	m.fv.labels = "create"
	m.fv.linearKey = "  lin_api_abcdefgh  "
	m.fv.team = "eng"

	m.writeConfigCmd()()
	got := fake.gotSetup
	if !got.CreateLabels {
		t.Error("linear + create should set CreateLabels")
	}
	if got.LinearAPIKey != "lin_api_abcdefgh" {
		t.Errorf("LinearAPIKey = %q, want trimmed key", got.LinearAPIKey)
	}
	if got.Team != "eng" || got.BaseBranch != "dev" {
		t.Errorf("Team/BaseBranch = %q/%q", got.Team, got.BaseBranch)
	}
}

// TestOnboardingWritePreviewMasks: the config preview masks secrets rather than
// printing them raw.
func TestOnboardingWritePreviewMasks(t *testing.T) {
	const token = "supersecrettoken"
	fake := &fakeOnboardActions{repoRoot: t.TempDir()}
	m := newOnboardingModelWithPrefill(context.Background(), fake, DefaultStyles(), 80, 40, OnboardingPrefill{})
	m.fv.tracker = "jira"
	m.fv.jiraToken = token
	m.fv.jiraBase = "https://acme.atlassian.net"

	out := m.renderWritePreview()
	if strings.Contains(out, token) {
		t.Errorf("write preview leaked the token: %q", out)
	}
	if !strings.Contains(out, maskAPIKey(token)) {
		t.Errorf("write preview should show the masked token %q; got %q", maskAPIKey(token), out)
	}

	const key = "lin_api_secretkey"
	m.fv.tracker = "linear"
	m.fv.linearKey = key
	out = m.renderWritePreview()
	if strings.Contains(out, key) {
		t.Errorf("write preview leaked the linear key: %q", out)
	}
	if !strings.Contains(out, maskAPIKey(key)) {
		t.Errorf("write preview should mask the linear key; got %q", out)
	}
}

// TestOnboardingLabelsTrackerAware: the labels step adapts to the tracker. Jira
// labels are freeform (a single Continue, never creates); Linear offers to
// create the routing labels.
func TestOnboardingLabelsTrackerAware(t *testing.T) {
	jira := labelOptions("jira")
	if len(jira) != 1 || jira[0].Key != "Continue" {
		t.Fatalf("jira label options = %+v, want a single Continue", jira)
	}
	lin := labelOptions("linear")
	if len(lin) != 2 || !strings.Contains(lin[0].Key, "Linear") {
		t.Fatalf("linear label options = %+v, want a create-in-Linear choice", lin)
	}
	if !strings.Contains(labelsDescription("jira"), "freeform") {
		t.Errorf("jira labels description should explain freeform labels")
	}

	mj := onboardingModel{fv: &formValues{tracker: "jira", labels: "create"}}
	if mj.wantsCreateLabels() {
		t.Error("jira must never create labels")
	}
	ml := onboardingModel{fv: &formValues{tracker: "linear", labels: "create"}}
	if !ml.wantsCreateLabels() {
		t.Error("linear + create should create labels")
	}
	ml.fv.labels = "self"
	if ml.wantsCreateLabels() {
		t.Error("linear + self should not create labels")
	}
}

// TestOnboardingFormJiraNavigation drives the huh form: selecting jira routes
// past the (hidden) Linear-key group into the three-field credential form, the
// typed values land in the bound state, and editing() flips on inside a text
// field. This is the AC3/AC-editing integration check.
func TestOnboardingFormJiraNavigation(t *testing.T) {
	fake := &fakeOnboardActions{repoRoot: t.TempDir()}
	m := formModel(fake, "jira")

	if got := m.focusedKey(); got != keyTracker {
		t.Fatalf("focused key at form start = %q, want %q", got, keyTracker)
	}
	if m.editing() {
		t.Error("tracker select should not report editing")
	}

	// tracker (jira) -> ai provider -> next group skips hidden linear-key -> jira base
	m = pressKey(m, tea.KeyEnter)
	if got := m.focusedKey(); got != keyAIProvider {
		t.Fatalf("focused key after tracker = %q, want %q", got, keyAIProvider)
	}
	m = pressKey(m, tea.KeyEnter)
	if got := m.focusedKey(); got != keyJiraBase {
		t.Fatalf("focused key after providers = %q, want %q (linear-key group should be hidden)", got, keyJiraBase)
	}
	if !m.editing() {
		t.Error("jira base URL input should report editing")
	}

	m = typeRunes(m, "https://acme.atlassian.net")
	m = pressKey(m, tea.KeyTab)
	m = typeRunes(m, "me@acme.com")
	m = pressKey(m, tea.KeyTab)
	m = typeRunes(m, "s3cr3t")

	if m.fv.jiraBase != "https://acme.atlassian.net" {
		t.Errorf("jiraBase = %q", m.fv.jiraBase)
	}
	if m.fv.jiraEmail != "me@acme.com" {
		t.Errorf("jiraEmail = %q", m.fv.jiraEmail)
	}
	if m.fv.jiraToken != "s3cr3t" {
		t.Errorf("jiraToken = %q", m.fv.jiraToken)
	}
}

// TestOnboardingFormBackToWelcome: esc on the very first form field bounces out
// to the welcome screen; esc deeper in the form steps back within it.
func TestOnboardingFormBackToWelcome(t *testing.T) {
	fake := &fakeOnboardActions{repoRoot: t.TempDir()}
	m := formModel(fake, "linear")

	// esc on the tracker (first) field exits the form to welcome.
	back := pressKey(m, tea.KeyEsc)
	if back.phase != phaseWelcome {
		t.Fatalf("esc on first form field → phase %v, want phaseWelcome", back.phase)
	}
}

// TestAccessibleOnboardingRequested checks the env gate for huh's accessible
// prompts.
func TestAccessibleOnboardingRequested(t *testing.T) {
	cases := []struct {
		accessible, term string
		want             bool
	}{
		{"", "xterm-256color", false},
		{"0", "xterm-256color", false},
		{"false", "xterm-256color", false},
		{"1", "xterm-256color", true},
		{"yes", "xterm-256color", true},
		{"", "dumb", true},
	}
	for _, c := range cases {
		t.Setenv("ACCESSIBLE", c.accessible)
		t.Setenv("TERM", c.term)
		if got := AccessibleOnboardingRequested(); got != c.want {
			t.Errorf("ACCESSIBLE=%q TERM=%q → %v, want %v", c.accessible, c.term, got, c.want)
		}
	}
}

// TestOnboardingClickAdvancesWelcome: a left click on a bespoke screen advances
// it like enter (the huh form itself has no mouse layer).
func TestOnboardingClickAdvancesWelcome(t *testing.T) {
	fake := &fakeOnboardActions{repoRoot: t.TempDir()}
	m := newOnboardingModelWithPrefill(context.Background(), fake, DefaultStyles(), 80, 40, OnboardingPrefill{})
	m.phase = phaseWelcome
	m, _ = m.handleMouseClick(tea.MouseClickMsg{Button: tea.MouseLeft})
	if m.phase != phaseForm {
		t.Fatalf("click on welcome → phase %v, want phaseForm", m.phase)
	}
}

// TestOnboardingNoRepo: with no repo the wizard opens on the no-repo screen and
// enter finishes it.
func TestOnboardingNoRepo(t *testing.T) {
	fake := &fakeOnboardActions{repoRoot: ""}
	m := newOnboardingModelWithPrefill(context.Background(), fake, DefaultStyles(), 80, 40, OnboardingPrefill{})
	if m.phase != phaseNoRepo {
		t.Fatalf("phase with no repo = %v, want phaseNoRepo", m.phase)
	}
	m = pressKey(m, tea.KeyEnter)
	if !m.Done() {
		t.Error("enter on the no-repo screen should finish onboarding")
	}
}
