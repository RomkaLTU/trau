package pipeline

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
)

// TestWarnBuildWithoutSkillsEmitsEvent guards the serve-mode visibility of a
// skill-less build: the durable build_no_skills event fires only when the repo
// expected skills and the build loaded none, carrying the ticket and the build
// phase. Any other combination — skills loaded, or no skills expected — stays
// silent so the web UI never flags a healthy run.
func TestWarnBuildWithoutSkillsEmitsEvent(t *testing.T) {
	expected := func(string) bool { return true }

	cases := []struct {
		name    string
		expects func(string) bool
		skills  []string
		want    bool
	}{
		{"skills expected and none loaded", expected, nil, true},
		{"skills expected but some loaded", expected, []string{"golang-code-style"}, false},
		{"no skills expected", func(string) bool { return false }, nil, false},
		{"gating disabled", nil, nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Events = event.New(&buf)
			p.SkillsExpected = tc.expects
			p.buildProvider = "claude"
			p.buildSkills = tc.skills

			p.warnBuildWithoutSkills("COD-1")

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
