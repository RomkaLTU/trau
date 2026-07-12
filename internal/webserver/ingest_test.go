package webserver

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

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

func TestIngestPopulatesTokensFromExistingFiles(t *testing.T) {
	f := newIngestFixture(t)

	sink := tokens.New(f.runsDir)
	sink.SetTicket("COD-1")
	sink.Append("build", tokens.Record{Input: 10, Output: 20, CostUSD: floatPtr(0.5), Provider: "claude", Model: "opus"})

	f.srv.ingestPass()

	calls, err := f.derived().TokenCalls(f.repo.Root, "COD-1")
	if err != nil {
		t.Fatalf("TokenCalls: %v", err)
	}
	if len(calls) != 1 || calls[0].Total != 30 || calls[0].Provider != "claude" {
		t.Fatalf("token calls = %+v, want one 30-total claude call", calls)
	}
}

func TestIngestStaysCurrentAsTokensAppend(t *testing.T) {
	f := newIngestFixture(t)

	sink := tokens.New(f.runsDir)
	sink.SetTicket("COD-1")
	sink.Append("build", tokens.Record{Input: 1, Output: 1, CostUSD: floatPtr(0.1)})
	f.srv.ingestPass()

	// The loop keeps appending after the first pass.
	sink.Append("verify", tokens.Record{Input: 2, Output: 2, CostUSD: floatPtr(0.2)})
	f.srv.ingestPass()

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

	sink := tokens.New(repo.RunsDir)
	sink.SetTicket("COD-1")
	sink.Append("build", tokens.Record{Input: 10, Output: 20, CostUSD: floatPtr(0.5)})

	pass := func() []hubstore.TokenRow {
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
		calls, err := stores.Derived().TokenCalls(repo.Root, "COD-1")
		if err != nil {
			t.Fatalf("TokenCalls: %v", err)
		}
		return calls
	}

	calls1 := pass()

	// Delete the database entirely (and its WAL sidecars); the token ledger lives
	// in the files and must survive.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(hubdb.Path(home) + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove db%s: %v", suffix, err)
		}
	}

	calls2 := pass()

	if !reflect.DeepEqual(calls1, calls2) {
		t.Fatalf("token calls not equivalent after rebuild:\n %+v\n %+v", calls1, calls2)
	}
	if len(calls2) != 1 {
		t.Fatalf("rebuilt content lost history: calls=%d", len(calls2))
	}
}

func floatPtr(v float64) *float64 { return &v }
