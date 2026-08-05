package agent

import (
	"context"
	"io"
	"path/filepath"
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
		{
			"space-mangled call is still seen",
			"● Skill(vercel- react- best- practices)",
			[]string{"vercel- react- best- practices"},
		},
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

// TestDropSkills pins the transcript's authority over the terminal: a name whose
// every Skill call errored leaves the loaded set, however the PTY drew it.
func TestDropSkills(t *testing.T) {
	cases := []struct {
		name                  string
		names, failed, loaded []string
		want                  []string
	}{
		{"failed name is dropped", []string{"tdd", "code-review"}, []string{"code-review"}, nil, []string{"tdd"}},
		{"nothing failed", []string{"tdd"}, nil, nil, []string{"tdd"}},
		{"every name failed", []string{"tdd"}, []string{"tdd"}, nil, nil},
		{"a mangled attempt leaves the loaded names alone", []string{"tdd"}, []string{"code- review"}, nil, []string{"tdd"}},
		{"the name a mangle snapped to is dropped", []string{"code-review"}, []string{"code_review"}, nil, nil},
		{"a mangle the transcript settled as loaded stays", []string{"code-review"}, []string{"code_review"}, []string{"code-review"}, []string{"code-review"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dropSkills(tc.names, tc.failed, tc.loaded); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("dropSkills(%v, %v, %v) = %v, want %v", tc.names, tc.failed, tc.loaded, got, tc.want)
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
		{"web- feature", ""},
		{"pest- testing", ""},
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

// TestSkillCaptureKeepsSpacedSightingsOut is the COD-1502 live-capture guard: the
// model re-typing a name with a space after every hyphen is a typo the Skill tool
// rejects, not terminal redraw damage the snapper should repair — the sighting has
// to be visible as evidence without ever counting as loaded, however close it sits
// to an installed name.
func TestSkillCaptureKeepsSpacedSightingsOut(t *testing.T) {
	c := newSkillCapture(claudeSkills, newSkillSnapper(mangleInventory))
	_, _ = c.Write([]byte("● Skill(web- feature)\n● Skill(pest-testing)\n"))

	if got, want := c.skills(), []string{"pest-testing"}; !reflect.DeepEqual(got, want) {
		t.Errorf("skills = %v, want %v", got, want)
	}
	if got, want := c.unmatchedSightings(), []string{"web- feature"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unmatchedSightings = %v, want %v", got, want)
	}
}

// TestEnrichSeparatesFailedSkillCalls is the COD-1502 result-boundary guard: the
// transcript decides what loaded, so a Skill call the tool rejected is reported as
// a failed attempt and kept out of Skills — even when the terminal drew it and its
// raw input sits within the snapper's edit-distance tolerance of an installed name.
func TestEnrichSeparatesFailedSkillCalls(t *testing.T) {
	const sessionID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	writeTranscript(t, filepath.Join(root, "projects", "-Users-dev-repo", sessionID+".jsonl"),
		`{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":2},"content":[{"type":"tool_use","id":"t1","name":"Skill","input":{"skill":"tdd"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":12,"output_tokens":2},"content":[{"type":"tool_use","id":"t2","name":"Skill","input":{"skill":"code- review"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","is_error":true}]}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":14,"output_tokens":2},"content":[{"type":"tool_use","id":"t3","name":"Skill","input":{"skill":"golang_pro"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","is_error":true}]}}`,
	)

	live := newSkillCapture(claudeSkills, newSkillSnapper([]string{"tdd", "code-review", "golang-pro"}))
	_, _ = live.Write([]byte("● Skill(tdd)\n● Skill(code- review)\n● Skill(golang_pro)\n"))

	res := (&ClaudeInteractive{}).enrich(Result{}, sessionID, live)

	if want := []string{"tdd"}; !reflect.DeepEqual(res.Skills, want) {
		t.Errorf("Skills = %v, want %v", res.Skills, want)
	}
	if want := []string{"code- review", "golang_pro"}; !reflect.DeepEqual(res.SkillsFailed, want) {
		t.Errorf("SkillsFailed = %v, want %v", res.SkillsFailed, want)
	}
	if want := []string{"code- review"}; !reflect.DeepEqual(res.SkillsUnmatched, want) {
		t.Errorf("SkillsUnmatched = %v, want %v", res.SkillsUnmatched, want)
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
			name:          "a space-mangled call is evidence, never a load",
			output:        "● Skill(bubbletea)\n● Skill(vercel- react- best- practices)\n",
			wantSkills:    []string{"bubbletea"},
			wantUnmatched: []string{"vercel- react- best- practices"},
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
