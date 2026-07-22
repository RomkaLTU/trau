package agent

import (
	"context"
	"strings"
)

// Canonical routable phases. Every per-call label maps to exactly one
// of these via RouteKey; a routing config keys overrides by these names.
const (
	PhaseBuild   = "build"
	PhaseHandoff = "handoff"
	PhaseVerify  = "verify"
	PhaseRepair  = "repair"
	PhaseBugfix  = "bugfix"
	PhaseCleanup = "cleanup"
	PhaseLintfix = "lintfix"
	PhaseCommit  = "commit"
	PhasePick    = "pick"
)

// Phases lists the routable phase keys in pipeline order (loop-level "pick" last).
var Phases = []string{PhaseBuild, PhaseHandoff, PhaseVerify, PhaseRepair, PhaseBugfix, PhaseCleanup, PhaseLintfix, PhaseCommit, PhasePick}

// RouteKey normalizes a per-call label (the tag passed to Run) to its routable
// phase. The pipeline emits dynamic labels — "verify-retry2", "repair1" — and the
// tracker emits the loop-level Linear labels; all collapse to one of [Phases].
// Anything unrecognized buckets under "pick" (the cheap MCP-only calls).
func RouteKey(label string) string {
	switch {
	case strings.HasPrefix(label, PhaseBuild):
		return PhaseBuild
	case strings.HasPrefix(label, PhaseHandoff):
		return PhaseHandoff
	case strings.HasPrefix(label, PhaseVerify):
		return PhaseVerify
	case strings.HasPrefix(label, PhaseRepair):
		return PhaseRepair
	case strings.HasPrefix(label, PhaseBugfix):
		return PhaseBugfix
	case strings.HasPrefix(label, PhaseCleanup):
		return PhaseCleanup
	case strings.HasPrefix(label, PhaseLintfix):
		return PhaseLintfix
	case strings.HasPrefix(label, PhaseCommit):
		return PhaseCommit
	default:
		return PhasePick
	}
}

// mechanicalPhasePrefixes name the pipeline phases that never read the tracker —
// cleanup, commit, repair, bugfix, and push-repair work purely from the code, the
// verdict/brief files, and the prompt. They are matched by raw-label prefix (not
// RouteKey) so push-repair, which RouteKey buckets under pick, is classified as
// mechanical without dragging the tracker-reading pick along with it.
var mechanicalPhasePrefixes = []string{PhaseCleanup, PhaseCommit, PhaseRepair, PhaseBugfix, "push-repair"}

// MechanicalPhase reports whether label names a mechanical phase — one a backend
// may launch with its tracker MCP servers stripped, since it consults only the
// code and files on disk. Build, handoff, and verify are not mechanical: they fall
// back to MCP ticket reads when REST ticket-content injection is unavailable.
func MechanicalPhase(label string) bool {
	for _, p := range mechanicalPhasePrefixes {
		if strings.HasPrefix(label, p) {
			return true
		}
	}
	return false
}

// steerablePhasePrefixes is deliberately an allow-list rather than the inverse of
// MechanicalPhase: repair and bugfix are mechanical in the MCP-stripping sense
// yet are exactly where a mid-run correction lands.
var steerablePhasePrefixes = []string{PhaseBuild, PhaseHandoff, PhaseVerify, PhaseRepair, PhaseBugfix}

// SteerablePhase reports whether label names a phase that takes operator steer
// notes. Prefix-matched on the raw label, so repair2, verify-retry1, and a verify
// panel member (verify-codex) all qualify.
func SteerablePhase(label string) bool {
	for _, p := range steerablePhasePrefixes {
		if strings.HasPrefix(label, p) {
			return true
		}
	}
	return false
}

// Router dispatches each agent call to a per-phase backend Runner, falling back
// to Default when a phase has no override. It is itself a [Runner], so the
// pipeline and tracker are unchanged — they call Run(ctx, prompt, label) exactly
// as before, and the label (the phase) selects which provider/model/effort runs.
// All per-provider divergence still lives inside the backend
// (ClaudeInteractive/Codex); routing is just dispatch on top.
type Router struct {
	Default Runner
	routes  map[string]Runner
}

// NewRouter returns a Router over def with the given per-phase overrides. routes
// is keyed by canonical phase (see [Phases]); an empty map means every phase uses
// def — identical to running def directly.
func NewRouter(def Runner, routes map[string]Runner) *Router {
	return &Router{Default: def, routes: routes}
}

// Run routes by the call's phase. A phase with no override (or a nil entry) uses
// Default, so a partial routing config still works for the unmapped phases.
func (r *Router) Run(ctx context.Context, prompt, label string) (Result, error) {
	if rr := r.routes[RouteKey(label)]; rr != nil {
		return rr.Run(ctx, prompt, label)
	}
	return r.Default.Run(ctx, prompt, label)
}

// Provider reports the default backend's provider, for callers that attribute the
// loop to a single agent. Per-call provider attribution rides on the agent_call
// event each backend emits, which is already routing-correct.
func (r *Router) Provider() string {
	if p, ok := r.Default.(interface{ Provider() string }); ok {
		return p.Provider()
	}
	return ""
}

// Route reports the provider/model/effort the given phase will run under, for
// pre-call display. It resolves the same backend Run would dispatch to (the
// per-phase override, else Default) and asks it; a backend that doesn't
// implement [PhaseRoute] yields empty strings.
func (r *Router) Route(label string) (provider, model, effort string) {
	rr := r.routes[RouteKey(label)]
	if rr == nil {
		rr = r.Default
	}
	if pr, ok := rr.(PhaseRoute); ok {
		return pr.Route(label)
	}
	return "", "", ""
}
