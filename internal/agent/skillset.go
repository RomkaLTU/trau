package agent

import (
	"slices"

	"github.com/RomkaLTU/trau/internal/skillrules"
)

// browser-harness loads from outside the repo, so it is named even when not
// installed. User-scope skill directories are deliberately not enumerated, so a
// repo's resolved sets stay a property of the repo, not of the machine running
// it (ADR 0021).
const browserSkill = "browser-harness"

// Chain steps a SkillSet can resolve from, in fallback order.
const (
	SkillsSourceRules       = "routing rules"
	SkillsSourceVerifyPins  = "REQUIRED_SKILLS_VERIFY + test skills"
	SkillsSourceRequired    = "REQUIRED_SKILLS"
	SkillsSourceRecommended = "project-type recommended"
	SkillsSourceInstalled   = "all installed"
)

// SkillSet is one phase's resolved skill set: the names its prompt tells the
// agent to load, the chain step that produced them, and the step each individual
// name came from — a union set draws from more than one.
type SkillSet struct {
	Names   []string
	Source  string
	Origins map[string]string
}

// SkillContext is what a phase's routing rules match against: the ticket's text
// during build (which has no diff yet, so path globs match the paths the ticket
// itself names), the slice's changed files for every phase after it. Phases
// after build never read the ticket, so a diff that cannot be listed matches
// nothing and the phase falls back rather than routing on ticket prose. A zero
// context matches nothing, so rules resolve as if no ticket and no diff existed.
type SkillContext struct {
	Text    string
	Changed []string
}

func (sc SkillContext) match(phase string) skillrules.Match {
	if phase == skillrules.PhaseBuild {
		return skillrules.Match{Phase: phase, Text: sc.Text, Paths: skillrules.PathsInText(sc.Text)}
	}
	return skillrules.Match{Phase: phase, Paths: sc.Changed}
}

// KnownExternalSkills returns the skills a repo may name without installing
// them — skills the agent loads from outside the repo tree.
func KnownExternalSkills() []string { return []string{browserSkill} }

// NameableSkills returns every skill name a repo's prompts may use: what it
// installs, plus the known out-of-repo skills.
func NameableSkills(repoRoot string) []string {
	return append(InstalledSkillNames(repoRoot), KnownExternalSkills()...)
}

// SkillResolver derives the skill set each phase's prompt names. The repo's
// routing rules decide relevance; the configured pins join them; and when both
// resolve empty the fallback chain takes over, so the set never lands empty in a
// repo that installs skills.
type SkillResolver struct {
	installed      []string
	nameable       []string
	required       []string
	requiredVerify []string
	projectType    string
	rules          skillrules.Set
	rulesErr       error
}

func NewSkillResolver(repoRoot string, required, requiredVerify []string) SkillResolver {
	rules, err := skillrules.Load(repoRoot)
	return SkillResolver{
		installed:      InstalledSkillNames(repoRoot),
		nameable:       NameableSkills(repoRoot),
		required:       required,
		requiredVerify: requiredVerify,
		projectType:    DetectProjectType(repoRoot),
		rules:          rules,
		rulesErr:       err,
	}
}

func (r SkillResolver) Installed() []string { return r.installed }

// Rules returns the repo's routing rules as loaded.
func (r SkillResolver) Rules() skillrules.Set { return r.rules }

// RulesError reports a rules file that failed to load. Resolution falls back to
// the configured pins and the chain, so the run is never blocked — the caller
// surfaces the error so a typo does not read as "no rules".
func (r SkillResolver) RulesError() error { return r.rulesErr }

// UnknownRuleSkills returns the skills the rules name that this repo cannot
// load, so a rule pointing at an uninstalled skill warns at loop start the same
// way a mistyped REQUIRED_SKILLS entry does.
func (r SkillResolver) UnknownRuleSkills() []string {
	var out []string
	for _, name := range r.rules.Skills() {
		if !slices.Contains(r.nameable, name) {
			out = append(out, name)
		}
	}
	return out
}

// Build resolves the set the build prompt names: the rules' always-skills and
// auto matches for the ticket, plus REQUIRED_SKILLS. An empty union falls back
// to the recommended set for the repo's project type, then to everything
// installed.
func (r SkillResolver) Build(sc SkillContext) SkillSet {
	return r.resolve(skillrules.PhaseBuild, sc)
}

// Repair resolves the set the repair and bugfix prompts name — the build set
// matched against the slice's diff rather than the ticket's text.
func (r SkillResolver) Repair(sc SkillContext) SkillSet {
	return r.resolve(skillrules.PhaseRepair, sc)
}

func (r SkillResolver) resolve(phase string, sc SkillContext) SkillSet {
	if len(r.installed) == 0 {
		return SkillSet{}
	}
	if set := r.union(phase, sc, intersectSkills(r.required, r.nameable), SkillsSourceRequired); len(set.Names) > 0 {
		return set
	}
	if names := intersectSkills(recommendedSkillNames(r.projectType), r.installed); len(names) > 0 {
		return skillSetFrom(names, SkillsSourceRecommended)
	}
	return skillSetFrom(r.installed, SkillsSourceInstalled)
}

// Verify resolves the set the verify prompt names: the rules' verify matches for
// the slice's diff, REQUIRED_SKILLS_VERIFY, the installed test-token skills, and
// browser-harness when browser verify is active for the slice. An empty union
// falls through to the repair set.
func (r SkillResolver) Verify(sc SkillContext, browserActive bool) SkillSet {
	pinned := appendSkills(intersectSkills(r.requiredVerify, r.nameable), TestSkillNames(r.installed)...)
	if browserActive {
		pinned = appendSkills(pinned, browserSkill)
	}
	if set := r.union(skillrules.PhaseVerify, sc, pinned, SkillsSourceVerifyPins); len(set.Names) > 0 {
		return set
	}
	return r.Repair(sc)
}

// union is the rule-driven part of a phase's set joined with the pins that phase
// forces, and the source describing which of the two contributed.
func (r SkillResolver) union(phase string, sc SkillContext, pinned []string, pinSource string) SkillSet {
	matched := intersectSkills(r.rules.Resolve(sc.match(phase)), r.nameable)
	set := skillSetFrom(matched, ruleSource(matched))
	if len(pinned) > 0 {
		for _, name := range pinned {
			if _, matched := set.Origins[name]; !matched {
				set.Origins[name] = pinSource
			}
		}
		set.Names = appendSkills(set.Names, pinned...)
		set.Source = joinSources(set.Source, pinSource)
	}
	return set
}

func skillSetFrom(names []string, source string) SkillSet {
	origins := make(map[string]string, len(names))
	for _, name := range names {
		origins[name] = source
	}
	return SkillSet{Names: names, Source: source, Origins: origins}
}

func ruleSource(matched []string) string {
	if len(matched) == 0 {
		return ""
	}
	return SkillsSourceRules
}

func joinSources(a, b string) string {
	if a == "" {
		return b
	}
	return a + " + " + b
}

func recommendedSkillNames(projectType string) []string {
	recs := RecommendedSkills(projectType)
	names := make([]string, 0, len(recs))
	for _, rec := range recs {
		names = append(names, rec.Name)
	}
	return names
}

func intersectSkills(want, have []string) []string {
	var out []string
	for _, w := range want {
		if slices.Contains(have, w) {
			out = appendSkills(out, w)
		}
	}
	return out
}

func appendSkills(dst []string, names ...string) []string {
	for _, n := range names {
		if !slices.Contains(dst, n) {
			dst = append(dst, n)
		}
	}
	return dst
}
