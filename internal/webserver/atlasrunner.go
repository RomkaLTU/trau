package webserver

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubatlas"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// atlasGenTimeout bounds one Atlas generation end to end, across both attempts. It
// is a var so tests can shorten it.
var atlasGenTimeout = 30 * time.Minute

const (
	// atlasMaxAttempts is the initial generation plus the one retry ADR 0013 allows
	// before a failure is stored.
	atlasMaxAttempts = 2

	// atlasBucket is the token-ledger ticket the generation's spend is attributed to,
	// mirroring the loop's _loop bucket so it never lands in a real ticket's run
	// breakdown. atlasPhase is the cost category the Costs page groups it under.
	atlasBucket = "_atlas"
	atlasPhase  = "atlas"

	atlasAuthFailure = "the generation agent needs re-authentication — re-login (run the provider CLI, then /login), then regenerate"
)

// atlasKey is a generation's identity: one in-flight run per (repo, view).
type atlasKey struct {
	repo string
	view string
}

// atlasRunner is the process side of Atlas generation: a hub-spawned, one-shot,
// headless agent session that reads a repo and emits a View's JSON document (ADR
// 0013). It runs in-process in the hub, mutating state through the same stores the
// API handlers use. One generation per (repo, view) at a time; a second request
// while one is in flight is refused. Generation is explicit-start only — nothing
// here is triggered by a merge or the pipeline.
type atlasRunner struct {
	srv     *Server
	baseCtx context.Context

	newRunner func(cfg config.Config, repo registry.Repo, view hubatlas.View) (agent.Runner, error)
	head      func(ctx context.Context, root string) string
	now       func() time.Time

	mu       sync.Mutex
	inflight map[atlasKey]bool
}

func newAtlasRunner(s *Server) *atlasRunner {
	r := &atlasRunner{
		srv:      s,
		baseCtx:  context.Background(),
		head:     defaultBranchHead,
		now:      time.Now,
		inflight: map[atlasKey]bool{},
	}
	r.newRunner = r.buildRunner
	return r
}

// EnableAtlas binds Atlas generation to ctx — the hub's lifetime — so cancelling it
// stops any generation in flight. Call it once, before serving.
func (s *Server) EnableAtlas(ctx context.Context) {
	s.atlas.baseCtx = ctx
}

// start launches a generation for view in repo in the background and reports whether
// it started. It returns false when a generation for the same (repo, view) is already
// in flight, so the handler can answer 409. The turn is bounded by the hub context,
// not the request's, so it outlives the POST that began it.
func (r *atlasRunner) start(repo registry.Repo, view hubatlas.View) bool {
	key := atlasKey{repo: repo.Root, view: view.ID}
	r.mu.Lock()
	if r.inflight[key] {
		r.mu.Unlock()
		return false
	}
	r.inflight[key] = true
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.inflight, key)
			r.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(r.baseCtx, atlasGenTimeout)
		defer cancel()
		r.generate(ctx, repo, view)
	}()
	return true
}

// generate runs the session to a stored outcome: it stamps the repo's default-branch
// HEAD, runs the agent, validates its output, retries once on invalid output feeding
// the errors back, and stores the good document or — after a second failure — an
// error row that never displaces the last good document. The session's token spend is
// recorded as the atlas cost category regardless of outcome.
func (r *atlasRunner) generate(ctx context.Context, repo registry.Repo, view hubatlas.View) {
	projectPath, userPath := r.srv.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		r.store(repo.Root, view.ID, "", "", 0, "could not load the repository config: "+err.Error())
		return
	}
	commit := r.head(ctx, repo.Root)

	runner, err := r.newRunner(cfg, repo, view)
	if err != nil {
		r.store(repo.Root, view.ID, commit, "", 0, "could not start the generation agent: "+err.Error())
		return
	}

	previous := r.previousDocument(repo.Root, view.ID)

	var (
		cost    float64
		failure string
	)
	prompt := view.GenerationPrompt(previous)
	for attempt := 0; attempt < atlasMaxAttempts; attempt++ {
		res, runErr := runner.Run(ctx, prompt, atlasPhase)
		cost += r.recordCall(repo.Root, cfg.Provider, res)

		if errors.Is(runErr, agent.ErrAuthRequired) {
			r.store(repo.Root, view.ID, commit, "", cost, atlasAuthFailure)
			return
		}

		document := strings.TrimSpace(res.Final)
		verr := view.Validate([]byte(document))
		if verr == nil {
			r.store(repo.Root, view.ID, commit, document, cost, "")
			return
		}
		failure = atlasFailure(runErr, verr, document)
		prompt = view.RetryPrompt(previous, document, failure)
	}
	r.store(repo.Root, view.ID, commit, "", cost, failure)
}

