package webserver

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// newHubRunner constructs the repo's default-provider backend for a one-shot,
// headless hub session in the repo directory, using the same provider registry the
// loop does. The prompt is passed whole to Run, so the backend's preamble stays
// empty.
func newHubRunner(cfg config.Config, repo registry.Repo) (agent.Runner, error) {
	spec, ok := agent.DefaultRegistry().Lookup(cfg.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
	bin, flags, model, effort, extra := hubProviderConfig(cfg)
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("provider %q: %q not found on PATH", cfg.Provider, bin)
	}
	return spec.New(agent.BackendParams{
		Bin:     bin,
		Flags:   strings.Fields(flags),
		Model:   model,
		Effort:  effort,
		Dir:     repo.Root,
		Timeout: time.Duration(cfg.AgentTimeout) * time.Second,
		Extra:   extra,
	})
}

// hubProviderConfig maps the repo's layered config to the default provider's
// bin/flags/model/effort, mirroring the loop's provider resolution for the fields a
// hub-spawned session needs.
func hubProviderConfig(cfg config.Config) (bin, flags, model, effort string, extra map[string]string) {
	extra = map[string]string{"result_dir": cfg.RunsDir}
	switch cfg.Provider {
	case "codex":
		extra["profile"] = cfg.CodexProfile
		extra["mode"] = cfg.CodexMode
		return cfg.CodexBin, cfg.CodexFlags, cfg.CodexModel, cfg.CodexEffort, extra
	case "kimi":
		extra["mode"] = cfg.KimiMode
		return cfg.KimiBin, cfg.KimiFlags, cfg.KimiModel, "", extra
	default:
		return cfg.ClaudeBin, cfg.ClaudeFlags, cfg.ClaudeModel, cfg.ClaudeEffort, extra
	}
}

// recordAgentSpend attributes a hub-spawned session's token spend to bucket under
// phase — mirroring the loop's _loop bucket so it never lands in a real ticket's
// run breakdown — and returns the call's cost. A call that captured no tokens
// records nothing but still returns its cost.
func recordAgentSpend(s *Server, repo, bucket, phase, provider string, res agent.Result, now time.Time) float64 {
	cost := res.CostUSD
	if cost == 0 {
		cost = tokens.EstimateCost(res.Model, res.Usage.Input, res.Usage.Output, res.Usage.CacheRead, res.Usage.CacheCreation)
	}
	total := res.Usage.Input + res.Usage.Output + res.Usage.CacheRead + res.Usage.CacheCreation
	if total <= 0 {
		return cost
	}
	c := cost
	call := hubstore.TokenCall{
		Ticket:        bucket,
		TS:            now.Format("2006-01-02T15:04:05"),
		Phase:         phase,
		Input:         res.Usage.Input,
		Output:        res.Usage.Output,
		CacheRead:     res.Usage.CacheRead,
		CacheCreation: res.Usage.CacheCreation,
		Reasoning:     res.Usage.Reasoning,
		Total:         total,
		CostUSD:       &c,
		Turns:         res.NumTurns,
		IsError:       res.IsError,
		Provider:      provider,
		Model:         res.Model,
		Context:       res.Context,
	}
	if err := s.stores.Tokens().Append(repo, []hubstore.TokenCall{call}); err != nil {
		logger.Verbosef("record token call %s: %v", repo, err)
	}
	return cost
}
