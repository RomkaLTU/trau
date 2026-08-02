// Package hubdbtest opens hub databases for tests without paying for the
// migration ladder on every open. hubdb.Open applies the embedded schema in one
// transaction per file, which costs ~115ms — a price the internal/webserver and
// internal/hubstore suites pay on nearly every test. Here the ladder runs once
// per test binary and every later open is seeded from its bytes, so all the
// migrations are already recorded and none of them is applied again.
//
// Packages that exercise migration itself keep calling hubdb.Open directly.
package hubdbtest

import (
	"fmt"
	"os"
	"sync"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

var template = sync.OnceValues(buildTemplate)

// Open seeds home with the migrated schema and opens it. It is a drop-in for
// hubdb.Open in tests.
func Open(home string) (*hubdb.DB, error) {
	if err := Seed(home); err != nil {
		return nil, err
	}
	return hubdb.Open(home)
}

// Seed writes the migrated schema to home's database path for callers that hand
// the home to code opening it themselves. A home that already holds a database
// is left alone, so two store sets over one home still share its state.
func Seed(home string) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("create trau home %s: %w", home, err)
	}
	path := hubdb.Path(home)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	seed, err := template()
	if err != nil {
		return fmt.Errorf("build hub database template: %w", err)
	}
	return os.WriteFile(path, seed, 0o644)
}

// buildTemplate migrates a throwaway home and returns the resulting file. The
// close is what makes the bytes complete: SQLite checkpoints the WAL back into
// the main file when the last connection goes away.
func buildTemplate() ([]byte, error) {
	home, err := os.MkdirTemp("", "trau-hubdb-template")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(home) }()

	db, err := hubdb.Open(home)
	if err != nil {
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(hubdb.Path(home))
}
