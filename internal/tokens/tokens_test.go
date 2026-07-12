package tokens

import "testing"

func TestEstimateCostByModelTier(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  float64
	}{
		{"opus input+output", "claude-opus-4-8", 5 + 25},
		{"sonnet cheaper", "claude-sonnet-5", 3 + 15},
		{"haiku cheapest", "claude-haiku-4-5", 1 + 5},
		{"unknown model is free", "some-mystery-model", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateCost(tc.model, 1_000_000, 1_000_000, 0, 0)
			if got != tc.want {
				t.Errorf("EstimateCost(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestEstimateCostCacheTiers(t *testing.T) {
	// Cache reads bill at 0.1x input, 5-minute cache writes at 1.25x input.
	got := EstimateCost("claude-opus-4-8", 0, 0, 1_000_000, 1_000_000)
	want := 5*0.1 + 5*1.25
	if got != want {
		t.Errorf("cache cost = %v, want %v", got, want)
	}
}

func TestProviderForModel(t *testing.T) {
	tests := map[string]string{
		"claude-opus-4-8": "claude",
		"claude-sonnet-5": "claude",
		"fable-5":         "claude",
		"gpt-5.4":         "codex",
		"codex-mini":      "codex",
		"kimi-k2":         "kimi",
		"moonshot-v1":     "kimi",
		"":                "",
		"llama-3":         "",
	}
	for model, want := range tests {
		if got := ProviderForModel(model); got != want {
			t.Errorf("ProviderForModel(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestDetectAnomaliesTripsHotPhases(t *testing.T) {
	phases := []PhaseSpend{
		{Phase: "build", Output: 500, Turns: 4, Cost: 0.30},       // quiet
		{Phase: "cleanup", Output: 120_000, Turns: 8, Cost: 6.50}, // cost + output
		{Phase: "verify", Output: 100, Turns: 40, Cost: 0.10},     // turns only
	}
	got := DetectAnomalies(phases)
	if len(got) != 2 {
		t.Fatalf("anomalies = %d, want 2 (cleanup and verify), got %+v", len(got), got)
	}
	if got[0].Phase != "cleanup" {
		t.Errorf("first anomaly = %q, want cleanup (order preserved)", got[0].Phase)
	}
	if got[0].Cost != 6.5 || len(got[0].Reasons) != 2 {
		t.Errorf("cleanup anomaly = %+v, want $6.50 with cost+output reasons", got[0])
	}
	if got[1].Phase != "verify" || len(got[1].Reasons) != 1 {
		t.Errorf("verify anomaly = %+v, want a single turns reason", got[1])
	}
}

func TestDetectAnomaliesQuiet(t *testing.T) {
	got := DetectAnomalies([]PhaseSpend{{Phase: "build", Output: 100, Turns: 2, Cost: 0.10}})
	if got != nil {
		t.Errorf("quiet phases flagged %+v, want none", got)
	}
}
