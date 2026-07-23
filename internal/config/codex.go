package config

// The Codex catalog is the single source of truth for the codex provider's clean
// default model and effort, its suggested models, and its reasoning-effort
// choices. A fresh install runs codex with CodexDefaultModel at CodexDefaultEffort;
// the same lists feed configuration metadata and the terminal selectors so the
// default, the suggestions, and the picker options never drift apart.
const (
	CodexDefaultModel  = "gpt-5.6-sol"
	CodexDefaultEffort = "medium"
	// CodexDefaultMode drives the codex TUI in a terminal session, which is what
	// makes a codex phase steerable mid-run. The exec print mode remains available
	// as a fallback while the interactive path soaks.
	CodexDefaultMode = "interactive"
)

// codexModels are the suggested Codex model IDs, ordered Sol, Terra, Luna, then
// the still-supported older releases. They are suggestions, not an allowlist —
// a custom model ID set via CODEX_MODEL remains usable.
var codexModels = []string{
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
}

// codexEfforts is the exact set of reasoning-effort values the Codex CLI accepts
// via model_reasoning_effort. The stale minimal and the Responses-API-only none
// are deliberately excluded.
var codexEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// CodexModels returns the suggested Codex model IDs from the catalog.
func CodexModels() []string { return codexModels }

// CodexEfforts returns the Codex reasoning-effort choices from the catalog.
func CodexEfforts() []string { return codexEfforts }
