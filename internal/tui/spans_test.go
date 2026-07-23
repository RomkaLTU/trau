package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/vterm"
)

func strip(s string) string { return ansi.Strip(s) }

// A completed phase folds to "✓ <label>  <duration>  <tag>".
func TestFoldedSpanComposesDurationAndTag(t *testing.T) {
	m := initialModel(nil)
	m.steps[0].state = stepDone
	m.steps[0].tag = "opus-4-8 @high"
	m.steps[0].took = 5*time.Minute + 3*time.Second

	got := strip(m.foldedSpan(m.steps[0], 80))
	if want := "✓ Build  5m03s  opus-4-8 @high"; got != want {
		t.Fatalf("folded = %q, want %q", got, want)
	}
}

// A folded non-agent Step carries just its duration when no model tag is known.
func TestFoldedSpanWithoutTag(t *testing.T) {
	m := initialModel(nil)
	m.steps[2].state = stepDone // Ship
	m.steps[2].took = time.Second

	if got, want := strip(m.foldedSpan(m.steps[2], 80)), "✓ Ship  1s"; got != want {
		t.Fatalf("folded = %q, want %q", got, want)
	}
}

// spanDetail ticks live elapsed while the Step is active.
func TestSpanDetailActiveUsesLiveElapsed(t *testing.T) {
	st := stepRow{state: stepActive, start: time.Now().Add(-90 * time.Second), tag: "sonnet"}
	if got := spanDetail(st); !strings.HasPrefix(got, "1m3") || !strings.HasSuffix(got, "sonnet") {
		t.Fatalf("detail = %q, want live elapsed + tag", got)
	}
}

// renderSpanList folds done Steps, expands the active one, and collapses the
// remaining pending Steps onto one compact row.
func TestRenderSpanListFoldExpandPending(t *testing.T) {
	m := initialModel(nil)
	m.steps = advanceActivity(m.steps, activity.Verify, "", time.Now()) // build done, verify active
	out := strip(m.renderSpanList(80))
	lines := strings.Split(out, "\n")

	if !strings.HasPrefix(lines[0], "✓ Build") {
		t.Errorf("line 0 should fold Build, got %q", lines[0])
	}
	if !strings.Contains(out, "Verify") {
		t.Errorf("active Verify missing:\n%s", out)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "○ Ship") {
		t.Errorf("pending row missing %q, got %q", "○ Ship", last)
	}
}

// The AUTO_MERGE=0 wait surfaces on the Ship step's activity line as
// "Ship · merge-wait", so the operator sees the run is parked on their manual merge.
func TestActivityLineRendersMergeWait(t *testing.T) {
	m := initialModel(nil)
	m.steps = advanceActivity(m.steps, activity.MergeWait, "", time.Now())
	if out := strip(m.renderSpanList(80)); !strings.Contains(out, "Ship · merge-wait") {
		t.Fatalf("activity line missing %q:\n%s", "Ship · merge-wait", out)
	}
}

// A fold transition: an active Step re-renders as a folded one-liner once the next
// Step starts.
func TestSpanFoldTransition(t *testing.T) {
	m := initialModel(nil)
	m.steps = advanceActivity(m.steps, activity.Build, "", time.Now().Add(-time.Minute))
	if before := strip(m.renderSpanList(80)); strings.HasPrefix(before, "✓ Build") {
		t.Fatalf("Build should still be active, got %q", before)
	}
	m.steps = advanceActivity(m.steps, activity.Verify, "", time.Now())
	if after := strip(m.renderSpanList(80)); !strings.HasPrefix(after, "✓ Build") {
		t.Fatalf("Build should have folded after verify started, got %q", after)
	}
}

// The active Step shows its live Activity sub-label ("Verify · repair 2"), the
// same string the web stepper renders for the same run.
func TestRenderSpanListShowsActivitySubLabel(t *testing.T) {
	m := initialModel(nil)
	m.steps = advanceActivity(m.steps, activity.Repair, "repair2", time.Now())

	out := strip(m.renderSpanList(80))
	if !strings.Contains(out, "Verify · repair 2") {
		t.Fatalf("activity sub-label missing:\n%s", out)
	}
}

// A failed Step stays expanded and re-surfaces its preserved tail.
func TestRenderSpanListFailedKeepsTail(t *testing.T) {
	m := initialModel(nil)
	m.steps = advanceActivity(m.steps, activity.Verify, "", time.Now())
	m.steps = finalize(m.steps, false, time.Now())
	idx := failedIndex(m.steps)
	m.steps[idx].tailSnapshot = []string{"panic: boom", "  goroutine 1"}

	out := strip(m.renderSpanList(80))
	if !strings.Contains(out, "✗ Verify") {
		t.Errorf("failed phase should show ✗ Verify:\n%s", out)
	}
	for _, want := range []string{"panic: boom", "goroutine 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("preserved tail line %q missing:\n%s", want, out)
		}
	}
}

