package webserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

type grillAdapter interface {
	resumable(chain string) bool
	turnSpec(sid int64, repo registry.Repo, cfg config.Config, mode, model, resume, prompt string) (grillTurnSpec, error)
	deltaText(line []byte) string
	activityEvent(line []byte) []GrillActivityView
	parseResult(stream []byte) (chainID string, resultErr bool)
}

const (
	grillActivityTool     = "tool"
	grillActivityThinking = "thinking"
	grillActivityResult   = "result"
)

// grillActivityDetailMax bounds a detail summary. Detail is a hint about a call — the
// command run, the file touched — never the whole input and never the result, and the
// stream is readable by any hub-token holder, so even that one field is cut short. The
// row shows less than this; the rest is what hovering it reveals.
const grillActivityDetailMax = 200

// grillActivityThinkingMax bounds a thinking summary that arrives whole rather than as
// deltas. A summary is already a sentence rather than a field, so it gets more room
// than a call's detail before it reads as clipped.
const grillActivityThinkingMax = 200

// grillToolInputField names the input field a tool's summary reads: the one argument
// that says what the call is about.
var grillToolInputField = map[string]string{
	"Bash":         "command",
	"Read":         "file_path",
	"Edit":         "file_path",
	"Write":        "file_path",
	"NotebookEdit": "file_path",
	"Grep":         "pattern",
	"Glob":         "pattern",
	"WebFetch":     "url",
	"WebSearch":    "query",
	"Task":         "description",
	"Agent":        "description",
	"Skill":        "skill",
}

func grillToolResult(id string, ok bool) GrillActivityView {
	return GrillActivityView{Kind: grillActivityResult, ID: id, OK: &ok}
}

func grillActivityDetail(s string) string {
	return grillActivityLine(s, grillActivityDetailMax)
}

func grillActivityLine(s string, max int) string {
	out := strings.Join(strings.Fields(s), " ")
	if r := []rune(out); len(r) > max {
		return string(r[:max]) + "…"
	}
	return out
}

// grillToolInputSummary says what a call was about in one line: the tool's
// representative argument, or the first string it was passed when the tool is one this
// hub does not know. A path is cut back to the file and the directory holding it —
// enough to recognize, without laying out where the tree lives.
func grillToolInputSummary(name string, input json.RawMessage) string {
	fields := grillInputStrings(input)
	want := grillToolInputField[name]
	for _, f := range fields {
		if f.key != want {
			continue
		}
		if want == "file_path" {
			return grillActivityDetail(grillPathSummary(f.value))
		}
		return grillActivityDetail(f.value)
	}
	if len(fields) == 0 {
		return ""
	}
	return grillActivityDetail(fields[0].value)
}

type grillInputString struct {
	key   string
	value string
}

// grillInputStrings reads a tool input's string arguments in the order the object
// carries them, so the fallback summary is the first thing the tool was passed rather
// than whichever key a map happens to hand back first.
func grillInputStrings(input json.RawMessage) []grillInputString {
	dec := json.NewDecoder(bytes.NewReader(input))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	out := []grillInputString{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return out
		}
		var value any
		if dec.Decode(&value) != nil {
			return out
		}
		if s, ok := value.(string); ok {
			out = append(out, grillInputString{key: key.(string), value: s})
		}
	}
	return out
}

func grillPathSummary(p string) string {
	dir, file := filepath.Split(p)
	if dir = strings.TrimSuffix(dir, string(filepath.Separator)); dir == "" {
		return file
	}
	return filepath.Join(filepath.Base(dir), file)
}

func grillAdapterFor(r *grillRunner, provider string) (grillAdapter, bool) {
	switch grillEffectiveProvider(provider) {
	case "claude":
		return claudeGrillAdapter{r: r}, true
	case "codex":
		return codexGrillAdapter{r: r}, true
	case "kimi":
		return kimiGrillAdapter{r: r}, true
	default:
		return nil, false
	}
}

func grillProviderBin(cfg config.Config, provider string) string {
	provider = grillEffectiveProvider(provider)
	bin := cfg.ClaudeBin
	switch provider {
	case "codex":
		bin = cfg.CodexBin
	case "kimi":
		bin = cfg.KimiBin
	}
	if bin := strings.TrimSpace(bin); bin != "" {
		return bin
	}
	return provider
}

