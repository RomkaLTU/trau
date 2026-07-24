package hubstore

import (
	"reflect"
	"testing"
)

func TestSetProviderPinsAndClears(t *testing.T) {
	store := testIssues(t)
	if _, _, err := store.Upsert("acme", "linear", []Issue{sampleIssue()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	iss, found, err := store.SetProvider("acme", "COD-1", "codex")
	if err != nil || !found {
		t.Fatalf("pin: found=%v err=%v", found, err)
	}
	if iss.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", iss.Provider)
	}
	if len(iss.Comments) != 1 {
		t.Fatalf("comments = %d, want the issue's content to survive the pin", len(iss.Comments))
	}

	iss, found, err = store.SetProvider("acme", "COD-1", "")
	if err != nil || !found {
		t.Fatalf("clear: found=%v err=%v", found, err)
	}
	if iss.Provider != "" {
		t.Fatalf("provider = %q, want it cleared back to the repo default", iss.Provider)
	}
}

func TestSetProviderPinsInternalIssues(t *testing.T) {
	store := testIssues(t)
	created, err := store.CreateInternal("acme", "LOOP", InternalDraft{Title: "Local"})
	if err != nil {
		t.Fatalf("create internal: %v", err)
	}

	iss, found, err := store.SetProvider("acme", created.Identifier, "kimi")
	if err != nil || !found {
		t.Fatalf("pin internal: found=%v err=%v", found, err)
	}
	if iss.Provider != "kimi" {
		t.Fatalf("provider = %q, want kimi on an internal issue too", iss.Provider)
	}
}

func TestSetProviderUnknownIdentifierIsNotFound(t *testing.T) {
	store := testIssues(t)
	if _, found, err := store.SetProvider("acme", "COD-404", "codex"); err != nil || found {
		t.Fatalf("pin missing issue: found=%v err=%v, want found=false", found, err)
	}
}

func TestSyncLeavesProviderPinIntact(t *testing.T) {
	store := testIssues(t)
	if _, _, err := store.Upsert("acme", "linear", []Issue{sampleIssue()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := store.SetProvider("acme", "COD-1", "codex"); err != nil {
		t.Fatalf("pin: %v", err)
	}

	updated := sampleIssue()
	updated.Title = "Renamed upstream"
	if _, _, err := store.Upsert("acme", "linear", []Issue{updated}); err != nil {
		t.Fatalf("resync: %v", err)
	}
	iss, _, err := store.Find("acme", "COD-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.Title != "Renamed upstream" {
		t.Fatalf("title = %q, want the sync to have landed", iss.Title)
	}
	if iss.Provider != "codex" {
		t.Fatalf("provider = %q, want a sync cycle to leave the pin alone", iss.Provider)
	}

	if _, _, err := store.UpdateSynced("acme", "COD-1", SyncedPatch{StatusGroup: "started"}); err != nil {
		t.Fatalf("mirror status: %v", err)
	}
	if iss, _, _ = store.Find("acme", "COD-1"); iss.Provider != "codex" {
		t.Fatalf("provider = %q, want a status mirror to leave the pin alone", iss.Provider)
	}
}

func TestProvidersReturnsOnlyPinnedIssues(t *testing.T) {
	store := testIssues(t)
	second := sampleIssue()
	second.Identifier = "COD-2"
	if _, _, err := store.Upsert("acme", "linear", []Issue{sampleIssue(), second}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := store.SetProvider("acme", "COD-2", "codex"); err != nil {
		t.Fatalf("pin: %v", err)
	}

	pins, err := store.Providers("acme")
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if want := map[string]string{"COD-2": "codex"}; !reflect.DeepEqual(pins, want) {
		t.Fatalf("pins = %v, want %v", pins, want)
	}
}

func TestProviderReadsOnePinAndIsEmptyForUnknownIssues(t *testing.T) {
	store := testIssues(t)
	if _, _, err := store.Upsert("acme", "linear", []Issue{sampleIssue()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := store.SetProvider("acme", "COD-1", "codex"); err != nil {
		t.Fatalf("pin: %v", err)
	}

	provider, err := store.Provider("acme", "COD-1")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if provider != "codex" {
		t.Fatalf("provider = %q, want codex", provider)
	}

	// COD-9 is the sample's parent, which was never stored: an inheriting caller
	// must read an absent parent as unpinned, not as an error.
	missing, err := store.Provider("acme", "COD-9")
	if err != nil {
		t.Fatalf("provider of a missing issue: %v", err)
	}
	if missing != "" {
		t.Fatalf("provider = %q, want empty for an issue that is not stored", missing)
	}
}
