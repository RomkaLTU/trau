package tracker

// jiraBoardStates maps a Jira workflow status onto the status group trau files it
// under — the JIRA_BOARD_STATES catalog key parsed. It keys off the status name
// because that is what a Jira board's columns are built from and what the project
// reports back on every issue; the statusCategory behind it is a three-value
// vocabulary far too coarse to name a single status by.
//
// It is an OVERLAY, not the exhaustive mapping AZURE_BOARD_STATES is (ADR 0038),
// for the same reason LINEAR_BOARD_STATES is one: every Jira status already
// carries a category that groups it unambiguously, so the mapping exists only to
// disagree with that reading. A status a project adds later keeps its category's
// section rather than falling to Other.
type jiraBoardStates struct{ statusMapping }

// parseJiraBoardStates reads the JIRA_BOARD_STATES spec, as in
// "Backlog=backlog, Ready for QA=started".
func parseJiraBoardStates(spec string) jiraBoardStates {
	return jiraBoardStates{parseStatusMapping("JIRA_BOARD_STATES", "status name", spec)}
}

// group resolves the section a Jira issue files under: the override this repo
// pins for its status, and the status's own category when it pins none.
//
// An explicit mapping is authoritative, so it also displaces the resolution
// nuance mapJiraGroup applies — a team that maps its "Done" status to done means
// done even for the ticket closed as a duplicate.
func (m jiraBoardStates) group(status, category, resolution string) StatusGroup {
	if group, ok := m.override(status); ok {
		return group
	}
	return mapJiraGroup(category, resolution)
}
