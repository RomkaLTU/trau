package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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

// A folded non-agent phase carries just its duration when no model tag is known.
func TestFoldedSpanWithoutTag(t *testing.T) {
	m := initialModel(nil)
	m.steps[4].state = stepDone // PR
	m.steps[4].took = time.Second

	if got, want := strip(m.foldedSpan(m.steps[4], 80)), "✓ PR  1s"; got != want {
		t.Fatalf("folded = %q, want %q", got, want)
	}
}

// spanDetail ticks live elapsed while the phase is active.
func TestSpanDetailActiveUsesLiveElapsed(t *testing.T) {
	st := phaseStep{state: stepActive, start: time.Now().Add(-90 * time.Second), tag: "sonnet"}
	if got := spanDetail(st); !strings.HasPrefix(got, "1m3") || !strings.HasSuffix(got, "sonnet") {
		t.Fatalf("detail = %q, want live elapsed + tag", got)
	}
}

// renderSpanList folds done phases, expands the active one, and collapses the
// remaining pending phases onto one compact row.
func TestRenderSpanListFoldExpandPending(t *testing.T) {
	m := initialModel(nil)
	m.steps = startPhase(m.steps, "handoff", time.Now()) // build done, handoff active
	out := strip(m.renderSpanList(80))
	lines := strings.Split(out, "\n")

	if !strings.HasPrefix(lines[0], "✓ Build") {
		t.Errorf("line 0 should fold Build, got %q", lines[0])
	}
	if !strings.Contains(out, "Handoff") {
		t.Errorf("active Handoff missing:\n%s", out)
	}
	last := lines[len(lines)-1]
	for _, p := range []string{"○ Verify", "○ Commit", "○ PR", "○ CI", "○ Merge"} {
		if !strings.Contains(last, p) {
			t.Errorf("pending row missing %q, got %q", p, last)
		}
	}
}

// A fold transition: an active phase re-renders as a folded one-liner once the
// next phase starts.
func TestSpanFoldTransition(t *testing.T) {
	m := initialModel(nil)
	m.steps = startPhase(m.steps, "build", time.Now().Add(-time.Minute))
	if before := strip(m.renderSpanList(80)); strings.HasPrefix(before, "✓ Build") {
		t.Fatalf("Build should still be active, got %q", before)
	}
	m.steps = startPhase(m.steps, "handoff", time.Now())
	if after := strip(m.renderSpanList(80)); !strings.HasPrefix(after, "✓ Build") {
		t.Fatalf("Build should have folded after handoff started, got %q", after)
	}
}

// Child spans render indented under the active phase with their counters.
func TestRenderSpanListShowsChildSpans(t *testing.T) {
	m := initialModel(nil)
	m.steps = startPhase(m.steps, "verify", time.Now())
	idx := activeIndex(m.steps)
	m.steps[idx].subs = []childSpan{{kind: "repair", label: "repair 2/3", detail: "lint"}}

	out := strip(m.renderSpanList(80))
	if !strings.Contains(out, "↻ repair 2/3 · lint") {
		t.Fatalf("child span missing:\n%s", out)
	}
}

// A failed phase stays expanded and re-surfaces its preserved tail.
func TestRenderSpanListFailedKeepsTail(t *testing.T) {
	m := initialModel(nil)
	m.steps = startPhase(m.steps, "verify", time.Now())
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

func TestParseChildSpan(t *testing.T) {
	cases := []struct {
		line       string
		wantKind   string
		wantLabel  string
		wantDetail string
		wantOK     bool
	}{
		{"  ⚠ verify failed — self-heal attempt 2/3", "repair", "repair 2/3", "", true},
		{"  ⚠ repairs exhausted — comprehensive bugfix attempt 1/2", "bugfix", "bugfix 1/2", "", true},
		{"  ⚠ push rejected by a pre-push gate — repair attempt 3/3", "repair", "repair 3/3", "push", true},
		{"  ⤳ verify: kimi exhausted — falling back to claude", "fallback", "fallback", "claude", true},
		{"  ↻ verify failed (timeout) — retrying 2/3", "retry", "retry 2/3", "", true},
		{"  ⟳ gh pr merge failed (boom) — retrying in 2s (2/2)", "retry", "retry 2/2", "", true},
		{"  ✓ verify passed", "", "", "", false},
		{"  ↻ adopted in-progress branch feature/x (checkpoint: build)", "", "", "", false},
		{"  ⚠ epic CI red — repair attempt 1/2", "", "", "", false}, // not a pre-push repair
	}
	for _, c := range cases {
		got, ok := parseChildSpan(c.line)
		if ok != c.wantOK {
			t.Errorf("parseChildSpan(%q) ok = %v, want %v", c.line, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if got.kind != c.wantKind || got.label != c.wantLabel || got.detail != c.wantDetail {
			t.Errorf("parseChildSpan(%q) = %+v, want {%s %s %s}", c.line, got, c.wantKind, c.wantLabel, c.wantDetail)
		}
	}
}

// upsertChildSpan updates a climbing counter in place and appends new kinds.
func TestUpsertChildSpan(t *testing.T) {
	var subs []childSpan
	subs = upsertChildSpan(subs, childSpan{kind: "repair", label: "repair 1/3"})
	subs = upsertChildSpan(subs, childSpan{kind: "repair", label: "repair 2/3"})
	if len(subs) != 1 || subs[0].label != "repair 2/3" {
		t.Fatalf("repair should update in place, got %+v", subs)
	}
	subs = upsertChildSpan(subs, childSpan{kind: "fallback", label: "fallback", detail: "claude"})
	if len(subs) != 2 {
		t.Fatalf("distinct kind should append, got %+v", subs)
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
	m.steps = startPhase(m.steps, "verify", time.Now())
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
