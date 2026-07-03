package tui

import (
	"context"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
)

// This file builds the embedded huh form that drives the middle onboarding
// steps (providers → credentials → base branch → team → labels → time → CI →
// write). The animated system check, welcome, and terminal screens stay bespoke
// in onboarding.go; huh owns the input mechanics, focus, validation, and
// step-by-step back navigation here.

// Field keys identify the focused step for the key contract, editing() gate, and
// per-step help. They also key the huh results map.
const (
	keyTracker    = "tracker"
	keyAIProvider = "aiprovider"
	keyLinearKey  = "linearkey"
	keyJiraBase   = "jirabase"
	keyJiraEmail  = "jiraemail"
	keyJiraToken  = "jiratoken"
	keyBaseBranch = "basebranch"
	keyBranching  = "branching"
	keyTeam       = "team"
	keyTeamManual = "teammanual"
	keyLabels     = "labels"
	keyTimelog    = "timelog"
	keyCI         = "ci"
	keyWrite      = "write"
)

// teamManualSentinel is the team-picker option value that routes to the free-text
// manual-entry group. The null byte keeps it from colliding with a real key.
const teamManualSentinel = "\x00manual"

const (
	linearAPIKeySettingsURL = "https://linear.app/settings/account/security"
	jiraTokenSettingsURL    = "https://id.atlassian.com/manage-profile/security/api-tokens"
)

// formValues holds the huh-bound wizard values. It is pointer-shared across the
// value copies of onboardingModel so bindings survive the Elm update loop.
type formValues struct {
	tracker    string
	aiProvider string
	linearKey  string
	jiraBase   string
	jiraEmail  string
	jiraToken  string
	baseBranch string
	epicFlow   bool
	team       string
	teamManual string
	labels     string
	timelog    bool
	requireCI  bool

	writeConfirm bool
}

type formCompletedMsg struct{}

type formAbortedMsg struct{}