// activityText spaces a letter/digit boundary so a raw call label reads cleanly,
// leaving bare Activities and hyphenated names untouched — matching the web.
func TestActivityText(t *testing.T) {
	cases := []struct {
		act    activity.Activity
		detail string
		want   string
	}{
		{activity.Repair, "repair2", "repair 2"},
		{activity.Bugfix, "bugfix10", "bugfix 10"},
		{activity.Verify, "", "verify"},
		{activity.CIWait, "", "ci-wait"},
	}
	for _, c := range cases {
		if got := activityText(c.act, c.detail); got != c.want {
			t.Errorf("activityText(%q, %q) = %q, want %q", c.act, c.detail, got, c.want)
		}
	}
}

// feedTail windows to the last n lines tagged with the phase, skipping phase-start
// (▸) and continuation (↳) rows.
func TestFeedTailFiltersAndWindows(t *testing.T) {
	m := initialModel(nil)
	m.feed = []feedEntry{
		{glyph: "▸", phase: "PR", text: "opus", gstyle: m.styles.Info},
		{glyph: "→", phase: "PR", text: "PR https://x/1", gstyle: m.styles.Info},
		{glyph: "·", phase: "CI", text: "other phase", gstyle: m.styles.Subtle},
		{glyph: "↳", phase: "PR", text: "detail", gstyle: m.styles.Subtle, sub: true},
	}
	got := m.feedTail("PR", tailWindow)
	if len(got) != 1 {
		t.Fatalf("want 1 PR line (skip ▸/↳/other phase), got %d: %v", len(got), got)
	}
	if !strings.Contains(strip(got[0]), "PR https://x/1") {
		t.Errorf("feed tail = %q, want the PR url", strip(got[0]))
	}
}

// At a deliberately narrow width the styled fold/active/pending lines truncate
// without overflowing — the styled-string path must go through ansi.Truncate so
// the trailing SGR reset survives and no line exceeds the pane.
func TestRenderSpanListNarrowWidthNoOverflow(t *testing.T) {
	m := initialModel(nil)
	m.steps = advanceActivity(m.steps, activity.Verify, "", time.Now())
	m.steps[0].tag = "claude-opus-4-8 @high"
	m.steps[0].took = 6*time.Minute + 2*time.Second
	idx := activeIndex(m.steps)
	m.steps[idx].tag = "claude-opus-4-8 @high"

	const width = 20
	for _, ln := range strings.Split(m.renderSpanList(width), "\n") {
		if w := lipgloss.Width(ln); w > width {
			t.Errorf("line overflows %d cols (%d): %q", width, w, ln)
		}
	}
}

func TestLastNWindows(t *testing.T) {
	in := []string{"a", "b", "c", "d"}
	if got := lastN(in, 2); len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Errorf("lastN 2 = %v, want [c d]", got)
	}
	if got := lastN(in, 10); len(got) != 4 {
		t.Errorf("lastN over-length = %v, want all 4", got)
	}
}

// vtermTail trims trailing blank rows and returns the last n non-blank lines.
func TestVtermTailWindowsNonBlank(t *testing.T) {
	s := vterm.New(40, 12)
	defer s.Close()
	s.Write([]byte("l1\r\nl2\r\nl3\r\nl4\r\nl5\r\nl6\r\nl7\r\nl8\r\n"))

	got := vtermTail(s, tailWindow)
	if len(got) != tailWindow {
		t.Fatalf("want %d tail lines, got %d: %v", tailWindow, len(got), got)
	}
	if !strings.Contains(strip(got[0]), "l3") || !strings.Contains(strip(got[len(got)-1]), "l8") {
		t.Errorf("vterm tail = %v, want l3..l8", got)
	}
}

// phaseTailLines prefers a failed phase's snapshot over live sources.
func TestPhaseTailLinesPrefersSnapshot(t *testing.T) {
	m := initialModel(nil)
	m.steps[2].state = stepFailed
	m.steps[2].tailSnapshot = []string{"frozen reason"}
	got := m.phaseTailLines(2, tailWindow)
	if len(got) != 1 || got[0] != "frozen reason" {
		t.Fatalf("want the snapshot, got %v", got)
	}
}
