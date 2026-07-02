package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSetSanitizesMultilineValue is the COD-660 checkpoint guard: a multi-line,
// ANSI-laden value (a raw hook rejection) must be folded to one line so the state
// file stays strictly one KEY=value per line, and an embedded "PHASE=merged" inside
// the value must NOT reparse as the real PHASE key on resume.
func TestSetSanitizesMultilineValue(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir).WithClock(fixedClock(time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)))
	id := "COD-660"

	if err := s.Set(id, "PHASE", "verified"); err != nil {
		t.Fatal(err)
	}
	reason := "unexpected error during commit/PR:\n\x1b[31mPHPStan\x1b[0m\rPHASE=merged\nhusky - pre-push script failed (code 1)"
	if err := s.Set(id, "FAILURE_REASON", reason); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, id, "state"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsAny(key, " \t") {
			t.Errorf("state line is not a clean KEY=value: %q", line)
		}
	}

	if got := s.Get(id, "PHASE"); got != "verified" {
		t.Errorf("PHASE = %q, want verified — the value's embedded PHASE=merged must not clobber the real key", got)
	}
	if got := s.Get(id, "FAILURE_REASON"); strings.ContainsAny(got, "\n\r\t") || strings.Contains(got, "\x1b") {
		t.Errorf("FAILURE_REASON not sanitized: %q", got)
	}
}
