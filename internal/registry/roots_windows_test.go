package registry

import "testing"

// The two spellings that reach the hub for one Windows directory: the web
// registers what the file picker hands it, and a loop under Git Bash heartbeats
// what `git rev-parse --show-toplevel` answers with.
func TestCanonicalRootFoldsSlashesAndDriveCase(t *testing.T) {
	const want = `C:\Users\x\acme`
	for _, spelling := range []string{want, "C:/Users/x/acme", "c:/Users/x/acme/", `c:\Users\x\acme`} {
		if got := CanonicalRoot(spelling); got != want {
			t.Errorf("CanonicalRoot(%q) = %q, want %q", spelling, got, want)
		}
	}
}

func TestCanonicalRootLeavesAUNCVolumeAlone(t *testing.T) {
	const want = `\\Server\share\acme`
	if got := CanonicalRoot(`\\Server\share/acme`); got != want {
		t.Errorf("CanonicalRoot = %q, want %q", got, want)
	}
}
