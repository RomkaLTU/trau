// Package tokens holds the loop's normalized per-call token/cost accounting
// primitives: the [Record] a provider call produces, the pricing model behind
// [EstimateCost], the provider/model mapping the cost views group by, and the
// soft-threshold anomaly detection ([DetectAnomalies]). The accounting itself is
// persisted DB-first through the hub (ADR 0008) — the child's hub-backed sink is
// internal/hubtokens and the authoritative store is internal/hubstore — so this
// package stays storage-agnostic and both sides share one normalization and pricing
// contract.
//
// Normalization: input is stored as the non-cached portion for every provider
// (claude's usage.input_tokens already excludes cache; codex's includes it and is
// adjusted), so the columns and the total mean the same thing regardless of
// backend. The per-call total is input+output+cache_read+cache_creation; a
// zero-total call is uncaptured and dropped by the sink.
package tokens

import "strings"

// Record is one call's raw, already-normalized counts, handed to the sink's Append.
// Input is the NON-cached input portion (see the package doc). CostUSD is a pointer
// so a provider that reports no per-call cost (codex on a ChatGPT-plan login) records
// a null; the claude path always supplies a value (0 when the field is absent).
type Record struct {
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
	Reasoning     int
	CostUSD       *float64
	Turns         int
	IsError       bool
	Provider      string
	Model         string
	Context       int
	Skills        []string
}

// DetailCost is the analytics reader's finest grain: one (date, provider, model,
// phase) cell of spend, keeping the model and its resolved provider so callers can
// regroup and filter along any dimension. Cost is left unrounded so a caller folding
// cells across repos rounds once at the end; Metered is false when any contributing
// call recorded no per-call cost.
type DetailCost struct {
	Date     string
	Phase    string
	Provider string
	Model    string
	Tokens   int
	Cost     float64
	Metered  bool
}

type modelRate struct{ input, output float64 }

var rates = []struct {
	match string
	modelRate
}{
	{"opus-4-8", modelRate{5, 25}},
	{"opus-4-7", modelRate{5, 25}},
	{"opus-4-6", modelRate{5, 25}},
	{"opus-4-5", modelRate{5, 25}},
	{"opus", modelRate{5, 25}},
	{"sonnet-5", modelRate{3, 15}},
	{"sonnet-4-6", modelRate{3, 15}},
	{"sonnet", modelRate{3, 15}},
	{"haiku-4-5", modelRate{1, 5}},
	{"haiku", modelRate{1, 5}},
	{"fable-5", modelRate{10, 50}},
	{"fable", modelRate{10, 50}},
	{"mythos-5", modelRate{10, 50}},
}

// EstimateCost returns the notional USD cost of one call from its token counts and
// the model that ran it. Cache reads bill at 0.1× input, 5-minute cache writes at
// 1.25× input. Returns 0 for an unknown/empty model so an unpriced call contributes
// nothing rather than a wrong number.
func EstimateCost(model string, input, output, cacheRead, cacheCreation int) float64 {
	r, ok := rateFor(model)
	if !ok {
		return 0
	}
	const m = 1_000_000.0
	return float64(input)*r.input/m +
		float64(output)*r.output/m +
		float64(cacheRead)*(r.input*0.1)/m +
		float64(cacheCreation)*(r.input*1.25)/m
}

func rateFor(model string) (modelRate, bool) {
	for _, r := range rates {
		if strings.Contains(model, r.match) {
			return r.modelRate, true
		}
	}
	return modelRate{}, false
}

// ProviderForModel maps a recorded model id back to the provider that served it,
// mirroring the built-in provider set (claude / codex / kimi). It is the read-side
// fallback for historical token lines logged before the provider was recorded
// inline; an unrecognized or empty model yields "" so callers bucket it as unknown.
func ProviderForModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case m == "":
		return ""
	case strings.Contains(m, "claude"), strings.Contains(m, "opus"),
		strings.Contains(m, "sonnet"), strings.Contains(m, "haiku"),
		strings.Contains(m, "fable"), strings.Contains(m, "mythos"):
		return "claude"
	case strings.Contains(m, "gpt"), strings.Contains(m, "codex"):
		return "codex"
	case strings.Contains(m, "kimi"), strings.Contains(m, "k2"),
		strings.Contains(m, "moonshot"):
		return "kimi"
	default:
		return ""
	}
}
