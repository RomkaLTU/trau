package hubstore

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/registry"
)

// The drive letter is the one part of a Windows path Clean leaves alone, and a
// clone the session already deleted has no directory left to resolve its spelling
// against, so the guard has to fold `c:` itself before it compares.
func TestRememberSkipsAScratchpadCloneSpelledWithALowercaseDrive(t *testing.T) {
	s := NewRegistrations(realHubHome, testHubDB(t, t.TempDir()))
	clone := filepath.Join(t.TempDir(), "scratch")
	vol := filepath.VolumeName(clone)
	spelling := strings.ToLower(vol) + clone[len(vol):]

	if err := s.Remember([]registry.Repo{
		{Name: "acme", Root: spelling, RunsDir: filepath.Join(spelling, "runs")},
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	known, err := s.Known()
	if err != nil {
		t.Fatalf("Known: %v", err)
	}
	if len(known) != 0 {
		t.Fatalf("known = %v, want no row — a temp-dir clone must not be adopted under any spelling", known)
	}
}
