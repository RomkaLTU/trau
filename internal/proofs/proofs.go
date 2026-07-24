// Package proofs reads the verify agent's browser proofs — the screenshots it
// saved and the trace directory it recorded under /tmp during a browser-verify
// run — so the loop can harvest them to the hub. Like attachfile, the files live
// under /tmp beside the other agent-interface artifacts, never inside the target
// repository's working tree.
package proofs

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const dirPrefix = "/tmp/trau-proofs-"

// MaxScreenshots caps how many screenshots a run harvests, matching the hub's cap.
const MaxScreenshots = 8

// ErrNoManifest reports a run that wrote no proofs manifest — the verify agent
// never followed the proofs contract (typically because it drove no browser). It
// is distinct from a real read error so the caller can stay silent.
var ErrNoManifest = errors.New("no proofs manifest")

// Dir is the contract directory the verify agent writes a run's proofs into.
func Dir(ticket string) string { return dirPrefix + ticket }

// Remove drops a ticket's harvested proofs directory.
func Remove(ticket string) { _ = os.RemoveAll(Dir(ticket)) }

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

// Read parses a run's manifest and reads up to MaxScreenshots screenshots in
// manifest order. A missing manifest returns ErrNoManifest. A screenshot the
// manifest names but cannot be read is skipped rather than failing the harvest.
func Read(ticket string) (Manifest, []Screenshot, error) {
	dir := Dir(ticket)
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, nil, ErrNoManifest
	}
	if err != nil {
		return Manifest{}, nil, err
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return Manifest{}, nil, err
	}
	shots := make([]Screenshot, 0, len(man.Screenshots))
	for _, s := range man.Screenshots {
		if len(shots) >= MaxScreenshots {
			break
		}
		name := safeName(s.File)
		if name == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(body) == 0 {
			continue
		}
		shots = append(shots, Screenshot{
			Filename: name,
			MimeType: detectMime(body),
			Caption:  strings.TrimSpace(s.Caption),
			Bytes:    body,
		})
	}
	return man, shots, nil
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