// newForm builds the huh form. Closures capture only the stable, pointer-shared
// pieces (actions, ctx, fv, repoRoot) — never the value-copied model.
func (m onboardingModel) newForm() *huh.Form {
	fv := m.fv
	actions := m.actions
	ctx := m.ctx
	repoRoot := m.repoRoot

	providers := huh.NewGroup(
		huh.NewSelect[string]().
			Key(keyTracker).
			Title("Project management").
			Options(trackerOptions(m)...).
			Value(&fv.tracker),
		huh.NewSelect[string]().
			Key(keyAIProvider).
			Title("AI agent").
			DescriptionFunc(func() string { return providerSkillWarning(repoRoot, fv.aiProvider) }, &fv.aiProvider).
			Options(providerOptions(m)...).
			Value(&fv.aiProvider),
	).Title("Choose providers")

	linearKey := huh.NewGroup(
		huh.NewInput().
			Key(keyLinearKey).
			Title("Linear API key").
			Description("Enter a Linear personal API key for fast direct API calls.\nLeave blank to keep using the Linear MCP. Press o to open key settings.").
			Placeholder("lin_api_...").
			EchoMode(huh.EchoModePassword).
			CharLimit(256).
			Value(&fv.linearKey),
	).Title("Linear API key").
		WithHideFunc(func() bool { return fv.tracker != "linear" })

	jiraCreds := huh.NewGroup(
		huh.NewInput().Key(keyJiraBase).Title("Base URL").Placeholder("https://acme.atlassian.net").CharLimit(200).Value(&fv.jiraBase),
		huh.NewInput().Key(keyJiraEmail).Title("Email").Placeholder("you@acme.com").CharLimit(200).Value(&fv.jiraEmail),
		huh.NewInput().Key(keyJiraToken).Title("API token").Placeholder("classic API token").EchoMode(huh.EchoModePassword).CharLimit(256).Value(&fv.jiraToken),
	).Title("Jira REST credentials").
		Description("Per-repo credentials let two repos use two separate Jira accounts.\nLeave blank to use the Atlassian (Rovo) MCP.\nGenerate a classic token: " + jiraTokenSettingsURL).
		WithHideFunc(func() bool { return fv.tracker != "jira" })

	baseBranch := huh.NewGroup(
		huh.NewInput().
			Key(keyBaseBranch).
			Title("Base branch").
			Description("Default branch for standalone tickets (blank uses main).").
			Placeholder("main").
			CharLimit(64).
			Value(&fv.baseBranch),
		huh.NewSelect[bool]().
			Key(keyBranching).
			Title("When a ticket has sub-issues").
			Options(
				huh.NewOption("Use epic branches for tickets with sub-issues", true),
				huh.NewOption("Process every ticket standalone", false),
			).
			Value(&fv.epicFlow),
	).Title("Base branch & branching strategy")

	team := huh.NewGroup(
		huh.NewSelect[string]().
			Key(keyTeam).
			TitleFunc(func() string { return teamTitle(fv.tracker) }, &fv.tracker).
			Description("Type to search; pick ✎ to enter it manually.").
			Filtering(true).
			Height(8).
			OptionsFunc(func() []huh.Option[string] { return detectTeamOptions(ctx, actions, fv) }, &fv.tracker).
			Value(&fv.team),
	)

	teamManual := huh.NewGroup(
		huh.NewInput().
			Key(keyTeamManual).
			TitleFunc(func() string { return teamTitle(fv.tracker) }, &fv.tracker).
			DescriptionFunc(func() string { return "Enter your " + teamNoun(fv.tracker) + "." }, &fv.tracker).
			CharLimit(64).
			Value(&fv.teamManual),
	).WithHideFunc(func() bool { return fv.team != teamManualSentinel })

	labels := huh.NewGroup(
		huh.NewSelect[string]().
			Key(keyLabels).
			TitleFunc(func() string { return titleTracker(fv.tracker) + " labels" }, &fv.tracker).
			DescriptionFunc(func() string { return labelsDescription(fv.tracker) }, &fv.tracker).
			OptionsFunc(func() []huh.Option[string] { return labelOptions(fv.tracker) }, &fv.tracker).
			Value(&fv.labels),
	)

	timeTracking := huh.NewGroup(
		huh.NewSelect[bool]().
			Key(keyTimelog).
			Title("Time tracking (optional)").
			Description("Write a per-ticket effort estimate to .dev-flow/time/<TICKET>.json after\nmerge. Off by default; it estimates human effort, not agent time.").
			Options(
				huh.NewOption("No — don't track time (default)", false),
				huh.NewOption("Yes — log estimated dev time per ticket", true),
			).
			Value(&fv.timelog),
	)

	ci := huh.NewGroup(
		huh.NewSelect[bool]().
			Key(keyCI).
			Title("CI merge gate").
			Description(m.ciDescription()).
			Options(
				huh.NewOption("Yes — wait for CI checks before merge (default)", true),
				huh.NewOption("No — this repo has no PR CI; skip the gate", false),
			).
			Value(&fv.requireCI),
	)

	write := huh.NewGroup(
		huh.NewConfirm().
			Key(keyWrite).
			Affirmative("Write config").
			Negative("").
			Value(&fv.writeConfirm),
	)

	form := huh.NewForm(
		providers, linearKey, jiraCreds, baseBranch,
		team, teamManual, labels, timeTracking, ci, write,
	).
		WithTheme(huhTheme(theme)).
		WithShowHelp(false).
		WithWidth(m.formWidth())
	form.SubmitCmd = func() tea.Msg { return formCompletedMsg{} }
	form.CancelCmd = func() tea.Msg { return formAbortedMsg{} }
	return form
}

func (m onboardingModel) ciDescription() string {
	if m.ciHasPRDet {
		return "Detected a pull_request-triggered workflow in .github/workflows.\nChange later in Settings or via REQUIRE_CI in .trau.ini."
	}
	return "No pull_request-triggered workflow found — PRs would get zero checks,\nread by the gate as never-green. Skip only if this repo has no PR CI."
}

