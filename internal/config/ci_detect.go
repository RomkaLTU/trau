package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PRCIScan summarizes how the GitHub Actions workflows under repoRoot respond
// to pull requests. HasPRWorkflows is true when at least one workflow triggers
// on pull_request or pull_request_target. AllPathFiltered is true when every
// such workflow scopes its PR trigger with paths/paths-ignore — a PR touching
// only files outside every filter then receives zero status checks even though
// the repo has PR CI for most changes.
type PRCIScan struct {
	HasPRWorkflows  bool
	AllPathFiltered bool
}

// ScanPullRequestCI inspects repoRoot's .github/workflows for pull_request
// triggers and their path filters. Detection only sees GitHub Actions; CI
// hosted elsewhere is invisible here, which is why the derived choices stay
// user-overridable.
func ScanPullRequestCI(repoRoot string) PRCIScan {
	var scan PRCIScan
	if repoRoot == "" {
		return scan
	}
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
		triggers, filtered := workflowPRTrigger(data)
		if !triggers {
			continue
		}
		scan.HasPRWorkflows = true
		if !filtered {
			unfiltered = true
		}
	}
	scan.AllPathFiltered = scan.HasPRWorkflows && !unfiltered
	return scan
}

// HasPullRequestCI reports whether repoRoot has at least one GitHub Actions
// workflow triggered by pull_request or pull_request_target. Repos with no
// workflows — or only push/schedule/deploy workflows — return false: their PRs
// receive zero status checks, so the merge gate (REQUIRE_CI) should default off.
func HasPullRequestCI(repoRoot string) bool {
	return ScanPullRequestCI(repoRoot).HasPRWorkflows
}

// workflowPRTrigger walks the YAML node tree rather than decoding into a map
// so the GitHub `on:` key — which some resolvers coerce to a boolean — is
// matched by its literal text. filtered reports whether every PR trigger
// carries a paths/paths-ignore filter; the scalar and sequence trigger forms
// can carry none.
func workflowPRTrigger(data []byte) (triggers, filtered bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return false, false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false, false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "on" {
			return prTrigger(root.Content[i+1])
		}
	}
	return false, false
}

func prTrigger(n *yaml.Node) (triggers, filtered bool) {
	switch n.Kind {
	case yaml.ScalarNode:
		return isPRTrigger(n.Value), false
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if isPRTrigger(c.Value) {
				return true, false
			}
		}
	case yaml.MappingNode:
		unfiltered := false
		for i := 0; i+1 < len(n.Content); i += 2 {
			if !isPRTrigger(n.Content[i].Value) {
				continue
			}
			triggers = true
			if !hasPathsFilter(n.Content[i+1]) {
				unfiltered = true
			}
		}
		return triggers, triggers && !unfiltered
	}
	return false, false
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
