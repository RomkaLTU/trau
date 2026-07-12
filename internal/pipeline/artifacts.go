package pipeline

// Artifact kinds — the durable per-run phase artifacts the pipeline persists
// through the hub (ADR 0008). They match hubstore's kind labels on the wire.
const (
	artifactHandoff    = "handoff"
	artifactRubric     = "rubric"
	artifactVerdict    = "verdict"
	artifactBuildNotes = "buildnotes"
)

// ArtifactStore is the pipeline's seam for the durable per-run phase artifacts. A
// phase posts what it produced; a resumed run restores it. The hub-backed
// implementation (internal/hubartifact) drives it over HTTP so the child writes no
// run files. Nil disables persistence — the /tmp agent-interface files still flow,
// but nothing is mirrored durably (the shape tests without a hub run in).
type ArtifactStore interface {
	Put(id, kind, content string) error
	Get(id, kind string) (content string, ok bool, err error)
	Remove(id string) error
}

// putArtifact persists a phase artifact through the hub, best-effort and silent —
// the same contract the file-era persist functions carried. A run whose hub is
// truly down pauses at the next checkpoint write, so a dropped artifact here is
// re-produced on resume rather than lost.
func (p *Pipeline) putArtifact(id, kind, content string) {
	if p.Artifacts == nil {
		return
	}
	_ = p.Artifacts.Put(id, kind, content)
}

// getArtifact reads a phase artifact from the hub. A nil store, a store error, or
// an absent artifact all read as "none".
func (p *Pipeline) getArtifact(id, kind string) (string, bool) {
	if p.Artifacts == nil {
		return "", false
	}
	content, ok, err := p.Artifacts.Get(id, kind)
	if err != nil || !ok {
		return "", false
	}
	return content, true
}

// clearArtifacts drops a ticket's artifacts from the hub — the fresh-build and
// reset sweep, mirroring the removal of the ticket's /tmp agent-interface files.
func (p *Pipeline) clearArtifacts(id string) {
	if p.Artifacts == nil {
		return
	}
	_ = p.Artifacts.Remove(id)
}
