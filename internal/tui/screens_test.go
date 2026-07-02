package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RomkaLTU/trau/internal/console"
)

type fakeAppActions struct {
	fakeOnboardActions
	fakeSettingsActions
}

func (f *fakeAppActions) MenuInfo() MenuInfo {
	return MenuInfo{
		Version:   "test",
		Provider:  "claude",
		Model:     "opus",
		Base:      "main",
		Prefix:    "COD",
		Providers: []ProviderChoice{{Name: "claude", Model: "opus"}},
	}
}

func (f *fakeAppActions) StatusRows() []StatusRow {
	return []StatusRow{{ID: "COD-1", Title: "One", Phase: "build", Tokens: 1200, Cost: 1.5, CostMetered: true}}
}

func (f *fakeAppActions) LogRuns() []LogRun {
	return []LogRun{{ID: "COD-1", Title: "One", Phase: "build", Updated: time.Now()}}
}

func (f *fakeAppActions) LogContent(string) string { return "build: ok" }

func (f *fakeAppActions) Reconcile(context.Context) ([]string, error) { return nil, nil }

func (f *fakeAppActions) DryRun(context.Context) (string, error) { return "COD-1", nil }

func (f *fakeAppActions) Reset(context.Context, string) error { return nil }

func (f *fakeAppActions) CheckoutBranch(context.Context, string) (string, error) { return "", nil }

func (f *fakeAppActions) RunLoop(context.Context, string, console.Renderer) {}

func (f *fakeAppActions) SubIssues(context.Context, string) ([]SubIssue, error) { return nil, nil }

func (f *fakeAppActions) ListEligible(context.Context) ([]ListedTicket, error) { return nil, nil }

func (f *fakeAppActions) RunTicket(context.Context, string, string, console.Renderer) {}

func (f *fakeAppActions) OnboardingNeeded() bool { return false }

// TestScreensRenderAcrossSizes walks the menu shell into every view at the
// three reference terminal sizes and renders each one, so a regression in any
// screen's layout code fails here instead of at runtime.
func TestScreensRenderAcrossSizes(t *testing.T) {
	sizes := [][2]int{{80, 24}, {120, 40}, {200, 60}}
	actions := []menuAction{
		actRun, actRunOnce, actDryRun, actStatus, actLogs,
		actReset, actVersion, actOnboarding, actSettings, actMore,
	}
	for _, sz := range sizes {
		base := newAppModel(context.Background(), &fakeAppActions{}, nil)
		nm, _ := base.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		base = nm.(appModel)
		if base.render() == "" {
			t.Fatalf("menu rendered empty at %dx%d", sz[0], sz[1])
		}

		for _, a := range actions {
			am, _ := base.selectAction(a)
			m := am.(appModel)
			if m.render() == "" {
				t.Fatalf("view %d rendered empty at %dx%d", m.view, sz[0], sz[1])
			}
			if m.loopCancel != nil {
				m.loopCancel()
			}
		}

		errView := base
		errView.view = viewError
		errView.errMsg = "boom"
		if errView.render() == "" {
			t.Fatalf("error view rendered empty at %dx%d", sz[0], sz[1])
		}

		rm, _ := base.startRunLoop("")
		running := rm.(appModel)
		if running.render() == "" {
			t.Fatalf("running view rendered empty at %dx%d", sz[0], sz[1])
		}
		running.loopCancel()

		sm, _ := running.dash.enterSummary(console.SessionSummary{Tickets: 1, Elapsed: time.Minute, CostMetered: true})
		if sm.(model).render() == "" {
			t.Fatalf("summary rendered empty at %dx%d", sz[0], sz[1])
		}
	}
}
