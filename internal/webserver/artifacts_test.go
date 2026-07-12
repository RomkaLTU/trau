package webserver

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactAPIRoundTrip(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)
	base := ts.URL + APIPrefix + "/repos/acme/runs/COD-1/artifacts"

	put := doReq(t, http.MethodPut, base+"/handoff", artifactBody{Content: "the QA brief"})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT artifact = %d, want 200", put.StatusCode)
	}
	_ = put.Body.Close()

	get := doReq(t, http.MethodGet, base+"/handoff", nil)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET artifact = %d, want 200", get.StatusCode)
	}
	var body artifactBody
	if err := json.NewDecoder(get.Body).Decode(&body); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	_ = get.Body.Close()
	if body.Content != "the QA brief" {
		t.Fatalf("content = %q, want the QA brief", body.Content)
	}

	absent := doReq(t, http.MethodGet, base+"/rubric", nil)
	if absent.StatusCode != http.StatusNotFound {
		t.Fatalf("GET absent artifact = %d, want 404", absent.StatusCode)
	}
	_ = absent.Body.Close()

	bad := doReq(t, http.MethodGet, base+"/nonsense", nil)
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET unknown kind = %d, want 400", bad.StatusCode)
	}
	_ = bad.Body.Close()

	del := doReq(t, http.MethodDelete, base, nil)
	if del.StatusCode != http.StatusOK {
		t.Fatalf("DELETE artifacts = %d, want 200", del.StatusCode)
	}
	_ = del.Body.Close()

	gone := doReq(t, http.MethodGet, base+"/handoff", nil)
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", gone.StatusCode)
	}
	_ = gone.Body.Close()
}

func TestArtifactAPIImportsLegacyOnFirstTouch(t *testing.T) {
	home := t.TempDir()
	_, runsDir := checkpointRepo(t, home, "acme")
	dir := filepath.Join(runsDir, "COD-7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "buildnotes.md"), []byte("legacy notes"), 0o644); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}
	_, ts := controlServer(t, home, nil)

	get := doReq(t, http.MethodGet, ts.URL+APIPrefix+"/repos/acme/runs/COD-7/artifacts/buildnotes", nil)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET legacy-imported artifact = %d, want 200", get.StatusCode)
	}
	var body artifactBody
	if err := json.NewDecoder(get.Body).Decode(&body); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	_ = get.Body.Close()
	if body.Content != "legacy notes" {
		t.Fatalf("content = %q, want legacy notes", body.Content)
	}
	if _, err := os.Stat(filepath.Join(dir, "buildnotes.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy file survived first-touch import (err=%v)", err)
	}
}
