package transcriptdb

import (
	"net/url"
	"strings"
	"testing"
)

// Same defect as hubdb's read-only DSN: url.URL percent-encodes a Windows path's
// backslashes and lets the drive letter parse as the URI authority, which the
// driver rejects before it ever touches the file.
func TestReadOnlyDSNSurvivesAWindowsPath(t *testing.T) {
	dsn := readOnlyDSN(`C:\Users\a\.trau\transcripts.db`)

	if strings.Contains(dsn, "%5C") || strings.Contains(dsn, `\`) {
		t.Errorf("DSN = %q, want no encoded or literal backslashes", dsn)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN %q: %v", dsn, err)
	}
	if u.Host != "" {
		t.Errorf("DSN authority = %q, want empty — a drive letter must not become the host", u.Host)
	}
	if want := "/C:/Users/a/.trau/transcripts.db"; u.Path != want {
		t.Errorf("DSN path = %q, want %q", u.Path, want)
	}
	if want := "mode=ro&_pragma=busy_timeout(2000)"; u.RawQuery != want {
		t.Errorf("DSN query = %q, want %q", u.RawQuery, want)
	}
}

func TestReadOnlyDSNLeavesAUnixPathUnchanged(t *testing.T) {
	got := readOnlyDSN("/home/a/.trau/transcripts.db")
	want := "file:///home/a/.trau/transcripts.db?mode=ro&_pragma=busy_timeout(2000)"
	if got != want {
		t.Errorf("readOnlyDSN = %q, want %q", got, want)
	}
}
