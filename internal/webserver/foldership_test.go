package webserver

import (
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/state"
)

// TestFolderRunLedgerRowCarriesEveryShippedChild is what makes a fan-out legible on
// the board: pr and pr_url name only the first target, so the plural set rides its
// own field, one entry per changed Child repo with the PR its branch carries.
func TestFolderRunLedgerRowCarriesEveryShippedChild(t *testing.T) {
	run := runViewFromCheckpoint(hubstore.TicketCheckpoint{
		Ticket: "COD-5",
		CheckpointRow: hubstore.CheckpointRow{
			Phase:  state.PROpen,
			Branch: "feature/COD-5-x",
			PR:     "3",
			PRURL:  "https://github.com/acme/api-companies/pull/3",
			Data:   `{"SHIP_TARGETS":"api-companies,api-billing","PR_URLS":"api-companies=https://github.com/acme/api-companies/pull/3,api-billing=https://github.com/acme/api-billing/pull/8"}`,
		},
	})

	want := []RunShip{
		{Repo: "api-companies", PR: "3", URL: "https://github.com/acme/api-companies/pull/3"},
		{Repo: "api-billing", PR: "8", URL: "https://github.com/acme/api-billing/pull/8"},
	}
	if len(run.Ships) != len(want) {
		t.Fatalf("ships = %+v, want one entry per changed child %+v", run.Ships, want)
	}
	for i, ship := range run.Ships {
		if ship != want[i] {
			t.Errorf("ships[%d] = %+v, want %+v", i, ship, want[i])
		}
	}
}

// TestSettleEvidenceWaitsForEveryChildPR is the sweep's half-shipped guard: a
// folder run's PRs live one per Child repo, so the forge is asked inside each of
// them — a folder root is no git repository — and a single merged sibling must not
// settle the item over the rest.
func TestSettleEvidenceWaitsForEveryChildPR(t *testing.T) {
	merged := map[string]bool{"api-companies": true, "api-billing": false}
	asked := map[string]string{}
	s, _, root := drainServer(t, "acme")
	s.drain.prState = func(dir, pr string) string {
		asked[filepath.Base(dir)] = pr
		if merged[filepath.Base(dir)] {
			return "MERGED"
		}
		return "OPEN"
	}
	if err := s.stores.Checkpoints().Upsert(root, "COD-6", map[string]string{
		"PHASE":        state.PROpen,
		"PR":           "3",
		"SHIP_TARGETS": "api-companies,api-billing",
		"PR_URLS":      "api-companies=https://github.com/acme/api-companies/pull/3,api-billing=https://github.com/acme/api-billing/pull/8",
	}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	item := queue.Item{Kind: queue.KindTicket, ID: "COD-6", Status: queue.StatusPaused, Reason: "outcome unknown"}

	if got := s.drain.settleEvidence(root, item); got != "" {
		t.Fatalf("evidence = %q, want none while api-billing's PR is still open", got)
	}
	if asked["api-billing"] != "8" {
		t.Errorf("api-billing was asked about PR %q, want 8 — each child answers for its own", asked["api-billing"])
	}

	merged["api-billing"] = true
	if got := s.drain.settleEvidence(root, item); got != evidencePR {
		t.Errorf("evidence = %q, want %q once every child's PR merged", got, evidencePR)
	}
}
