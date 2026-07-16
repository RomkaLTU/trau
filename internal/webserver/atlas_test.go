package webserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/state"
)

func mergedRun(updatedAt string) hubstore.TicketCheckpoint {
	return hubstore.TicketCheckpoint{
		CheckpointRow: hubstore.CheckpointRow{Phase: state.Merged, UpdatedAt: updatedAt},
	}
}

func TestAtlasStaleness(t *testing.T) {
	runs := []hubstore.TicketCheckpoint{
		mergedRun("2026-07-16 08:00:00"), // before → not counted
		mergedRun("2026-07-16 10:00:00"), // after → counted
		mergedRun("2026-07-16 09:00:00"), // equal → not after
		{CheckpointRow: hubstore.CheckpointRow{Phase: state.Building, UpdatedAt: "2026-07-16 11:00:00"}}, // not merged
		mergedRun("2026-07-16T12:00:00Z"), // RFC3339, after → counted
	}
	if got := atlasStaleness(runs, "2026-07-16 09:00:00"); got != 2 {
		t.Fatalf("staleness = %d, want 2", got)
	}
	if got := atlasStaleness(runs, ""); got != 0 {
		t.Fatalf("staleness with no baseline = %d, want 0", got)
	}
}

func TestAtlasAPI(t *testing.T) {
	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	stores := testStoresAt(t, home)
	doc := `{"entities":[{"id":"user","name":"User"}]}`
	if _, err := stores.Atlas().Insert(root, "data-model", "abc123", doc, 0.25, ""); err != nil {
		t.Fatalf("seed atlas doc: %v", err)
	}
	if err := stores.Checkpoints().Upsert(root, "COD-9", map[string]string{
		"PHASE": state.Merged, "UPDATED": "2099-01-01 00:00:00",
	}); err != nil {
		t.Fatalf("seed merged run: %v", err)
	}
	_, ts := controlServer(t, home, nil)
	base := ts.URL + APIPrefix + "/repos/acme/atlas"

	t.Run("catalog", func(t *testing.T) {
		res := doReq(t, http.MethodGet, base, nil)
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET catalog = %d, want 200", res.StatusCode)
		}
		var cat atlasCatalogResponse
		if err := json.NewDecoder(res.Body).Decode(&cat); err != nil {
			t.Fatalf("decode catalog: %v", err)
		}
		if len(cat.Views) != 2 {
			t.Fatalf("catalog views = %d, want 2", len(cat.Views))
		}
		views := map[string]atlasCatalogView{}
		for _, v := range cat.Views {
			views[v.ID] = v
		}
		dm := views["data-model"]
		if !dm.HasDocument || dm.Version != 1 || dm.Commit != "abc123" {
			t.Fatalf("data-model view = %+v, want version 1 / abc123", dm)
		}
		if dm.CostUSD == nil || *dm.CostUSD != 0.25 {
			t.Fatalf("data-model cost = %v, want 0.25", dm.CostUSD)
		}
		if dm.Stale != 1 {
			t.Fatalf("data-model stale = %d, want 1 (one merged run after generation)", dm.Stale)
		}
		if af := views["app-flows"]; af.HasDocument || af.Stale != 0 {
			t.Fatalf("app-flows view = %+v, want no document and no staleness", af)
		}
	})

	t.Run("document", func(t *testing.T) {
		res := doReq(t, http.MethodGet, base+"/data-model", nil)
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET document = %d, want 200", res.StatusCode)
		}
		var body atlasDocumentResponse
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("decode document: %v", err)
		}
		if body.Version != 1 || !strings.Contains(string(body.Document), `"user"`) {
			t.Fatalf("document = %+v, want version 1 carrying the user entity", body)
		}
	})

	t.Run("version history and misses", func(t *testing.T) {
		if res := doReq(t, http.MethodGet, base+"/data-model?version=1", nil); res.StatusCode != http.StatusOK {
			_ = res.Body.Close()
			t.Fatalf("GET version=1 = %d, want 200", res.StatusCode)
		} else {
			_ = res.Body.Close()
		}
		if res := doReq(t, http.MethodGet, base+"/data-model?version=9", nil); res.StatusCode != http.StatusNotFound {
			_ = res.Body.Close()
			t.Fatalf("GET version=9 = %d, want 404", res.StatusCode)
		} else {
			_ = res.Body.Close()
		}
		if res := doReq(t, http.MethodGet, base+"/app-flows", nil); res.StatusCode != http.StatusNotFound {
			_ = res.Body.Close()
			t.Fatalf("GET view with no document = %d, want 404", res.StatusCode)
		} else {
			_ = res.Body.Close()
		}
		if res := doReq(t, http.MethodGet, base+"/nonsense", nil); res.StatusCode != http.StatusNotFound {
			_ = res.Body.Close()
			t.Fatalf("GET unknown view = %d, want 404", res.StatusCode)
		} else {
			_ = res.Body.Close()
		}
	})

	t.Run("generate is not yet implemented", func(t *testing.T) {
		res := doReq(t, http.MethodPost, base+"/data-model/generate", nil)
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusNotImplemented {
			t.Fatalf("POST generate = %d, want 501", res.StatusCode)
		}
	})

	t.Run("unknown repo", func(t *testing.T) {
		res := doReq(t, http.MethodGet, ts.URL+APIPrefix+"/repos/ghost/atlas", nil)
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("GET unknown repo = %d, want 404", res.StatusCode)
		}
	})
}
