package webserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"testing"
)

// cacheNamePattern reads the stamp back out of the built service worker, the one
// place in dist where it appears in a fixed shape.
var cacheNamePattern = regexp.MustCompile(`CACHE_NAME = 'trau-([^']+)'`)

func servedStamp(t *testing.T) string {
	t.Helper()
	sw, err := distFS.ReadFile("dist/sw.js")
	if err != nil {
		t.Fatalf("read embedded sw.js: %v", err)
	}
	m := cacheNamePattern.FindSubmatch(sw)
	if m == nil {
		t.Fatal("no CACHE_NAME in the embedded sw.js; the build stamp cannot be checked")
	}
	return string(m[1])
}

// TestHealthCarriesTheServedWebBuild pins the handshake a stale page heals
// itself with: the stamp the hub answers must be the one baked into the bundle
// it serves, or a page comparing the two would reload forever or never.
func TestHealthCarriesTheServedWebBuild(t *testing.T) {
	ts := newTestServer(t)

	_, body := get(t, ts, APIPrefix+"/health")
	var h Health
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatalf("decode health: %v (body %q)", err, body)
	}

	want := servedStamp(t)
	if h.WebBuild != want {
		t.Fatalf("webBuild = %q, want %q — the stamp the served bundle carries", h.WebBuild, want)
	}
}

// TestWebBuildMatchesTheBundleDefine covers the other half of the same claim:
// the emitted stamp file is only trustworthy while the bundle carries the same
// value.
func TestWebBuildMatchesTheBundleDefine(t *testing.T) {
	stamp := webBuild()
	if stamp == "" {
		t.Fatal("webBuild is empty; dist/web-build was not emitted by the web build")
	}
	index, err := distFS.ReadFile("dist/assets/index.js")
	if err != nil {
		t.Fatalf("read embedded index.js: %v", err)
	}
	if !bytes.Contains(index, []byte(stamp)) {
		t.Fatalf("the served bundle does not carry the stamp %q the hub answers", stamp)
	}
}

// TestWebBuildStampIsServable keeps the stamp reachable as a file too.
func TestWebBuildStampIsServable(t *testing.T) {
	ts := newTestServer(t)

	res, body := get(t, ts, "/web-build")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := string(bytes.TrimSpace([]byte(body))); got != webBuild() {
		t.Fatalf("served stamp = %q, want %q", got, webBuild())
	}
}