// detectTeamOptions runs the async tracker probe (huh shows a loading spinner)
// and maps the result to picker options plus a manual-entry escape hatch. On
// error or empty detection only the manual option is offered.
func detectTeamOptions(ctx context.Context, actions OnboardingActions, fv *formValues) []huh.Option[string] {
	det, _ := actions.DetectTeams(ctx, fv.tracker, fv.aiProvider, JiraCreds{
		BaseURL:  strings.TrimSpace(fv.jiraBase),
		Email:    strings.TrimSpace(fv.jiraEmail),
		APIToken: strings.TrimSpace(fv.jiraToken),
	})
	opts := make([]huh.Option[string], 0, len(det.Teams)+1)
	for _, t := range det.Teams {
		label := t.Key
		if t.Name != "" && t.Name != t.Key {
			label = t.Key + " — " + t.Name
		}
		opts = append(opts, huh.NewOption(label, t.Key))
	}
	if det.AutoFill && len(opts) == 1 {
		opts[0] = opts[0].Selected(true)
	}
	opts = append(opts, huh.NewOption("✎ Enter it manually", teamManualSentinel))
	return opts
}

func trackerOptions(m onboardingModel) []huh.Option[string] {
	return onboardOptions(m, []string{"linear", "jira", "github"}, map[string]string{
		"linear": "Linear", "jira": "Jira", "github": "GitHub",
	})
}

func providerOptions(m onboardingModel) []huh.Option[string] {
	return onboardOptions(m, []string{"claude", "codex", "kimi"}, map[string]string{
		"claude": "claude", "codex": "codex", "kimi": "kimi",
	})
}

// onboardOptions offers the ready choices, dropping any whose readiness probe
// failed. If every option failed (the readiness gate should prevent this) it
// falls back to offering all so the select is never empty.
func onboardOptions(m onboardingModel, names []string, labels map[string]string) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(names))
	for _, n := range names {
		if m.checkStatusFor(n) == checkFailed {
			continue
		}
		opts = append(opts, huh.NewOption(labels[n], n))
	}
	if len(opts) == 0 {
		for _, n := range names {
			opts = append(opts, huh.NewOption(labels[n], n))
		}
	}
	return opts
}

func labelOptions(tracker string) []huh.Option[string] {
	if labelCreationSupported(tracker) {
		return []huh.Option[string]{
			huh.NewOption("Create the labels in "+titleTracker(tracker)+" now", "create"),
			huh.NewOption("I'll create the labels myself", "self"),
		}
	}
	return []huh.Option[string]{huh.NewOption("Continue", "continue")}
}

func labelsDescription(tracker string) string {
	if labelCreationSupported(tracker) {
		return "Trau routes tickets with two labels:\n  • ready-for-agent → tickets to pick up\n  • needs-human → tickets that failed\nDefaults are ready-for-agent and needs-human."
	}
	return "Jira labels are freeform — Trau applies ready-for-agent and needs-human\nautomatically as tickets move, so there is nothing to create."
}

func teamTitle(tracker string) string {
	switch tracker {
	case "jira":
		return "Jira project"
	case "github":
		return "GitHub repository"
	default:
		return "Linear team"
	}
}

func teamNoun(tracker string) string {
	switch tracker {
	case "jira":
		return "project key (e.g. PROJ)"
	case "github":
		return "repository slug (e.g. owner/repo)"
	default:
		return "team name or key"
	}
}

