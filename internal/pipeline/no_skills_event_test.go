package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
)

// TestWarnBuildWithoutSkillsEmitsEvent guards the serve-mode visibility of a
// skill-less build: the durable build_no_skills event fires only when the prompt
// named a skill set and the build is confirmed to have loaded none of it, carrying
// the ticket and the build phase. Any other combination — skills loaded, nothing
// named, no skills expected, or an Unknown result with no recoverable evidence —
// stays silent so the web UI never flags a healthy or unobserved run.
func TestWarnBuildWithoutSkillsEmitsEvent(t *testing.T) {
	expected := func(string) bool { return true }
	named := []string{"golang-code-style"}

	cases := []struct {
		name    string
		expects func(string) bool
		named   []string
		skills  []string
		known   bool
		want    bool
	}{
		{"named a set and confirmed none loaded", expected, named, nil, true, true},
		{"named a set but result is unknown", expected, named, nil, false, false},
		{"named a set and some loaded", expected, named, []string{"golang-code-style"}, true, false},
		{"named nothing", expected, nil, nil, true, false},
		{"no skills expected", func(string) bool { return false }, named, nil, true, false},
		{"gating disabled", nil, named, nil, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := newTestPipeline(t, routedRunner{provider: "claude"}, &fakeTracker{})
			p.Events = event.New(&buf)
			p.SkillsExpected = tc.expects
			p.buildProvider = "claude"
			p.buildSkills = tc.skills
			p.buildSkillsKnown = tc.known

			p.warnBuildWithoutSkills("COD-1", tc.named)

			evs := kindEvents(t, &buf, event.KindBuildNoSkills)
			if tc.want {
				if len(evs) != 1 {
					t.Fatalf("emitted %d build_no_skills events, want exactly 1", len(evs))
				}
				ev := evs[0]
				if got := strField(ev.Fields, "ticket"); got != "COD-1" {
					t.Errorf("ticket = %q, want %q", got, "COD-1")
				}
				if ev.Phase != "build" {
					t.Errorf("phase = %q, want %q", ev.Phase, "build")
				}
				return
			}
			if len(evs) != 0 {
				t.Fatalf("emitted %d build_no_skills events, want 0", len(evs))
			}
		})
	}
}

// TestWarnVerifyWithoutSkillsEmitsEvent mirrors the build guard for the QA
// phase: the durable verify_no_skills event fires only when the prompt named a
// skill set and the primary verify loaded none of it.
func TestWarnVerifyWithoutSkillsEmitsEvent(t *testing.T) {
	expected := func(string) bool { return true }
	named := []string{"tdd"}

	cases := []struct {
		name    string
		expects func(string) bool
		named   []string
		skills  []string
		known   bool
		want    bool
	}{
		{"named a set and confirmed none loaded", expected, named, nil, true, true},
		{"named a set but result is unknown", expected, named, nil, false, false},
		{"named a set and some loaded", expected, named, []string{"tdd"}, true, false},
		{"named nothing", expected, nil, nil, true, false},
		{"no skills expected", func(string) bool { return false }, named, nil, true, false},
		{"gating disabled", nil, named, nil, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := newTestPipeline(t, routedRunner{provider: "claude"}, &fakeTracker{})
			p.Events = event.New(&buf)
			p.SkillsExpected = tc.expects
			p.verifyProvider = "claude"
			p.verifySkills = tc.skills
			p.verifySkillsKnown = tc.known

			p.warnVerifyWithoutSkills("COD-1", tc.named)

			evs := kindEvents(t, &buf, event.KindVerifyNoSkills)
			if tc.want {
				if len(evs) != 1 {
					t.Fatalf("emitted %d verify_no_skills events, want exactly 1", len(evs))
				}
				ev := evs[0]
				if got := strField(ev.Fields, "ticket"); got != "COD-1" {
					t.Errorf("ticket = %q, want %q", got, "COD-1")
				}
				if ev.Phase != "verify" {
					t.Errorf("phase = %q, want %q", ev.Phase, "verify")
				}
				return
			}
			if len(evs) != 0 {
				t.Fatalf("emitted %d verify_no_skills events, want 0", len(evs))
			}
		})
	}
}

