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
	Build     Activity = "build"
	LintFix   Activity = "lintfix"
	Cleanup   Activity = "cleanup"
	Handoff   Activity = "handoff"
	Verify    Activity = "verify"
	Repair    Activity = "repair"
	Bugfix    Activity = "bugfix"
	Commit    Activity = "commit"
	PR        Activity = "pr"
	CIWait    Activity = "ci-wait"
	Merge     Activity = "merge"
	MergeWait Activity = "merge-wait"
)

// Step is a display grouping of Activities: the three phases every surface shows
// (ADR 0009). Its value is the display label. The Activity→Step map lives here so
// the TUI and web read the same grouping and cannot drift from the pipeline that
// writes the Activities; it stays a display concern and never crosses the wire.
type Step string

const (
	StepBuild  Step = "Build"
	StepVerify Step = "Verify"
	StepShip   Step = "Ship"
)

// Steps is the canonical Step order.
var Steps = []Step{StepBuild, StepVerify, StepShip}

// StepOf groups an Activity into its Step. Build absorbs the concurrent build tail
// (lintfix, cleanup, handoff); Verify holds the self-heal loop; Ship covers commit
// through merge.
func StepOf(a Activity) Step {
	switch a {
	case Verify, Repair, Bugfix:
		return StepVerify
	case Commit, PR, CIWait, Merge, MergeWait:
		return StepShip
	default:
		return StepBuild
	}
}
