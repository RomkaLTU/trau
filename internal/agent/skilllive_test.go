package agent

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestClaudeSkills(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"tool-call header", "● Skill(bubbletea)", []string{"bubbletea"}},
		{"ansi wrapped", "\x1b[1m\x1b[38;5;5mSkill(golang-code-style)\x1b[0m", []string{"golang-code-style"}},
		{"launch line", "  ⎿  Launching skill: tdd", []string{"tdd"}},
		{"plugin scope", "Skill(samber:golang-lint)", []string{"samber:golang-lint"}},
		{
			"both markers keep first-seen order",
			"● Skill(alpha)\n  ⎿  Launching skill: beta\n● Skill(gamma)",
			[]string{"alpha", "beta", "gamma"},
		},
		{"prose without parens", "used the Skill tool but named none", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeSkills(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("claudeSkills(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMergeSkills(t *testing.T) {
	cases := []struct {
		name             string
		live, transcript []string
		want             []string
	}{
		{"live leads, transcript adds", []string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{"transcript only", nil, []string{"x"}, []string{"x"}},
		{"live only", []string{"y"}, nil, []string{"y"}},
		{"both empty", nil, nil, nil},
		{"dedup within live", []string{"z", "z"}, nil, []string{"z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeSkills(tc.live, tc.transcript); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeSkills(%v, %v) = %v, want %v", tc.live, tc.transcript, got, tc.want)
			}
		})
	}
}

// TestSkillCaptureAcrossWrites pins that a marker split across two teed writes is
// still recovered — the rolling tail rescans the boundary.
func TestSkillCaptureAcrossWrites(t *testing.T) {
	c := newSkillCapture(claudeSkills, newSkillSnapper([]string{"bubbletea", "tdd"}))
	_, _ = c.Write([]byte("working... ● Skill(bub"))
	_, _ = c.Write([]byte("bletea)\n  ⎿  Launching skill: tdd\n"))
	if got, want := c.skills(), []string{"bubbletea", "tdd"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skills = %v, want %v", got, want)
	}
}

// mangleInventory is the installed set the observed mangles were drawn against.
var mangleInventory = []string{
	"golang-code-style",
	"laravel-best-practices",
	"pest-testing",
	"typescript-expert",
	"web-feature",
}

func TestSkillSnapper(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"web-feature", "web-feature"},
		{"wb-eature", "web-feature"},
		{"typescipt-expt", "typescript-expert"},
		{"typescript-exprt", "typescript-expert"},
		{"typscrpt-expert", "typescript-expert"},
		{"olang-code-style", "golang-code-style"},
		{"larave-best-practices", "laravel-best-practices"},
		{"pst-testing", "pest-testing"},
		{"pt-testing", "pest-testing"},
		{"artifact-design", ""},
		{"typescr", ""},
		{"web", ""},
	}
	snap := newSkillSnapper(mangleInventory)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := snap.snap(tc.name)
			if ok != (tc.want != "") {
				t.Fatalf("snap(%q) matched = %v, want %v", tc.name, ok, tc.want != "")
			}
			if got != tc.want {
				t.Fatalf("snap(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestSkillSnapperDuplicateInventory pins the shape NameableSkills returns for a
// repo that installs a known external skill: the name arrives twice, and the
// second copy must not read as an ambiguous tie against the first.
func TestSkillSnapperDuplicateInventory(t *testing.T) {
	snap := newSkillSnapper([]string{"browser-harness", "web-feature", "browser-harness"})
	got, ok := snap.snap("brower-harness")
	if !ok || got != "browser-harness" {
		t.Fatalf("snap = %q, %v, want %q, true", got, ok, "browser-harness")
	}
}

// TestSkillSnapperEmptyInventory pins that a repo with nothing installed can never
// contribute a loaded name: there is no inventory to vouch for the sighting.
func TestSkillSnapperEmptyInventory(t *testing.T) {
	if got, ok := newSkillSnapper(nil).snap("web-feature"); ok {
		t.Fatalf("snap = %q, want no match", got)
	}
}

// TestSkillSnapperAll is the transcript-reconcile side: names the session JSONL
// settles are held to the same inventory, and what it cannot vouch for lands in
// the debug list instead of the loaded set.
func TestSkillSnapperAll(t *testing.T) {
	loaded, unmatched := newSkillSnapper(mangleInventory).snapAll([]string{
		"pest-testing",
		"pst-testing",
		"artifact-design",
	})
	if want := []string{"pest-testing"}; !reflect.DeepEqual(loaded, want) {
		t.Errorf("loaded = %v, want %v", loaded, want)
	}
	if want := []string{"artifact-design"}; !reflect.DeepEqual(unmatched, want) {
		t.Errorf("unmatched = %v, want %v", unmatched, want)
	}
}

// TestSkillCaptureSnapsMangles pins that one call whose PTY drew three renderings
// of the same skill records that skill once, and that a sighting no installed
// skill accounts for stays out of the loaded set.
func TestSkillCaptureSnapsMangles(t *testing.T) {
	c := newSkillCapture(claudeSkills, newSkillSnapper(mangleInventory))
	_, _ = c.Write([]byte("● Skill(typescipt-expt)\n  ⎿  Launching skill: typescript-exprt\n"))
	_, _ = c.Write([]byte("● Skill(typscrpt-expert)\n● Skill(wb-eature)\n● Skill(artifact-design)\n"))

	if got, want := c.skills(), []string{"typescript-expert", "web-feature"}; !reflect.DeepEqual(got, want) {
		t.Errorf("skills = %v, want %v", got, want)
	}
	if got, want := c.unmatchedSightings(), []string{"artifact-design"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unmatchedSightings = %v, want %v", got, want)
	}
}

// scriptSession delivers one scripted chunk of terminal output, then signals it
// has been drained and blocks until the run kills it — so a test can let the live
// capture consume the output before ending the run.
type scriptSession struct {
	chunk    []byte
	sent     bool
	consumed chan struct{}
	done     chan struct{}
	onceC    sync.Once
	onceD    sync.Once
}

func newScriptSession(chunk []byte) *scriptSession {
	return &scriptSession{chunk: chunk, consumed: make(chan struct{}), done: make(chan struct{})}
}

func (s *scriptSession) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		return copy(p, s.chunk), nil
	}
	s.onceC.Do(func() { close(s.consumed) })
	<-s.done
	return 0, io.EOF
}

