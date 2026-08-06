package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// BitbucketPipelinesFile is the repository-root file Bitbucket Cloud reads its
// pipeline definitions from. Bitbucket accepts exactly this name at exactly this
// place, so there is no directory to walk.
const BitbucketPipelinesFile = "bitbucket-pipelines.yml"

// scanBitbucketPipelines reports how repoRoot's bitbucket-pipelines.yml answers a
// pull request. Three of the file's sections can put a build status on a PR: the
// pull-requests section, which Bitbucket triggers on the pull request itself; the
// branches section, whose pipelines run on each push to the PR's source branch;
// and default, which runs on any push no other section claims. The custom and
// tags sections cannot — one is manual and the other only fires on a tag — so a
// file holding only those reads as no PR CI, exactly as a repo with only
// push-triggered Actions workflows does.
//
// Every branch is reported as covered whenever such a section exists. Bitbucket
// filters its pipelines on a pull request's SOURCE branch and offers no filter on
// the destination at all, so a base branch says nothing about whether a pipeline
// could have run — and the merge gate's only safe reading of an unanswerable
// question is that CI is coming, which keeps it waiting rather than waiving.
func scanBitbucketPipelines(repoRoot string) PRCIScan {
	var scan PRCIScan
	data, err := os.ReadFile(filepath.Join(repoRoot, BitbucketPipelinesFile))
	if err != nil {
		return scan
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return scan
	}
	pipelines := mappingValue(doc.Content[0], "pipelines")
	if pipelines == nil {
		return scan
	}
	for i := 0; i+1 < len(pipelines.Content); i += 2 {
		section, spec := pipelines.Content[i].Value, pipelines.Content[i+1]
		var definitions []*yaml.Node
		switch section {
		case "default":
			definitions = []*yaml.Node{spec}
		case "pull-requests", "branches":
			for j := 1; j < len(spec.Content); j += 2 {
				definitions = append(definitions, spec.Content[j])
			}
		default:
			continue
		}
		for _, def := range definitions {
			scan.HasPRWorkflows = true
			scan.branches = append(scan.branches, branchFilter{})
			if !everyStepConditioned(def) {
				scan.unfiltered = true
			}
		}
	}
	scan.AllPathFiltered = scan.HasPRWorkflows && !scan.unfiltered
	return scan
}

// everyStepConditioned reports whether every step of one pipeline definition
// carries a condition, which on Bitbucket is the changesets path filter. Such a
// pipeline reports nothing at all for a change matching none of its paths — the
// same blind spot a fully paths-filtered Actions workflow leaves, and the reason
// the merge gate waives a checkless PR immediately rather than waiting it out. A
// definition holding no step is not conditioned: an empty answer must never be
// the one that waives a gate.
func everyStepConditioned(def *yaml.Node) bool {
	steps := collectSteps(def)
	if len(steps) == 0 {
		return false
	}
	for _, step := range steps {
		if mappingValue(step, "condition") == nil {
			return false
		}
	}
	return true
}

// collectSteps gathers a pipeline definition's step mappings, descending through
// the parallel and stage wrappers that group them. Both wrappers hold their steps
// under a steps key — always for a stage ({name, steps}), and in the expanded
// form of parallel ({fail-fast, steps}) — so that key is descended through too, or
// a definition whose only steps live inside a wrapper reads as having none.
func collectSteps(n *yaml.Node) []*yaml.Node {
	if n == nil {
		return nil
	}
	var steps []*yaml.Node
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			steps = append(steps, collectSteps(item)...)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch n.Content[i].Value {
			case "step":
				steps = append(steps, n.Content[i+1])
			case "parallel", "stage", "steps":
				steps = append(steps, collectSteps(n.Content[i+1])...)
			}
		}
	}
	return steps
}

// mappingValue is the value node a mapping holds for key, or nil. The parallel
// section takes both a bare sequence and a mapping wrapping one, so callers walk
// nodes rather than decoding into Go types.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
