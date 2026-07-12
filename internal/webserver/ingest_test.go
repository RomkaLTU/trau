package webserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// ingestFixture is a server wired to a throwaway hub database with one known repo
// whose run artifacts live under a temp runs dir.
type ingestFixture struct {
	srv     *Server
	runsDir string
	repo    registry.Repo
}

func newIngestFixture(t *testing.T) ingestFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)
	stores := testStoresAt(t, home)
	if err := stores.EnsureDerivedSchema(); err != nil {
		t.Fatalf("ensure derived schema: %v", err)
	}
	root := t.TempDir()
	repo := registry.Repo{Name: "demo", Root: root, RunsDir: filepath.Join(root, "runs")}
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}
	return ingestFixture{
		srv:     New("test", "127.0.0.1", "", nil, false, stores),
		runsDir: repo.RunsDir,
		repo:    repo,
	}
}

func (f ingestFixture) derived() *hubstore.Derived { return f.srv.stores.Derived() }

func eventKinds(t *testing.T, d *hubstore.Derived, repo string) []string {
	t.Helper()
	evs, err := d.Events(repo)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	kinds := make([]string, len(evs))
	for i, e := range evs {
		kinds[i] = e.Kind
	}
	return kinds
}

func TestIngestPopulatesFromExistingFiles(t *testing.T) {
	f := newIngestFixture(t)

	appendEvent(t, f.runsDir, event.Event{Time: "t1", Kind: "phase_start", Phase: "build"})
	sink := tokens.New(f.runsDir)
	sink.SetTicket("COD-1")
	sink.Append("build", tokens.Record{Input: 10, Output: 20, CostUSD: floatPtr(0.5), Provider: "claude", Model: "opus"})

	f.srv.ingestPass()

	if got := eventKinds(t, f.derived(), f.repo.Root); !reflect.DeepEqual(got, []string{"phase_start"}) {
		t.Fatalf("event kinds = %v, want [phase_start]", got)
	}
	calls, err := f.derived().TokenCalls(f.repo.Root, "COD-1")
	if err != nil {
		t.Fatalf("TokenCalls: %v", err)
	}
	if len(calls) != 1 || calls[0].Total != 30 || calls[0].Provider != "claude" {
		t.Fatalf("token calls = %+v, want one 30-total claude call", calls)
	}
}

func TestIngestStaysCurrentAsLoopAppends(t *testing.T) {
	f := newIngestFixture(t)

	appendEvent(t, f.runsDir, event.Event{Time: "t1", Kind: "phase_start"})
	sink := tokens.New(f.runsDir)
	sink.SetTicket("COD-1")
	sink.Append("build", tokens.Record{Input: 1, Output: 1, CostUSD: floatPtr(0.1)})
	f.srv.ingestPass()

	// The loop keeps appending after the first pass.
	appendEvent(t, f.runsDir, event.Event{Time: "t2", Kind: "phase_end"})
	sink.Append("verify", tokens.Record{Input: 2, Output: 2, CostUSD: floatPtr(0.2)})
	f.srv.ingestPass()

	if got := eventKinds(t, f.derived(), f.repo.Root); !reflect.DeepEqual(got, []string{"phase_start", "phase_end"}) {
		t.Fatalf("event kinds = %v, want [phase_start phase_end]", got)
	}
	calls, err := f.derived().TokenCalls(f.repo.Root, "COD-1")
	if err != nil {
		t.Fatalf("TokenCalls: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("token calls = %d, want 2", len(calls))
	}
}

func TestIngestRebuildEquivalentAfterDatabaseDeleted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)
	root := t.TempDir()
	repo := registry.Repo{Name: "demo", Root: root, RunsDir: filepath.Join(root, "runs")}

	appendEvent(t, repo.RunsDir, event.Event{Time: "t1", Kind: "phase_start", Phase: "build"})
	appendEvent(t, repo.RunsDir, event.Event{Time: "t2", Kind: "phase_end", Phase: "build"})
	sink := tokens.New(repo.RunsDir)
	sink.SetTicket("COD-1")
	sink.Append("build", tokens.Record{Input: 10, Output: 20, CostUSD: floatPtr(0.5)})

	pass := func() ([]hubstore.EventRow, []hubstore.TokenRow) {
		t.Helper()
		db, err := hubdb.Open(home)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer func() { _ = db.Close() }()
		stores := hubstore.NewStores(db.SQL())
		if err := stores.EnsureDerivedSchema(); err != nil {
			t.Fatalf("ensure derived: %v", err)
		}
		// known_repos is authoritative and is lost with the database; a live loop
		// or re-registration makes the repo known again on restart.
		if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
			t.Fatalf("remember: %v", err)
		}
		srv := New("test", "127.0.0.1", "", nil, false, stores)
		srv.ingestPass()
		evs, err := stores.Derived().Events(repo.Root)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		calls, err := stores.Derived().TokenCalls(repo.Root, "COD-1")
		if err != nil {
			t.Fatalf("TokenCalls: %v", err)
		}
		return evs, calls
	}

	evs1, calls1 := pass()

	// Delete the database entirely (and its WAL sidecars); run history lives in
	// the files and must survive.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(hubdb.Path(home) + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove db%s: %v", suffix, err)
		}
	}

	evs2, calls2 := pass()

	if !reflect.DeepEqual(evs1, evs2) {
		t.Fatalf("events not equivalent after rebuild:\n %+v\n %+v", evs1, evs2)
	}
	if !reflect.DeepEqual(calls1, calls2) {
		t.Fatalf("token calls not equivalent after rebuild:\n %+v\n %+v", calls1, calls2)
	}
	if len(evs2) != 2 || len(calls2) != 1 {
		t.Fatalf("rebuilt content lost history: events=%d calls=%d", len(evs2), len(calls2))
	}
}

