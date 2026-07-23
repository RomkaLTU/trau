package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// kimiSessionFixture lays out one kimi session the way the CLI does — a line in
// the index beside the sessions dir, and the agent's wire.jsonl under it — and
// returns the session's directory.
func kimiSessionFixture(t *testing.T, home, id, workDir string, wire []string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "wd_fixture", id)
	if err := os.MkdirAll(filepath.Join(dir, "agents", "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	if len(wire) > 0 {
		path := filepath.Join(dir, "agents", "main", "wire.jsonl")
		if err := os.WriteFile(path, []byte(strings.Join(wire, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	line, err := json.Marshal(kimiIndexEntry{SessionID: id, SessionDir: dir, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.OpenFile(filepath.Join(home, kimiSessionIndex), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	if _, err := index.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	return dir
}

func kimiUsageLine(input, output, cacheRead, cacheCreation int) string {
	return fmt.Sprintf(`{"type":"usage.record","model":"kimi-code/kimi-for-coding","usageScope":"turn",`+
		`"usage":{"inputOther":%d,"output":%d,"inputCacheRead":%d,"inputCacheCreation":%d}}`,
		input, output, cacheRead, cacheCreation)
}

// TestFindKimiSessionMatchesWorkspaceAndStart pins how an interactive session is
// recovered with no resume hint to echo: the newest index entry for this call's
// workspace, and only one written since the call began.
func TestFindKimiSessionMatchesWorkspaceAndStart(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	repo, other := t.TempDir(), t.TempDir()

	old := time.Now().Add(-time.Hour)
	for _, f := range []struct{ id, dir string }{{"session_stale", repo}, {"session_elsewhere", other}} {
		if err := os.Chtimes(kimiSessionFixture(t, home, f.id, f.dir, nil), old, old); err != nil {
			t.Fatal(err)
		}
	}

	since := time.Now()
	if _, ok := findKimiSession(sessions, repo, since); ok {
		t.Error("a session last written before the call started must not be adopted")
	}

	kimiSessionFixture(t, home, "session_live", repo, nil)
	got, ok := findKimiSession(sessions, repo, since)
	if !ok {
		t.Fatal("the session started for this workspace was not found")
	}
	if got != "session_live" {
		t.Errorf("session = %q, want the one indexed for the workspace since the call began", got)
	}

	if _, ok := findKimiSession(sessions, t.TempDir(), since); ok {
		t.Error("a workspace with no indexed session must report none")
	}
}

// TestReadKimiSessionStatsMatchesPrintMode is the accounting guard for the
// interactive path: once the session id is recovered from the index, the numbers
// come out of wire.jsonl through exactly the reader print mode uses.
func TestReadKimiSessionStatsMatchesPrintMode(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")

	kimiSessionFixture(t, home, "session_usage", t.TempDir(), []string{
		`{"type":"metadata","protocol_version":"1.4"}`,
		kimiUsageLine(1200, 300, 4000, 500),
		`{"type":"context.append_message"}`,
		kimiUsageLine(800, 150, 6000, 0),
		`{"type":"usage.record","usageScope":"session","usage":{"inputOther":99999}}`,
	})

	stats, ok := readKimiSessionStats(sessions, "session_usage")
	if !ok {
		t.Fatal("usage was not recovered from wire.jsonl")
	}

	want := Usage{Input: 2000, Output: 450, CacheRead: 10000, CacheCreation: 500}
	if stats.Usage != want {
		t.Errorf("Usage = %+v, want the summed turn records %+v", stats.Usage, want)
	}
	if stats.Turns != 2 {
		t.Errorf("Turns = %d, want the 2 turn-scoped records", stats.Turns)
	}
	if stats.Model != "kimi-code/kimi-for-coding" {
		t.Errorf("Model = %q, want the model the turns ran under", stats.Model)
	}

	u, turns, model, printOK := readKimiUsage(sessions, "session_usage")
	if !printOK || u != stats.Usage || turns != stats.Turns || model != stats.Model {
		t.Errorf("interactive stats %+v diverge from print mode's (%+v, %d, %q)", stats, u, turns, model)
	}

	if _, ok := readKimiSessionStats(sessions, "session_missing"); ok {
		t.Error("a session with no wire.jsonl must report its usage unrecovered")
	}
}