// TestWarnSkillLoadFailedEmitsEvent is the COD-1502 partial-failure guard: a
// prompt-named skill the agent tried to load and could not is warned about by
// name — next to the raw input it typed — even when other skills loaded. A skill
// a later retry loaded is the nudge working, not a failure, and evidence that
// resolves to nothing the prompt named stays debug-only.
func TestWarnSkillLoadFailedEmitsEvent(t *testing.T) {
	named := []string{"tdd", "vercel-react-best-practices"}

	cases := []struct {
		name        string
		named       []string
		skills      []string
		attempts    []string
		known       bool
		wantSkill   string
		wantAttempt string
	}{
		{
			name:        "one skill loads while another is mangled",
			named:       named,
			skills:      []string{"tdd"},
			attempts:    []string{"vercel- react- best- practices"},
			known:       true,
			wantSkill:   "vercel-react-best-practices",
			wantAttempt: "vercel- react- best- practices",
		},
		{
			name:        "nothing loaded at all",
			named:       named,
			attempts:    []string{"vercel- react- best- practices"},
			known:       true,
			wantSkill:   "vercel-react-best-practices",
			wantAttempt: "vercel- react- best- practices",
		},
		{
			name:        "a mangle carrying no space at all",
			named:       []string{"tdd", "code-review"},
			skills:      []string{"tdd"},
			attempts:    []string{"code_review"},
			known:       true,
			wantSkill:   "code-review",
			wantAttempt: "code_review",
		},
		{
			name:     "a failed attempt the agent then retried correctly",
			named:    named,
			skills:   []string{"tdd", "vercel-react-best-practices"},
			attempts: []string{"vercel- react- best- practices"},
			known:    true,
		},
		{
			name:     "an attempt at a skill the prompt never named",
			named:    []string{"tdd"},
			skills:   []string{"tdd"},
			attempts: []string{"artifact- design"},
			known:    true,
		},
		{
			name:   "no evidence of an attempt",
			named:  named,
			skills: []string{"tdd"},
			known:  true,
		},
		{
			name:     "an unobserved run stays silent",
			named:    named,
			attempts: []string{"vercel- react- best- practices"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := newTestPipeline(t, routedRunner{provider: "claude"}, &fakeTracker{})
			p.Events = event.New(&buf)
			p.SkillsExpected = func(string) bool { return true }
			p.buildProvider = "claude"
			p.buildSkills = tc.skills
			p.buildSkillAttempts = tc.attempts
			p.buildSkillsKnown = tc.known

			p.warnBuildWithoutSkills("COD-1", tc.named)

			evs := kindEvents(t, &buf, event.KindSkillLoadFailed)
			if tc.wantSkill == "" {
				if len(evs) != 0 {
					t.Fatalf("emitted %d skill_load_failed events, want 0", len(evs))
				}
				return
			}
			if len(evs) != 1 {
				t.Fatalf("emitted %d skill_load_failed events, want exactly 1", len(evs))
			}
			ev := evs[0]
			if ev.Phase != "build" {
				t.Errorf("phase = %q, want %q", ev.Phase, "build")
			}
			if got := strField(ev.Fields, "ticket"); got != "COD-1" {
				t.Errorf("ticket = %q, want %q", got, "COD-1")
			}
			skill, attempt := firstAttempt(t, ev)
			if skill != tc.wantSkill {
				t.Errorf("attempt skill = %q, want %q", skill, tc.wantSkill)
			}
			if attempt != tc.wantAttempt {
				t.Errorf("attempt raw input = %q, want %q", attempt, tc.wantAttempt)
			}
			for _, want := range []string{tc.wantSkill, tc.wantAttempt} {
				if !strings.Contains(ev.Msg, want) {
					t.Errorf("message %q does not name %q", ev.Msg, want)
				}
			}
		})
	}
}

// TestNoSkillsWarningNamesMangledAttempts pins the zero-loaded warning's evidence:
// it fires exactly as before, but when the phase left mangled attempts behind it
// names them instead of the bare generic line and carries them in the payload.
func TestNoSkillsWarningNamesMangledAttempts(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPipeline(t, routedRunner{provider: "claude"}, &fakeTracker{})
	p.Events = event.New(&buf)
	p.SkillsExpected = func(string) bool { return true }
	p.verifyProvider = "claude"
	p.verifySkillsKnown = true
	p.verifySkillAttempts = []string{"code- review"}

	p.warnVerifyWithoutSkills("COD-1", []string{"code-review"})

	evs := kindEvents(t, &buf, event.KindVerifyNoSkills)
	if len(evs) != 1 {
		t.Fatalf("emitted %d verify_no_skills events, want exactly 1", len(evs))
	}
	ev := evs[0]
	for _, want := range []string{"verify loaded no skills", "code-review", "code- review"} {
		if !strings.Contains(ev.Msg, want) {
			t.Errorf("message %q does not name %q", ev.Msg, want)
		}
	}
	if skill, attempt := firstAttempt(t, ev); skill != "code-review" || attempt != "code- review" {
		t.Errorf("attempt = %q/%q, want %q/%q", skill, attempt, "code-review", "code- review")
	}
}

