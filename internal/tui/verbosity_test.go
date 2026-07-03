package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func keyRune(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// pressDash routes a key through the dashboard's Update, the same path the app
// shell uses via applyDashCmd.
func pressDash(m model, msg tea.KeyPressMsg) model {
	nm, _ := m.Update(msg)
	return nm.(model)
}

// v cycles the pane through spans → feed → raw → spans, and the pane title names
// the active tier at each step.
func TestVerbosityTierCycling(t *testing.T) {
	m := initialModel(nil)
	if m.tier != tierSpans {
		t.Fatalf("default tier = %v, want spans", m.tier)
	}
	if !strings.HasPrefix(m.tierTitle(), "Pipeline") {
		t.Errorf("spans title = %q, want Pipeline prefix", m.tierTitle())
	}

	m = pressDash(m, keyRune('v'))
	if m.tier != tierFeed {
		t.Fatalf("after 1st v, tier = %v, want feed", m.tier)
	}
	if m.tierTitle() != "Activity feed" {
		t.Errorf("feed title = %q", m.tierTitle())
	}

	m = pressDash(m, keyRune('v'))
	if m.tier != tierRaw {
		t.Fatalf("after 2nd v, tier = %v, want raw", m.tier)
	}
	if m.tierTitle() != "Raw log" {
		t.Errorf("raw title = %q", m.tierTitle())
	}

	m = pressDash(m, keyRune('v'))
	if m.tier != tierSpans {
		t.Fatalf("after 3rd v, tier = %v, want spans (wrapped)", m.tier)
	}
}

// The feed tier renders every classified line, while the raw tier shows the same
// lines before classification stripped their prefixes — so the two tiers differ
// exactly by what classification ate.
func TestFeedAndRawContentParity(t *testing.T) {
	m := initialModel(nil)
	m.addLog("▸ verify · claude-opus-4-8")
	m.addLog("✓ build passed")
	m.addLog("✗ tests failed")

	feed := strip(m.renderFeed(120))
	for _, want := range []string{"claude-opus-4-8", "build passed", "tests failed"} {
		if !strings.Contains(feed, want) {
			t.Errorf("feed missing %q\n%s", want, feed)
		}
	}

	raw := strip(m.renderRaw(120))
	// Raw keeps the original "▸ verify · " prefix the feed classification dropped.
	if !strings.Contains(raw, "▸ verify · claude-opus-4-8") {
		t.Errorf("raw missing the pre-classification line\n%s", raw)
	}
	if strings.Contains(feed, "▸ verify · claude-opus-4-8") {
		t.Errorf("feed should show the classified text, not the raw prefix\n%s", feed)
	}
}

// A filter hides non-matching rows in the feed and raw tiers, case-insensitively.
func TestFilterHidesNonMatchingRows(t *testing.T) {
	m := initialModel(nil)
	m.addLog("build started")
	m.addLog("tests running")

	m.tier = tierFeed
	m.filter = "BUILD"
	feed := strip(m.renderFeed(120))
	if !strings.Contains(feed, "build started") {
		t.Errorf("filtered feed dropped the match\n%s", feed)
	}
	if strings.Contains(feed, "tests running") {
		t.Errorf("filtered feed kept a non-match\n%s", feed)
	}

	m.tier = tierRaw
	raw := strip(m.renderRaw(120))
	if !strings.Contains(raw, "build started") || strings.Contains(raw, "tests running") {
		t.Errorf("raw filter mismatch\n%s", raw)
	}
}

// filterMatch is the substring predicate; empty filter matches everything.
func TestFilterMatch(t *testing.T) {
	m := initialModel(nil)
	if !m.filterMatch("anything") {
		t.Error("empty filter should match")
	}
	m.filter = "ci"
	if !m.filterMatch("CI green") || m.filterMatch("build") {
		t.Error("filterMatch case-insensitive substring failed")
	}
}

// / opens the filter input on filterable tiers, typing narrows live, and esc
// clears both the filter and the input mode.
func TestFilterInputLifecycle(t *testing.T) {
	m := initialModel(nil)
	m = pressDash(m, keyRune('v')) // feed

	m = pressDash(m, keyRune('/'))
	if !m.filterActive() {
		t.Fatal("/ should open the filter input on the feed tier")
	}
	for _, r := range "ci" {
		m = pressDash(m, keyRune(r))
	}
	if m.filter != "ci" {
		t.Fatalf("typed filter = %q, want ci", m.filter)
	}
	if !strings.Contains(m.spanPaneTitle(), "/ci") {
		t.Errorf("title should show the filter, got %q", m.spanPaneTitle())
	}

	m = pressDash(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.filterActive() || m.filter != "" {
		t.Errorf("esc should clear filter+input, got filtering=%v filter=%q", m.filterActive(), m.filter)
	}
}

// / is inert on the span tier — there is nothing to filter there.
func TestSlashInertOnSpanTier(t *testing.T) {
	m := initialModel(nil)
	m = pressDash(m, keyRune('/'))
	if m.filterActive() {
		t.Error("/ should not open the filter on the span tier")
	}
}

// The chosen tier and filter survive a ticket transition within a run.
func TestTierSurvivesTicketTransition(t *testing.T) {
	m := initialModel(nil)
	m = pressDash(m, keyRune('v')) // feed
	m.filter = "verify"
	m.startTicket("COD-1")
	if m.tier != tierFeed {
		t.Errorf("tier reset on ticket start: %v", m.tier)
	}
	if m.filter != "verify" {
		t.Errorf("filter reset on ticket start: %q", m.filter)
	}
}

// While the filter input is capturing, the app shell routes every key to it — so
// an action key like q types into the filter instead of stopping the loop, and
// editing() reports true so the global ? / : overlays stay closed.
func TestFilterInputOwnsKeysInAppShell(t *testing.T) {
	fake := &fakeAppActions{fakeOnboardActions: fakeOnboardActions{repoRoot: t.TempDir()}}
	app := newAppModel(context.Background(), fake, nil)
	napp, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	app = napp.(appModel)
	app.view = viewRunning
	app.dash = app.dash.cycleTier() // feed
	app.dash.filtering = true

	if !app.editing() {
		t.Fatal("editing() should be true while the dash filter is capturing")
	}
	napp2, _ := app.Update(keyRune('q'))
	app = napp2.(appModel)
	if app.dash.filter != "q" {
		t.Errorf("q should type into the filter, got %q", app.dash.filter)
	}
	if app.dash.stopping {
		t.Error("q while filtering must not stop the loop")
	}

	// ctrl+c is the emergency stop and must win even mid-filter.
	napp3, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = napp3.(appModel)
	if !app.dash.stopping {
		t.Error("ctrl+c must stop the loop even while filtering")
	}
}

// cycleTier drops any active filter so each tier is entered fresh — no filter
// silently reactivates after cycling back around.
func TestCycleTierClearsFilter(t *testing.T) {
	m := initialModel(nil)
	m = pressDash(m, keyRune('v')) // feed
	m.filter = "err"
	m = m.cycleTier() // raw
	if m.filter != "" {
		t.Errorf("cycleTier left a stale filter: %q", m.filter)
	}
}

// A continuation (↳) row is shown only under a visible parent: a filter that
// matches the parent keeps its detail, one that misses the parent hides both.
func TestFilterKeepsSubRowsWithParent(t *testing.T) {
	m := initialModel(nil)
	m.addLog("✗ tests failed: TestFoo")
	m.addLog("  ↳ expected 3 got 4")
	m.tier = tierFeed

	m.filter = "tests"
	out := strip(m.renderFeed(120))
	if !strings.Contains(out, "tests failed") || !strings.Contains(out, "expected 3 got 4") {
		t.Errorf("matched parent should keep its detail\n%s", out)
	}

	m.filter = "expected" // matches only the sub's text, not the parent
	out = strip(m.renderFeed(120))
	if strings.Contains(out, "expected 3 got 4") {
		t.Errorf("sub row must not orphan without its parent\n%s", out)
	}
}

// / does not open the filter while the full-screen watch stream is up, where its
// caret would be invisible and it would hijack the watch controls.
func TestSlashInertDuringWatch(t *testing.T) {
	m := initialModel(nil)
	m = pressDash(m, keyRune('v')) // feed (filterable)
	m.streaming = true
	m = pressDash(m, keyRune('/'))
	if m.filterActive() {
		t.Error("/ should be inert while watching the stream")
	}
}
