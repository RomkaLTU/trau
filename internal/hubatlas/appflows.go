package hubatlas

import "fmt"

const maxFlows = 8

// AppFlows is the app-flows flavor document: a set of named small graphs, one per
// significant runtime flow.
type AppFlows struct {
	Flows []Flow `json:"flows"`
}

// Flow is one runtime flow — its own small graph of steps and the edges between
// them.
type Flow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Steps   []Step `json:"steps"`
	Edges   []Edge `json:"edges"`
}

// Step is one node in a Flow. Kind classifies the step for styling.
type Step struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Edge is a directed transition between two steps of the same Flow, by step id.
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

func validStepKind(kind string) bool {
	switch kind {
	case "ui", "http", "service", "job", "queue", "db", "external", "other":
		return true
	default:
		return false
	}
}

func validateAppFlows(document []byte) error {
	var doc AppFlows
	if err := strictUnmarshal(document, &doc); err != nil {
		return err
	}
	if len(doc.Flows) < 1 || len(doc.Flows) > maxFlows {
		return fmt.Errorf("app flows has %d flows, want 1 to %d", len(doc.Flows), maxFlows)
	}

	flows := make(map[string]bool, len(doc.Flows))
	for _, f := range doc.Flows {
		if !isSlug(f.ID) {
			return fmt.Errorf("flow id %q is not a kebab-case slug", f.ID)
		}
		if flows[f.ID] {
			return fmt.Errorf("duplicate flow id %q", f.ID)
		}
		flows[f.ID] = true
		if f.Name == "" {
			return fmt.Errorf("flow %q has no name", f.ID)
		}
		if err := validateFlowGraph(f); err != nil {
			return err
		}
	}
	return nil
}

func validateFlowGraph(f Flow) error {
	steps := make(map[string]bool, len(f.Steps))
	for _, s := range f.Steps {
		if !isSlug(s.ID) {
			return fmt.Errorf("flow %q step id %q is not a kebab-case slug", f.ID, s.ID)
		}
		if steps[s.ID] {
			return fmt.Errorf("flow %q has duplicate step id %q", f.ID, s.ID)
		}
		steps[s.ID] = true
		if s.Name == "" {
			return fmt.Errorf("flow %q step %q has no name", f.ID, s.ID)
		}
		if !validStepKind(s.Kind) {
			return fmt.Errorf("flow %q step %q has unknown kind %q", f.ID, s.ID, s.Kind)
		}
	}
	for _, e := range f.Edges {
		if !steps[e.From] {
			return fmt.Errorf("flow %q edge references unknown step %q", f.ID, e.From)
		}
		if !steps[e.To] {
			return fmt.Errorf("flow %q edge references unknown step %q", f.ID, e.To)
		}
	}
	return nil
}