func providerSkillWarning(repoRoot, provider string) string {
	if !providerNeedsSkills(provider) {
		return ""
	}
	r := agent.CheckSkillReadiness(repoRoot)
	if r.HasSkills {
		return ""
	}
	msg := agent.MissingSkillsMessage(r)
	if msg == "" {
		return provider + " expects skills in this repo, but none were found. Add skills to .agents/skills/ before running the loop."
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

// wantsCreateLabels reports whether the wizard should create routing labels: the
// tracker supports it and the user chose to.
func (m onboardingModel) wantsCreateLabels() bool {
	return labelCreationSupported(m.fv.tracker) && m.fv.labels == "create"
}

// renderWritePreview is the masked config preview shown above the write Confirm.
func (m onboardingModel) renderWritePreview() string {
	fv := m.fv
	s := m.styles
	path := filepath.Join(m.repoRoot, config.ProjectConfigName)
	base := strings.TrimSpace(fv.baseBranch)
	if base == "" {
		base = "main"
	}
	var rows []string
	rows = append(rows, s.SummaryTitle.Render("Ready to write config"))
	rows = append(rows, "")
	rows = append(rows, "Path: "+path)
	rows = append(rows, "")
	rows = append(rows, "Values:")
	rows = append(rows, "  TRACKER_PROVIDER="+fv.tracker)
	rows = append(rows, "  LINEAR_TEAM="+resolveTeam(fv))
	if fv.tracker == "linear" {
		if key := strings.TrimSpace(fv.linearKey); key != "" {
			rows = append(rows, "  LINEAR_API_KEY="+maskAPIKey(key))
		} else {
			rows = append(rows, "  LINEAR_API_KEY=(blank — will use MCP)")
		}
	}
	if fv.tracker == "jira" {
		if v := strings.TrimSpace(fv.jiraBase); v != "" {
			rows = append(rows, "  JIRA_BASE_URL="+v)
		}
		if v := strings.TrimSpace(fv.jiraEmail); v != "" {
			rows = append(rows, "  JIRA_EMAIL="+v)
		}
		if tok := strings.TrimSpace(fv.jiraToken); tok != "" {
			rows = append(rows, "  JIRA_API_TOKEN="+maskAPIKey(tok))
		} else {
			rows = append(rows, "  JIRA_API_TOKEN=(blank — will use MCP)")
		}
	}
	rows = append(rows, "  BASE_BRANCH="+base)
	rows = append(rows, "  PROVIDER="+fv.aiProvider)
	rows = append(rows, "  READY_LABEL=ready-for-agent")
	rows = append(rows, "  QUARANTINE_LABEL=needs-human")
	rows = append(rows, "  EPIC_FLOW="+boolDigit(fv.epicFlow))
	rows = append(rows, "  TIMELOG_ENABLED="+boolDigit(fv.timelog))
	if labelCreationSupported(fv.tracker) {
		if m.wantsCreateLabels() {
			rows = append(rows, "  Create labels in "+titleTracker(fv.tracker)+": yes")
		} else {
			rows = append(rows, "  Create labels in "+titleTracker(fv.tracker)+": no")
		}
	}
	return strings.Join(rows, "\n")
}

func (m onboardingModel) writeConfigCmd() tea.Cmd {
	setup := projectSetupFrom(m.fv)
	actions := m.actions
	ctx := m.ctx
	return func() tea.Msg {
		res, err := actions.SetupProject(ctx, setup)
		return setupDoneMsg{result: res, err: err}
	}
}

// projectSetupFrom maps the collected form values onto the ProjectSetup handed to
// SetupProject. It is the single source of the config mapping, shared by the
// interactive wizard and the accessible flow so both write identical config.
func projectSetupFrom(fv *formValues) ProjectSetup {
	base := strings.TrimSpace(fv.baseBranch)
	if base == "" {
		base = "main"
	}
	return ProjectSetup{
		Provider:        firstNonEmpty(fv.aiProvider, "claude"),
		TrackerProvider: firstNonEmpty(fv.tracker, "linear"),
		BaseBranch:      base,
		Team:            resolveTeam(fv),
		ReadyLabel:      "ready-for-agent",
		QuarantineLabel: "needs-human",
		CreateLabels:    labelCreationSupported(fv.tracker) && fv.labels == "create",
		EpicFlow:        fv.epicFlow,
		Timelog:         fv.timelog,
		RequireCI:       fv.requireCI,
		LinearAPIKey:    strings.TrimSpace(fv.linearKey),
		JiraBaseURL:     strings.TrimSpace(fv.jiraBase),
		JiraEmail:       strings.TrimSpace(fv.jiraEmail),
		JiraAPIToken:    strings.TrimSpace(fv.jiraToken),
	}
}

func resolveTeam(fv *formValues) string {
	if fv.team == teamManualSentinel {
		return strings.TrimSpace(fv.teamManual)
	}
	return strings.TrimSpace(fv.team)
}

func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

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

// labelCreationSupported reports whether Trau can pre-create routing labels for a
// tracker. Jira labels are freeform strings created implicitly on first use.
func labelCreationSupported(provider string) bool {
	return provider != "jira"
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
