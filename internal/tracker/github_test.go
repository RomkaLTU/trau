package tracker

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
)

// runnerFunc adapts a function to agent.Runner, so a test can inspect what the
// prompt points at while the run is still in flight.
type runnerFunc func(ctx context.Context, prompt, label string) (agent.Result, error)

func (f runnerFunc) Run(ctx context.Context, prompt, label string) (agent.Result, error) {
	return f(ctx, prompt, label)
}

// A GitHub-tracked run's QA report reaches the agent as a gh comment command
// whose --body-file holds the rendered Markdown verbatim, and the file is gone
// once the post returns.
func TestGitHubPostQANoteHandsTheBodyToTheAgent(t *testing.T) {
	note := QANote{
		Body:   "## Trau QA report\n\nVerify passed: all green\n\nPR: https://github.test/pr/7\n",
		Images: []QAImage{{Name: "proof-1.png", Mime: "image/png"}},
	}
	var prompt, posted, bodyPath string
	runner := runnerFunc(func(_ context.Context, p, label string) (agent.Result, error) {
		if label != "qa_note" {
			t.Errorf("run label = %q, want qa_note", label)
		}
		prompt = p
		bodyPath = bodyFileArg(p)
		body, err := os.ReadFile(bodyPath)
		if err != nil {
			t.Errorf("reading the prompt's --body-file: %v", err)
		}
		posted = string(body)
		return agent.Result{}, nil
	})
	g := &GitHub{Runner: runner, Repo: "acme/tools"}

	if err := g.PostQANote(context.Background(), "GH-7", note); err != nil {
		t.Fatalf("PostQANote error: %v", err)
	}
	if posted != note.Body {
		t.Errorf("body file = %q, want the report rendered verbatim", posted)
	}
	for _, want := range []string{"GH-7", "gh issue comment", "--repo acme/tools", "--body-file " + bodyPath} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if _, err := os.Stat(bodyPath); !os.IsNotExist(err) {
		t.Errorf("body file %s survived the post (stat err = %v)", bodyPath, err)
	}
}

// A failed run surfaces as an error the pipeline can warn on rather than being
// swallowed, and still leaves no body file behind.
func TestGitHubPostQANoteSurfacesTheRunError(t *testing.T) {
	var bodyPath string
	runner := runnerFunc(func(_ context.Context, p, _ string) (agent.Result, error) {
		bodyPath = bodyFileArg(p)
		return agent.Result{}, context.DeadlineExceeded
	})
	g := &GitHub{Runner: runner, Repo: "acme/tools"}

	if err := g.PostQANote(context.Background(), "GH-7", QANote{Body: "note\n"}); err == nil {
		t.Fatal("PostQANote error = nil, want the agent failure surfaced")
	}
	if _, err := os.Stat(bodyPath); !os.IsNotExist(err) {
		t.Errorf("body file %s survived a failed post (stat err = %v)", bodyPath, err)
	}
}

// bodyFileArg recovers the path the prompt's gh command reads the comment from.
func bodyFileArg(prompt string) string {
	_, rest, ok := strings.Cut(prompt, "--body-file ")
	if !ok {
		return ""
	}
	path, _, _ := strings.Cut(rest, "`")
	return path
}
