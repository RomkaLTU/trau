package webserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(New("1.2.3", "127.0.0.1", "", nil, false, testStores(t)).Handler())
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

func deleteReq(t *testing.T, ts *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("new DELETE %s: %v", path, err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
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
	if h.TokenRequired {
		t.Error("token_required = true on a loopback bind, want false")
	}
}

func TestHealthReportsTokenGate(t *testing.T) {
	ts := httptest.NewServer(New("1.2.3", "10.0.0.5", "s3cret", nil, false, testStores(t)).Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+APIPrefix+"/health", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer s3cret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	var h Health
	err = json.NewDecoder(res.Body).Decode(&h)
	_ = res.Body.Close()
	if err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !h.TokenRequired {
		t.Error("token_required = false on a token-gated bind, want true")
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
	for _, route := range []string{"/some/client/route", "/settings", "/runs/deep/link"} {
		res, body := get(t, ts, route)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (SPA shell)", route, res.StatusCode)
		}
		if !strings.Contains(body, `<div id="root">`) {
			t.Errorf("%s did not fall back to the SPA shell: %q", route, body)
		}
	}
}

func TestServesServiceWorker(t *testing.T) {
	ts := newTestServer(t)

	res, body := get(t, ts, "/sw.js")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /sw.js status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if strings.Contains(body, `<div id="root">`) {
		t.Fatalf("/sw.js was swallowed by the SPA fallback: %q", body)
	}
	if !strings.Contains(body, "addEventListener('fetch'") {
		t.Errorf("/sw.js has no fetch handler: %q", body)
	}
}

func TestServesWebManifest(t *testing.T) {
	ts := newTestServer(t)

	res, body := get(t, ts, "/manifest.webmanifest")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /manifest.webmanifest status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("Content-Type = %q, want application/manifest+json", ct)
	}

	var manifest struct {
		Name     string `json:"name"`
		Display  string `json:"display"`
		StartURL string `json:"start_url"`
		Icons    []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal([]byte(body), &manifest); err != nil {
		t.Fatalf("decode manifest: %v (body %q)", err, body)
	}
	if manifest.Name != "trau" || manifest.Display != "standalone" || manifest.StartURL != "/" {
		t.Errorf("manifest = %+v, want name trau, display standalone, start_url /", manifest)
	}

	declared := map[string]bool{}
	for _, icon := range manifest.Icons {
		declared[icon.Sizes+" "+icon.Purpose] = true
		res, _ := get(t, ts, icon.Src)
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", icon.Src, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); ct != "image/png" {
			t.Errorf("%s Content-Type = %q, want image/png", icon.Src, ct)
		}
	}
	for _, want := range []string{"192x192 any", "512x512 any", "512x512 maskable"} {
		if !declared[want] {
			t.Errorf("manifest declares no %q icon, got %v", want, declared)
		}
	}
}

func TestServiceWorkerIsBuildScoped(t *testing.T) {
	ts := newTestServer(t)

	_, sw := get(t, ts, "/sw.js")
	_, shell := get(t, ts, "/assets/index.js")

	cache := regexp.MustCompile(`'trau-(g[0-9a-f]+)'`).FindStringSubmatch(sw)
	if cache == nil {
		t.Fatalf("/sw.js has no build-keyed cache name: %q", sw)
	}
	if !strings.Contains(shell, cache[1]) {
		t.Errorf("worker cache is keyed to %q, which the bundle does not stamp", cache[1])
	}
	if !strings.Contains(sw, `"/assets/index.js"`) {
		t.Error("/sw.js does not precache the app shell")
	}
	if !strings.Contains(sw, `startsWith('/api/')`) {
		t.Error("/sw.js does not exempt the API namespace")
	}
}

func TestSPAShellIsSelfContained(t *testing.T) {
	ts := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, "/assets/index.js") {
		t.Errorf("shell does not reference the embedded bundle: %q", body)
	}
	for _, ext := range []string{"http://", "https://", "//cdn", "fonts.googleapis", "fonts.gstatic"} {
		if strings.Contains(body, ext) {
			t.Errorf("shell references external resource %q, want a self-contained bundle", ext)
		}
	}
}
