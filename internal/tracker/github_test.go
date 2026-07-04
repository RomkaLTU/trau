package tracker

import (
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
)

func TestParseCreated(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    string
		matched bool
	}{
		{"plain sentinel", "CREATED=COD-42", "COD-42", true},
		{"line prefix before sentinel", "The new issue is #42 CREATED=COD-42", "COD-42", true},
		{"last sentinel wins", "CREATED=COD-41\nCREATED=COD-42", "COD-42", true},
		{"wrong prefix not matched", "CREATED=OTHER-42", "", false},
		{"no sentinel", "I created the issue", "", false},
	}
	for _, tc := range tests {
		got, ok := parseCreated(tc.text, "COD")
		if got != tc.want || ok != tc.matched {
			t.Errorf("%s: parseCreated(%q) = (%q, %v), want (%q, %v)", tc.name, tc.text, got, ok, tc.want, tc.matched)
		}
	}
}

// An epic create carries the title and full multi-line body into the MCP prompt,
// no labels and no parent link, and recovers the mapped identifier from the
// CREATED= sentinel.
func TestGitHubCreateIssueEpic(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"create_issue": {Final: "CREATED=COD-42"},
	}}
	g := &GitHub{Runner: runner, Repo: "acme/widgets", ReadyLabel: "ready-for-agent"}

	id, err := g.CreateIssue(context.Background(), IssueSpec{
		Title:       "Export widgets",
		Description: "# Export widgets\n\nThe full PRD body.",
	})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if id != "COD-42" {
		t.Errorf("CreateIssue = %q, want COD-42", id)
	}
	prompt := runner.prompts["create_issue"]
	for _, want := range []string{`"acme/widgets"`, `"Export widgets"`, "The full PRD body."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "sub-issue") {
		t.Error("a parentless epic must not be linked as a sub-issue")
	}
	if strings.Contains(prompt, "ready-for-agent") {
		t.Error("an epic spec without labels must not grow the ready label")
	}
}

// A child create links the issue under its parent, applies the drafted labels
// (the ready label among them), and derives the sentinel prefix from the parent
// identifier rather than the default.
func TestGitHubCreateIssueChild(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"create_issue": {Final: "CREATED=GH-43"},
	}}
	g := &GitHub{Runner: runner, Repo: "acme/widgets"}

	id, err := g.CreateIssue(context.Background(), IssueSpec{
		Title:       "csv download",
		Description: "## What to build\n\ncsv",
		Labels:      []string{"backend", "ready-for-agent"},
		Parent:      "GH-42",
	})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if id != "GH-43" {
		t.Errorf("CreateIssue = %q, want GH-43", id)
	}
	prompt := runner.prompts["create_issue"]
	for _, want := range []string{"sub-issue (child) of issue GH-42", "'backend', 'ready-for-agent'", "CREATED=GH-N"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// A response with no CREATED= sentinel is a real error — an issue whose
// identifier is lost cannot be recorded as the epic or a child.
func TestGitHubCreateIssueUnparseableErrors(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"create_issue": {Final: "done, made the issue"},
	}}
	g := &GitHub{Runner: runner, Repo: "acme/widgets"}

	if _, err := g.CreateIssue(context.Background(), IssueSpec{Title: "Export widgets"}); err == nil {
		t.Fatal("CreateIssue without a sentinel should error, got nil")
	}
}