func grillModelDefault(cfg config.Config, provider string) string {
	switch grillEffectiveProvider(provider) {
	case "codex":
		return strings.TrimSpace(cfg.CodexModel)
	case "kimi":
		return strings.TrimSpace(cfg.KimiModel)
	}
	if m := strings.TrimSpace(cfg.GrillModel); m != "" {
		return m
	}
	return strings.TrimSpace(cfg.ClaudeModel)
}

type claudeGrillAdapter struct{ r *grillRunner }

func (a claudeGrillAdapter) resumable(chain string) bool { return agent.SessionExists(chain) }

// Claude ignores mode: its grill children run permission-skipped and already carry the
// built-in web search and fetch tools.
func (a claudeGrillAdapter) turnSpec(sid int64, repo registry.Repo, cfg config.Config, mode, model, resume, prompt string) (grillTurnSpec, error) {
	return grillTurnSpec{
		bin:  grillProviderBin(cfg, "claude"),
		dir:  repo.Root,
		args: grillTurnArgs(strings.Fields(cfg.ClaudeFlags), model, a.r.mcpConfigJSON(sid), resume, prompt),
		env:  grillChildEnv(),
	}, nil
}

func (a claudeGrillAdapter) deltaText(line []byte) string { return grillDeltaText(line) }

func (a claudeGrillAdapter) activityEvent(line []byte) []GrillActivityView {
	return grillActivityEvent(line)
}

func (a claudeGrillAdapter) parseResult(stream []byte) (string, bool) {
	return parseGrillStream(stream)
}

type codexGrillAdapter struct{ r *grillRunner }

func (a codexGrillAdapter) resumable(chain string) bool { return codexGrillSessionExists(chain) }

func (a codexGrillAdapter) turnSpec(sid int64, repo registry.Repo, cfg config.Config, mode, model, resume, prompt string) (grillTurnSpec, error) {
	return grillTurnSpec{
		bin: grillProviderBin(cfg, "codex"),
		dir: repo.Root,
		args: codexGrillArgs(
			strings.Fields(cfg.CodexFlags),
			cfg.CodexProfile,
			model,
			cfg.CodexEffort,
			mode,
			a.r.codexMCPConfigArgs(sid),
			resume,
			prompt,
		),
		env: codexChildEnv(a.r.srv.token),
	}, nil
}

func (a codexGrillAdapter) deltaText(line []byte) string { return codexGrillDeltaText(line) }

func (a codexGrillAdapter) activityEvent(line []byte) []GrillActivityView {
	return codexGrillActivity(line)
}

func (a codexGrillAdapter) parseResult(stream []byte) (string, bool) {
	return parseCodexGrillStream(stream)
}

const codexGrillMCPTokenEnv = "TRAU_GRILL_MCP_TOKEN"

func (r *grillRunner) codexMCPConfigArgs(sid int64) []string {
	url := fmt.Sprintf("%s%s/grill/%d/mcp", r.baseURL, APIPrefix, sid)
	args := []string{"-c", "mcp_servers.trau-grill.url=" + strconv.Quote(url)}
	if r.srv.token != "" {
		args = append(args, "-c", "mcp_servers.trau-grill.bearer_token_env_var="+strconv.Quote(codexGrillMCPTokenEnv))
	}
	return args
}

func codexChildEnv(token string) []string {
	base := grillChildEnv()
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, codexGrillMCPTokenEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	if token != "" {
		out = append(out, codexGrillMCPTokenEnv+"="+token)
	}
	return out
}

// A research turn switches codex's first-class web_search tool on here rather than
// relying on the user's ~/.codex/config.toml, which leaves it off by default.
func codexGrillArgs(flags []string, profile, model, effort, mode string, mcpArgs []string, resumeID, prompt string) []string {
	args := []string{"exec"}
	args = append(args, flags...)
	args = append(args, "--json")
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+effort)
	}
	if mode == hubstore.GrillModeResearch {
		args = append(args, "-c", "tools.web_search=true")
	}
	args = append(args, mcpArgs...)
	if resumeID != "" {
		args = append(args, "resume", resumeID)
	}
	return append(args, prompt)
}

