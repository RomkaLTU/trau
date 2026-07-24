package agent

import (
	"os"
	"path/filepath"
)

// InjectedSkill is one skill delivered by content rather than by name: its
// repo-relative SKILL.md path and the file's full text. Inject mode carries the
// skill itself into a phase prompt so an agent without the Skill tool still has
// it, while the path lets the agent open the skill's references/ and asset files
// on its own (progressive disclosure preserved).
type InjectedSkill struct {
	Name string
	Path string
	Body string
}

// LoadInjectableSkills returns the SKILL.md path and content for each named skill
// installed in the repo, in the given order. A name with no readable in-repo
// SKILL.md — a known out-of-repo skill like browser-harness, or a broken install
// — is skipped, so the result carries only skills whose content can actually be
// delivered.
func LoadInjectableSkills(repoRoot string, names []string) []InjectedSkill {
	if repoRoot == "" || len(names) == 0 {
		return nil
	}
	dirs := installedSkillDirs(repoRoot)
	out := make([]InjectedSkill, 0, len(names))
	for _, name := range names {
		dir, ok := dirs[name]
		if !ok {
			continue
		}
		manifest := filepath.Join(dir, SkillMetaFile)
		body, err := os.ReadFile(manifest)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(repoRoot, manifest)
		if err != nil {
			rel = manifest
		}
		out = append(out, InjectedSkill{
			Name: name,
			Path: filepath.ToSlash(rel),
			Body: string(body),
		})
	}
	return out
}
