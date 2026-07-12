package webserver

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

func TestDrainOutcomeAPIRoundTrip(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)
	base := ts.URL + APIPrefix + "/repos/acme/runs/COD-1/drain-outcome"

	absent := doReq(t, http.MethodGet, base, nil)
	if absent.StatusCode != http.StatusNotFound {
		t.Fatalf("GET before any write = %d, want 404", absent.StatusCode)
	}
	_ = absent.Body.Close()

	put := doReq(t, http.MethodPut, base, drainOutcomeBody{Class: state.FailFaulted, Reason: "sub-issue faulted"})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT outcome = %d, want 200", put.StatusCode)
	}
	_ = put.Body.Close()

	get := doReq(t, http.MethodGet, base, nil)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET outcome = %d, want 200", get.StatusCode)
	}
	var body drainOutcomeBody
	if err := json.NewDecoder(get.Body).Decode(&body); err != nil {
		t.Fatalf("decode outcome: %v", err)
	}
	_ = get.Body.Close()
	if body.Class != state.FailFaulted || body.Reason != "sub-issue faulted" {
		t.Fatalf("outcome = %+v, want the faulted report", body)
	}

	// A clean finish reports an empty class but must still read as present (200).
	clean := doReq(t, http.MethodPut, base, drainOutcomeBody{})
	if clean.StatusCode != http.StatusOK {
		t.Fatalf("PUT clean outcome = %d, want 200", clean.StatusCode)
	}
	_ = clean.Body.Close()
	cleanGet := doReq(t, http.MethodGet, base, nil)
	if cleanGet.StatusCode != http.StatusOK {
		t.Fatalf("GET clean outcome = %d, want 200 — a reported clean finish must read present", cleanGet.StatusCode)
	}
	_ = cleanGet.Body.Close()

	del := doReq(t, http.MethodDelete, base, nil)
	if del.StatusCode != http.StatusOK {
		t.Fatalf("DELETE outcome = %d, want 200", del.StatusCode)
	}
	_ = del.Body.Close()

	gone := doReq(t, http.MethodGet, base, nil)
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", gone.StatusCode)
	}
	_ = gone.Body.Close()
}
