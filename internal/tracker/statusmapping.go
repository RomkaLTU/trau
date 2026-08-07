package tracker

import (
	"slices"
	"strings"

	"github.com/RomkaLTU/trau/internal/logger"
)

// mappableGroups is the vocabulary a mapped status may be filed under.
// StatusGroupUnknown is not in it: on Azure DevOps that is what an unlisted
// column already resolves to and on Linear it is what an overlay never produces,
// so naming it buys nothing and reads as a typo.
var mappableGroups = []StatusGroup{
	StatusGroupBacklog,
	StatusGroupUnstarted,
	StatusGroupStarted,
	StatusGroupDone,
	StatusGroupCanceled,
}

// statusMapping is a parsed "<status name>=<group>" spec: the tracker's own name
// for a board column or a workflow state, normalized, against the group trau
// files its work under. AZURE_BOARD_STATES and LINEAR_BOARD_STATES share this
// grammar and this parse; what an *unlisted* name means is provider-specific —
// exhaustive on Azure DevOps, an overlay on Linear (ADR 0038) — so each provider
// wraps the map with its own lookup rather than sharing one.
type statusMapping map[string]StatusGroup

// parseStatusMapping reads a mapping spec: comma-separated "<name>=<group>"
// pairs, as in "New=backlog, Ready to Develop=unstarted, Done=done". Names are
// trimmed and case-folded so a spec matches the tracker's own casing loosely. A
// pair naming a group trau does not have is dropped and named in the debug log
// under key, so one typo costs that row rather than the whole mapping.
func parseStatusMapping(key, noun, spec string) statusMapping {
	out := statusMapping{}
	for _, pair := range strings.Split(spec, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		name, target, _ := strings.Cut(pair, "=")
		status := normalizeStatus(name)
		group := StatusGroup(strings.ToLower(strings.TrimSpace(target)))
		if status == "" || !slices.Contains(mappableGroups, group) {
			logger.Debugf("tracker: %s ignores %q; expected <%s>=%s",
				key, strings.TrimSpace(pair), noun, mappableGroupNames())
			continue
		}
		out[status] = group
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// key renders the mapping as one stable string, so two repos sharing a board or a
// team recognise each other's pull only when they group it the same way.
func (m statusMapping) key() string {
	pairs := make([]string, 0, len(m))
	for name, group := range m {
		pairs = append(pairs, name+"="+string(group))
	}
	slices.Sort(pairs)
	return strings.Join(pairs, ";")
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

// issueStatusOf reports the reconcile status behind a mapped group, so a
// --status reconcile and epic finalization agree with the board the mapping
// groups. Backlog and unstarted are both live, not-yet-started work.
func issueStatusOf(group StatusGroup) IssueStatus {
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
