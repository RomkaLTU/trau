package tui

import (
	"context"
	"strings"
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

func (f *fakeAppActions) StartPlan(context.Context, string) (PlanOutcome, error) {
	return PlanOutcome{Status: "prd", Title: "Draft PRD", Markdown: "# Draft PRD\n\nbody"}, nil
}

func (f *fakeAppActions) AnswerPlan(context.Context, string, []PlanAnswer) (PlanOutcome, error) {
	return PlanOutcome{Status: "prd", Title: "Draft PRD", Markdown: "# Draft PRD\n\nbody"}, nil
}

func (f *fakeAppActions) RevisePlan(context.Context, string, string) (PlanOutcome, error) {
	return PlanOutcome{Status: "prd", Title: "Draft PRD", Markdown: "# Draft PRD\n\nrevised"}, nil
}

func (f *fakeAppActions) ApprovePlan(context.Context, string) (PublishOutcome, error) {
	return PublishOutcome{}, nil
}

func (f *fakeAppActions) SlicePlan(context.Context, string) (PlanOutcome, error) {
	return PlanOutcome{Status: "slices", Epic: "COD-1", Slices: []PlanSlice{{Title: "First slice"}}}, nil
}

func (f *fakeAppActions) CreateSlices(context.Context, string, []PlanSlice) (SliceOutcome, error) {
	return SliceOutcome{Epic: "COD-1", Children: []string{"COD-2"}, Created: true}, nil
}

func (f *fakeAppActions) ListPlans() []PlanSession { return nil }

func (f *fakeAppActions) ResumePlan(context.Context, string) (PlanOutcome, error) {
	return PlanOutcome{Status: "prd", Title: "Draft PRD", Markdown: "# Draft PRD\n\nbody"}, nil
}

func (f *fakeAppActions) AbortPlan(context.Context, string) error { return nil }

// TestScreensRenderAcrossSizes walks the menu shell into every view at the
// three reference terminal sizes and renders each one, so a regression in any
// screen's layout code fails here instead of at runtime.
func TestScreensRenderAcrossSizes(t *testing.T) {
	sizes := [][2]int{{80, 24}, {120, 40}, {200, 60}}
	actions := []menuAction{
		actRun, actRunOnce, actDryRun, actPlan, actStatus, actLogs,
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

// TestResizeRoutesToEverySubModel resizes the terminal while each sub-model-backed
// screen is active and asserts that screen picked up the new dimensions — the
// shell must fan WindowSizeMsg out to every view, not just the dashboard.
func TestResizeRoutesToEverySubModel(t *testing.T) {
	cases := []struct {
		name   string
		action menuAction
		width  func(m appModel) int
	}{
		{"run loop", actRun, func(m appModel) int { return m.loopSetup.width }},
		{"run once", actRunOnce, func(m appModel) int { return m.runOnce.width }},
		{"logs", actLogs, func(m appModel) int { return m.logs.width }},
		{"settings", actSettings, func(m appModel) int { return m.settings.width }},
		{"onboarding", actOnboarding, func(m appModel) int { return m.onboard.width }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := newAppModel(context.Background(), &fakeAppActions{}, nil)
			nm, _ := base.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			am, _ := nm.(appModel).selectAction(c.action)

			rm, _ := am.(appModel).Update(tea.WindowSizeMsg{Width: 140, Height: 50})
			m := rm.(appModel)

			if got := c.width(m); got != 140 {
				t.Errorf("%s width after resize = %d, want 140", c.name, got)
			}
		})
	}
}

// TestStatusScreenRendersQueue checks the Status screen loads its rows onto the
// shared attention queue and renders through it across a resize.
func TestStatusScreenRendersQueue(t *testing.T) {
	base := newAppModel(context.Background(), &fakeAppActions{}, nil)
	nm, _ := base.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	am, _ := nm.(appModel).selectAction(actStatus)
	m := am.(appModel)
	if len(m.statusRows) == 0 {
		t.Fatal("Status screen loaded no rows")
	}
	if sel, ok := m.selectedStatusRow(); !ok || sel.ID != "COD-1" {
		t.Fatalf("selected status row = %q (ok=%v), want COD-1", sel.ID, ok)
	}

	rm, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m = rm.(appModel)
	if !strings.Contains(m.render(), "COD-1") {
		t.Error("Status screen did not render its ticket after resize")
	}
}
