package planning

import (
	"strings"
	"testing"
)

func TestBuildPromptUncapped(t *testing.T) {
	p := BuildPrompt("let users export widgets", nil, false)
	if !strings.Contains(p, "let users export widgets") {
		t.Error("prompt dropped the idea")
	}
	if !strings.Contains(p, `"status":"questions"`) {
		t.Error("uncapped prompt should offer the questions contract")
	}
	if !strings.Contains(p, `"status":"prd"`) {
		t.Error("prompt should always offer the prd contract")
	}
	if strings.Contains(p, "transcript>") {
		t.Error("empty transcript should not be rendered")
	}
}

func TestBuildPromptCapped(t *testing.T) {
	p := BuildPrompt("an idea", nil, true)
	if strings.Contains(p, `"status":"questions"`) {
		t.Error("capped prompt must not offer the questions contract")
	}
	if !strings.Contains(p, "## Assumptions") {
		t.Error("capped prompt must instruct a PRD-with-assumptions")
	}
	if !strings.Contains(p, `"status":"prd"`) {
		t.Error("capped prompt should still require the prd contract")
	}
}

func TestBuildPromptRendersTranscript(t *testing.T) {
	transcript := []QARound{
		{Round: 1, Answers: []Answer{
			{ID: "q1", Question: "who is the actor?", Values: []string{"admins", "editors"}},
			{ID: "q2", Question: "what name?", Values: []string{"Widgets"}, Skipped: true},
		}},
	}
	p := BuildPrompt("an idea", transcript, false)
	for _, want := range []string{"who is the actor?", "admins, editors", "what name?", "Widgets (default — skipped)"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing transcript fragment %q", want)
		}
	}
}