func TestIngestToleratesTornAndMalformedLines(t *testing.T) {
	f := newIngestFixture(t)
	path := filepath.Join(f.runsDir, "events.jsonl")
	if err := os.MkdirAll(f.runsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// A complete line, a malformed line, then a half-written trailing line.
	complete, _ := json.Marshal(event.Event{Time: "t1", Kind: "phase_start"})
	partial, _ := json.Marshal(event.Event{Time: "t2", Kind: "phase_end"})
	content := append(complete, '\n')
	content = append(content, []byte("{not json}\n")...)
	content = append(content, partial...) // no trailing newline
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	f.srv.ingestPass()
	if got := eventKinds(t, f.derived(), f.repo.Root); !reflect.DeepEqual(got, []string{"phase_start"}) {
		t.Fatalf("kinds after torn/malformed = %v, want [phase_start]", got)
	}

	// Completing the torn line makes it ingestable on the next pass.
	f2, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_, _ = f2.WriteString("\n")
	_ = f2.Close()

	f.srv.ingestPass()
	if got := eventKinds(t, f.derived(), f.repo.Root); !reflect.DeepEqual(got, []string{"phase_start", "phase_end"}) {
		t.Fatalf("kinds after completing torn line = %v, want [phase_start phase_end]", got)
	}
}

func TestIngestResyncsRewrittenFile(t *testing.T) {
	f := newIngestFixture(t)

	appendEvent(t, f.runsDir, event.Event{Time: "t1", Kind: "a"})
	appendEvent(t, f.runsDir, event.Event{Time: "t2", Kind: "b"})
	appendEvent(t, f.runsDir, event.Event{Time: "t3", Kind: "c"})
	f.srv.ingestPass()
	if got := eventKinds(t, f.derived(), f.repo.Root); len(got) != 3 {
		t.Fatalf("kinds before rewrite = %v, want 3", got)
	}

	// A shorter rewrite (log rotation / fresh run) falls below the persisted
	// cursor; ingestion resyncs from the start instead of crashing.
	replacement, _ := json.Marshal(event.Event{Time: "t9", Kind: "z"})
	if err := os.WriteFile(filepath.Join(f.runsDir, "events.jsonl"), append(replacement, '\n'), 0o644); err != nil {
		t.Fatalf("rewrite events: %v", err)
	}
	f.srv.ingestPass()
	if got := eventKinds(t, f.derived(), f.repo.Root); !reflect.DeepEqual(got, []string{"z"}) {
		t.Fatalf("kinds after rewrite = %v, want [z]", got)
	}
}

func floatPtr(v float64) *float64 { return &v }
