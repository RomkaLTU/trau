package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPhaseDisallowedToolsDefaults pins the byte-identical default: with no opt-in
// and no per-phase override, every phase resolves to the provider default and takes
// the standard fan-out-disabled preamble.
func TestPhaseDisallowedToolsDefaults(t *testing.T) {
	c := Defaults()
	for _, ph := range phases {
		if got := c.PhaseDisallowedTools(ph); got != "Agent,Workflow" {
			t.Errorf("PhaseDisallowedTools(%q) = %q, want Agent,Workflow", ph, got)
		}
		if got := c.PhasePreamble("claude", ph); got != Preamble {
			t.Errorf("PhasePreamble(claude, %q) = Explore variant, want standard", ph)
		}
	}
}

// TestPhaseDisallowedToolsExploreOptIn is the core opt-in behavior: build and verify
// drop the Agent tool (permitting read-only Explore subagents) and switch to the
// Explore preamble, every other phase is untouched, and Workflow stays blocked
// everywhere.
func TestPhaseDisallowedToolsExploreOptIn(t *testing.T) {
	c := Defaults()
	c.ExploreSubagents = true

	for _, ph := range []string{"build", "verify"} {
		if got := c.PhaseDisallowedTools(ph); got != "Workflow" {
			t.Errorf("opt-in PhaseDisallowedTools(%q) = %q, want Workflow", ph, got)
		}
		if got := c.PhasePreamble("claude", ph); got != ExplorePreamble {
			t.Errorf("opt-in PhasePreamble(claude, %q) = standard, want Explore variant", ph)
		}
	}

	for _, ph := range phases {
		got := c.PhaseDisallowedTools(ph)
		if !strings.Contains(got, "Workflow") {
			t.Errorf("Workflow must stay blocked in %q, got %q", ph, got)
		}
		if ph == "build" || ph == "verify" {
			continue
		}
		if got != "Agent,Workflow" {
			t.Errorf("opt-in leaked into %q: %q, want Agent,Workflow", ph, got)
		}
		if c.PhasePreamble("claude", ph) != Preamble {
			t.Errorf("opt-in flipped preamble for %q, want standard", ph)
		}
	}
}

// TestPhaseDisallowedToolsOverrideWins checks an explicit per-phase override beats
// both the provider default and the Explore seed, and the preamble follows whatever
// the override resolves to.
func TestPhaseDisallowedToolsOverrideWins(t *testing.T) {
	c := Defaults()
	c.ExploreSubagents = true
	c.ClaudePhaseDisallowedTools = map[string]string{
		"build":  "Agent,Workflow",
		"repair": "Workflow",
	}

	// An override restores the full block on build even under the opt-in.
	if got := c.PhaseDisallowedTools("build"); got != "Agent,Workflow" {
		t.Errorf("build override = %q, want Agent,Workflow", got)
	}
	if c.PhasePreamble("claude", "build") != Preamble {
		t.Errorf("build override kept Agent blocked, preamble should be standard")
	}
	// An override can enable Explore on a phase the opt-in never touches; the
	// preamble must follow.
	if got := c.PhaseDisallowedTools("repair"); got != "Workflow" {
		t.Errorf("repair override = %q, want Workflow", got)
	}
	if c.PhasePreamble("claude", "repair") != ExplorePreamble {
		t.Errorf("repair override enabled Agent, preamble should be Explore variant")
	}
}

// TestPhasePreambleOverrides checks stored prompt overrides replace either preamble
// body on their matching branch, and a nil override map renders the built-in
// defaults byte-identically.
func TestPhasePreambleOverrides(t *testing.T) {
	c := Defaults()
	c.ExploreSubagents = true
	c.PromptOverrides = map[string]string{
		"preamble":         "standard override",
		"explore_preamble": "explore override",
	}

	if got := c.PhasePreamble("claude", "commit"); got != "standard override" {
		t.Errorf("standard branch = %q, want the preamble override", got)
	}
	if got := c.PhasePreamble("claude", "build"); got != "explore override" {
		t.Errorf("explore branch = %q, want the explore_preamble override", got)
	}

	c.PromptOverrides = map[string]string{"explore_preamble": "explore override"}
	if got := c.PhasePreamble("claude", "commit"); got != Preamble {
		t.Errorf("partial overrides leaked into the standard branch: %q", got)
	}

	c.PromptOverrides = nil
	if got := c.PhasePreamble("claude", "commit"); got != Preamble {
		t.Errorf("nil overrides standard branch = %q, want built-in Preamble", got)
	}
	if got := c.PhasePreamble("claude", "build"); got != ExplorePreamble {
		t.Errorf("nil overrides explore branch = %q, want built-in ExplorePreamble", got)
	}
}

// TestPreambleMatchesToolPolicy is the acceptance invariant: the preamble a phase is
// sent never contradicts its effective disallowed-tools — Explore variant exactly
// when the Agent tool is left enabled.
func TestPreambleMatchesToolPolicy(t *testing.T) {
	c := Defaults()
	c.ExploreSubagents = true
	c.ClaudePhaseDisallowedTools = map[string]string{"commit": "Workflow"}
	for _, ph := range phases {
		disallowed := c.PhaseDisallowedTools(ph)
		wantExplore := !strings.Contains(disallowed, "Agent")
		got := c.PhasePreamble("claude", ph)
		if wantExplore && got != ExplorePreamble {
			t.Errorf("%q allows Agent (%q) but preamble is standard", ph, disallowed)
		}
		if !wantExplore && got != Preamble {
			t.Errorf("%q blocks Agent (%q) but preamble is Explore variant", ph, disallowed)
		}
	}
}

// TestExploreSubagentsLoad checks EXPLORE_SUBAGENTS and CLAUDE_<PHASE>_DISALLOWED_TOOLS
// parse across the config layers and surface in the doctor/config reference with
// their effective per-phase values.
func TestExploreSubagentsLoad(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "trau.ini")
	content := "PROVIDER=claude\nEXPLORE_SUBAGENTS=1\nCLAUDE_VERIFY_DISALLOWED_TOOLS=Agent,Workflow\n"
	if err := os.WriteFile(local, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadLayered("", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ExploreSubagents {
		t.Fatal("EXPLORE_SUBAGENTS=1 did not enable the opt-in")
	}
	// build takes the seed (Agent dropped); verify's explicit override wins back the
	// full block.
	if got := cfg.PhaseDisallowedTools("build"); got != "Workflow" {
		t.Errorf("build = %q, want Workflow (seed)", got)
	}
	if got := cfg.PhaseDisallowedTools("verify"); got != "Agent,Workflow" {
		t.Errorf("verify = %q, want Agent,Workflow (explicit override)", got)
	}

	items, err := ResolveConfigItems(cfg, local, "", "", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]ConfigItem{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	for key, want := range map[string]string{
		"EXPLORE_SUBAGENTS":              "1",
		"CLAUDE_BUILD_DISALLOWED_TOOLS":  "Workflow",
		"CLAUDE_VERIFY_DISALLOWED_TOOLS": "Agent,Workflow",
		"CLAUDE_REPAIR_DISALLOWED_TOOLS": "Agent,Workflow",
	} {
		it, ok := byKey[key]
		if !ok {
			t.Errorf("config reference missing %q", key)
			continue
		}
		if it.Value != want {
			t.Errorf("doctor %q = %q, want %q", key, it.Value, want)
		}
	}
}
