// Package activity names the present-tense pipeline work a Working loop session is
// doing right now — the Activity of ADR 0009. It is deliberately distinct from the
// past-tense checkpoint phase (internal/state), which records how far a ticket
// durably got, and from the agent router's call keys (internal/agent/router.go):
// ci-wait and merge have no agent call. The pipeline reports the current Activity
// in its presence heartbeat and as an activity_change event; a display groups
// Activities into Steps at the edge, never on the wire.
package activity

// Activity is one unit of present-tense pipeline work.
type Activity string

const (
	Build   Activity = "build"
	LintFix Activity = "lintfix"
	Cleanup Activity = "cleanup"
	Handoff Activity = "handoff"
	Verify  Activity = "verify"
	Repair  Activity = "repair"
	Bugfix  Activity = "bugfix"
	Commit  Activity = "commit"
	PR      Activity = "pr"
	CIWait  Activity = "ci-wait"
	Merge   Activity = "merge"
)
