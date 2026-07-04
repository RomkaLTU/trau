package webserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(New("1.2.3").Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatalf("read %s body: %v", path, err)
	}
	return res, string(body)
}

func TestHealthResource(t *testing.T) {
	ts := newTestServer(t)

	res, body := get(t, ts, APIPrefix+"/health")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var h Health
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatalf("decode health: %v (body %q)", err, body)
	}
	if h.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", h.Version)
	}
	if h.Status != "ok" {
		t.Errorf("status = %q, want ok", h.Status)
	}
	if h.UptimeSeconds < 0 {
		t.Errorf("uptime_seconds = %v, want >= 0", h.UptimeSeconds)
	}
}

func TestHealthRejectsNonGET(t *testing.T) {
	ts := newTestServer(t)
	res, err := http.Post(ts.URL+APIPrefix+"/health", "application/json", nil)
	if err != nil {
		t.Fatalf("POST health: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}

func TestUnknownAPIPathReturns404JSON(t *testing.T) {
	ts := newTestServer(t)
	res, body := get(t, ts, APIPrefix+"/does-not-exist")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if strings.Contains(body, "<div") {
		t.Errorf("unknown API path leaked the SPA shell: %q", body)
	}
}

func TestServesEmbeddedSPA(t *testing.T) {
	ts := newTestServer(t)

	res, body := get(t, ts, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(body, `<div id="root">`) {
		t.Errorf("GET / body missing SPA root element: %q", body)
	}

	res, _ = get(t, ts, "/assets/index.js")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/index.js status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("asset Content-Type = %q, want javascript", ct)
	}
}

func TestSPAFallbackServesShell(t *testing.T) {
	ts := newTestServer(t)
	res, body := get(t, ts, "/some/client/route")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unknown route status = %d, want 200 (SPA shell)", res.StatusCode)
	}
	if !strings.Contains(body, `<div id="root">`) {
		t.Errorf("unknown route did not fall back to the SPA shell: %q", body)
	}
}
