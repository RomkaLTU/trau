package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
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
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
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

// seqVerdictRunner writes the next verdict in the sequence on each call (the
// last one repeats), reporting a confirmed empty skill set (SkillsKnown, no
// names), so a fail→repair→pass verify can be driven end-to-end and its
// no-skills warning fires as it would for a real observed run.
type seqVerdictRunner struct {
	path  string
	seq   []verdict
	calls int
}

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

func repoWithSkill(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+name), 0o644); err != nil {
		t.Fatal(err)
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
