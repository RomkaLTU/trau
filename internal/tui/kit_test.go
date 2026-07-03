package tui

import (
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
