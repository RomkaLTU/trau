package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type fakeOnboardActions struct {
	repoRoot string
	prefill  OnboardingPrefill
	gotSetup ProjectSetup
}

func (f *fakeOnboardActions) RepoRoot() string                     { return f.repoRoot }
func (f *fakeOnboardActions) OnboardingPrefill() OnboardingPrefill { return f.prefill }
func (f *fakeOnboardActions) LinearAPIKeyConfigured() bool         { return false }

func (f *fakeOnboardActions) DetectTeams(context.Context, string, string, JiraCreds) (TeamDetection, error) {
	return TeamDetection{Label: "project"}, nil
}

func (f *fakeOnboardActions) SetupProject(_ context.Context, s ProjectSetup) (SetupResult, error) {
	f.gotSetup = s
	return SetupResult{ConfigPath: "/tmp/.trau.ini"}, nil
}

func typeRunes(m onboardingModel, s string) onboardingModel {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(onboardingModel)
	}
	return m
}

func pressKey(m onboardingModel, t tea.KeyType) onboardingModel {
	next, _ := m.Update(tea.KeyMsg{Type: t})
	return next.(onboardingModel)
}

// Selecting the jira tracker routes the provider step into the three-field
// credential form; the typed base URL, email and token flow through
// writeConfigCmd into the ProjectSetup, and 'o' inside the fields types normally
// (no shortcut steals it).
func TestOnboardingJiraCredsFlow(t *testing.T) {
	fake := &fakeOnboardActions{repoRoot: t.TempDir()}
	m := newOnboardingModelWithPrefill(context.Background(), fake, DefaultStyles(), 80, 40, OnboardingPrefill{})

	m.step = onboardProviders
	m.providersPMFocused = false
	for i, tr := range m.trackers {
		if tr == "jira" {
			m.trackerCursor = i
		}
	}

	m = pressKey(m, tea.KeyEnter)
	if m.step != onboardJiraCreds {
		t.Fatalf("step after selecting jira = %v, want onboardJiraCreds", m.step)
	}

	m = typeRunes(m, "https://acme.atlassian.net")
	m = pressKey(m, tea.KeyTab)
	m = typeRunes(m, "me@acme.com")
	m = pressKey(m, tea.KeyTab)
	m = typeRunes(m, "s3cr3t-token")

	m = pressKey(m, tea.KeyEnter)
	if m.step != onboardBaseBranch {
		t.Fatalf("step after last field = %v, want onboardBaseBranch", m.step)
	}

	msg := m.writeConfigCmd()()
	if _, ok := msg.(setupDoneMsg); !ok {
		t.Fatalf("writeConfigCmd msg = %T, want setupDoneMsg", msg)
	}
	got := fake.gotSetup
	if got.TrackerProvider != "jira" {
		t.Errorf("TrackerProvider = %q, want jira", got.TrackerProvider)
	}
	if got.JiraBaseURL != "https://acme.atlassian.net" {
		t.Errorf("JiraBaseURL = %q, want the typed URL", got.JiraBaseURL)
	}
	if got.JiraEmail != "me@acme.com" {
		t.Errorf("JiraEmail = %q, want me@acme.com", got.JiraEmail)
	}
	if got.JiraAPIToken != "s3cr3t-token" {
		t.Errorf("JiraAPIToken = %q, want s3cr3t-token", got.JiraAPIToken)
	}
}

// esc walks the jira credential step back to the provider picker, and the
// rendered write-summary masks the token rather than printing it raw.
func TestOnboardingJiraCredsBackAndMasking(t *testing.T) {
	const token = "supersecrettoken"
	fake := &fakeOnboardActions{repoRoot: t.TempDir()}
	m := newOnboardingModelWithPrefill(context.Background(), fake, DefaultStyles(), 80, 40, OnboardingPrefill{})
	m.step = onboardProviders
	m.providersPMFocused = false
	for i, tr := range m.trackers {
		if tr == "jira" {
			m.trackerCursor = i
		}
	}
	m = pressKey(m, tea.KeyEnter)

	// Advance to the token field (field 2) and type the secret there.
	m = pressKey(m, tea.KeyTab)
	m = pressKey(m, tea.KeyTab)
	m = typeRunes(m, token)

	out := m.renderWrite()
	if strings.Contains(out, token) {
		t.Errorf("write summary leaked the token: %q", out)
	}
	if !strings.Contains(out, maskAPIKey(token)) {
		t.Errorf("write summary should show the masked token %q; got %q", maskAPIKey(token), out)
	}

	back := pressKey(m, tea.KeyEsc)
	if back.step != onboardProviders {
		t.Fatalf("esc from jira creds = %v, want onboardProviders", back.step)
	}
}
