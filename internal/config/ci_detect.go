package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// CIGate is the merge gate's mode, selected by REQUIRE_CI. CIGateAuto requires
// CI whenever the PR's base branch has it and merges with a warning when no
// workflow targets that base at all; CIGateOn always waits for green checks;
// CIGateOff never waits.
type CIGate string

const (
	CIGateAuto CIGate = "auto"
	CIGateOn   CIGate = "1"
	CIGateOff  CIGate = "0"
)

// ParseCIGate maps a REQUIRE_CI value to a gate mode. Anything but the two
// explicit modes is auto, which is also what an unset key resolves to.
func ParseCIGate(v string) CIGate {
	switch strings.TrimSpace(v) {
	case string(CIGateOn):
		return CIGateOn
	case string(CIGateOff):
		return CIGateOff
	default:
		return CIGateAuto
	}
}

// PRCIScan summarizes how the CI a repository configures for itself responds to
// pull requests, read from GitHub Actions workflows and Bitbucket Pipelines
// alike. HasPRWorkflows is true when at least one pipeline can be triggered by a
// pull request. AllPathFiltered is true when every such pipeline scopes itself to
// a set of paths — a PR touching only files outside every filter then receives
// zero status checks even though the repo has PR CI for most changes.
type PRCIScan struct {
	HasPRWorkflows  bool
	AllPathFiltered bool
	branches        []branchFilter
	// unfiltered records that at least one PR trigger carries no path filter. It
	// is kept alongside AllPathFiltered so two sources can be merged: a repo with
	// both an Actions workflow and a Pipelines file is path-filtered only when
	// neither of them has an unfiltered trigger.
	unfiltered bool
}

// merge folds another source's scan into this one. Coverage is a union: any
// pipeline from any source that could check a PR counts, and a repo is only
// checkless by design when no source configured one at all.
func (s *PRCIScan) merge(other PRCIScan) {
	s.HasPRWorkflows = s.HasPRWorkflows || other.HasPRWorkflows
	s.unfiltered = s.unfiltered || other.unfiltered
	s.branches = append(s.branches, other.branches...)
	s.AllPathFiltered = s.HasPRWorkflows && !s.unfiltered
}

// CoversBranch reports whether any PR trigger admits a pull request whose base
// is branch. Repos routinely scope their PR workflows to the default branch, so
// a PR into an epic or release base receives no check at all — which the merge
// gate must tell apart from CI that ran and never reported. An unnamed branch
// reads as covered whenever the repo has PR CI: only a definite answer may relax
// the gate.
func (s PRCIScan) CoversBranch(branch string) bool {
	if branch == "" {
		return s.HasPRWorkflows
	}
	for _, f := range s.branches {
		if f.admits(branch) {
			return true
		}
	}
	return false
}

// branchFilter is one PR trigger's branches / branches-ignore lists; both empty
// means the trigger accepts every base branch.
type branchFilter struct {
	include []string
	exclude []string
}

func (f branchFilter) admits(branch string) bool {
	for _, pattern := range f.exclude {
		if branchPatternExcludes(pattern, branch) {
			return false
		}
	}
	positives, matched := 0, false
	for _, pattern := range f.include {
		if negated, ok := strings.CutPrefix(pattern, "!"); ok {
			if branchPatternExcludes(negated, branch) {
				return false
			}
			continue
		}
		positives++
		if branchPatternCovers(pattern, branch) {
			matched = true
		}
	}
	return matched || positives == 0
}

// branchPatternCovers reports whether a positive branch-filter pattern matches
// branch; branchPatternExcludes answers the same for a branches-ignore or
// '!'-negated one. They differ only in what they make of the ?, + and [] pattern
// forms, which are not modelled: each resolves those toward the base reading as
// covered, since over-reporting coverage only keeps the merge gate waiting while
// over-reporting exclusion waives it.
func branchPatternCovers(pattern, branch string) bool {
	return branchPatternMatches(pattern, branch, true)
}

func branchPatternExcludes(pattern, branch string) bool {
	return branchPatternMatches(pattern, branch, false)
}

func branchPatternMatches(pattern, branch string, unmodelled bool) bool {
	if strings.ContainsAny(pattern, "?+[]") {
		return unmodelled
	}
	re, err := regexp.Compile("^" + globExpr(pattern) + "$")
	if err != nil {
		return unmodelled
	}
	return re.MatchString(branch)
}

