package main

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
)

func syncedRepoConfig() config.Config {
	return config.Config{
		LinearTeam:   "COD",
		LinearAPIKey: "lin_api_key",
		IssuePrefix:  "COD",
		RepoRoot:     "/src/acme",
	}
}

func TestValidateTicketIDAcceptsInternalIDInSyncedRepo(t *testing.T) {
	cfg := syncedRepoConfig()
	for _, id := range []string{"COD-11", "ACME-1", ""} {
		if err := validateTicketID(cfg, id); err != nil {
			t.Errorf("validate %q: %v", id, err)
		}
	}
	if err := validateTicketID(cfg, "TMS-4"); err == nil {
		t.Error("validate TMS-4 = nil, want a refusal — it is neither this tracker's nor an internal id")
	}
}

func TestValidateTicketIDKeepsTrackerIDSpaceWhenPrefixesCollide(t *testing.T) {
	cfg := syncedRepoConfig()
	cfg.IssuePrefixConfigured = "COD"
	if p := internalIDPrefix(cfg); p != "" {
		t.Fatalf("internal prefix = %q, want empty so an ISSUE_PREFIX equal to the team key leaves every id with the tracker", p)
	}
	if err := validateTicketID(cfg, "ACME-1"); err == nil {
		t.Error("validate ACME-1 = nil, want a refusal — internal ids are minted as COD- here")
	}
}

func TestValidateTicketIDRefusesInternalIDWithoutHubStore(t *testing.T) {
	cfg := syncedRepoConfig()
	cfg.LinearAPIKey = ""
	if err := validateTicketID(cfg, "ACME-1"); err == nil {
		t.Error("validate ACME-1 = nil, want a refusal — an MCP-path repo reads no internal issues")
	}
}
