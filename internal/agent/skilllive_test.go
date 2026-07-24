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
	c := newSkillCapture(claudeSkills)
	_, _ = c.Write([]byte("working... ● Skill(bub"))
	_, _ = c.Write([]byte("bletea)\n  ⎿  Launching skill: tdd\n"))
	if got, want := c.skills(), []string{"bubbletea", "tdd"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skills = %v, want %v", got, want)
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
		name       string
		output     string
		wantSkills []string
		wantKnown  bool
	}{
		{
			"skills seen live without a transcript",
			"● Skill(bubbletea)\n  ⎿  Launching skill: tdd\n",
			[]string{"bubbletea", "tdd"},
			true,
		},
		{
			"no skill named leaves the result unknown",
			"working on the ticket, nothing to load\n",
			nil,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // no transcript can be found

			sess := newScriptSession([]byte(tc.output))
			defer sess.stop()

			var resultPath string
			c := &ClaudeInteractive{
				Bin:             "claude",
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
				if got.res.SkillsKnown != tc.wantKnown {
					t.Errorf("SkillsKnown = %v, want %v", got.res.SkillsKnown, tc.wantKnown)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not return")
			}
		})
	}
}