// TestBuildCarriesFailedSkillAttemptsIntoTheWarning drives the whole build phase:
// the agent result's failed attempts have to survive the phase capture and reach
// the warning, with the loaded skill suppressing the zero-loaded event.
func TestBuildCarriesFailedSkillAttemptsIntoTheWarning(t *testing.T) {
	id := "COD-91502"
	writeHandoff(t, id)
	var buf bytes.Buffer
	p := newTestPipeline(t, skillResultRunner{res: agent.Result{
		Skills:       []string{"web-feature"},
		SkillsKnown:  true,
		SkillsFailed: []string{"td d"},
	}}, &fakeTracker{})
	p.Events = event.New(&buf)
	p.SkillsExpected = func(string) bool { return true }
	p.RepoRoot = repoWithSkill(t, "web-feature", "tdd")

	if err := p.build(context.Background(), id, false); err != nil {
		t.Fatalf("build: %v", err)
	}

	evs := kindEvents(t, &buf, event.KindSkillLoadFailed)
	if len(evs) != 1 {
		t.Fatalf("emitted %d skill_load_failed events, want exactly 1", len(evs))
	}
	if skill, attempt := firstAttempt(t, evs[0]); skill != "tdd" || attempt != "td d" {
		t.Errorf("attempt = %q/%q, want %q/%q", skill, attempt, "tdd", "td d")
	}
	if evs := kindEvents(t, &buf, event.KindBuildNoSkills); len(evs) != 0 {
		t.Fatalf("emitted %d build_no_skills events for a build that loaded a skill, want 0", len(evs))
	}
}

// skillResultRunner answers every phase with one fixed skill outcome, so a phase
// can be driven end to end over a chosen mix of loaded and failed Skill calls.
type skillResultRunner struct{ res agent.Result }

func (r skillResultRunner) Route(string) (string, string, string) { return "claude", "", "" }

func (r skillResultRunner) Run(context.Context, string, string) (agent.Result, error) {
	return r.res, nil
}

// firstAttempt reads the failed-attempt pair an event carries: the raw name the
// agent typed and the skill it was meant to be.
func firstAttempt(t *testing.T, ev event.Event) (skill, attempt string) {
	t.Helper()
	attempts, ok := ev.Fields["attempts"].([]any)
	if !ok || len(attempts) == 0 {
		t.Fatalf("event %s carries no attempts: %v", ev.Kind, ev.Fields)
	}
	pair, ok := attempts[0].(map[string]any)
	if !ok {
		t.Fatalf("attempt is not an object: %v", attempts[0])
	}
	return strField(pair, "skill"), strField(pair, "attempt")
}

// seqVerdictRunner writes the next verdict in the sequence on each call (the
// last one repeats), reporting a confirmed empty skill set (SkillsKnown, no
// names), so a fail→repair→pass verify can be driven end-to-end and its
// no-skills warning fires as it would for a real observed run.
type seqVerdictRunner struct {
	path  string
	seq   []verdict
	calls int
}

func (r *seqVerdictRunner) Route(string) (string, string, string) { return "claude", "", "" }

func (r *seqVerdictRunner) Run(context.Context, string, string) (agent.Result, error) {
	i := r.calls
	if i >= len(r.seq) {
		i = len(r.seq) - 1
	}
	r.calls++
	data, _ := json.Marshal(r.seq[i])
	_ = os.WriteFile(r.path, data, 0o644)
	return agent.Result{SkillsKnown: true}, nil
}

// TestVerifyNoSkillsEmittedExactlyOnce runs the whole Verify phase — a failing
// first attempt, one repair, a passing retry — and asserts the skill-less run
// produced exactly one verify_no_skills event, keyed to the first attempt. A repo
// that installs no skills names none either, so it stays silent.
func TestVerifyNoSkillsEmittedExactlyOnce(t *testing.T) {
	cases := []struct {
		name     string
		repoRoot func(*testing.T) string
		want     int
	}{
		{"repo with skills", func(t *testing.T) string { return repoWithSkill(t, "golang-code-style") }, 1},
		{"repo without skills", func(t *testing.T) string { return t.TempDir() }, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "COD-91061"
			writeHandoff(t, id)
			runner := &seqVerdictRunner{path: verifyPath(id), seq: []verdict{
				{Pass: false, Summary: "boom", Failures: []string{"boom"}},
				{Pass: true, Summary: "ok"},
			}}
			var buf bytes.Buffer
			p := newTestPipeline(t, runner, &fakeTracker{})
			p.Events = event.New(&buf)
			p.SkillsExpected = func(string) bool { return true }
			p.RepoRoot = tc.repoRoot(t)
			p.MaxRepairs = 1

			if err := p.Verify(context.Background(), id); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if evs := kindEvents(t, &buf, event.KindVerifyNoSkills); len(evs) != tc.want {
				t.Fatalf("emitted %d verify_no_skills events, want %d", len(evs), tc.want)
			}
		})
	}
}

func repoWithSkill(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		dir := filepath.Join(root, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func kindEvents(t *testing.T, buf *bytes.Buffer, kind string) []event.Event {
	t.Helper()
	var out []event.Event
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}
