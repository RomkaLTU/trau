package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

func TestResolveSince(t *testing.T) {
	got, err := resolveSince("")
	if err != nil || got != "" {
		t.Fatalf("empty since = (%q, %v), want (\"\", nil)", got, err)
	}

	got, err = resolveSince("30m")
	if err != nil {
		t.Fatalf("duration since: %v", err)
	}
	ts, perr := time.Parse(time.RFC3339, got)
	if perr != nil {
		t.Fatalf("duration since %q is not RFC3339: %v", got, perr)
	}
	if d := time.Since(ts); d < 29*time.Minute || d > 31*time.Minute {
		t.Fatalf("30m window resolved %v ago, want ~30m", d)
	}

	got, err = resolveSince("2026-07-12T10:00:00Z")
	if err != nil || got != "2026-07-12T10:00:00Z" {
		t.Fatalf("rfc3339 since = (%q, %v), want the same timestamp", got, err)
	}

	if _, err := resolveSince("last tuesday"); err == nil {
		t.Fatal("resolveSince(garbage) = nil error, want a parse error")
	}
}

func TestParseForensicsFlags(t *testing.T) {
	f, err := parseForensicsFlags("events", []string{"--ticket", "COD-1", "--since", "2h", "--grep", "boom", "--kind", "state_change", "--limit", "50", "--follow", "--json"}, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.ticket != "COD-1" || f.since != "2h" || f.grep != "boom" || f.kind != "state_change" || f.limit != 50 || !f.follow || !f.json {
		t.Fatalf("flags = %+v, want the parsed values", f)
	}

	sp, err := parseForensicsFlags("spend", []string{"COD-9", "--json"}, true)
	if err != nil {
		t.Fatalf("parse spend: %v", err)
	}
	if sp.ticket != "COD-9" || !sp.json {
		t.Fatalf("spend flags = %+v, want ticket COD-9 json", sp)
	}

	if _, err := parseForensicsFlags("runs", []string{"--limit", "0"}, false); err == nil {
		t.Fatal("--limit 0 accepted, want a usage error")
	}
	if _, err := parseForensicsFlags("runs", []string{"--bogus"}, false); err == nil {
		t.Fatal("--bogus accepted, want a usage error")
	}
}

func TestWriteEventLine(t *testing.T) {
	var b bytes.Buffer
	writeEventLine(&b, hubclient.EventRecord{
		TS:     "2026-07-12T10:01:00Z",
		Kind:   "state_change",
		Phase:  "build",
		Fields: map[string]any{"ticket": "COD-1", "state": "faulted", "reason": "boom", "turns": float64(3)},
	})
	line := b.String()
	// Fields render as sorted key=value pairs; a whole-number JSON number stays an int.
	for _, want := range []string{"state_change", "[build]", "reason=boom", "state=faulted", "ticket=COD-1", "turns=3"} {
		if !strings.Contains(line, want) {
			t.Fatalf("event line %q missing %q", line, want)
		}
	}
}

func TestWriteRunsTable(t *testing.T) {
	var b bytes.Buffer
	writeRunsTable(&b, []hubclient.RunSummary{
		{Ticket: "COD-1", Phase: "building", FailureClass: "faulted", FailureReason: "agent stalled"},
	})
	out := b.String()
	if !strings.Contains(out, "TICKET") || !strings.Contains(out, "COD-1") || !strings.Contains(out, "faulted") || !strings.Contains(out, "agent stalled") {
		t.Fatalf("runs table missing expected content:\n%s", out)
	}

	b.Reset()
	writeRunsTable(&b, nil)
	if !strings.Contains(b.String(), "No runs") {
		t.Fatalf("empty runs table = %q, want a no-runs line", b.String())
	}
}

func TestMaxCursor(t *testing.T) {
	if got := maxCursor(5, "9"); got != 9 {
		t.Fatalf("maxCursor(5, 9) = %d, want 9", got)
	}
	if got := maxCursor(9, "5"); got != 9 {
		t.Fatalf("maxCursor(9, 5) = %d, want 9 (never regress)", got)
	}
	if got := maxCursor(7, "not-a-number"); got != 7 {
		t.Fatalf("maxCursor with bad id = %d, want the unchanged cursor 7", got)
	}
}