func (s *scriptSession) Write(p []byte) (int, error) { return len(p), nil }
func (s *scriptSession) Wait() error                 { <-s.done; return nil }
func (s *scriptSession) stop()                       { s.onceD.Do(func() { close(s.done) }) }
func (s *scriptSession) Close() error                { s.stop(); return nil }
func (s *scriptSession) Kill() error                 { s.stop(); return nil }

// TestClaudeLiveCaptureRecordsSkills is the COD-1136 guard: with no session
// transcript on disk (the flush is delayed or lost), the loaded skills are still
// recovered from the live PTY and the result reports them as known. A run whose
// output names no skill reports the Unknown state, not a confirmed empty set.
func TestClaudeLiveCaptureRecordsSkills(t *testing.T) {
	cases := []struct {
		name          string
		output        string
		wantSkills    []string
		wantUnmatched []string
		wantKnown     bool
	}{
		{
			name:       "skills seen live without a transcript",
			output:     "● Skill(bubbletea)\n  ⎿  Launching skill: tdd\n",
			wantSkills: []string{"bubbletea", "tdd"},
			wantKnown:  true,
		},
		{
			name:          "mangled draws snap to the installed names",
			output:        "● Skill(bubbleta)\n● Skill(bubbltea)\n● Skill(artifact-design)\n",
			wantSkills:    []string{"bubbletea"},
			wantUnmatched: []string{"artifact-design"},
			wantKnown:     true,
		},
		{
			name:      "no skill named leaves the result unknown",
			output:    "working on the ticket, nothing to load\n",
			wantKnown: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // no transcript can be found

			repo := t.TempDir()
			mkSkill(t, repo, ".agents/skills", "bubbletea")
			mkSkill(t, repo, ".agents/skills", "tdd")

			sess := newScriptSession([]byte(tc.output))
			defer sess.stop()

			var resultPath string
			c := &ClaudeInteractive{
				Bin:             "claude",
				Dir:             repo,
				ResultDir:       t.TempDir(),
				TrustPromptWait: time.Millisecond,
				start:           finishOnResultPath(t, sess, &resultPath),
			}

			type outcome struct {
				res Result
				err error
			}
			ch := make(chan outcome, 1)
			go func() {
				res, err := c.Run(context.Background(), "do the thing", "build")
				ch <- outcome{res, err}
			}()

			select {
			case <-sess.consumed:
			case <-time.After(3 * time.Second):
				t.Fatal("live output was never drained")
			}
			writeResult(t, resultPath)

			select {
			case got := <-ch:
				if got.err != nil {
					t.Fatalf("Run: %v", got.err)
				}
				if !reflect.DeepEqual(got.res.Skills, tc.wantSkills) {
					t.Errorf("Skills = %v, want %v", got.res.Skills, tc.wantSkills)
				}
				if !reflect.DeepEqual(got.res.SkillsUnmatched, tc.wantUnmatched) {
					t.Errorf("SkillsUnmatched = %v, want %v", got.res.SkillsUnmatched, tc.wantUnmatched)
				}
				if got.res.SkillsKnown != tc.wantKnown {
					t.Errorf("SkillsKnown = %v, want %v", got.res.SkillsKnown, tc.wantKnown)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not return")
			}
		})
	}
}
