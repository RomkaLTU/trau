package planning

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// transcriptFile is the durable, append-only Q&A transcript within a session
// directory — one JSON line per answered round.
const transcriptFile = "transcript.jsonl"

// Answer is one answered question in a Q&A round: the question as asked plus the
// values the user chose — a single entry for a single-select or free-text answer,
// several for a multi-select. Skipped marks an answer that took the question's
// stated default rather than an explicit choice.
type Answer struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Values   []string `json:"values"`
	Skipped  bool     `json:"skipped,omitempty"`
}

// QARound is one round of the durable transcript — the answers the user gave to
// the questions a single planning round asked. Each QARound is one JSON line,
// appended in round order and never rewritten, so the accumulated file is the
// whole planning conversation on disk that the next fresh round re-reads.
type QARound struct {
	Round   int      `json:"round"`
	Answers []Answer `json:"answers"`
}

// AppendRound appends one answered Q&A round to the durable transcript. The file
// is append-only — a round is never rewritten.
func (s *Session) AppendRound(round QARound) error {
	line, err := json.Marshal(round)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, transcriptFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Transcript reads the durable Q&A transcript in round order. A missing file is
// an empty transcript, not an error — a session that has not yet asked anything.
func (s *Session) Transcript() ([]QARound, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, transcriptFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rounds []QARound
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r QARound
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("planning transcript: %w", err)
		}
		rounds = append(rounds, r)
	}
	return rounds, nil
}
