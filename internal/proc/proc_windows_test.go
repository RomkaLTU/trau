package proc

import (
	"path/filepath"
	"strings"
	"testing"
)

// netstat ships with Windows, so the port->pid lookup has nothing to skip for.
func skipWithoutPortTool(*testing.T) {}

// LookBin must find a bare name via %PATH% and return it absolute: the ConPTY
// spawn hands the result to a raw CreateProcess, which never searches %PATH%
// itself and probes a bare name against the child's working directory instead
// (COD-1324). cmd.exe is on every supported Windows.
func TestLookBinResolvesABareNameViaPATH(t *testing.T) {
	got, err := LookBin("cmd")
	if err != nil {
		t.Fatalf("LookBin(cmd): %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("LookBin(cmd) = %q, want an absolute path", got)
	}
	if !strings.EqualFold(filepath.Base(got), "cmd.exe") {
		t.Errorf("LookBin(cmd) = %q, want a path to cmd.exe", got)
	}
}

func TestLookBinReportsAMissingBinary(t *testing.T) {
	if got, err := LookBin("trau-test-no-such-bin"); err == nil {
		t.Errorf("LookBin(trau-test-no-such-bin) = %q, want an error", got)
	}
}