func codexGrillDeltaText(line []byte) string {
	var ev struct {
		Type string `json:"type"`
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Type != "item.completed" || ev.Item.Type != "agent_message" {
		return ""
	}
	return ev.Item.Text
}

// codexGrillActivity maps codex's item lifecycle onto activity: a started item is a
// tool the agent reached for, and the matching completed item is that tool coming
// back. Codex reports the reply as an item too; that already streams as deltas. Its
// reasoning arrives as whole summaries rather than a stretch written out live, so a
// completed one is capped like a call's detail before it rides the stream.
func codexGrillActivity(line []byte) []GrillActivityView {
	var ev struct {
		Type string `json:"type"`
		Item struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Command  string `json:"command"`
			Query    string `json:"query"`
			Text     string `json:"text"`
			ExitCode int    `json:"exit_code"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	if ev.Item.Type == "reasoning" {
		if ev.Type != "item.completed" || ev.Item.Text == "" {
			return nil
		}
		text := grillActivityLine(ev.Item.Text, grillActivityThinkingMax)
		return []GrillActivityView{{Kind: grillActivityThinking, Text: text}}
	}
	name, detail := "", ""
	switch ev.Item.Type {
	case "command_execution":
		name, detail = "shell", grillActivityDetail(ev.Item.Command)
	case "web_search":
		name, detail = "web_search", grillActivityDetail(ev.Item.Query)
	default:
		return nil
	}
	switch ev.Type {
	case "item.started":
		return []GrillActivityView{{Kind: grillActivityTool, ID: ev.Item.ID, Name: name, Detail: detail}}
	case "item.completed":
		return []GrillActivityView{grillToolResult(ev.Item.ID, ev.Item.ExitCode == 0)}
	}
	return nil
}

func parseCodexGrillStream(stream []byte) (sessionID string, resultErr bool) {
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(b, &ev) != nil {
			continue
		}
		if ev.ThreadID != "" {
			sessionID = ev.ThreadID
		}
		if ev.Type == "turn.failed" || ev.Type == "error" || ev.Item.Type == "error" {
			resultErr = true
		}
	}
	return sessionID, resultErr
}

func codexGrillSessionExists(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(codexHomeDir(), "sessions", "*", "*", "*", "rollout-*"+sessionID+".jsonl"))
	return len(matches) > 0
}

func codexHomeDir() string {
	if d := strings.TrimSpace(os.Getenv("CODEX_HOME")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

type kimiGrillAdapter struct{ r *grillRunner }

func (a kimiGrillAdapter) resumable(chain string) bool { return kimiGrillSessionExists(chain) }

func (a kimiGrillAdapter) turnSpec(sid int64, repo registry.Repo, cfg config.Config, mode, model, resume, prompt string) (grillTurnSpec, error) {
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

func (a kimiGrillAdapter) activityEvent(line []byte) []GrillActivityView {
	return kimiGrillActivity(line)
}

func (a kimiGrillAdapter) parseResult(stream []byte) (string, bool) {
	return parseKimiGrillStream(stream)
}

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

// kimiGrillActivity reads kimi's tool lines, the only work its stream-json names on
// the wire: one line per tool the agent ran, carrying the tool's own output, which
// stays in the child. The line only appears once the tool is back, so the frame is
// born resolved rather than waiting for a result that never comes.
func kimiGrillActivity(line []byte) []GrillActivityView {
	var ev struct {
		Role string `json:"role"`
		Name string `json:"name"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Role != "tool" || ev.Name == "" {
		return nil
	}
	ran := true
	return []GrillActivityView{{Kind: grillActivityTool, Name: ev.Name, OK: &ran}}
}

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

// Kimi only reads MCP servers from its home at session start, so each grill session
// gets an overlay home with shared auth/session state and a scoped mcp.json.
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

func kimiGrillSessionExists(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(kimiHomeDir(), "sessions", "*", sessionID))
	return len(matches) > 0
}

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
