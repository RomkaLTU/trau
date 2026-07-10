package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// SkillSearchPaths lists the repo-relative directories where agent skills may
// live. The order is irrelevant; CheckSkills reports every path that exists and
// contains at least one skill entry.
var SkillSearchPaths = []string{
	".agents/skills",
	".claude/skills",
	".kimi/skills",
}

// SkillRecommendation is one skill that skills.sh recommends for a given
// project type. Name is the install directory name (e.g. "golang-code-style"),
// Package is the `npx skills add` argument, and URL is the skills.sh page.
type SkillRecommendation struct {
	Name    string
	Package string
	URL     string
}

// SkillReadiness reports what skills are present in the repo and which
// recommended skills are missing.
type SkillReadiness struct {
	ProjectType string
	HasSkills   bool
	FoundDirs   []string
	Installed   []string // recommended skill names that are present
	Missing     []SkillRecommendation
}

// CheckSkills returns whether at least one skill directory exists and is
// non-empty, plus the list of directories that were found.
func CheckSkills(repoRoot string) (found bool, dirs []string) {
	if repoRoot == "" {
		return false, nil
	}
	for _, rel := range SkillSearchPaths {
		path := filepath.Join(repoRoot, rel)
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) == 0 {
			continue
		}
		dirs = append(dirs, rel)
		found = true
	}
	return found, dirs
}

