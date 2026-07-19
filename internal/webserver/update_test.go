package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpdateEndpointReportsRunningVersion(t *testing.T) {
	ts := newTestServer(t)

	res, body := get(t, ts, APIPrefix+"/update")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /update = %d, want 200 (%s)", res.StatusCode, body)
	}

	var got struct {
		Running       string `json:"running"`
		ChecksEnabled bool   `json:"checksEnabled"`
		InstallMethod string `json:"installMethod"`
		ApplyState    struct {
			State string `json:"state"`
		} `json:"applyState"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if got.Running != "1.2.3" {
		t.Errorf("running = %q, want the hub's own version", got.Running)
	}
	if !got.ChecksEnabled {
		t.Error("checksEnabled = false, want true by default")
	}
	if got.InstallMethod == "" {
		t.Error("installMethod is empty, want brew or other")
	}
	if got.ApplyState.State != "idle" {
		t.Errorf("applyState.state = %q, want idle before any apply", got.ApplyState.State)
	}
}

func TestUpdateEndpointRejectsPost(t *testing.T) {
	ts := newTestServer(t)

	res, err := http.Post(ts.URL+APIPrefix+"/update", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /update: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /update = %d, want 405", res.StatusCode)
	}
}

// TestUpdateCheckWithChecksDisabled covers the UPDATE_CHECK=0 endpoint contract:
// the payload comes back reporting checks off, and nothing is fetched.
func TestUpdateCheckWithChecksDisabled(t *testing.T) {
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStores(t))
	s.SetUpdateChecks(false)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Post(ts.URL+APIPrefix+"/update/check", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /update/check: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /update/check = %d, want 200", res.StatusCode)
	}

	var got struct {
		ChecksEnabled bool   `json:"checksEnabled"`
		Latest        string `json:"latest"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ChecksEnabled {
		t.Error("checksEnabled = true after SetUpdateChecks(false)")
	}
	if got.Latest != "" {
		t.Errorf("latest = %q, want empty with checks disabled", got.Latest)
	}
}

// TestUpdateApplyRejectsGet is the only apply-endpoint test that runs here: a
// POST would shell out to a real `brew upgrade` on a brew-managed machine. The
// gate and the state machine are covered in internal/update, where brew is
// stubbed.
func TestUpdateApplyRejectsGet(t *testing.T) {
	ts := newTestServer(t)

	res, _ := get(t, ts, APIPrefix+"/update/apply")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /update/apply = %d, want 405", res.StatusCode)
	}
}

func TestUpdateCheckRejectsGet(t *testing.T) {
	ts := newTestServer(t)

	res, _ := get(t, ts, APIPrefix+"/update/check")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /update/check = %d, want 405", res.StatusCode)
	}
}
