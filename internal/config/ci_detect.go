package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// HasPullRequestCI reports whether repoRoot has at least one GitHub Actions
// workflow triggered by pull_request or pull_request_target. Repos with no
// workflows — or only push/schedule/deploy workflows — return false: their PRs
// receive zero status checks, so the merge gate (REQUIRE_CI) should default off.
// Detection only sees GitHub Actions under .github/workflows; CI hosted elsewhere
// is invisible here, which is why the choice stays user-overridable.
func HasPullRequestCI(repoRoot string) bool {
	if repoRoot == "" {
		return false
	}
	dir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
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
		if workflowTriggersOnPR(data) {
			return true
		}
	}
	return false
}

// workflowTriggersOnPR walks the YAML node tree rather than decoding into a map
// so the GitHub `on:` key — which some resolvers coerce to a boolean — is matched
// by its literal text.
func workflowTriggersOnPR(data []byte) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "on" {
			return nodeMentionsPRTrigger(root.Content[i+1])
		}
	}
	return false
}

func nodeMentionsPRTrigger(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.ScalarNode:
		return isPRTrigger(n.Value)
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if isPRTrigger(c.Value) {
				return true
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if isPRTrigger(n.Content[i].Value) {
				return true
			}
		}
	}
	return false
}

func isPRTrigger(s string) bool {
	return s == "pull_request" || s == "pull_request_target"
}
