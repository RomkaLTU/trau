package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

func steeredRun(t *testing.T, home string, fields map[string]string) *httptest.Server {
	t.Helper()
	_, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", fields)
	_, ts := controlServer(t, home, nil)
	return ts
}

func advance(t *testing.T, ts *httptest.Server, body any) (int, map[string]any) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-1/advance", body)
	defer func() { _ = res.Body.Close() }()
	var decoded map[string]any
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode advance body: %v", err)
	}
	return res.StatusCode, decoded
}

func checkpointData(t *testing.T, ts *httptest.Server) map[string]string {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/runs/COD-1/checkpoint")
	if err != nil {
		t.Fatalf("GET checkpoint: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("checkpoint status = %d, want 200", res.StatusCode)
	}
	var view struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	return view.Data
}

func TestAdvanceRecordsInterruptedPhaseAsDone(t *testing.T) {
	home := t.TempDir()
	ts := steeredRun(t, home, map[string]string{
		"PHASE":         state.Building,
		"SESSION":       "sess-1",
		"SESSION_PHASE": "build",
		"TAKEOVER":      time.Now().UTC().Format(time.RFC3339),
		"ANOMALIES":     "takeover",
	})

	status, body := advance(t, ts, AdvanceRequest{})
	if status != http.StatusOK {
		t.Fatalf("advance status = %d, want 200: %v", status, body)
	}
	if body["from"] != state.Building || body["phase"] != state.Built {
		t.Errorf("advance body = %+v, want building → built", body)
	}

	data := checkpointData(t, ts)
	if data["PHASE"] != state.Built {
		t.Errorf("PHASE = %q, want %q so the hand-back enters the next step", data["PHASE"], state.Built)
	}
	if data["TAKEOVER"] != "" {
		t.Errorf("TAKEOVER = %q, want cleared so a later hand-back does not re-prompt", data["TAKEOVER"])
	}
	if data["ANOMALIES"] != "takeover" {
		t.Errorf("ANOMALIES = %q, want the takeover marker kept for run history", data["ANOMALIES"])
	}
}

func TestAdvanceRerunKeepsPhaseAndClearsStamp(t *testing.T) {
	home := t.TempDir()
	ts := steeredRun(t, home, map[string]string{
		"PHASE":         state.Building,
		"SESSION_PHASE": "build",
		"TAKEOVER":      time.Now().UTC().Format(time.RFC3339),
	})

	status, body := advance(t, ts, AdvanceRequest{Rerun: true})
	if status != http.StatusOK {
		t.Fatalf("re-run status = %d, want 200: %v", status, body)
	}

	data := checkpointData(t, ts)
	if data["PHASE"] != state.Building {
		t.Errorf("PHASE = %q, want %q untouched on the re-run branch", data["PHASE"], state.Building)
	}
	if data["TAKEOVER"] != "" {
		t.Errorf("TAKEOVER = %q, want cleared on the re-run branch too", data["TAKEOVER"])
	}
}

func TestAdvanceWithoutTakeoverStampConflicts(t *testing.T) {
	home := t.TempDir()
	ts := steeredRun(t, home, map[string]string{"PHASE": state.Building})

	status, body := advance(t, ts, AdvanceRequest{})
	if status != http.StatusConflict {
		t.Fatalf("advance status = %d, want 409 without a takeover stamp", status)
	}
	if body["reason"] != "no_takeover" {
		t.Errorf("reason = %v, want no_takeover", body["reason"])
	}
	if data := checkpointData(t, ts); data["PHASE"] != state.Building {
		t.Errorf("PHASE = %q, want %q untouched by a refused advance", data["PHASE"], state.Building)
	}
}

func TestAdvanceUnmappablePhaseRejected(t *testing.T) {
	home := t.TempDir()
	stamp := time.Now().UTC().Format(time.RFC3339)
	ts := steeredRun(t, home, map[string]string{
		"PHASE":         state.Verified,
		"SESSION_PHASE": "commit",
		"TAKEOVER":      stamp,
	})

	status, body := advance(t, ts, AdvanceRequest{})
	if status != http.StatusBadRequest {
		t.Fatalf("advance status = %d, want 400 for a phase with no completed value", status)
	}
	if body["reason"] != "unmappable_phase" {
		t.Errorf("reason = %v, want unmappable_phase", body["reason"])
	}
	if data := checkpointData(t, ts); data["TAKEOVER"] != stamp {
		t.Errorf("TAKEOVER = %q, want the stamp kept so the choice can still be re-run", data["TAKEOVER"])
	}

	if status, body := advance(t, ts, AdvanceRequest{Rerun: true}); status != http.StatusOK {
		t.Fatalf("re-run status = %d, want 200 — an unmappable phase still hands back: %v", status, body)
	}
}

func TestAdvanceRefusedWhileTakeoverHoldsRepo(t *testing.T) {
	home := t.TempDir()
	root, runsDir := checkpointRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{
		"PHASE":    state.Building,
		"TAKEOVER": time.Now().UTC().Format(time.RFC3339),
	})
	writeEntry(t, home, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		RunsDir:      runsDir,
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateTakeover,
		Ticket:       "COD-1",
	})
	_, ts := controlServer(t, home, nil)

	status, body := advance(t, ts, AdvanceRequest{})
	if status != http.StatusConflict {
		t.Fatalf("advance status = %d, want 409 while a terminal holds the repo", status)
	}
	if body["reason"] != "taken_over" {
		t.Errorf("reason = %v, want taken_over", body["reason"])
	}
}

func TestRunsSurfacePendingHandback(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{
		"PHASE":         state.Building,
		"SESSION_PHASE": "build",
		"TAKEOVER":      "2026-07-22T10:00:00Z",
	})
	seedCheckpoint(t, runsDir, "COD-2", map[string]string{"PHASE": state.Building})
	_, ts := controlServer(t, home, nil)

	runs := getRuns(t, ts, "acme").Runs
	byTicket := map[string]RunView{}
	for _, r := range runs {
		byTicket[r.Ticket] = r
	}
	steered := byTicket["COD-1"].Handback
	if steered == nil {
		t.Fatalf("COD-1 handback = nil, want the pending choice")
	}
	if steered.Phase != "build" || steered.Advance != state.Built {
		t.Errorf("handback = %+v, want phase build advancing to %q", steered, state.Built)
	}
	if byTicket["COD-2"].Handback != nil {
		t.Errorf("COD-2 handback = %+v, want none for a ticket no terminal steered", byTicket["COD-2"].Handback)
	}
}
