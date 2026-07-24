package pipeline

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/proofs"
)

func TestProofsEnabled(t *testing.T) {
	cases := []struct {
		name    string
		note    string
		setting string
		want    bool
	}{
		{"no browser note", "", "on", false},
		{"browser note, default", "drive the app", "", true},
		{"browser note, on", "drive the app", "on", true},
		{"browser note, off", "drive the app", "off", false},
		{"browser note, off mixed case", "drive the app", "OFF", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{VerifyProofs: tc.setting}
			if got := p.proofsEnabled(tc.note); got != tc.want {
				t.Errorf("proofsEnabled(%q) with VERIFY_PROOFS=%q = %v, want %v", tc.note, tc.setting, got, tc.want)
			}
		})
	}
}

func writeProofsDir(t *testing.T, id string, man proofs.Manifest, shots map[string][]byte) {
	t.Helper()
	dir := proofs.Dir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, err := json.Marshal(man)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for name, body := range shots {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatalf("write shot: %v", err)
		}
	}
}

func TestHarvestProofsWarnsWhenDrivenButAbsent(t *testing.T) {
	const id = "COD-1146-warn"
	var buf bytes.Buffer
	p := newTestPipeline(t, &verdictRunner{}, &fakeTracker{})
	p.Events = event.New(&buf)
	p.VerifyProofs = "on"

	proofs.Remove(id)
	if err := writeVerdictFile(verifyPath(id), verdict{Pass: true, Browser: "driven"}); err != nil {
		t.Fatalf("write verdict: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(verifyPath(id)) })

	p.harvestProofs(context.Background(), id)

	evs := kindEvents(t, &buf, event.KindVerifyNoProofs)
	if len(evs) != 1 {
		t.Fatalf("emitted %d verify_no_proofs events, want 1", len(evs))
	}
	if strField(evs[0].Fields, "ticket") != id {
		t.Errorf("event ticket = %q, want %q", strField(evs[0].Fields, "ticket"), id)
	}
}

func TestHarvestProofsSilentWhenNotDriven(t *testing.T) {
	const id = "COD-1146-quiet"
	var buf bytes.Buffer
	p := newTestPipeline(t, &verdictRunner{}, &fakeTracker{})
	p.Events = event.New(&buf)
	p.VerifyProofs = "on"

	proofs.Remove(id)
	if err := writeVerdictFile(verifyPath(id), verdict{Pass: true, Browser: "not-applicable"}); err != nil {
		t.Fatalf("write verdict: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(verifyPath(id)) })

	p.harvestProofs(context.Background(), id)

	if evs := kindEvents(t, &buf, event.KindVerifyNoProofs); len(evs) != 0 {
		t.Fatalf("emitted %d verify_no_proofs events for a backend-only slice, want 0", len(evs))
	}
}

func TestHarvestProofsUploadsAndCleansUp(t *testing.T) {
	const id = "COD-1146-upload"
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	t.Cleanup(func() { proofs.Remove(id) })
	writeProofsDir(t, id,
		proofs.Manifest{TraceDir: "/tmp/rec/xyz", Screenshots: []proofs.ManifestShot{{File: "login.png", Caption: "login"}}},
		map[string][]byte{"login.png": png},
	)

	var (
		gotTrace string
		gotShots []hubclient.ProofScreenshot
	)
	p := newTestPipeline(t, &verdictRunner{}, &fakeTracker{})
	p.VerifyProofs = "on"
	p.UploadProofs = func(_ context.Context, ticket, traceDir string, shots []hubclient.ProofScreenshot) error {
		if ticket != id {
			t.Errorf("uploaded ticket = %q, want %q", ticket, id)
		}
		gotTrace = traceDir
		gotShots = shots
		return nil
	}

	p.harvestProofs(context.Background(), id)

	if gotTrace != "/tmp/rec/xyz" {
		t.Errorf("uploaded trace_dir = %q, want /tmp/rec/xyz", gotTrace)
	}
	if len(gotShots) != 1 {
		t.Fatalf("uploaded %d screenshots, want 1", len(gotShots))
	}
	if gotShots[0].Caption != "login" || gotShots[0].Mime != "image/png" {
		t.Errorf("screenshot = %+v, want the login png", gotShots[0])
	}
	decoded, err := base64.StdEncoding.DecodeString(gotShots[0].Data)
	if err != nil || !bytes.Equal(decoded, png) {
		t.Errorf("screenshot data did not round-trip the file bytes (err=%v)", err)
	}
	if _, err := os.Stat(proofs.Dir(id)); !os.IsNotExist(err) {
		t.Errorf("harvest left the proofs dir %q behind", proofs.Dir(id))
	}
}

func TestHarvestProofsOffSkipsUpload(t *testing.T) {
	const id = "COD-1146-off"
	t.Cleanup(func() { proofs.Remove(id) })
	writeProofsDir(t, id,
		proofs.Manifest{TraceDir: "/tmp/rec/off", Screenshots: []proofs.ManifestShot{{File: "s.png", Caption: "x"}}},
		map[string][]byte{"s.png": append([]byte("\x89PNG\r\n\x1a\n"), 0)},
	)

	called := false
	p := newTestPipeline(t, &verdictRunner{}, &fakeTracker{})
	p.VerifyProofs = "off"
	p.UploadProofs = func(context.Context, string, string, []hubclient.ProofScreenshot) error {
		called = true
		return nil
	}

	p.harvestProofs(context.Background(), id)

	if called {
		t.Fatal("harvestProofs uploaded while VERIFY_PROOFS=off")
	}
}
