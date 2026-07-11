package agent

import (
	"sort"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
)

// BackendParams carries everything a backend needs to construct itself, mapped
// from configuration at the composition root (cmd/trau). It is a superset across
// providers; a backend reads only the fields it uses. Provider-specific knobs that
// are not shared — the codex profile, claude's result dir and disallowed tools —
// ride in Extra so the struct need not widen each time a provider is added.
type BackendParams struct {
	Bin      string
	Flags    []string
	Model    string
	Effort   string
	Dir      string
	Preamble string
	// PlanPreamble, when set, replaces Preamble for the plan phase only (calls
	// whose label routes to PhasePlan). Empty keeps every phase on Preamble.
	PlanPreamble string
	Timeout      time.Duration
	// StripMechanicalMCP launches mechanical phases (see agent.MechanicalPhase)
	// with the target repo's MCP servers stripped, where the provider CLI supports
	// it (Claude's --strict-mcp-config). Providers without an equivalent ignore it.
	StripMechanicalMCP bool
	Cols               int
	Rows               int
	SizeFn             func() (cols, rows int)
	// StallWindow kills+fails a call that produces no transcript output for this
	// long, before Timeout. Zero disables the watchdog (wait the full Timeout).
	StallWindow time.Duration
	Log         *event.Log
	Tokens      TokenSink
	Extra       map[string]string
}

// Spec is one provider's contract: identity metadata plus a constructor. Adding a
// provider is one Spec plus one Runner implementation — no phase logic and no
// composition-root branch keys off the provider name.
type Spec struct {
	Name string

	KeyPrefix string

	// NeedsSkills is true when the provider's phase prompts assume that
	// skills/slash-commands are available in the target repo. When true and no
	// skills are found, trau warns the user at onboarding and at loop start.
	NeedsSkills bool

	// ReportsSkills is true when the provider's Result carries the skills a
	// session actually loaded, so the pipeline can warn when a build in a
	// skill-equipped repo used none.
	ReportsSkills bool

	New func(BackendParams) (Runner, error)
}

// Registry is an immutable lookup of provider Specs keyed by Name.
type Registry struct{ specs map[string]Spec }

// NewRegistry builds a Registry from specs. A later spec with the same Name wins,
// so a caller can layer a custom provider over the built-ins.
func NewRegistry(specs ...Spec) Registry {
	m := make(map[string]Spec, len(specs))
	for _, s := range specs {
		m[s.Name] = s
	}
	return Registry{specs: m}
}

// Lookup returns the spec for name and whether it is registered.
func (r Registry) Lookup(name string) (Spec, bool) {
	s, ok := r.specs[name]
	return s, ok
}

// Names returns the registered provider names sorted, for stable error messages.
func (r Registry) Names() []string {
	out := make([]string, 0, len(r.specs))
	for n := range r.specs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// DefaultRegistry returns the built-in providers. Construction is explicit — no
// package-level mutable state and no init() side effects — so it stays
// deterministic and testable.
func DefaultRegistry() Registry {
	return NewRegistry(claudeSpec, codexSpec, kimiSpec)
}

var claudeSpec = Spec{
	Name:          "claude",
	KeyPrefix:     "CLAUDE",
	ReportsSkills: true,
	New: func(p BackendParams) (Runner, error) {
		return &ClaudeInteractive{
			Bin:                p.Bin,
			Flags:              p.Flags,
			Model:              p.Model,
			Effort:             p.Effort,
			DisallowedTools:    p.Extra["disallowed_tools"],
			StripMechanicalMCP: p.StripMechanicalMCP,
			Preamble:           p.Preamble,
			PlanPreamble:       p.PlanPreamble,
			ResultDir:          p.Extra["result_dir"],
			Dir:                p.Dir,
			Cols:               p.Cols,
			Rows:               p.Rows,
			SizeFn:             p.SizeFn,
			Timeout:            p.Timeout,
			StallWindow:        p.StallWindow,
			Log:                p.Log,
			Tokens:             p.Tokens,
		}, nil
	},
}

var codexSpec = Spec{
	Name:        "codex",
	KeyPrefix:   "CODEX",
	NeedsSkills: true,
	New: func(p BackendParams) (Runner, error) {
		return &Codex{
			Bin:          p.Bin,
			Flags:        p.Flags,
			Profile:      p.Extra["profile"],
			Model:        p.Model,
			Effort:       p.Effort,
			Preamble:     p.Preamble,
			PlanPreamble: p.PlanPreamble,
			Dir:          p.Dir,
			ResultDir:    p.Extra["result_dir"],
			Cols:         p.Cols,
			Rows:         p.Rows,
			SizeFn:       p.SizeFn,
			Log:          p.Log,
			Tokens:       p.Tokens,
		}, nil
	},
}

var kimiSpec = Spec{
	Name:        "kimi",
	KeyPrefix:   "KIMI",
	NeedsSkills: true,
	New: func(p BackendParams) (Runner, error) {
		return &Kimi{
			Bin:          p.Bin,
			Flags:        p.Flags,
			Model:        p.Model,
			Preamble:     p.Preamble,
			PlanPreamble: p.PlanPreamble,
			Dir:          p.Dir,
			ResultDir:    p.Extra["result_dir"],
			Cols:         p.Cols,
			Rows:         p.Rows,
			SizeFn:       p.SizeFn,
			Timeout:      p.Timeout,
			Log:          p.Log,
			Tokens:       p.Tokens,
		}, nil
	},
}
