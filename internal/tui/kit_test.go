package tui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// maxLineWidth returns the widest visible line in a rendered block.
func maxLineWidth(s string) int {
	w := 0
	for _, ln := range strings.Split(s, "\n") {
		if lw := lipgloss.Width(ln); lw > w {
			w = lw
		}
	}
	return w
}

// TestTitledPanelDimensions locks the panel container to exactly w×h across a
// range of sizes — including a sub-floor width and a title far wider than the
// box — so the tiled dashboard/logs panes never over- or under-draw their cell.
func TestTitledPanelDimensions(t *testing.T) {
	s := DefaultStyles()
	cases := []struct {
		name        string
		title, body string
		w, h        int
	}{
		{"standard", "Pipeline", "one\ntwo", 30, 6},
		{"narrow clamps to floor", "x", "hi", 2, 4},
		{"over-long title truncates", "a-very-long-panel-title", "body", 14, 5},
		{"tall body truncated", "T", "a\nb\nc\nd\ne\nf", 20, 4},
		{"empty body padded", "T", "", 16, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := titledPanel(s, c.title, c.body, c.w, c.h)
			lines := strings.Split(out, "\n")

			wantW := c.w
			if wantW < 6 {
				wantW = 6
			}
			wantH := c.h
			if wantH < 3 { // innerH floors at 1 → top + 1 body + bottom
				wantH = 3
			}
			if len(lines) != wantH {
				t.Fatalf("height = %d lines, want %d:\n%s", len(lines), wantH, out)
			}
			for i, ln := range lines {
				if w := lipgloss.Width(ln); w != wantW {
					t.Errorf("line %d width = %d, want %d: %q", i, w, wantW, ln)
				}
			}
		})
	}
}

// TestListRowMarkerAndAlignment covers the cursor idiom and column alignment: a
// focused row shows the ▸ pointer, an unfocused one does not, and the label is
// padded to labelW with at least one space before the description — even when
// the label overruns labelW.
func TestListRowMarkerAndAlignment(t *testing.T) {
	s := DefaultStyles()

	focused := listRow(s, true, "Run", "start the loop", 14)
	if !strings.Contains(focused, "▸") {
		t.Errorf("focused row missing ▸ cursor: %q", focused)
	}
	blurred := listRow(s, false, "Run", "start the loop", 14)
	if strings.Contains(blurred, "▸") {
		t.Errorf("unfocused row should not show the ▸ cursor: %q", blurred)
	}

	// Label shorter than labelW: width = marker(2) + labelW + desc.
	row := listRow(s, false, "ab", "x", 10)
	if got, want := lipgloss.Width(row), 2+10+1; got != want {
		t.Errorf("aligned row width = %d, want %d", got, want)
	}

	// Label longer than labelW: the gap collapses to a single space, not negative.
	long := listRow(s, false, "abcdefghijk", "x", 4)
	if got, want := lipgloss.Width(long), 2+11+1+1; got != want {
		t.Errorf("overflow row width = %d, want %d (marker+label+1 gap+desc)", got, want)
	}

	// No description: just marker + label, nothing trailing.
	bare := listRow(s, false, "Solo", "", 14)
	if got, want := lipgloss.Width(bare), 2+4; got != want {
		t.Errorf("bare row width = %d, want %d", got, want)
	}
}

// TestRadioRow checks exactly one option carries the ● filled glyph and the rest
// are ○, for a valid index and an out-of-range one (all empty).
func TestRadioRow(t *testing.T) {
	s := DefaultStyles()

	out := radioRow(s, []string{"user", "project", "local"}, 1)
	if got := strings.Count(out, "●"); got != 1 {
		t.Errorf("filled radios = %d, want 1: %q", got, out)
	}
	if got := strings.Count(out, "○"); got != 2 {
		t.Errorf("empty radios = %d, want 2: %q", got, out)
	}

	none := radioRow(s, []string{"a", "b"}, 9)
	if strings.Contains(none, "●") {
		t.Errorf("out-of-range index should fill nothing: %q", none)
	}
	if got := strings.Count(none, "○"); got != 2 {
		t.Errorf("empty radios = %d, want 2: %q", got, none)
	}
}

