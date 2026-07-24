package webserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
)

// grillAdapter is the per-provider seam a grilling turn runs through. Each provider
// resolves its own child invocation (binary, flags, per-session MCP config and
// resume), reads the reply text off one stream line for the live panel, and mints
// the session chain id from the child's stream so the next turn resumes it.
type grillAdapter interface {
	// resumable reports whether chain still names a resumable session for this
	// provider, so a fresh id self-heals a stale chain.
	resumable(chain string) bool
	// turnSpec builds the child invocation. resume is "" for a first turn.
	turnSpec(sid int64, repo registry.Repo, cfg config.Config, model, resume, prompt string) (grillTurnSpec, error)
	// deltaText returns the reply text one stream line carries, or empty.
	deltaText(line []byte) string
	// parseResult mints the session chain id and error flag from the whole stream.
	parseResult(stream []byte) (chainID string, resultErr bool)
}

// grillAdapterFor resolves the adapter for a session's provider. An empty provider
// runs claude; an unknown one (a validated provider with no interview adapter yet,
// such as codex) has no adapter, so the caller parks the session.
func grillAdapterFor(r *grillRunner, provider string) (grillAdapter, bool) {
	switch grillEffectiveProvider(provider) {
	case "claude":
		return claudeGrillAdapter{r: r}, true
	case "kimi":
		return kimiGrillAdapter{r: r}, true
	default:
		return nil, false
	}
}

// grillProviderBin is the CLI a provider's turn spawns: the repo config's binary, or
// the provider's own name on PATH.
func grillProviderBin(cfg config.Config, provider string) string {
	provider = grillEffectiveProvider(provider)
	bin := cfg.ClaudeBin
	if provider == "kimi" {
		bin = cfg.KimiBin
	}
	if bin := strings.TrimSpace(bin); bin != "" {
		return bin
	}
	return provider
}

// grillModelDefault is the model a provider's session with no explicit choice runs
// on: for claude the repo config's GRILL_MODEL then CLAUDE_MODEL, for kimi
// KIMI_MODEL, else empty — the provider CLI's own default.
func grillModelDefault(cfg config.Config, provider string) string {
	if grillEffectiveProvider(provider) == "kimi" {
		return strings.TrimSpace(cfg.KimiModel)
	}
	if m := strings.TrimSpace(cfg.GrillModel); m != "" {
		return m
	}
	return strings.TrimSpace(cfg.ClaudeModel)
}

// claudeGrillAdapter is the original grilling behavior: the headless claude CLI with
// its stream-json partial-message contract and transcript-backed resume.
type claudeGrillAdapter struct{ r *grillRunner }

func (a claudeGrillAdapter) resumable(chain string) bool { return agent.SessionExists(chain) }

func (a claudeGrillAdapter) turnSpec(sid int64, repo registry.Repo, cfg config.Config, model, resume, prompt string) (grillTurnSpec, error) {
	return grillTurnSpec{
		bin:  grillProviderBin(cfg, "claude"),
		dir:  repo.Root,
		args: grillTurnArgs(strings.Fields(cfg.ClaudeFlags), model, a.r.mcpConfigJSON(sid), resume, prompt),
		env:  grillChildEnv(),
	}, nil
}

func (a claudeGrillAdapter) deltaText(line []byte) string { return grillDeltaText(line) }

func (a claudeGrillAdapter) parseResult(stream []byte) (string, bool) {
	return parseGrillStream(stream)
}

// kimiGrillAdapter drives the kimi CLI in headless print mode. Kimi has no
// per-invocation MCP flag — servers load from mcp.json at session start — so the
// session's scoped endpoint is written into a per-session KIMI_CODE_HOME that
// mirrors the real home but for its own mcp.json. Resume is kimi's native
// --session, its id read back from the stream's session.resume_hint line.
type kimiGrillAdapter struct{ r *grillRunner }

func (a kimiGrillAdapter) resumable(chain string) bool { return kimiGrillSessionExists(chain) }

