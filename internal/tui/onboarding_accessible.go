package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"charm.land/huh/v2"
	"github.com/RomkaLTU/trau/internal/config"
)

// AccessibleOnboardingRequested reports whether the environment asked for huh's
// accessible (screen-reader) prompts instead of the animated wizard: a non-empty
// ACCESSIBLE flag, or a dumb terminal.
func AccessibleOnboardingRequested() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ACCESSIBLE"))) {
	case "", "0", "false", "no":
	default:
		return true
	}
	return os.Getenv("TERM") == "dumb"
}

// RunAccessibleOnboarding collects the project configuration through huh's
// accessible prompts (plain, screen-reader-friendly, no full-screen TUI) and
// writes it via actions.SetupProject. It runs in two passes so the credential
// questions match the chosen tracker — huh's accessible runner presents every
// field of a form and does not honour conditional groups, so branching is done
// by building the second form once the tracker is known. Team detection is
// skipped in favour of a typed entry, since the spinner/picker UI is not
// accessible.
func RunAccessibleOnboarding(ctx context.Context, actions OnboardingActions) (SetupResult, error) {
	if actions.RepoRoot() == "" {
		return SetupResult{}, fmt.Errorf("no git repo found — run trau from inside a git repo or pass --repo <path>")
	}

	fv := &formValues{}
	p := actions.OnboardingPrefill()
	fv.tracker = firstNonEmpty(p.TrackerProvider, "linear")
	fv.aiProvider = firstNonEmpty(p.Provider, "claude")
	fv.baseBranch = p.BaseBranch
	fv.teamManual = p.Team
	fv.epicFlow = p.EpicFlow
	fv.timelog = p.Timelog
	fv.requireCI = config.HasPullRequestCI(actions.RepoRoot())
	fv.linearKey = p.LinearAPIKey
	fv.jiraBase = p.JiraBaseURL
	fv.jiraEmail = p.JiraEmail
	fv.jiraToken = p.JiraAPIToken

	providers := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Project management").Options(trackerOptionList()...).Value(&fv.tracker),
		huh.NewSelect[string]().Title("AI agent").Options(providerOptionList()...).Value(&fv.aiProvider),
	)).WithAccessible(true).WithTheme(huhTheme(theme))
	if err := providers.RunWithContext(ctx); err != nil {
		return SetupResult{}, err
	}

	rest := huh.NewForm(accessibleGroups(fv)...).WithAccessible(true).WithTheme(huhTheme(theme))
	if err := rest.RunWithContext(ctx); err != nil {
		return SetupResult{}, err
	}
	fv.team = strings.TrimSpace(fv.teamManual)

	return actions.SetupProject(ctx, projectSetupFrom(fv))
}

// accessibleGroups builds the second-pass form: credentials for the chosen
// tracker, then the shared settings.
func accessibleGroups(fv *formValues) []*huh.Group {
	var groups []*huh.Group
	switch fv.tracker {
	case "linear":
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Linear API key (blank keeps using the MCP)").
				EchoMode(huh.EchoModePassword).Value(&fv.linearKey),
		))
	case "jira":
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Jira base URL (blank keeps using the MCP)").Value(&fv.jiraBase),
			huh.NewInput().Title("Jira email").Value(&fv.jiraEmail),
			huh.NewInput().Title("Jira API token").EchoMode(huh.EchoModePassword).Value(&fv.jiraToken),
		))
	}

	groups = append(groups,
		huh.NewGroup(
			huh.NewInput().Title("Base branch (blank uses main)").Value(&fv.baseBranch),
			huh.NewSelect[bool]().Title("When a ticket has sub-issues").
				Options(
					huh.NewOption("Use epic branches", true),
					huh.NewOption("Process every ticket standalone", false),
				).Value(&fv.epicFlow),
			huh.NewInput().Title(teamTitle(fv.tracker)).Validate(requireTeam).Value(&fv.teamManual),
		),
		huh.NewGroup(
			huh.NewSelect[string]().Title(titleTracker(fv.tracker)+" labels").
				Options(labelOptions(fv.tracker)...).Value(&fv.labels),
			huh.NewSelect[bool]().Title("Track estimated dev time per ticket?").
				Options(huh.NewOption("No (default)", false), huh.NewOption("Yes", true)).
				Value(&fv.timelog),
			huh.NewSelect[bool]().Title("Wait for CI checks before merge?").
				Options(
					huh.NewOption("Yes (default)", true),
					huh.NewOption("No — this repo has no PR CI", false),
				).Value(&fv.requireCI),
		),
	)
	return groups
}
