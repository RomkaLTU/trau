package tokens

import (
	"fmt"
	"math"
)

// Soft per-phase thresholds. They start loose — a slice that trips any of them spent
// far more than its size implies — and are meant to be tuned down from observed
// medians. Tickets fed to the loop are expected to be single-window by construction,
// so these flat thresholds apply to every ticket.
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

// PhaseSpend is one phase's accumulated output/turns/cost — the input to
// [DetectAnomalies], folded by the sink from the phase's in-session calls.
type PhaseSpend struct {
	Phase  string
	Output int
	Turns  int
	Cost   float64
}

// DetectAnomalies flags each phase whose spend cleared a soft threshold, preserving
// the given order, and returns one Anomaly per tripped phase with its reasons most
// cost-relevant first. A phase under every threshold is dropped. Cost is rounded to
// cents.
func DetectAnomalies(phases []PhaseSpend) []Anomaly {
	var out []Anomaly
	for _, p := range phases {
		var reasons []string
		if p.Cost > anomalyCostUSD {
			reasons = append(reasons, fmt.Sprintf("cost $%.2f > $%.2f", p.Cost, anomalyCostUSD))
		}
		if p.Output > anomalyOutputTokens {
			reasons = append(reasons, fmt.Sprintf("output %d > %d", p.Output, anomalyOutputTokens))
		}
		if p.Turns > anomalyTurns {
			reasons = append(reasons, fmt.Sprintf("turns %d > %d", p.Turns, anomalyTurns))
		}
		if len(reasons) == 0 {
			continue
		}
		out = append(out, Anomaly{
			Phase:   p.Phase,
			Output:  p.Output,
			Turns:   p.Turns,
			Cost:    math.Round(p.Cost*100) / 100,
			Reasons: reasons,
		})
	}
	return out
}
