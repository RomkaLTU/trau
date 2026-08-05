// Package proofs reads the verify agent's browser proofs — the screenshots it
// saved and the trace directory it recorded during a browser-verify run — so the
// loop can harvest them to the hub. Like attachfile, the files live in the OS
// temp directory beside the other agent-interface artifacts, never inside the
// target repository's working tree.
package proofs

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/tmpfile"
)

const dirPrefix = "trau-proofs-"

// MaxScreenshots caps how many screenshots a run harvests, matching the hub's cap.
const MaxScreenshots = 8

// ErrNoManifest reports a run that saved nothing to harvest — no usable manifest
// and no screenshot file of its own — the one case the caller may stay silent
// about, since a verify that drove no browser leaves exactly this.
var ErrNoManifest = errors.New("no proofs manifest")

// Dir is the contract directory the verify agent writes a run's proofs into.
func Dir(ticket string) string { return tmpfile.Path(dirPrefix + ticket) }

// Remove drops a ticket's harvested proofs directory.
func Remove(ticket string) { _ = os.RemoveAll(Dir(ticket)) }

// Reset clears a ticket's leftover proofs and creates the contract directory, so an
// attempt finds the path its prompt names already on disk and can never harvest an
// earlier attempt's screenshots as its own. Best-effort: a directory the loop cannot
// create is one the agent creates itself.
func Reset(ticket string) {
	Remove(ticket)
	_ = os.MkdirAll(Dir(ticket), 0o755)
}

// Manifest is the contract file the verify agent writes alongside the screenshots.
type Manifest struct {
	TraceDir    string         `json:"trace_dir"`
	Screenshots []ManifestShot `json:"screenshots"`
}

// ManifestShot names one saved screenshot file and its one-line caption.
type ManifestShot struct {
	File    string `json:"file"`
	Caption string `json:"caption"`
}

// Screenshot is a materialized proof: a saved file's bytes plus its manifest
// metadata and detected mime type.
type Screenshot struct {
	Filename string
	MimeType string
	Caption  string
	Bytes    []byte
}

// Read materializes a run's proofs: up to MaxScreenshots screenshots, in manifest
// order, plus the manifest itself for its trace directory. A screenshot the
// manifest names but cannot be read is skipped rather than failing the harvest,
// and when the manifest is unusable — absent, unreadable, malformed, or naming
// nothing readable — the directory's own image files stand in for it.
// ErrNoManifest is returned only when there is neither a usable manifest nor a
// single readable image, the one case the caller may stay silent about.
func Read(ticket string) (Manifest, []Screenshot, error) {
	dir := Dir(ticket)
	man, haveManifest := readManifest(dir)

	shots := listedScreenshots(dir, man.Screenshots)
	if len(shots) == 0 {
		shots = scannedScreenshots(dir)
	}
	if !haveManifest && len(shots) == 0 {
		return Manifest{}, nil, ErrNoManifest
	}
	return man, shots, nil
}

// readManifest parses the run's manifest.json, reporting false when there is no
// usable one. A missing, unreadable or malformed manifest — a fenced or truncated
// write is a plausible agent slip — reads as absent rather than aborting the
// harvest: the screenshots on disk are the evidence, and losing them to a bad
// sidecar is exactly the false "no proofs" this contract exists to avoid.
func readManifest(dir string) (Manifest, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, false
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return Manifest{}, false
	}
	return man, true
}

// listedScreenshots reads the files the manifest names, in manifest order.
func listedScreenshots(dir string, listed []ManifestShot) []Screenshot {
	shots := make([]Screenshot, 0, min(len(listed), MaxScreenshots))
	for _, s := range listed {
		if len(shots) >= MaxScreenshots {
			break
		}
		name := safeName(s.File)
		if name == "" {
			continue
		}
		shot, ok := readShot(dir, name)
		if !ok {
			continue
		}
		shot.Caption = strings.TrimSpace(s.Caption)
		shots = append(shots, shot)
	}
	return shots
}

// scannedScreenshots harvests a proofs directory that has no usable manifest: the
// image files sitting in it, in filename order, capped like the manifest path.
// These shots carry no caption — the hub labels them by sequence number instead.
func scannedScreenshots(dir string) []Screenshot {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	shots := make([]Screenshot, 0, min(len(entries), MaxScreenshots))
	for _, e := range entries {
		if len(shots) >= MaxScreenshots {
			break
		}
		if e.IsDir() {
			continue
		}
		shot, ok := readShot(dir, e.Name())
		if !ok || !strings.HasPrefix(shot.MimeType, "image/") {
			continue
		}
		shots = append(shots, shot)
	}
	return shots
}

// readShot materializes one saved file, reporting false for a file that cannot be
// read or is empty: a screenshot the agent named but never wrote is skipped rather
// than failing the harvest.
func readShot(dir, name string) (Screenshot, bool) {
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || len(body) == 0 {
		return Screenshot{}, false
	}
	return Screenshot{Filename: name, MimeType: detectMime(body), Bytes: body}, true
}

// safeName keeps a manifest-named file inside the proofs directory: a screenshot
// reference is never trusted to point elsewhere.
func safeName(file string) string {
	name := filepath.Base(filepath.Clean("/" + strings.TrimSpace(file)))
	if name == string(filepath.Separator) || name == "." {
		return ""
	}
	return name
}

func detectMime(body []byte) string {
	sniff, _, _ := strings.Cut(http.DetectContentType(body), ";")
	return strings.ToLower(strings.TrimSpace(sniff))
}
