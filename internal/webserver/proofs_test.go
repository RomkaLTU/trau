package webserver

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func proofsServer(t *testing.T) (string, *httptest.Server) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "acme")
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStores(t))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return root, ts
}

var proofPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)

func TestRunProofsRoundTrip(t *testing.T) {
	_, ts := proofsServer(t)
	base := ts.URL + APIPrefix + "/repos/acme/runs/COD-1/proofs"

	body := proofUploadRequest{
		TraceDir: "/tmp/rec/abc",
		Screenshots: []proofScreenshotInput{
			{Filename: "login.png", Mime: "image/png", Caption: "login", Data: base64.StdEncoding.EncodeToString(proofPNG)},
		},
	}
	res := postJSON(t, base, body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201", res.StatusCode)
	}
	_ = res.Body.Close()

	listRes, listBody := get(t, ts, APIPrefix+"/repos/acme/runs/COD-1/proofs")
	if listRes.StatusCode != http.StatusOK {
		t.Fatalf("GET list status = %d, want 200", listRes.StatusCode)
	}
	var views []ProofView
	if err := json.Unmarshal([]byte(listBody), &views); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("list returned %d proofs, want a video row plus the screenshot", len(views))
	}
	if views[0].Kind != "video" || views[0].TraceDir != "/tmp/rec/abc" {
		t.Errorf("proof 0 = %+v, want the trace video row", views[0])
	}
	shot := views[1]
	if shot.Kind != "screenshot" || !shot.IsImage || shot.URL == "" {
		t.Fatalf("proof 1 = %+v, want an inline screenshot carrying a byte URL", shot)
	}

	byteRes, byteBody := get(t, ts, shot.URL)
	if byteRes.StatusCode != http.StatusOK {
		t.Fatalf("GET bytes status = %d, want 200", byteRes.StatusCode)
	}
	if byteBody != string(proofPNG) {
		t.Errorf("served bytes did not round-trip the uploaded screenshot")
	}
	if ct := byteRes.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if cc := byteRes.Header.Get("Cache-Control"); cc == "" {
		t.Errorf("expected an immutable Cache-Control header")
	}
}

func TestRunProofsReplaceOnRetry(t *testing.T) {
	_, ts := proofsServer(t)
	base := ts.URL + APIPrefix + "/repos/acme/runs/COD-2/proofs"

	first := postJSON(t, base, proofUploadRequest{
		Screenshots: []proofScreenshotInput{
			{Filename: "a.png", Mime: "image/png", Data: base64.StdEncoding.EncodeToString(proofPNG)},
			{Filename: "b.png", Mime: "image/png", Data: base64.StdEncoding.EncodeToString(proofPNG)},
		},
	})
	_ = first.Body.Close()

	retryPNG := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 48)...)
	second := postJSON(t, base, proofUploadRequest{
		Screenshots: []proofScreenshotInput{
			{Filename: "retry.png", Mime: "image/png", Caption: "retry", Data: base64.StdEncoding.EncodeToString(retryPNG)},
		},
	})
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("retry POST status = %d, want 201", second.StatusCode)
	}
	_ = second.Body.Close()

	_, listBody := get(t, ts, APIPrefix+"/repos/acme/runs/COD-2/proofs")
	var views []ProofView
	if err := json.Unmarshal([]byte(listBody), &views); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("list returned %d proofs, want the retry to have replaced the prior two", len(views))
	}
	if views[0].Caption != "retry" {
		t.Errorf("remaining proof = %+v, want only the retry screenshot", views[0])
	}
}

func TestRunProofsRejectsOversizedScreenshot(t *testing.T) {
	_, ts := proofsServer(t)
	base := ts.URL + APIPrefix + "/repos/acme/runs/COD-4/proofs"

	oversized := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, proofUploadMaxBytes+1)...)
	res := postJSON(t, base, proofUploadRequest{
		Screenshots: []proofScreenshotInput{
			{Filename: "huge.png", Mime: "image/png", Data: base64.StdEncoding.EncodeToString(oversized)},
		},
	})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized screenshot POST = %d, want 413", res.StatusCode)
	}

	_, listBody := get(t, ts, APIPrefix+"/repos/acme/runs/COD-4/proofs")
	var views []ProofView
	if err := json.Unmarshal([]byte(listBody), &views); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("stored %d proofs, want the rejected upload to persist nothing", len(views))
	}
}

func TestRunProofsCapsAtEight(t *testing.T) {
	_, ts := proofsServer(t)
	base := ts.URL + APIPrefix + "/repos/acme/runs/COD-3/proofs"

	shots := make([]proofScreenshotInput, 0, 10)
	for i := range 10 {
		payload := append([]byte("\x89PNG\r\n\x1a\n"), byte(i))
		shots = append(shots, proofScreenshotInput{
			Filename: "s.png",
			Mime:     "image/png",
			Data:     base64.StdEncoding.EncodeToString(payload),
		})
	}
	res := postJSON(t, base, proofUploadRequest{Screenshots: shots})
	_ = res.Body.Close()

	_, listBody := get(t, ts, APIPrefix+"/repos/acme/runs/COD-3/proofs")
	var views []ProofView
	if err := json.Unmarshal([]byte(listBody), &views); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(views) != proofsMaxScreenshots {
		t.Fatalf("stored %d proofs, want the %d screenshot cap", len(views), proofsMaxScreenshots)
	}
}
