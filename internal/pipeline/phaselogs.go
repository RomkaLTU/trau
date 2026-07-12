package pipeline

// PhaseLogStore is the pipeline's seam for the per-phase agent logs the TUI log
// inspector browses. A phase posts its final output; the inspector reads them
// back. The hub-backed implementation (internal/hubphaselog) drives it over HTTP
// so the child writes no run files. Nil disables persistence (the shape tests
// without a hub run in).
type PhaseLogStore interface {
	Put(id, phase, content string) error
	Remove(id string) error
}

// putPhaseLog stores a phase's log through the hub, best-effort and silent — the
// same contract the file-era transcript write carried. A run whose hub is truly
// down pauses at the next checkpoint write, so a dropped log here is re-produced
// on resume rather than lost.
func (p *Pipeline) putPhaseLog(id, phase, content string) {
	if p.PhaseLogs == nil {
		return
	}
	_ = p.PhaseLogs.Put(id, phase, content)
}

// clearPhaseLogs drops a ticket's phase logs from the hub — the fresh-build and
// reset sweep, mirroring the removal of the ticket's other durable run data.
func (p *Pipeline) clearPhaseLogs(id string) {
	if p.PhaseLogs == nil {
		return
	}
	_ = p.PhaseLogs.Remove(id)
}
