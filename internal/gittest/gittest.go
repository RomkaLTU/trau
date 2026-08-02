// Package gittest pins the git configuration every `git` child of a test binary
// resolves. Fixtures shell out to git and then hand the repo to production code,
// and ExecGit runs git with the plain process environment — so without a pin the
// identity comes from the developer's own ~/.gitconfig, and a commit made through
// the code under test dies with "Author identity unknown" on a machine that has
// none. That is every CI runner, which is where the suite has to pass.
//
// It is the same stance as the loopback-only network guard (ADR 0027): a test
// must not be able to read the developer's real configuration.
package gittest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// pinned is the whole configuration a test's git sees — nothing outside the
// checkout contributes. GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM need git 2.32+.
const pinned = `[user]
	name = trau-test
	email = trau-test@example.invalid
[init]
	defaultBranch = main
`

// Main pins the configuration for the lifetime of the test binary and runs m.
// Packages whose tests reach git wire it in as:
//
//	func TestMain(m *testing.M) { os.Exit(gittest.Main(m)) }
func Main(m *testing.M) int {
	dir, err := os.MkdirTemp("", "trau-gitconfig")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gittest: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(path, []byte(pinned), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gittest: %v\n", err)
		return 1
	}
	_ = os.Setenv("GIT_CONFIG_GLOBAL", path)
	_ = os.Setenv("GIT_CONFIG_SYSTEM", path)
	return m.Run()
}
