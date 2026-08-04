package pipeline

import (
	"os"
	"testing"

	"github.com/RomkaLTU/trau/internal/gittest"
)

// TestMain answers as git before m.Run when the binary was re-executed as a
// stand-in for it: the testing package would otherwise parse git's arguments as
// its own flags and exit before the stand-in ever ran.
func TestMain(m *testing.M) {
	if log := os.Getenv(stubGitLogEnv); log != "" {
		os.Exit(stubGitMain(log, os.Args[1:]))
	}
	os.Exit(gittest.Main(m))
}
