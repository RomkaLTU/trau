package proofs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

func writeManifest(t *testing.T, ticket string, man Manifest) {
	t.Helper()
	dir := Dir(ticket)
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
}

func writeShot(t *testing.T, ticket, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(Dir(ticket), name), pngBytes, 0o644); err != nil {
		t.Fatalf("write shot: %v", err)
	}
}

func TestReadParsesManifestAndScreenshots(t *testing.T) {
	ticket := "COD-1146-" + t.Name()
	t.Cleanup(func() { Remove(ticket) })

	writeManifest(t, ticket, Manifest{
		TraceDir: "/tmp/rec/xyz",
		Screenshots: []ManifestShot{
			{File: "01-login.png", Caption: "login screen"},
			{File: "02-dashboard.png", Caption: "dashboard after login"},
		},
	})
	writeShot(t, ticket, "01-login.png")
	writeShot(t, ticket, "02-dashboard.png")

	man, shots, err := Read(ticket)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if man.TraceDir != "/tmp/rec/xyz" {
		t.Errorf("trace_dir = %q, want /tmp/rec/xyz", man.TraceDir)
	}
	if len(shots) != 2 {
		t.Fatalf("read %d screenshots, want 2", len(shots))
	}
	if shots[0].Filename != "01-login.png" || shots[0].Caption != "login screen" {
		t.Errorf("shot 0 = %+v, want the login shot in manifest order", shots[0])
	}
	if shots[0].MimeType != "image/png" {
		t.Errorf("shot 0 mime = %q, want image/png detected from bytes", shots[0].MimeType)
	}
}

func TestReadMissingManifest(t *testing.T) {
	ticket := "COD-1146-" + t.Name()
	if _, _, err := Read(ticket); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("Read err = %v, want ErrNoManifest", err)
	}
}

func TestReadCapsScreenshots(t *testing.T) {
	ticket := "COD-1146-" + t.Name()
	t.Cleanup(func() { Remove(ticket) })

	shots := make([]ManifestShot, 0, 12)
	for i := range 12 {
		name := fmt.Sprintf("shot-%02d.png", i)
		shots = append(shots, ManifestShot{File: name, Caption: name})
	}
	writeManifest(t, ticket, Manifest{Screenshots: shots})
	for i := range 12 {
		writeShot(t, ticket, fmt.Sprintf("shot-%02d.png", i))
	}

	_, got, err := Read(ticket)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != MaxScreenshots {
		t.Fatalf("read %d screenshots, want the %d cap", len(got), MaxScreenshots)
	}
	if got[0].Filename != "shot-00.png" || got[MaxScreenshots-1].Filename != "shot-07.png" {
		t.Errorf("cap kept the wrong slice: first=%q last=%q", got[0].Filename, got[MaxScreenshots-1].Filename)
	}
}

func TestReadSkipsUnsafeAndMissingFiles(t *testing.T) {
	ticket := "COD-1146-" + t.Name()
	t.Cleanup(func() { Remove(ticket) })

	writeManifest(t, ticket, Manifest{
		Screenshots: []ManifestShot{
			{File: "../escape.png", Caption: "traversal"},
			{File: "gone.png", Caption: "never written"},
			{File: "real.png", Caption: "present"},
		},
	})
	writeShot(t, ticket, "escape.png")
	writeShot(t, ticket, "real.png")

	_, got, err := Read(ticket)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d screenshots, want the traversal collapsed to base and the missing one skipped", len(got))
	}
	if got[1].Filename != "real.png" {
		t.Errorf("last shot = %q, want real.png (gone.png skipped)", got[1].Filename)
	}
}

func TestRemoveClearsDir(t *testing.T) {
	ticket := "COD-1146-" + t.Name()
	writeManifest(t, ticket, Manifest{})
	Remove(ticket)
	if _, err := os.Stat(Dir(ticket)); !os.IsNotExist(err) {
		t.Errorf("Remove left %q behind", Dir(ticket))
	}
}
