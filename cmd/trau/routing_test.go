package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
)

// TestFingerprintMatchesDispatch is the cohort contract: the model and effort
// ResolveRouting records for a phase are the ones buildRouter hands the backend, so
// a fingerprint never describes a run that did not happen.
func TestFingerprintMatchesDispatch(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		migrate bool
	}{
		{name: "fresh claude", body: "PROVIDER=claude\n"},
		{name: "fresh codex", body: "PROVIDER=codex\n"},
		{name: "per-phase overrides", body: "PROVIDER=claude\nCLAUDE_BUILD_MODEL=haiku\nCLAUDE_VERIFY_EFFORT=max\n"},
		{name: "provider default set", body: "PROVIDER=codex\nCODEX_MODEL=gpt-5.5\nCODEX_EFFORT=xhigh\n"},
		{name: "kimi", body: "PROVIDER=kimi\nKIMI_MODEL=kimi-for-coding\nKIMI_LINTFIX_MODEL=kimi-fast\n"},
		{name: "migrated", body: "PROVIDER=claude\nCLAUDE_MODEL=sonnet\nCLAUDE_EFFORT=high\n", migrate: true},
	}

	reg := agent.DefaultRegistry()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trau.ini")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.migrate {
				if err := config.MigratePhaseRoutes("", "", path, t.TempDir()); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := config.LoadLayered("", "", path, "")
			if err != nil {
				t.Fatal(err)
			}

			fp := config.ResolveRouting(cfg)
			for _, phase := range agent.Phases {
				provider, model, effort := cfg.Provider, "", ""
				if spec, routed := cfg.Routes[phase]; routed {
					provider, model, effort, err = parsePhaseRoute(reg, spec, cfg)
					if err != nil {
						t.Fatalf("%s route %q: %v", phase, spec, err)
					}
				} else {
					pc := providerConfigFor(cfg, cfg.Provider)
					model, effort = pc.model, pc.effort
				}
				want := provider + ":" + model + ":" + effort
				if got := fp.Keys["PHASE_"+strings.ToUpper(phase)]; got != want {
					t.Errorf("fingerprint %s = %q, dispatch runs %q", phase, got, want)
				}
			}
		})
	}
}

// TestPhaseRoutesIgnoreTheProviderDefault is the ADR 0025 split: CLAUDE_MODEL and
// CLAUDE_EFFORT steer the non-phase agents only, so moving them must not move a
// single phase's backend.
func TestPhaseRoutesIgnoreTheProviderDefault(t *testing.T) {
	reg := agent.DefaultRegistry()
	dispatch := func(body string) map[string]string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "trau.ini")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadLayered("", "", path, "")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for phase, spec := range cfg.Routes {
			provider, model, effort, err := parsePhaseRoute(reg, spec, cfg)
			if err != nil {
				t.Fatalf("%s route %q: %v", phase, spec, err)
			}
			out[phase] = provider + ":" + model + ":" + effort
		}
		return out
	}

	base := dispatch("PROVIDER=claude\n")
	moved := dispatch("PROVIDER=claude\nCLAUDE_MODEL=haiku\nCLAUDE_EFFORT=max\n")
	for phase, want := range base {
		if got := moved[phase]; got != want {
			t.Errorf("%s dispatches to %q with CLAUDE_MODEL set, want %q", phase, got, want)
		}
	}
}