// globExpr translates a filter pattern's * (a run without a slash) and ** (any
// run) wildcards into a regular expression body.
func globExpr(pattern string) string {
	var b strings.Builder
	for i, span := range strings.Split(pattern, "**") {
		if i > 0 {
			b.WriteString(".*")
		}
		for j, literal := range strings.Split(span, "*") {
			if j > 0 {
				b.WriteString("[^/]*")
			}
			b.WriteString(regexp.QuoteMeta(literal))
		}
	}
	return b.String()
}

// ScanPullRequestCI reports how repoRoot's own CI configuration answers a pull
// request, reading both sources trau delivers to: .github/workflows for GitHub
// Actions and bitbucket-pipelines.yml for Bitbucket Pipelines. It is keyed on the
// files present rather than on the repo's forge, so a repo carrying both is
// covered by either. CI hosted anywhere else is still invisible here, which is
// why the derived choices stay user-overridable.
func ScanPullRequestCI(repoRoot string) PRCIScan {
	if repoRoot == "" {
		return PRCIScan{}
	}
	scan := scanActionsWorkflows(repoRoot)
	scan.merge(scanBitbucketPipelines(repoRoot))
	return scan
}

func scanActionsWorkflows(repoRoot string) PRCIScan {
	var scan PRCIScan
	dir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return scan
	}
	unfiltered := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yml", ".yaml":
		default:
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		pr := workflowPRTriggers(data)
		if !pr.present {
			continue
		}
		scan.HasPRWorkflows = true
		if !pr.filtered {
			unfiltered = true
		}
		scan.branches = append(scan.branches, pr.branches...)
	}
	scan.unfiltered = unfiltered
	scan.AllPathFiltered = scan.HasPRWorkflows && !unfiltered
	return scan
}

// HasPullRequestCI reports whether repoRoot configures any CI a pull request can
// trigger. Repos with no workflows and no pipelines — or only push/schedule/
// deploy ones — return false: their PRs receive zero status checks, so the merge
// gate (REQUIRE_CI) should default off.
func HasPullRequestCI(repoRoot string) bool {
	return ScanPullRequestCI(repoRoot).HasPRWorkflows
}

// prTriggers is what one workflow's `on:` block says about pull requests:
// whether it has a PR trigger at all, whether every one carries a
// paths/paths-ignore filter, and the base-branch filter each one scopes itself
// with.
type prTriggers struct {
	present  bool
	filtered bool
	branches []branchFilter
}

// workflowPRTriggers walks the YAML node tree rather than decoding into a map
// so the GitHub `on:` key — which some resolvers coerce to a boolean — is
// matched by its literal text. The scalar and sequence trigger forms can carry
// neither a path nor a branch filter.
func workflowPRTriggers(data []byte) prTriggers {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return prTriggers{}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return prTriggers{}
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "on" {
			return prTrigger(root.Content[i+1])
		}
	}
	return prTriggers{}
}

func prTrigger(n *yaml.Node) prTriggers {
	everyBranch := prTriggers{present: true, branches: []branchFilter{{}}}
	switch n.Kind {
	case yaml.ScalarNode:
		if isPRTrigger(n.Value) {
			return everyBranch
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if isPRTrigger(c.Value) {
				return everyBranch
			}
		}
	case yaml.MappingNode:
		var out prTriggers
		unfiltered := false
		for i := 0; i+1 < len(n.Content); i += 2 {
			if !isPRTrigger(n.Content[i].Value) {
				continue
			}
			out.present = true
			spec := n.Content[i+1]
			if !hasPathsFilter(spec) {
				unfiltered = true
			}
			out.branches = append(out.branches, branchesFilter(spec))
		}
		out.filtered = out.present && !unfiltered
		return out
	}
	return prTriggers{}
}

func branchesFilter(n *yaml.Node) branchFilter {
	var f branchFilter
	if n.Kind != yaml.MappingNode {
		return f
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "branches":
			f.include = scalarList(n.Content[i+1])
		case "branches-ignore":
			f.exclude = scalarList(n.Content[i+1])
		}
	}
	return f
}

func scalarList(n *yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "" {
			return nil
		}
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
		return out
	}
	return nil
}

func hasPathsFilter(n *yaml.Node) bool {
	if n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "paths", "paths-ignore":
			return true
		}
	}
	return false
}

func isPRTrigger(s string) bool {
	return s == "pull_request" || s == "pull_request_target"
}
