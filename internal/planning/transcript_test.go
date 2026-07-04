package planning

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestTranscriptRoundTrip appends several rounds and reads them back in order,
// covering the durable append-only artifact the next round re-reads.
func TestTranscriptRoundTrip(t *testing.T) {
	sess := OpenSession(t.TempDir())

	if got, err := sess.Transcript(); err != nil || got != nil {
		t.Fatalf("empty transcript: got %v, err %v; want nil, nil", got, err)
	}

	rounds := []QARound{
		{Round: 1, Answers: []Answer{
			{ID: "q1", Question: "scope?", Values: []string{"backend"}},
			{ID: "q2", Question: "who?", Values: []string{"admins", "editors"}},
		}},
		{Round: 2, Answers: []Answer{
			{ID: "q3", Question: "name?", Values: []string{"Widgets"}, Skipped: true},
		}},
	}
	for _, r := range rounds {
		if err := sess.AppendRound(r); err != nil {
			t.Fatalf("AppendRound(%d): %v", r.Round, err)
		}
	}

	got, err := sess.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if !reflect.DeepEqual(got, rounds) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, rounds)
	}
}

// TestTranscriptAppendOnly checks a second append adds a line rather than
// rewriting the file, so the transcript accumulates.
func TestTranscriptAppendOnly(t *testing.T) {
	dir := t.TempDir()
	sess := OpenSession(dir)
	for i := 1; i <= 3; i++ {
		if err := sess.AppendRound(QARound{Round: i}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, transcriptFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Errorf("transcript has %d lines, want 3", len(lines))
	}
}
