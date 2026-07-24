// Package skillrules is the repo-owned routing layer that decides which of a
// repo's installed skills each phase's prompt names. A target repo declares one
// rule per skill under .trau/skills-rules.json; the loop resolves them against
// the phase and the run's match inputs — the ticket's text at build time, the
// slice's changed files afterwards — so a slice loads the skills that matter to
// it instead of the same pinned list every run (ADR 0021).
//
// Resolution here is deliberately partial: it produces the rule-driven part of a
// phase's set and nothing else. The never-empty fallback chain that guarantees a
// skill-equipped repo never renders a skill-less prompt stays in agent's
// SkillResolver, which folds this result together with the configured pins.
package skillrules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// File is the conventional in-repo location for routing rules, relative to the
// target repo root.
const File = ".trau/skills-rules.json"

// Scopes a skill can be routed under.
const (
	// ScopeAlways names the skill in every phase the rule applies to.
	ScopeAlways = "always"
	// ScopeAuto names the skill only when the rule's path globs or keywords hit.
	ScopeAuto = "auto"
	// ScopeManual never names the skill automatically. Human-workflow skills
	// (releases, ticket bootstrapping) live here; a config pin can still name one.
	ScopeManual = "manual"
)

// Phases a rule can apply to. Bugfix shares the repair phase: both run against
// the slice's diff and load the same set.
const (
	PhaseBuild  = "build"
	PhaseVerify = "verify"
	PhaseRepair = "repair"
)

// Rule routes one skill. Phases empty means every phase; Paths are `/`-separated
// globs where `**` spans any number of segments; Keywords match the ticket text
// on word boundaries, case-insensitively. An auto rule with neither Paths nor
// Keywords can never hit, so a half-written rule stays inert rather than
// forcing itself into every set.
type Rule struct {
	Skill    string   `json:"skill"`
	Scope    string   `json:"scope"`
	Phases   []string `json:"phases,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
}

// Set is a repo's whole rule list, in declaration order — the order resolved
// names come back in.
type Set struct {
	Rules []Rule `json:"rules"`
}

// Match is what a phase's rules are evaluated against: the phase key, the
// ticket's text (build only), and the paths in play — the slice's changed files
// after build, the paths the ticket itself names during it.
type Match struct {
	Phase string
	Text  string
	Paths []string
}

// Load reads <repoRoot>/.trau/skills-rules.json. A repo with no rules file reads
// as an empty set and no error — routing rules are opt-in. A malformed file is an
// error the caller surfaces, so a typo is visible instead of silently reverting
// the repo to its config pins.
func Load(repoRoot string) (Set, error) {
	if repoRoot == "" {
		return Set{}, nil
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, File))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Set{}, nil
		}
		return Set{}, fmt.Errorf("read skill rules: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return Set{}, nil
	}
	var s Set
	if err := json.Unmarshal(data, &s); err != nil {
		return Set{}, fmt.Errorf("parse %s: %w", File, err)
	}
	return s, nil
}

// Save writes the rule set back to <repoRoot>/.trau/skills-rules.json, creating
// the .trau directory when the repo has none. It is the hub's write path: the web
// edits rules through the hub, which owns this file, never the running loop.
func Save(repoRoot string, s Set) error {
	if repoRoot == "" {
		return errors.New("save skill rules: no repo root")
	}
	dest := filepath.Join(repoRoot, File)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("save skill rules: %w", err)
	}
	if s.Rules == nil {
		s.Rules = []Rule{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("save skill rules: %w", err)
	}
	if err := os.WriteFile(dest, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("save skill rules: %w", err)
	}
	return nil
}

// NormalizeScope canonicalizes a scope. Anything unrecognized reads as auto,
// which without match rules never hits — a misspelled scope leaves its skill out
// of every set rather than forcing it into all of them.
func NormalizeScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case ScopeAlways:
		return ScopeAlways
	case ScopeManual:
		return ScopeManual
	default:
		return ScopeAuto
	}
}

// Resolve returns the skills the rules name for this match, in declaration
// order: every applicable always-skill, plus the auto-skills whose globs or
// keywords hit. Manual skills are never returned.
func (s Set) Resolve(m Match) []string {
	var out []string
	for _, r := range s.Rules {
		name := strings.TrimSpace(r.Skill)
		if name == "" || !r.appliesTo(m.Phase) {
			continue
		}
		switch NormalizeScope(r.Scope) {
		case ScopeManual:
			continue
		case ScopeAuto:
			if !r.hits(m) {
				continue
			}
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// Skills returns every skill a rule names, so the loop-start check can flag a
// rule pointing at a skill the repo does not have.
func (s Set) Skills() []string {
	var out []string
	for _, r := range s.Rules {
		name := strings.TrimSpace(r.Skill)
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func (r Rule) appliesTo(phase string) bool {
	if len(r.Phases) == 0 {
		return true
	}
	for _, p := range r.Phases {
		if strings.EqualFold(strings.TrimSpace(p), phase) {
			return true
		}
	}
	return false
}

func (r Rule) hits(m Match) bool {
	for _, glob := range r.Paths {
		for _, p := range m.Paths {
			if MatchPath(glob, p) {
				return true
			}
		}
	}
	if m.Text == "" {
		return false
	}
	text := strings.ToLower(m.Text)
	for _, kw := range r.Keywords {
		if containsWord(text, strings.ToLower(strings.TrimSpace(kw))) {
			return true
		}
	}
	return false
}

// MatchPath reports whether a `/`-separated glob matches a repo-relative path.
// Within a segment the syntax is path.Match's (`*`, `?`, character classes); a
// `**` segment matches any number of segments, so `web/**` covers everything
// under web/ and `**/*.go` covers Go files at any depth.
func MatchPath(glob, name string) bool {
	glob = strings.TrimSpace(glob)
	if glob == "" || name == "" {
		return false
	}
	return matchSegments(strings.Split(glob, "/"), strings.Split(filepath.ToSlash(name), "/"))
}

func matchSegments(pattern, segments []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			if len(pattern) == 1 {
				return true
			}
			for i := 0; i <= len(segments); i++ {
				if matchSegments(pattern[1:], segments[i:]) {
					return true
				}
			}
			return false
		}
		if len(segments) == 0 {
			return false
		}
		ok, err := path.Match(pattern[0], segments[0])
		if err != nil || !ok {
			return false
		}
		pattern, segments = pattern[1:], segments[1:]
	}
	return len(segments) == 0
}

// containsWord reports whether needle occurs in the already-lowercased hay
// bounded by non-alphanumeric characters, so a "go" keyword matches "go build"
// but not "google". Multi-word keywords work unchanged; only the outer edges are
// checked.
func containsWord(hay, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); {
		j := strings.Index(hay[i:], needle)
		if j < 0 {
			return false
		}
		start := i + j
		if !alnumAt(hay, start-1) && !alnumAt(hay, start+len(needle)) {
			return true
		}
		i = start + 1
	}
	return false
}

func alnumAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// maxTextPaths bounds how many path-looking tokens one ticket contributes, so a
// pathological description cannot turn matching into a long scan.
const maxTextPaths = 200

var textPath = regexp.MustCompile(`[A-Za-z0-9_.*-]+(?:/[A-Za-z0-9_.*-]+)+`)

// PathsInText pulls the path-looking tokens out of a ticket's text, so a build —
// which has no diff yet — can still match path globs against the paths the
// ticket itself names.
func PathsInText(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, m := range textPath.FindAllString(text, -1) {
		if len(out) >= maxTextPaths {
			break
		}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}
