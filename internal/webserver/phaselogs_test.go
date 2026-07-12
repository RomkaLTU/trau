package webserver

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPhaseLogAPIRoundTrip(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)
	base := ts.URL + APIPrefix + "/repos/acme/runs/COD-1/logs"

	put := doReq(t, http.MethodPut, base+"/build", phaseLogBody{Content: "build output"})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT build log = %d, want 200", put.StatusCode)
	}
	_ = put.Body.Close()
	putVerify := doReq(t, http.MethodPut, base+"/verify", phaseLogBody{Content: "verify output"})
	if putVerify.StatusCode != http.StatusOK {
		t.Fatalf("PUT verify log = %d, want 200", putVerify.StatusCode)
	}
	_ = putVerify.Body.Close()

	get := doReq(t, http.MethodGet, base, nil)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET logs = %d, want 200", get.StatusCode)
	}
	var listed phaseLogsResponse
	if err := json.NewDecoder(get.Body).Decode(&listed); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	_ = get.Body.Close()
	if len(listed.Logs) != 2 || listed.Logs[0].Phase != "verify" || listed.Logs[0].Content != "verify output" {
		t.Fatalf("logs = %+v, want verify newest-first", listed.Logs)
	}

	del := doReq(t, http.MethodDelete, base, nil)
	if del.StatusCode != http.StatusOK {
		t.Fatalf("DELETE logs = %d, want 200", del.StatusCode)
	}
	_ = del.Body.Close()

	gone := doReq(t, http.MethodGet, base, nil)
	if gone.StatusCode != http.StatusOK {
		t.Fatalf("GET after delete = %d, want 200", gone.StatusCode)
	}
	var empty phaseLogsResponse
	if err := json.NewDecoder(gone.Body).Decode(&empty); err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	_ = gone.Body.Close()
	if len(empty.Logs) != 0 {
		t.Fatalf("logs after delete = %+v, want none", empty.Logs)
	}
}

func TestPhaseLogAPIImportsLegacyOnFirstTouch(t *testing.T) {
	home := t.TempDir()
	_, runsDir := checkpointRepo(t, home, "acme")
	dir := filepath.Join(runsDir, "COD-7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build.log"), []byte("legacy build"), 0o644); err != nil {
		t.Fatalf("seed legacy log: %v", err)
	}
	_, ts := controlServer(t, home, nil)

	get := doReq(t, http.MethodGet, ts.URL+APIPrefix+"/repos/acme/runs/COD-7/logs", nil)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET legacy-imported logs = %d, want 200", get.StatusCode)
	}
	var listed phaseLogsResponse
	if err := json.NewDecoder(get.Body).Decode(&listed); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	_ = get.Body.Close()
	if len(listed.Logs) != 1 || listed.Logs[0].Content != "legacy build" {
		t.Fatalf("imported logs = %+v, want the single build log", listed.Logs)
	}
	if _, err := os.Stat(filepath.Join(dir, "build.log")); !os.IsNotExist(err) {
		t.Fatalf("legacy file survived first-touch import (err=%v)", err)
	}
}
