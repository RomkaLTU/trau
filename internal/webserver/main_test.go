package webserver

import (
	"fmt"
	"os"
	"testing"

	"github.com/RomkaLTU/trau/internal/gittest"
)

func TestMain(m *testing.M) { os.Exit(webserverMain(m)) }

// webserverMain gives the whole binary a home of its own before any test runs.
// Per-test t.Setenv is not enough here: a drain goroutine outlives the test that
// armed it, and the restore fires the moment that test returns, so the leaked
// pass resolves its repo config against the developer's real ~/.trau.ini and
// writes the queued label to whatever tracker is configured there — the live
// Linear write ADR 0027 exists to stop. A binary-wide home leaves that config
// empty whichever goroutine reads it, whenever it reads it. USERPROFILE rides
// along because os.UserHomeDir reads that one on Windows.
func webserverMain(m *testing.M) int {
	home, err := os.MkdirTemp("", "trau-webserver-home")
	if err != nil {
		fmt.Fprintf(os.Stderr, "webserver test home: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(home) }()

	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, home); err != nil {
			fmt.Fprintf(os.Stderr, "webserver test home: %v\n", err)
			return 1
		}
	}
	return gittest.Main(m)
}
