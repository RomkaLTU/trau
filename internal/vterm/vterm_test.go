package vterm

import (
	"strings"
	"testing"
)

// TestScreenReconstructsCursorAddressedOutput feeds an alt-screen, cursor-addressed
// frame (the shape Claude emits) and checks the rendered screen shows the text in
// place, keeps SGR color, drops control sequences, and that a device-attributes
// query does not deadlock the writer.
func TestScreenReconstructsCursorAddressedOutput(t *testing.T) {
	s := New()
	defer s.Close()

	s.Write([]byte("\x1b[?1049h\x1b[2J\x1b[H")) // enter alt-screen, clear, home
	s.Write([]byte("\x1b[1;1HHello"))
	s.Write([]byte("\x1b[2;1H\x1b[31mRED\x1b[0m"))
	s.Write([]byte("\x1b[c")) // device-attributes query: must not block
	s.Write([]byte("\x1b[5;1Hbottom line"))

	lines := s.Lines()
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Hello", "RED", "bottom line"} {
		if !strings.Contains(joined, want) {
			t.Errorf("screen missing %q; got:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "\x1b[") {
		t.Error("expected SGR styling in the rendered screen, found none")
	}
	for _, garbage := range []string{"[?1049h", "[2J", "[1;1H", "[5;1H"} {
		if strings.Contains(joined, garbage) {
			t.Errorf("control sequence %q leaked into the rendered screen", garbage)
		}
	}
}

// TestScreenLastWriteWins checks a later repaint of a cell replaces the earlier
// content rather than appending — the collapse that makes spinners legible.
func TestScreenLastWriteWins(t *testing.T) {
	s := New()
	defer s.Close()
	s.Write([]byte("\x1b[1;1Hfirst"))
	s.Write([]byte("\x1b[1;1Hsecond"))
	joined := strings.Join(s.Lines(), "\n")
	if !strings.Contains(joined, "second") {
		t.Errorf("expected latest repaint, got:\n%s", joined)
	}
	if strings.Contains(joined, "firstst") {
		t.Errorf("repaint appended instead of overwriting:\n%s", joined)
	}
}