// TestCardBoxCapsWidth asserts the shared card never overflows a narrow terminal:
// a body far wider than the viewport is hard-capped to the cap width.
func TestCardBoxCapsWidth(t *testing.T) {
	s := DefaultStyles()
	wide := strings.Repeat("x", 200)
	for _, cap := range []int{10, 24, 40} {
		out := cardBox(s, cap, wide)
		if got := maxLineWidth(out); got > cap {
			t.Errorf("card at cap %d rendered %d wide, want ≤ %d", cap, got, cap)
		}
	}
}

// mkLines returns n lines "L0".."Ln-1" for exercising the scroll helpers.
func mkLines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "L" + strconv.Itoa(i)
	}
	return out
}

// TestScrollToCursor covers the selection-list window: content that fits is
// returned untouched (no visual change on a tall terminal), and content that
// overflows is clamped to exactly height rows with the anchor always visible and
// edge-scrolled at both ends.
func TestScrollToCursor(t *testing.T) {
	// Fits: returned unchanged, same backing slice length.
	lines := mkLines(5)
	if got := scrollToCursor(lines, 4, 10); len(got) != 5 || got[0] != "L0" || got[4] != "L4" {
		t.Fatalf("fitting content should be unchanged, got %v", got)
	}

	// Anchor at the top: window starts at the top.
	if got := scrollToCursor(mkLines(50), 0, 8); len(got) != 8 || got[0] != "L0" || got[7] != "L7" {
		t.Fatalf("top anchor window = %v", got)
	}

	// Anchor in the middle: anchor is the last visible row (edge scroll).
	got := scrollToCursor(mkLines(50), 20, 8)
	if len(got) != 8 || got[7] != "L20" || got[0] != "L13" {
		t.Fatalf("mid anchor window = %v, want L13..L20", got)
	}

	// Anchor at the bottom: window pins to the end, never scrolls past it.
	got = scrollToCursor(mkLines(50), 49, 8)
	if len(got) != 8 || got[0] != "L42" || got[7] != "L49" {
		t.Fatalf("bottom anchor window = %v, want L42..L49", got)
	}

	// Degenerate height floors at 1 row.
	if got := scrollToCursor(mkLines(5), 3, 0); len(got) != 1 || got[0] != "L3" {
		t.Fatalf("zero height should show 1 row at anchor, got %v", got)
	}
}

// TestWindowAt covers the prose scroll window: it clamps the offset into range,
// reports overflow, and leaves fitting content untouched.
func TestWindowAt(t *testing.T) {
	win, off, overflow := windowAt(mkLines(5), 3, 10)
	if overflow || off != 0 || len(win) != 5 {
		t.Fatalf("fitting content: overflow=%v off=%d len=%d", overflow, off, len(win))
	}

	win, off, overflow = windowAt(mkLines(50), 5, 8)
	if !overflow || off != 5 || win[0] != "L5" || win[7] != "L12" {
		t.Fatalf("mid window: off=%d win=%v", off, win)
	}

	// Offset past the end is clamped so the last page stays full.
	win, off, overflow = windowAt(mkLines(50), 999, 8)
	if !overflow || off != 42 || win[0] != "L42" || win[7] != "L49" {
		t.Fatalf("clamped window: off=%d win=%v", off, win)
	}

	// Negative offset clamps to the top.
	_, off, _ = windowAt(mkLines(50), -5, 8)
	if off != 0 {
		t.Fatalf("negative offset should clamp to 0, got %d", off)
	}
}

// TestCardBodyBudget checks the chrome math (border+padding+hint = 5) and the
// extra-rows subtraction, floored at 1.
func TestCardBodyBudget(t *testing.T) {
	if got := cardBodyBudget(24, 0); got != 19 {
		t.Errorf("budget(24,0) = %d, want 19", got)
	}
	if got := cardBodyBudget(24, 4); got != 15 {
		t.Errorf("budget(24,4) = %d, want 15", got)
	}
	if got := cardBodyBudget(3, 0); got != 1 {
		t.Errorf("tiny terminal should floor at 1, got %d", got)
	}
}

// TestCursorMarkerWidth keeps focused and unfocused markers the same width so
// list rows don't shift horizontally as the cursor moves.
func TestCursorMarkerWidth(t *testing.T) {
	s := DefaultStyles()
	on, off := cursorMarker(s, true), cursorMarker(s, false)
	if lipgloss.Width(on) != lipgloss.Width(off) {
		t.Errorf("marker widths differ: focused %d vs blurred %d",
			lipgloss.Width(on), lipgloss.Width(off))
	}
	if !strings.Contains(on, "▸") {
		t.Errorf("focused marker missing ▸: %q", on)
	}
}