// InstalledSkillNames returns the sorted, de-duplicated names of the skills
// installed in the repo — the subdirectories of each skill search path.
// .claude/skills entries symlink into .agents/skills, so names are resolved
// through symlinks and collapsed to one entry per name.
func InstalledSkillNames(repoRoot string) []string {
	if repoRoot == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var names []string
	for _, rel := range SkillSearchPaths {
		dir := filepath.Join(repoRoot, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			fi, err := os.Stat(filepath.Join(dir, name))
			if err != nil || !fi.IsDir() {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// MissingRequiredSkills returns the names in required that are not installed in
// repoRoot, preserving the input order. It backs the loop-start warning that
// keeps a mistyped or uninstalled REQUIRED_SKILLS name from silently vanishing
// from the build prompt.
func MissingRequiredSkills(repoRoot string, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	installed := InstalledSkillNames(repoRoot)
	set := make(map[string]struct{}, len(installed))
	for _, n := range installed {
		set[n] = struct{}{}
	}
	var missing []string
	for _, r := range required {
		if _, ok := set[r]; !ok {
			missing = append(missing, r)
		}
	}
	return missing
}

// CheckSkillReadiness scans the repo for skills and compares the result against
// the recommended starter set for the detected project type.
func CheckSkillReadiness(repoRoot string) SkillReadiness {
	hasSkills, dirs := CheckSkills(repoRoot)
	projectType := DetectProjectType(repoRoot)
	recs := RecommendedSkills(projectType)

	installed := make([]string, 0, len(recs))
	missing := make([]SkillRecommendation, 0, len(recs))
	for _, rec := range recs {
		if skillInstalled(repoRoot, rec.Name) {
			installed = append(installed, rec.Name)
		} else {
			missing = append(missing, rec)
		}
	}

	return SkillReadiness{
		ProjectType: projectType,
		HasSkills:   hasSkills,
		FoundDirs:   dirs,
		Installed:   installed,
		Missing:     missing,
	}
}

// DetectProjectType returns a short label for the project in repoRoot, or ""
// when the type cannot be determined.
func DetectProjectType(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	switch {
	case fileExists(repoRoot, "go.mod"):
		return "go"
	case fileExists(repoRoot, "package.json"):
		return detectNodeProjectType(repoRoot)
	case fileExists(repoRoot, "requirements.txt"), fileExists(repoRoot, "pyproject.toml"):
		return "python"
	case fileExists(repoRoot, "Cargo.toml"):
		return "rust"
	case fileExists(repoRoot, "Gemfile"):
		return "ruby"
	case fileExists(repoRoot, "pom.xml"), fileExists(repoRoot, "build.gradle.kts"), fileExists(repoRoot, "build.gradle"):
		return "java"
	}
	return ""
}

// RecommendedSkills returns the skills.sh starter recommendations for a project
// type. The list is intentionally short so the message stays readable.
func RecommendedSkills(projectType string) []SkillRecommendation {
	switch projectType {
	case "go":
		return []SkillRecommendation{
			{Name: "golang-code-style", Package: "samber/cc-skills-golang@golang-code-style", URL: "https://skills.sh/samber/cc-skills-golang/golang-code-style"},
			{Name: "golang-error-handling", Package: "samber/cc-skills-golang@golang-error-handling", URL: "https://skills.sh/samber/cc-skills-golang/golang-error-handling"},
			{Name: "golang-performance", Package: "samber/cc-skills-golang@golang-performance", URL: "https://skills.sh/samber/cc-skills-golang/golang-performance"},
		}
	case "react":
		return []SkillRecommendation{
			{Name: "vercel-react-best-practices", Package: "vercel-labs/agent-skills@vercel-react-best-practices", URL: "https://skills.sh/vercel-labs/agent-skills/vercel-react-best-practices"},
		}
	case "nextjs":
		return []SkillRecommendation{
			{Name: "vercel-react-best-practices", Package: "vercel-labs/agent-skills@vercel-react-best-practices", URL: "https://skills.sh/vercel-labs/agent-skills/vercel-react-best-practices"},
		}
	case "python":
		return []SkillRecommendation{
			{Name: "python-best-practices", Package: "anthropics/skills@python-best-practices", URL: "https://skills.sh/anthropics/skills/python-best-practices"},
		}
	case "rust":
		return []SkillRecommendation{
			{Name: "rust-best-practices", Package: "anthropics/skills@rust-best-practices", URL: "https://skills.sh/anthropics/skills/rust-best-practices"},
		}
	}
	return nil
}

// MissingSkillsMessage returns a human-readable sentence describing the missing
// skills and how to install them. It is used by both the TUI and the console.
func MissingSkillsMessage(r SkillReadiness) string {
	if r.HasSkills || len(r.Missing) == 0 {
		return ""
	}

	var msg string
	switch r.ProjectType {
	case "go":
		msg = "this Go project has no skills. Recommended: "
	case "react":
		msg = "this React project has no skills. Recommended: "
	case "nextjs":
		msg = "this Next.js project has no skills. Recommended: "
	case "python":
		msg = "this Python project has no skills. Recommended: "
	case "rust":
		msg = "this Rust project has no skills. Recommended: "
	case "ruby":
		msg = "this Ruby project has no skills. Recommended: "
	case "java":
		msg = "this Java project has no skills. Recommended: "
	default:
		msg = "no skills found. Find relevant skills at https://skills.sh or run `npx skills find <topic>`. Recommended: "
	}

	for i, rec := range r.Missing {
		if i > 0 {
			msg += ", "
		}
		msg += rec.Name
	}
	msg += ". Install with: npx skills add " + r.Missing[0].Package
	if len(r.Missing) > 1 {
		msg += " ..."
	}
	return msg
}

// InstallSkill installs one recommended skill into repoRoot via the skills.sh
// CLI. The CLI writes into .agents/skills/<name> (the universal directory,
// first in SkillSearchPaths) and records the pin in skills-lock.json.
func InstallSkill(ctx context.Context, repoRoot string, rec SkillRecommendation) error {
	cmd := exec.CommandContext(ctx, "npx", skillInstallArgs(rec)...)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("install skill %s: %w: %s", rec.Name, err, lastOutputLine(out))
	}
	return nil
}

func skillInstallArgs(rec SkillRecommendation) []string {
	repo, skill, ok := strings.Cut(rec.Package, "@")
	if !ok {
		return []string{"-y", "skills", "add", rec.Package, "-y"}
	}
	return []string{"-y", "skills", "add", repo, "-s", skill, "-y"}
}

func lastOutputLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 200 {
		last = last[:200]
	}
	return last
}

func skillInstalled(repoRoot, name string) bool {
	if repoRoot == "" || name == "" {
		return false
	}
	for _, rel := range SkillSearchPaths {
		path := filepath.Join(repoRoot, rel, name)
		if fi, err := os.Stat(path); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

func fileExists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

func detectNodeProjectType(repoRoot string) string {
	data, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err != nil {
		return "node"
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "node"
	}
	deps := mergeDeps(pkg.Dependencies, pkg.DevDependencies)
	switch {
	case deps["next"] != "":
		return "nextjs"
	case deps["react"] != "", deps["react-dom"] != "":
		return "react"
	}
	return "node"
}

func mergeDeps(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
