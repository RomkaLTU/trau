package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
)

func modelArg(args []string) string {
	for i, a := range args {
		if a == "--model" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// spawnRefusingClaude is a backend whose terminal never starts, so Run reaches
// the argv it would have exec'd and returns immediately. The captured args are
// written to argv.
func spawnRefusingClaude(t *testing.T, model string, notice *ModelFallbackNotice, log *event.Log, argv *[]string) *ClaudeInteractive {
	t.Helper()
	return &ClaudeInteractive{
		Bin:           "claude",
		Model:         model,
		ResultDir:     t.TempDir(),
		Log:           log,
		ModelFallback: notice,
		start: func(_ context.Context, _, _ string, args []string, _, _ int) (terminalSession, error) {
			*argv = args
			return nil, errors.New("spawn refused")
		},
	}
}

// fallbackEvents returns the model_fallback records on a JSON event stream.
func fallbackEvents(t *testing.T, stream *bytes.Buffer) []event.Event {
	t.Helper()
	var out []event.Event
	for _, line := range strings.Split(strings.TrimSpace(stream.String()), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event stream carries a non-JSON line %q: %v", line, err)
		}
		if ev.Kind == event.KindModelFallback {
			out = append(out, ev)
		}
	}
	return out
}

// TestClaudeAlwaysExecsWithModel pins the spawn invariant: a claude child is never
// spawned flag-less, because an omitted --model hands the routing decision to the
// user's own Claude Code settings. An empty resolved model — the shape a
// present-but-empty CLAUDE_MODEL produces when it masks the layer below it — runs
// on the built-in default; a configured model is passed through untouched.
func TestClaudeAlwaysExecsWithModel(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		want       string
	}{
		{"masked or unset", "", ClaudeDefaultModel},
		{"explicit", "sonnet", "sonnet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &ClaudeInteractive{Bin: "claude", Model: tc.configured}
			if got := modelArg(c.args("prompt", "sid", "build")); got != tc.want {
				t.Errorf("--model = %q, want %q", got, tc.want)
			}
			if _, model, _ := c.Route("build"); model != tc.want {
				t.Errorf("Route model = %q, want %q", model, tc.want)
			}
		})
	}
}

// TestPhaseRouteFallsBackToBuiltInModel covers the per-phase half of the
// invariant: a route that resolves no model of its own gets the same built-in
// default the default runner does, and the Router reports it for pre-call display.
func TestPhaseRouteFallsBackToBuiltInModel(t *testing.T) {
	phase := &ClaudeInteractive{Bin: "claude"}
	r := NewRouter(&ClaudeInteractive{Bin: "claude", Model: "sonnet"}, map[string]Runner{PhaseVerify: phase})

	if _, model, _ := r.Route("verify-retry2"); model != ClaudeDefaultModel {
		t.Errorf("routed verify model = %q, want %q", model, ClaudeDefaultModel)
	}
	if got := modelArg(phase.args("prompt", "sid", "verify-retry2")); got != ClaudeDefaultModel {
		t.Errorf("routed verify --model = %q, want %q", got, ClaudeDefaultModel)
	}
	if _, model, _ := r.Route("build"); model != "sonnet" {
		t.Errorf("unrouted build model = %q, want the configured sonnet", model)
	}
}

// TestModelFallbackWarnsOncePerRun pins the warning's scope: every backend a run
// builds shares one notice, so a masked model is announced once — naming the phase
// that first hit it — no matter how many phase routes and spawns follow.
func TestModelFallbackWarnsOncePerRun(t *testing.T) {
	var stream bytes.Buffer
	log := event.New(&stream)
	notice := &ModelFallbackNotice{}

	var argv []string
	def := spawnRefusingClaude(t, "", notice, log, &argv)
	routed := spawnRefusingClaude(t, "", notice, log, &argv)
	for _, call := range []struct {
		runner *ClaudeInteractive
		label  string
	}{{def, "build"}, {def, "handoff"}, {routed, "verify"}} {
		if _, err := call.runner.Run(context.Background(), "prompt", call.label); err == nil {
			t.Fatalf("%s: Run with a refused spawn should error", call.label)
		}
	}

	evs := fallbackEvents(t, &stream)
	if len(evs) != 1 {
		t.Fatalf("emitted %d model_fallback events, want exactly 1", len(evs))
	}
	if evs[0].Phase != "build" {
		t.Errorf("phase = %q, want the first phase that hit the fallback", evs[0].Phase)
	}
	if evs[0].Fields["model"] != ClaudeDefaultModel || evs[0].Fields["provider"] != "claude" {
		t.Errorf("fields = %v, want the claude built-in default", evs[0].Fields)
	}
	if !strings.Contains(evs[0].Msg, ClaudeDefaultModel) {
		t.Errorf("msg = %q, want it to name the model that carried the run", evs[0].Msg)
	}
	if got := modelArg(argv); got != ClaudeDefaultModel {
		t.Errorf("spawned --model = %q, want %q", got, ClaudeDefaultModel)
	}
}

// TestConfiguredModelWarnsNothing pins the quiet path: an explicitly configured
// model is not a fallback, so it spawns unchanged and the run stays warning-free.
func TestConfiguredModelWarnsNothing(t *testing.T) {
	var stream bytes.Buffer
	var argv []string
	c := spawnRefusingClaude(t, "opus-4-8", &ModelFallbackNotice{}, event.New(&stream), &argv)

	if _, err := c.Run(context.Background(), "prompt", "build"); err == nil {
		t.Fatal("Run with a refused spawn should error")
	}
	if evs := fallbackEvents(t, &stream); len(evs) != 0 {
		t.Errorf("emitted %d model_fallback events for a configured model, want 0", len(evs))
	}
	if got := modelArg(argv); got != "opus-4-8" {
		t.Errorf("spawned --model = %q, want the configured opus-4-8", got)
	}
}
