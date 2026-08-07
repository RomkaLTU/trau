package tracker

// azureBoardStates maps an Azure DevOps board column onto the status group trau
// files it under — the AZURE_BOARD_STATES catalog key parsed. It keys off
// System.BoardColumn rather than System.State because a team's Kanban columns are
// the list the team reads its board by, and a longer one than the states behind
// them: two columns routinely share a state, so no state mapping can tell them
// apart (ADR 0036).
//
// When a repo sets one, the mapping is authoritative and exhaustive: a column it
// does not name groups as unknown rather than falling back to the category
// grouping ADR 0033 derives. That fallback stays for a work item with no board
// column at all, which is the case the mapping cannot answer. Linear's
// LINEAR_BOARD_STATES shares the grammar but not that rule — it overlays instead
// (ADR 0038).
type azureBoardStates struct{ statusMapping }

// parseAzureBoardStates reads the AZURE_BOARD_STATES spec, as in
// "New=backlog, Ready to Develop=unstarted, Ready to test=started, Done=done".
func parseAzureBoardStates(spec string) azureBoardStates {
	return azureBoardStates{parseStatusMapping("AZURE_BOARD_STATES", "board column", spec)}
}

// group resolves the status group a work item files under from its board column,
// falling back to its state name for an item the board places nowhere — one
// outside the team's area path, or a Task on the sprint taskboard — which keeps
// off-board work pickable instead of stranding it under Other. ok is false when
// the mapping has nothing to say, leaving the category-derived grouping in charge.
func (m azureBoardStates) group(column, state, reason string) (StatusGroup, bool) {
	if len(m.statusMapping) == 0 {
		return StatusGroupUnknown, false
	}
	if col := normalizeStatus(column); col != "" {
		group, mapped := m.statusMapping[col]
		if !mapped {
			return StatusGroupUnknown, true
		}
		return canceledByReason(group, reason), true
	}
	group, mapped := m.statusMapping[normalizeStatus(state)]
	if !mapped {
		return StatusGroupUnknown, false
	}
	return canceledByReason(group, reason), true
}

// canceledByReason applies the System.Reason override a mapped column is still
// subject to: Azure DevOps closes a discarded work item into the same column as a
// finished one, so the reason is the only discriminator (ADR 0033).
func canceledByReason(group StatusGroup, reason string) StatusGroup {
	if group == StatusGroupDone && isCanceledReason(reason) {
		return StatusGroupCanceled
	}
	return group
}
