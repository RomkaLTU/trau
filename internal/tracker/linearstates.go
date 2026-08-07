package tracker

// linearBoardStates maps a Linear workflow state onto the status group trau files
// it under — the LINEAR_BOARD_STATES catalog key parsed. It keys off the state
// name because Linear has no board column: a team's board columns *are* its
// workflow states, so the name a team reads its board by is the name the mapping
// spells.
//
// It is an OVERLAY, not the exhaustive mapping AZURE_BOARD_STATES is (ADR 0038).
// A listed state takes its mapped section; an unlisted one keeps the automatic
// grouping its Linear state type gives it. Linear teams add states freely, so an
// exhaustive rule would file every state created after the mapping under Other
// until someone edited config; degrading to the type-derived section instead
// costs only the customization, never the row.
type linearBoardStates struct{ statusMapping }

// parseLinearBoardStates reads the LINEAR_BOARD_STATES spec, as in
// "Triage=backlog, Ready for QA=started".
func parseLinearBoardStates(spec string) linearBoardStates {
	return linearBoardStates{parseStatusMapping("LINEAR_BOARD_STATES", "workflow state name", spec)}
}

// group resolves the section a Linear issue files under: the override this repo
// pins for its workflow state, and the state's own type when it pins none. It
// never answers unknown — mapLinearGroup floors an unrecognized type at backlog —
// which is what makes the key safe to leave half-written.
func (m linearBoardStates) group(state, stateType string) StatusGroup {
	if group, ok := m.override(state); ok {
		return group
	}
	return mapLinearGroup(stateType)
}
