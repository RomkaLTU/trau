package hubstore

import (
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testAtlas(t *testing.T) *AtlasDocuments {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := NewAtlasDocuments(db.SQL())
	a.now = func() time.Time { return time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC) }
	return a
}

func TestAtlasVersionCounterPerView(t *testing.T) {
	a := testAtlas(t)
	for want := 1; want <= 3; want++ {
		v, err := a.Insert("repo", "data-model", "sha", `{"ok":true}`, 0, "")
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		if v != want {
			t.Fatalf("version = %d, want %d", v, want)
		}
	}
	v, err := a.Insert("repo", "app-flows", "sha", `{"ok":true}`, 0, "")
	if err != nil || v != 1 {
		t.Fatalf("app-flows first version = %d, %v; want 1", v, err)
	}
}

func TestAtlasFailedGenerationDoesNotDisplaceGood(t *testing.T) {
	a := testAtlas(t)
	if _, err := a.Insert("repo", "data-model", "abc123", `{"entities":[]}`, 0.5, ""); err != nil {
		t.Fatalf("insert good: %v", err)
	}
	if _, err := a.Insert("repo", "data-model", "def456", "", 0.2, "invalid output"); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	good, ok, err := a.Latest("repo", "data-model")
	if err != nil || !ok {
		t.Fatalf("Latest = %v, ok=%v", err, ok)
	}
	if good.Version != 1 || good.Commit != "abc123" {
		t.Fatalf("latest good = %+v, want version 1 / abc123", good)
	}

	meta, err := a.Meta("repo", "data-model")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if !meta.HasDocument || meta.Version != 1 || meta.Commit != "abc123" || meta.CostUSD != 0.5 {
		t.Fatalf("meta good fields = %+v, want version 1 / abc123 / 0.5", meta)
	}
	if meta.Error != "invalid output" {
		t.Fatalf("meta error = %q, want the latest attempt's error", meta.Error)
	}
	if meta.GeneratedAt != "2026-07-16 09:00:00" {
		t.Fatalf("meta generated-at = %q", meta.GeneratedAt)
	}
}

func TestAtlasMetaEmptyWhenNoGeneration(t *testing.T) {
	a := testAtlas(t)
	meta, err := a.Meta("repo", "data-model")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if meta.HasDocument || meta.Error != "" {
		t.Fatalf("meta = %+v, want empty", meta)
	}
}

func TestAtlasVersionHistory(t *testing.T) {
	a := testAtlas(t)
	_, _ = a.Insert("repo", "data-model", "v1", `{"n":1}`, 0, "")
	_, _ = a.Insert("repo", "data-model", "v2", `{"n":2}`, 0, "")

	doc, ok, err := a.Version("repo", "data-model", 1)
	if err != nil || !ok {
		t.Fatalf("Version(1) = %v, ok=%v", err, ok)
	}
	if doc.Commit != "v1" || doc.Document != `{"n":1}` {
		t.Fatalf("Version(1) = %+v, want v1", doc)
	}
	if _, ok, _ := a.Version("repo", "data-model", 99); ok {
		t.Fatalf("Version(99) reported present")
	}
}

func TestAtlasRetentionKeepsLastTen(t *testing.T) {
	a := testAtlas(t)
	for i := 0; i < 12; i++ {
		if _, err := a.Insert("repo", "data-model", "sha", `{"ok":true}`, 0, ""); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	if _, ok, _ := a.Version("repo", "data-model", 1); ok {
		t.Fatalf("version 1 survived retention")
	}
	if _, ok, _ := a.Version("repo", "data-model", 2); ok {
		t.Fatalf("version 2 survived retention")
	}
	if _, ok, _ := a.Version("repo", "data-model", 3); !ok {
		t.Fatalf("version 3 was pruned but should be the oldest kept")
	}
	latest, ok, _ := a.Latest("repo", "data-model")
	if !ok || latest.Version != 12 {
		t.Fatalf("latest = %+v, want version 12", latest)
	}
}
