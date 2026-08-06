package sanitize

import (
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b[2K\x1b[1G1068/1068", "1068/1068"},
		{"no\x1b]0;title\x07codes", "nocodes"},
	}
	for _, c := range cases {
		if got := StripANSI(c.in); got != c.want {
			t.Errorf("StripANSI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFeedLineIsSingleRow(t *testing.T) {
	raw := "git push: exit status 1: \x1b[33mNote: config\x1b[0m\n1068/1068 [====]\r1068/1068 [====]\nhusky - pre-push script failed (code 1)"
	got := FeedLine(raw)
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("FeedLine kept a control char: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("FeedLine kept an ANSI escape: %q", got)
	}
	if !strings.Contains(got, "husky - pre-push script failed (code 1)") {
		t.Errorf("FeedLine dropped the informative tail: %q", got)
	}
}

func TestFeedLinePreservesIndentAndSpacing(t *testing.T) {
	// A normal, already-clean feed line must survive verbatim — leading indentation
	// and single-space word gaps carry the feed's structure.
	in := "  ✓ merged COD-1, marked Done"
	if got := FeedLine(in); got != in {
		t.Errorf("FeedLine mangled a clean line: %q → %q", in, got)
	}
}

func TestFeedLineTruncatesKeepingHeadAndTail(t *testing.T) {
	head := "git push failed: "
	tail := " husky - pre-push script failed (code 1)"
	in := head + strings.Repeat("x", 4000) + tail
	got := FeedLine(in)
	if n := len([]rune(got)); n > feedMax {
		t.Errorf("FeedLine length = %d, want <= %d", n, feedMax)
	}
	if !strings.HasPrefix(got, "git push") {
		t.Errorf("FeedLine dropped the head context: %q", got)
	}
	if !strings.HasSuffix(got, "(code 1)") {
		t.Errorf("FeedLine dropped the informative tail: %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("FeedLine should mark the elided middle: %q", got)
	}
}

// TestPromptTextScrubsOnlyNUL pins the argv contract: the one byte fork/exec
// rejects is replaced by its visible escape, and every other control byte a
// prompt legitimately carries — newlines, tabs, ANSI-colored gate output — is
// left exactly as the source produced it.
func TestPromptTextScrubsOnlyNUL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{"clean text passes through", "build COD-1", "build COD-1", false},
		{"one NUL becomes the escape", "a\x00b", "a" + NULEscape + "b", true},
		{"every NUL is replaced", "\x00a\x00\x00", NULEscape + "a" + NULEscape + NULEscape, true},
		{"newlines and tabs survive", "line\n\tindented\r\n", "line\n\tindented\r\n", false},
		{"ANSI evidence survives", "\x1b[31mFAIL\x1b[0m", "\x1b[31mFAIL\x1b[0m", false},
		{"a NUL beside kept control bytes", "gate:\n\x1b[31mFAIL\x1b[0m\t\x00end", "gate:\n\x1b[31mFAIL\x1b[0m\t" + NULEscape + "end", true},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := PromptText(tc.in)
			if got != tc.want {
				t.Errorf("PromptText(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if changed != tc.changed {
				t.Errorf("PromptText(%q) changed = %v, want %v", tc.in, changed, tc.changed)
			}
			if strings.ContainsRune(got, 0) {
				t.Errorf("PromptText(%q) left a NUL in the result: %q", tc.in, got)
			}
		})
	}
}

// TestPromptTextEscapeIsWritable guards the replacement text itself: it must be
// the escape an agent would have typed in source, not another raw byte.
func TestPromptTextEscapeIsWritable(t *testing.T) {
	if strings.ContainsRune(NULEscape, 0) {
		t.Fatalf("NULEscape is not an escape, it holds a raw byte: %q", NULEscape)
	}
}

func TestStateValueIsSingleLine(t *testing.T) {
	// The dangerous case: output that itself contains a KEY=value line must not
	// survive as a second line that reparses as a state key.
	raw := "unexpected error during commit/PR:\nPHASE=merged\n\x1b[31mboom\x1b[0m\r"
	got := StateValue(raw)
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("StateValue kept a control char: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("StateValue kept an ANSI escape: %q", got)
	}
	if n := len([]rune(got)); n > stateMax {
		t.Errorf("StateValue length = %d, want <= %d", n, stateMax)
	}
}
