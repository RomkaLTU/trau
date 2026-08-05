package pipeline

import (
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
)

// skillLoadFailure pairs a skill the phase's prompt named and never loaded with
// the raw name the agent typed at the Skill tool instead.
type skillLoadFailure struct {
	skill   string
	attempt string
}

// skillAttempts is everything one call typed at the Skill tool that loaded
// nothing: the inputs the tool rejected, then the sightings that matched no
// installed skill.
func skillAttempts(res agent.Result) []string {
	if len(res.SkillsFailed) == 0 && len(res.SkillsUnmatched) == 0 {
		return nil
	}
	out := make([]string, 0, len(res.SkillsFailed)+len(res.SkillsUnmatched))
	out = append(out, res.SkillsFailed...)
	return append(out, res.SkillsUnmatched...)
}

// failedSkillLoads reads the phase's evidence as a per-skill verdict: for every
// skill the prompt named that never loaded, the attempt that was meant to be it.
// Attempts are paired by agent.SkillAttemptMatches — the same reading of a mangled
// name that keeps it out of the phase's loaded set, so what the agent dropped is
// exactly what this reports.
func failedSkillLoads(named, loaded, attempts []string) []skillLoadFailure {
	if len(named) == 0 || len(attempts) == 0 {
		return nil
	}
	got := make(map[string]bool, len(loaded))
	for _, name := range loaded {
		got[agent.SkillNameKey(name)] = true
	}
	var fails []skillLoadFailure
	for _, name := range named {
		if got[agent.SkillNameKey(name)] {
			continue
		}
		for _, attempt := range attempts {
			if agent.SkillAttemptMatches(attempt, name) {
				fails = append(fails, skillLoadFailure{skill: name, attempt: attempt})
				break
			}
		}
	}
	return fails
}

// warnSkillLoadFailures reports each prompt-named skill the phase demonstrably
// tried to load and could not, naming the mangled attempt next to what it was
// meant to be. Advisory only, and independent of how many other skills loaded — a
// partial failure is otherwise invisible. In serve mode the whole set lands in one
// durable event for the phase attempt.
func (p *Pipeline) warnSkillLoadFailures(id, phase string, fails []skillLoadFailure) {
	if len(fails) == 0 {
		return
	}
	for _, f := range fails {
		p.logf("  ⚠ %s failed to load skill '%s' — the agent called Skill(%q) and the call errored", phase, f.skill, f.attempt)
	}
	if p.Events == nil {
		return
	}
	p.Events.Emit(event.KindSkillLoadFailed, phase, phase+" failed to load "+skillFailurePhrase(fails), map[string]any{
		"ticket":   id,
		"skills":   failedSkillNames(fails),
		"attempts": skillFailureFields(fails),
	})
}

// noSkillsMessage is the zero-loaded warning and its payload: the bare line when
// there is nothing more to say, and otherwise one naming the failed attempts.
func noSkillsMessage(id, phase, generic string, fails []skillLoadFailure) (string, map[string]any) {
	fields := map[string]any{"ticket": id}
	if len(fails) == 0 {
		return generic, fields
	}
	fields["attempts"] = skillFailureFields(fails)
	return phase + " loaded no skills — the agent's Skill calls failed: " + skillFailurePhrase(fails), fields
}

// skillFailurePhrase renders the failures for a one-line message.
func skillFailurePhrase(fails []skillLoadFailure) string {
	parts := make([]string, 0, len(fails))
	for _, f := range fails {
		parts = append(parts, fmt.Sprintf("'%s' (called as Skill(%q))", f.skill, f.attempt))
	}
	return strings.Join(parts, ", ")
}

func failedSkillNames(fails []skillLoadFailure) []string {
	out := make([]string, 0, len(fails))
	for _, f := range fails {
		out = append(out, f.skill)
	}
	return out
}

func skillFailureFields(fails []skillLoadFailure) []map[string]string {
	out := make([]map[string]string, 0, len(fails))
	for _, f := range fails {
		out = append(out, map[string]string{"skill": f.skill, "attempt": f.attempt})
	}
	return out
}
