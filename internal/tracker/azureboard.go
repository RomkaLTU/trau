package tracker

import (
	"slices"
	"strings"

	"github.com/RomkaLTU/trau/internal/logger"
)

// mappableGroups is the vocabulary a board column may be mapped to.
// StatusGroupUnknown is not in it: that is what an unlisted column already resolves
// to, so naming it buys nothing and reads as a typo.
var mappableGroups = []StatusGroup{
	StatusGroupBacklog,
	StatusGroupUnstarted,
	StatusGroupStarted,
	StatusGroupDone,
	StatusGroupCanceled,
}

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
// column at all, which is the case the mapping cannot answer.
type azureBoardStates map[string]StatusGroup

// parseAzureBoardStates reads the AZURE_BOARD_STATES spec: comma-separated
// "<board column>=<status group>" pairs, as in
// "New=backlog, Ready to Develop=unstarted, Ready to test=started, Done=done".
// A pair naming a group trau does not have is dropped and named in the debug log,
// so one typo costs that column rather than the whole mapping.
func parseAzureBoardStates(spec string) azureBoardStates {
	out := azureBoardStates{}
	for _, pair := range strings.Split(spec, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		name, target, _ := strings.Cut(pair, "=")
		column := normalizeStatus(name)
		group := StatusGroup(strings.ToLower(strings.TrimSpace(target)))
		if column == "" || !slices.Contains(mappableGroups, group) {
			logger.Debugf("azure: AZURE_BOARD_STATES ignores %q; expected <board column>=%s",
				strings.TrimSpace(pair), mappableGroupNames())
			continue
		}
		out[column] = group
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// group resolves the status group a work item files under from its board column,
// falling back to its state name for an item the board places nowhere — one
// outside the team's area path, or a Task on the sprint taskboard — which keeps
// off-board work pickable instead of stranding it under Other. ok is false when
// the mapping has nothing to say, leaving the category-derived grouping in charge.
func (m azureBoardStates) group(column, state, reason string) (StatusGroup, bool) {
	if len(m) == 0 {
		return StatusGroupUnknown, false
	}
	if col := normalizeStatus(column); col != "" {
		group, mapped := m[col]
		if !mapped {
			return StatusGroupUnknown, true
		}
		return canceledByReason(group, reason), true
	}
	group, mapped := m[normalizeStatus(state)]
	if !mapped {
		return StatusGroupUnknown, false
	}
	return canceledByReason(group, reason), true
}

// key renders the mapping as one stable string, so two repos sharing a board pull
// recognise each other only when they group its columns the same way.
func (m azureBoardStates) key() string {
	pairs := make([]string, 0, len(m))
	for column, group := range m {
		pairs = append(pairs, column+"="+string(group))
	}
	slices.Sort(pairs)
	return strings.Join(pairs, ";")
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

// mappableGroupNames renders the vocabulary the way the log message naming a
// dropped pair spells it out.
func mappableGroupNames() string {
	names := make([]string, len(mappableGroups))
	for i, group := range mappableGroups {
		names[i] = string(group)
	}
	return strings.Join(names, "|")
}

// azureIssueStatus reports the reconcile status behind a board group, so a
// --status reconcile and epic finalization agree with the board the mapping
// groups. Backlog and unstarted are both live, not-yet-started work.
func azureIssueStatus(group StatusGroup) IssueStatus {
	switch group {
	case StatusGroupBacklog, StatusGroupUnstarted:
		return StatusOpen
	case StatusGroupStarted:
		return StatusStarted
	case StatusGroupDone:
		return StatusDone
	case StatusGroupCanceled:
		return StatusCanceled
	default:
		return StatusUnknown
	}
}
