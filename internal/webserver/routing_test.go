package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
)

func postRouting(t *testing.T, ts *httptest.Server, repo string, fp routingInput) RoutingResponse {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/routing", fp)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST routing status = %d, want 200", res.StatusCode)
	}
	var out RoutingResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode routing response: %v", err)
	}
	return out
}

func configChanges(t *testing.T, ts *httptest.Server, repo string) []FeedEvent {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/repos/"+repo+"/events")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET events status = %d, want 200", res.StatusCode)
	}
	var page EventsResponse
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode events: %v (body %q)", err, body)
	}
	out := []FeedEvent{}
	for _, ev := range page.Events {
		if ev.Kind == event.KindConfigChange {
			out = append(out, ev)
		}
	}
	return out
}

// TestRoutingEmitsOneConfigChangePerChange is the acceptance contract: a changed
// routing key produces exactly one config_change event carrying the new hash and
// the key's before/after, and re-reporting the same fingerprint produces none.
func TestRoutingEmitsOneConfigChangePerChange(t *testing.T) {
	home := t.TempDir()
	seedRepos(t, home, "acme")
	ts := ingestedServer(t, home)

	first := routingInput{Hash: "hash-1", Keys: map[string]string{"PROVIDER": "claude", "PHASE_VERIFY": "claude:opus:xhigh"}}
	if out := postRouting(t, ts, "acme", first); !out.Changed {
		t.Fatal("first fingerprint reported unchanged, want the opening cohort boundary")
	}
	if got := len(configChanges(t, ts, "acme")); got != 1 {
		t.Fatalf("config_change events after the first report = %d, want 1", got)
	}

	if out := postRouting(t, ts, "acme", first); out.Changed {
		t.Error("re-reporting the same fingerprint reported a change, want none")
	}
	if got := len(configChanges(t, ts, "acme")); got != 1 {
		t.Fatalf("config_change events after an unchanged report = %d, want 1", got)
	}

	second := routingInput{Hash: "hash-2", Keys: map[string]string{"PROVIDER": "claude", "PHASE_VERIFY": "claude:opus:high"}}
	if out := postRouting(t, ts, "acme", second); !out.Changed {
		t.Fatal("changed fingerprint reported unchanged")
	}
	events := configChanges(t, ts, "acme")
	if len(events) != 2 {
		t.Fatalf("config_change events after the change = %d, want 2", len(events))
	}

	ev := events[1]
	if ev.Fields["hash"] != "hash-2" || ev.Fields["previous_hash"] != "hash-1" {
		t.Errorf("fields = %v, want hash-2 following hash-1", ev.Fields)
	}
	changes, ok := ev.Fields["changes"].([]any)
	if !ok || len(changes) != 1 {
		t.Fatalf("changes = %v, want only the key that moved", ev.Fields["changes"])
	}
	change, _ := changes[0].(map[string]any)
	if change["key"] != "PHASE_VERIFY" || change["from"] != "claude:opus:xhigh" || change["to"] != "claude:opus:high" {
		t.Errorf("change = %v, want PHASE_VERIFY xhigh → high", change)
	}
}

// TestRoutingRejectsUnknownRepoAndMethod keeps the endpoint's guards in line with
// the other child-write resources.
func TestRoutingRejectsUnknownRepoAndMethod(t *testing.T) {
	home := t.TempDir()
	seedRepos(t, home, "acme")
	ts := ingestedServer(t, home)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/nope/routing", routingInput{Hash: "h"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo status = %d, want 404", res.StatusCode)
	}

	getRes, _ := get(t, ts, APIPrefix+"/repos/acme/routing")
	if getRes.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", getRes.StatusCode)
	}
}
