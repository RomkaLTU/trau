package hubdbtest

import (
	"slices"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

// TestSeededOpenMatchesAFreshMigration pins the fast path to the slow one: a home
// seeded from the template comes up on the same schema version with the same
// migrations recorded, so Open finds nothing left to apply.
func TestSeededOpenMatchesAFreshMigration(t *testing.T) {
	fresh, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open a freshly migrated hub db: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })

	seeded, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open a seeded hub db: %v", err)
	}
	t.Cleanup(func() { _ = seeded.Close() })

	if seeded.Version() != fresh.Version() {
		t.Errorf("seeded version = %d, want the freshly migrated %d", seeded.Version(), fresh.Version())
	}
	got, want := appliedKeys(t, seeded), appliedKeys(t, fresh)
	if !slices.Equal(got, want) {
		t.Errorf("seeded migrations = %v, want %v", got, want)
	}
}

func appliedKeys(t *testing.T, db *hubdb.DB) []string {
	t.Helper()
	rows, err := db.SQL().Query(`SELECT key FROM schema_migrations ORDER BY key`)
	if err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	defer func() { _ = rows.Close() }()

	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan schema_migrations: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	return keys
}