// store appends a generation to the Atlas store: a good document with an empty error,
// or a failure with an empty document and the reason.
func (r *atlasRunner) store(repo, viewID, commit, document string, cost float64, failure string) {
	if _, err := r.srv.stores.Atlas().Insert(repo, viewID, commit, document, cost, failure); err != nil {
		logger.Verbosef("atlas: store %s/%s: %v", repo, viewID, err)
	}
}

// previousDocument returns the last good document for (repo, view), or empty when the
// View has never generated one — the prior graph the prompt reuses ids from.
func (r *atlasRunner) previousDocument(repo, viewID string) string {
	doc, ok, err := r.srv.stores.Atlas().Latest(repo, viewID)
	if err != nil || !ok {
		return ""
	}
	return doc.Document
}

// recordCall records one attempt's token spend as the atlas cost category attributed
// to repo and returns the call's cost, which the caller sums into the document's
// stamped cost. A call that captured no tokens records nothing.
func (r *atlasRunner) recordCall(repo, provider string, res agent.Result) float64 {
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
		Ticket:        atlasBucket,
		TS:            r.now().Format("2006-01-02T15:04:05"),
		Phase:         atlasPhase,
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
	if err := r.srv.stores.Tokens().Append(repo, []hubstore.TokenCall{call}); err != nil {
		logger.Verbosef("atlas: record token call %s: %v", repo, err)
	}
	return cost
}

// buildRunner constructs the repo's default-provider backend for a one-shot headless
// session in the repo directory, using the same provider registry the loop does. The
// prompt is passed whole to Run, so the backend's preamble stays empty.
func (r *atlasRunner) buildRunner(cfg config.Config, repo registry.Repo, _ hubatlas.View) (agent.Runner, error) {
	spec, ok := agent.DefaultRegistry().Lookup(cfg.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
	bin, flags, model, effort, extra := atlasProviderConfig(cfg)
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

// atlasProviderConfig maps the repo's layered config to the default provider's
// bin/flags/model/effort, mirroring the loop's provider resolution for the fields an
// Atlas session needs.
func atlasProviderConfig(cfg config.Config) (bin, flags, model, effort string, extra map[string]string) {
	extra = map[string]string{"result_dir": cfg.RunsDir}
	switch cfg.Provider {
	case "codex":
		extra["profile"] = cfg.CodexProfile
		return cfg.CodexBin, cfg.CodexFlags, cfg.CodexModel, cfg.CodexEffort, extra
	case "kimi":
		return cfg.KimiBin, cfg.KimiFlags, cfg.KimiModel, "", extra
	default:
		return cfg.ClaudeBin, cfg.ClaudeFlags, cfg.ClaudeModel, cfg.ClaudeEffort, extra
	}
}

// atlasFailure phrases why an attempt's output was rejected, for the retry feedback
// and the stored error row. It is only reached for output that failed validation:
// an empty document names the run error behind it, otherwise the validation error.
func atlasFailure(runErr, verr error, document string) string {
	if document == "" {
		if runErr != nil {
			return "the generation agent produced no output: " + runErr.Error()
		}
		return "the generation agent produced no output"
	}
	return "the generated document failed validation: " + verr.Error()
}

// defaultBranchHead resolves the repo's default-branch tip, the commit the generated
// document is stamped with. It prefers the remote default branch so a generation
// running while a loop works a feature branch still stamps the default branch, and
// falls back to the checked-out HEAD when there is no remote.
func defaultBranchHead(ctx context.Context, root string) string {
	branch := strings.TrimPrefix(gitOutput(ctx, root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"), "origin/")
	if branch != "" {
		if sha := gitOutput(ctx, root, "rev-parse", "refs/remotes/origin/"+branch); sha != "" {
			return sha
		}
	}
	return gitOutput(ctx, root, "rev-parse", "HEAD")
}
