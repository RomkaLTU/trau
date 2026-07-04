package tokens

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Soft per-phase thresholds. They start loose — a slice that trips any of them
// spent far more than its size implies — and are meant to be tuned down from
// observed medians. Tickets fed to the loop are expected to be single-window by
// construction, so these flat thresholds apply to every ticket.
const (
	anomalyOutputTokens = 25_000
	anomalyTurns        = 25
	anomalyCostUSD      = 3.0
)

// Anomaly is one phase whose spend cleared a soft threshold. Reasons lists every
// threshold it tripped, most cost-relevant first.
type Anomaly struct {
	Phase   string   `json:"phase"`
	Output  int      `json:"output"`
	Turns   int      `json:"turns"`
	Cost    float64  `json:"cost_usd"`
	Reasons []string `json:"reasons"`
}

// Flag detects post-run cost anomalies for id and records them to
// runs/<id>/anomalies.jsonl, returning the trips. It sums each phase's
// output/turns/cost recorded by THIS process — spend loaded from an earlier
// run's tokens.jsonl is never re-flagged on resume — and flags any phase over a
// soft threshold. I/O errors are swallowed (same contract as Append): flagging
// never aborts the loop.
func (s *Sink) Flag(id string) []Anomaly {
	s.mu.Lock()
	sp := s.session[id]
	s.mu.Unlock()
	if sp == nil {
		return nil
	}

	var anomalies []Anomaly
	for _, phase := range sp.order {
		a := sp.phases[phase]
		var reasons []string
		if a.cost > anomalyCostUSD {
			reasons = append(reasons, fmt.Sprintf("cost $%.2f > $%.2f", a.cost, anomalyCostUSD))
		}
		if a.output > anomalyOutputTokens {
			reasons = append(reasons, fmt.Sprintf("output %d > %d", a.output, anomalyOutputTokens))
		}
		if a.turns > anomalyTurns {
			reasons = append(reasons, fmt.Sprintf("turns %d > %d", a.turns, anomalyTurns))
		}
		if len(reasons) == 0 {
			continue
		}
		anomalies = append(anomalies, Anomaly{
			Phase:   phase,
			Output:  a.output,
			Turns:   a.turns,
			Cost:    math.Round(a.cost*100) / 100,
			Reasons: reasons,
		})
	}

	s.writeAnomalies(id, anomalies)
	return anomalies
}

// writeAnomalies overwrites runs/<id>/anomalies.jsonl with one line per anomaly so
// a re-run (resume) reflects current totals rather than appending duplicates. A run
// with no anomalies leaves any prior file untouched.
func (s *Sink) writeAnomalies(id string, anomalies []Anomaly) {
	if len(anomalies) == 0 {
		return
	}
	dir := filepath.Join(s.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	var buf bytes.Buffer
	ts := s.now().Format("2006-01-02T15:04:05")
	for _, a := range anomalies {
		data, err := json.Marshal(struct {
			TS string `json:"ts"`
			Anomaly
		}{TS: ts, Anomaly: a})
		if err != nil {
			continue
		}
		buf.Write(append(data, '\n'))
	}
	_ = os.WriteFile(filepath.Join(dir, "anomalies.jsonl"), buf.Bytes(), 0o644)
}