func (a kimiGrillAdapter) turnSpec(sid int64, repo registry.Repo, cfg config.Config, model, resume, prompt string) (grillTurnSpec, error) {
	home, err := a.r.kimiGrillHome(sid)
	if err != nil {
		return grillTurnSpec{}, err
	}
	return grillTurnSpec{
		bin:  grillProviderBin(cfg, "kimi"),
		dir:  repo.Root,
		args: kimiGrillArgs(strings.Fields(cfg.KimiFlags), model, resume, prompt),
		env:  kimiChildEnv(home),
	}, nil
}

// kimiChildEnv is the grilling child env with KIMI_CODE_HOME pinned to the session's
// overlay, replacing any the hub itself inherited so a relocated kimi home cannot
// shadow the scoped one.
func kimiChildEnv(home string) []string {
	base := grillChildEnv()
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "KIMI_CODE_HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "KIMI_CODE_HOME="+home)
}

func (a kimiGrillAdapter) deltaText(line []byte) string { return kimiGrillDeltaText(line) }

func (a kimiGrillAdapter) parseResult(stream []byte) (string, bool) {
	return parseKimiGrillStream(stream)
}

// kimiGrillArgs assembles the kimi print-mode vector: the configured flags, the
// resolved model alias, the stream-json contract, an optional native resume, and the
// prompt last. Kimi auto-executes tools in print mode (it rejects --yolo), so no
// approval flag is needed for the grilling MCP calls.
func kimiGrillArgs(flags []string, model, resumeID, prompt string) []string {
	args := append([]string{}, flags...)
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "--output-format", "stream-json")
	if resumeID != "" {
		args = append(args, "--session", resumeID)
	}
	return append(args, "-p", prompt)
}

// kimiGrillDeltaText returns the reply text one kimi stream line carries. Kimi emits
// whole assistant messages rather than token deltas, so each assistant line is
// published as one chunk; the meta resume-hint line carries a role that excludes it.
func kimiGrillDeltaText(line []byte) string {
	var ev struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Role != "assistant" {
		return ""
	}
	return ev.Content
}

// parseKimiGrillStream reads the kimi session id from the stream's session.resume_hint
// line, the id the next turn resumes with. Kimi's print stream carries no terminal
// error event — a failed turn exits non-zero and is caught by the spawn error — so
// the error flag is always false here.
func parseKimiGrillStream(stream []byte) (sessionID string, resultErr bool) {
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ev struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(b, &ev) != nil {
			continue
		}
		if ev.Type == "session.resume_hint" && ev.SessionID != "" {
			sessionID = ev.SessionID
		}
	}
	return sessionID, false
}

// kimiGrillHome builds the per-session KIMI_CODE_HOME the kimi child runs under: a
// stable scratch dir keyed by session id, symlinking every entry of the real kimi
// home (config, credentials, sessions) so auth and native resume keep working, but
// owning its own mcp.json that exposes only this session's grill endpoint. Kimi
// loads MCP servers at session start with no command-line override, so the scoped
// endpoint has to arrive as a file in a home of its own. It is rebuilt idempotently
// each turn so a changed base URL or token is picked up.
func (r *grillRunner) kimiGrillHome(sid int64) (string, error) {
	real := kimiHomeDir()
	entries, err := os.ReadDir(real)
	if err != nil {
		return "", fmt.Errorf("read kimi home %s: %w", real, err)
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("trau-grill-kimi-%d", sid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name() == "mcp.json" {
			continue
		}
		link := filepath.Join(dir, e.Name())
		_ = os.Remove(link)
		if err := os.Symlink(filepath.Join(real, e.Name()), link); err != nil {
			return "", err
		}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"trau-grill": r.grillMCPServer(sid)}})
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), b, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// kimiGrillSessionExists reports whether sessionID still has a session directory
// under the kimi home, the resume gate that lets a stale chain self-heal to a fresh
// first turn. It mirrors kimi's own sessions layout, <home>/sessions/<workspace>/<id>.
func kimiGrillSessionExists(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(kimiHomeDir(), "sessions", "*", sessionID))
	return len(matches) > 0
}

// kimiHomeDir resolves kimi's data directory the way the CLI does: KIMI_CODE_HOME,
// else ~/.kimi-code. The hub does not set KIMI_CODE_HOME for itself, so this reads
// the real home the per-session overlay is layered over.
func kimiHomeDir() string {
	if d := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".kimi-code"
	}
	return filepath.Join(home, ".kimi-code")
}
