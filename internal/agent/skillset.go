package agent

import "slices"

// browser-harness loads from outside the repo, so it is named even when not installed.
const browserSkill = "browser-harness"

// Chain steps a SkillSet can resolve from, in fallback order.
const (
	SkillsSourceVerifyPins  = "REQUIRED_SKILLS_VERIFY + test skills"
	SkillsSourceRequired    = "REQUIRED_SKILLS"
	SkillsSourceRecommended = "project-type recommended"
	SkillsSourceInstalled   = "all installed"
)

// SkillSet is one phase's resolved skill set: the names its prompt tells the
// agent to load, plus the chain step that produced them.
type SkillSet struct {
	Names  []string
	Source string
}

// SkillResolver derives the skill set each phase's prompt names. The chain never
// lands on an empty set in a repo that installs skills.
type SkillResolver struct {
	installed      []string
	required       []string
	requiredVerify []string
	projectType    string
}

func NewSkillResolver(repoRoot string, required, requiredVerify []string) SkillResolver {
	return SkillResolver{
		installed:      InstalledSkillNames(repoRoot),
		required:       required,
		requiredVerify: requiredVerify,
		projectType:    DetectProjectType(repoRoot),
	}
}

func (r SkillResolver) Installed() []string { return r.installed }

// Build resolves the set the build, repair, and bugfix prompts name:
// REQUIRED_SKILLS, else the recommended set for the repo's project type, else
// everything installed. Each step is intersected with the installed names.
func (r SkillResolver) Build() SkillSet {
	if len(r.installed) == 0 {
		return SkillSet{}
	}
	if names := intersectSkills(r.required, r.installed); len(names) > 0 {
		return SkillSet{Names: names, Source: SkillsSourceRequired}
	}
	if names := intersectSkills(recommendedSkillNames(r.projectType), r.installed); len(names) > 0 {
		return SkillSet{Names: names, Source: SkillsSourceRecommended}
	}
	return SkillSet{Names: r.installed, Source: SkillsSourceInstalled}
}

// Verify resolves the set the verify prompt names: REQUIRED_SKILLS_VERIFY, the
// installed test-token skills, and browser-harness when browser verify is active
// for the slice. An empty union falls through to the build set.
func (r SkillResolver) Verify(browserActive bool) SkillSet {
	names := intersectSkills(r.requiredVerify, r.installed)
	names = appendSkills(names, TestSkillNames(r.installed)...)
	if browserActive {
		names = appendSkills(names, browserSkill)
	}
	if len(names) == 0 {
		return r.Build()
	}
	return SkillSet{Names: names, Source: SkillsSourceVerifyPins}
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
