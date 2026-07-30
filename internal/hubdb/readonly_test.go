package hubdb

import (
	"net/url"
	"strings"
	"testing"
)

// The driver rejects a percent-encoded Windows path outright, so the DSN has to
// keep its separators intact and anchor the drive letter below the root instead of
// leaving it to parse as the URI authority.
func TestReadOnlyDSNSurvivesAWindowsPath(t *testing.T) {
	dsn := readOnlyDSN(`C:\Users\a\.trau\trau.db`)

	if strings.Contains(dsn, "%5C") || strings.Contains(dsn, `\`) {
		t.Errorf("DSN = %q, want no encoded or literal backslashes", dsn)
	}
	if !strings.HasPrefix(dsn, "file:///") {
		t.Errorf("DSN = %q, want a root-anchored file:/// URI", dsn)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN %q: %v", dsn, err)
	}
	if u.Host != "" {
		t.Errorf("DSN authority = %q, want empty — a drive letter must not become the host", u.Host)
	}
	if want := "/C:/Users/a/.trau/trau.db"; u.Path != want {
		t.Errorf("DSN path = %q, want %q", u.Path, want)
	}
	if want := "mode=ro&_pragma=busy_timeout(2000)"; u.RawQuery != want {
		t.Errorf("DSN query = %q, want %q", u.RawQuery, want)
	}
}

func TestReadOnlyDSNLeavesAUnixPathUnchanged(t *testing.T) {
	got := readOnlyDSN("/home/a/.trau/trau.db")
	want := "file:///home/a/.trau/trau.db?mode=ro&_pragma=busy_timeout(2000)"
	if got != want {
		t.Errorf("readOnlyDSN = %q, want %q", got, want)
	}
}

// CheckHealth is the path `trau doctor` takes. On Windows it reported the database
// unopenable — "invalid uri authority" — while the hub held the very same file open
// and healthy, which read as data loss and invited deleting a good database.
func TestCheckHealthReadsADatabaseTheHubCreated(t *testing.T) {
	home := t.TempDir()
	db, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	wantVersion := db.Version()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	h := CheckHealth(home)
	if h.Err != nil {
		t.Fatalf("CheckHealth reported %v, want a clean read of the hub's own database", h.Err)
	}
	if !h.Exists {
		t.Error("CheckHealth reported the database missing")
	}
	if h.Version != wantVersion {
		t.Errorf("CheckHealth version = %d, want %d", h.Version, wantVersion)
	}
}
