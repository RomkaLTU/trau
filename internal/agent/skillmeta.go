package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillMetaFile is the manifest every installed skill carries, relative to its
// own directory.
const SkillMetaFile = "SKILL.md"

// SkillMeta is one installed skill as its manifest declares it. Description is
// the frontmatter line skill authors write to say when the skill applies — the
// only machine-readable relevance signal a skill ships — and seeds the suggested
// keywords for an auto routing rule. Invalid marks a directory that looks like a
// skill but has no readable SKILL.md frontmatter: present on disk, but nothing
// can be said about it, so it is reported rather than silently counted.
type SkillMeta struct {
	Name         string
	DeclaredName string
	Description  string
	Invalid      bool
}

// InstalledSkills returns the installed skills with their manifest metadata, in
// the same sorted, de-duplicated order as InstalledSkillNames.
func InstalledSkills(repoRoot string) []SkillMeta {
	dirs := installedSkillDirs(repoRoot)
	names := sortedSkillNames(dirs)
	out := make([]SkillMeta, 0, len(names))
	for _, name := range names {
		out = append(out, readSkillMeta(name, dirs[name]))
	}
	return out
}

func readSkillMeta(name, dir string) SkillMeta {
	meta := SkillMeta{Name: name}
	data, err := os.ReadFile(filepath.Join(dir, SkillMetaFile))
	if err != nil {
		meta.Invalid = true
		return meta
	}
	meta.DeclaredName, meta.Description = parseSkillFrontmatter(data)
	meta.Invalid = meta.DeclaredName == "" && meta.Description == ""
	return meta
}

// parseSkillFrontmatter reads the name and description out of a SKILL.md YAML
// frontmatter block. It is deliberately tolerant: skills come from third-party
// registries and a manifest whose YAML does not parse still yields its name and
// description on a line scan, so one malformed field never costs the whole
// inventory entry.
func parseSkillFrontmatter(data []byte) (name, description string) {
	block, ok := frontmatterBlock(string(data))
	if !ok {
		return "", ""
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(block), &fm); err == nil {
		if fm.Name != "" || fm.Description != "" {
			return strings.TrimSpace(fm.Name), strings.TrimSpace(collapseSpace(fm.Description))
		}
	}
	return scanFrontmatterField(block, "name"), collapseSpace(scanFrontmatterField(block, "description"))
}

func frontmatterBlock(text string) (string, bool) {
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", false
	}
	for i := 1; i < len(lines); i++ {
		if t := strings.TrimSpace(lines[i]); t == "---" || t == "..." {
			return strings.Join(lines[1:i], "\n"), true
		}
	}
	return "", false
}

// scanFrontmatterField is the fallback for a frontmatter block YAML rejects: it
// takes the field's value from its own line, plus any indented continuation
// lines belonging to it.
func scanFrontmatterField(block, field string) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		rest, ok := strings.CutPrefix(line, field+":")
		if !ok {
			continue
		}
		value := strings.TrimSpace(rest)
		for _, cont := range lines[i+1:] {
			if strings.TrimSpace(cont) == "" || !strings.HasPrefix(cont, " ") && !strings.HasPrefix(cont, "\t") {
				break
			}
			value += " " + strings.TrimSpace(cont)
		}
		return strings.Trim(strings.TrimSpace(strings.TrimLeft(value, ">|-")), `"'`)
	}
	return ""
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// maxSuggestedKeywords caps how many keywords a description seeds, so the
// suggestion stays a starting point an editor prunes rather than a wall.
const maxSuggestedKeywords = 8

var keywordStopWords = map[string]bool{
	"and": true, "any": true, "are": true, "app": true, "for": true, "from": true,
	"has": true, "its": true, "not": true, "the": true, "this": true, "use": true,
	"used": true, "user": true, "uses": true, "using": true, "when": true,
	"with": true,
}

// SuggestedKeywords derives candidate auto-rule keywords from a skill's
// description — the distinctive words in the order the author wrote them. It
// seeds the rules editor; nothing routes on it until a keyword is saved into a
// rule.
func SuggestedKeywords(description string) []string {
	var out []string
	for _, word := range strings.FieldsFunc(strings.ToLower(description), notKeywordRune) {
		word = strings.Trim(word, "-")
		if len(word) < 3 || keywordStopWords[word] || slices.Contains(out, word) {
			continue
		}
		out = append(out, word)
		if len(out) == maxSuggestedKeywords {
			break
		}
	}
	return out
}

func notKeywordRune(r rune) bool {
	return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-'
}
