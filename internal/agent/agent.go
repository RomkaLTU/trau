// Package agent runs headless agent CLIs and returns only their final message.
//
// Each call spawns a fresh, isolated subprocess — a brand-new session, never
// --continue/--resume — so phases can never share context. This is the property
// that keeps the verify phase cold: it can only inherit the durable handoff file
// and the code on disk, not the build agent's reasoning. The interface is
// provider-agnostic; Claude and Codex are the two backends. All per-provider
// divergence lives inside the implementation; nothing branches on the provider
// name outside this package.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/creack/pty"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/sanitize"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// TokenSink persists one normalized token-accounting record per agent call.
// agent depends only on this narrow behavior; internal/tokens implements it and
// the main loop points the sink's bucket at the current ticket. A nil sink
// disables persistence (e.g. unit tests that only assert the event stream).
type TokenSink interface {
	Append(phase string, rec tokens.Record)
}

// TranscriptSink captures an agent call's live PTY output. Open returns a writer
// for one transcript session keyed by stem and sized cols×rows; the agent tees the
// raw terminal bytes to it, and the hub persists and fans them out (ADR 0008 §4).
// internal/hubtranscript implements it; a nil sink disables capture (e.g. tests).
type TranscriptSink interface {
	Open(stem string, cols, rows int) io.WriteCloser
}

// Usage is the normalized per-call token accounting. Input is stored as the
// non-cached portion so totals mean the same thing across providers.
type Usage struct {
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
	Reasoning     int
}

// Result is the outcome of one agent invocation.
type Result struct {
	Final    string
	Usage    Usage
	CostUSD  float64
	IsError  bool
	NumTurns int
	Model    string
	Context  int
	Skills   []string
}

// Runner runs one prompt to completion in a fresh process and returns the final
// message. label is the phase tag used for logging/token attribution.
type Runner interface {
	Run(ctx context.Context, prompt, label string) (Result, error)
}

// PhaseRoute optionally reports the provider/model/effort a labeled phase is
// configured to run under, before the call starts — so callers can name the
// agent at the moment a phase begins instead of only on the closing stat line.
// The concrete backends and the Router implement it. The model returned is the
// one the call will be launched with, which may be "" for a backend that leaves
// the choice to its CLI; the actually-recovered (and possibly more specific)
// model still rides the agent_call event afterward.
type PhaseRoute interface {
	Route(label string) (provider, model, effort string)
}

type terminalSession interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Wait() error
	Close() error
	Kill() error
}

type terminalStarter func(ctx context.Context, bin, dir string, args []string, cols, rows int) (terminalSession, error)

type ptySession struct {
	cmd *exec.Cmd
	tty *os.File
}

func startPTY(ctx context.Context, bin, dir string, args []string, cols, rows int) (terminalSession, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var (
		tty *os.File
		err error
	)
	if cols > 0 && rows > 0 {
		tty, err = pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	} else {
		tty, err = pty.Start(cmd)
	}
	if err != nil {
		return nil, err
	}
	return &ptySession{cmd: cmd, tty: tty}, nil
}

func (s *ptySession) Read(p []byte) (int, error)  { return s.tty.Read(p) }
func (s *ptySession) Write(p []byte) (int, error) { return s.tty.Write(p) }
func (s *ptySession) Wait() error                 { return s.cmd.Wait() }
func (s *ptySession) Close() error                { return s.tty.Close() }
func (s *ptySession) Kill() error {
	if s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Kill()
}

// ClaudeDefaultModel is the model a claude child runs under when no config layer
// supplies one. Omitting --model hands the choice to the user's own Claude Code
// settings, so an unset — or present-but-empty, which masks the layer below it —
// CLAUDE_MODEL would silently route every phase through whatever they last picked
// interactively.
const ClaudeDefaultModel = "opus"

// ModelFallbackNotice announces the built-in-model fallback once for the run that
// shares it. A run builds one claude backend per diverging phase route, so the
// notice — not the backend — is what keeps the warning to one per run. A nil
// notice announces nothing; the fallback still applies.
type ModelFallbackNotice struct{ announced atomic.Bool }

func (n *ModelFallbackNotice) announce(log *event.Log, phase, model string) {
	if n == nil || log == nil || !n.announced.CompareAndSwap(false, true) {
		return
	}
	log.Emit(event.KindModelFallback, phase, "no Claude model configured — running on the built-in default "+model, map[string]any{
		"provider": "claude",
		"model":    model,
	})
}

// ClaudeInteractive runs Claude in a real terminal session and uses a result
// file as the machine protocol. It deliberately does not pass -p/--print or
// --output-format; stdout is terminal UI only, never parsed for correctness.
type ClaudeInteractive struct {
	Bin                string
	Flags              []string
	Model              string
	Effort             string
	DisallowedTools    string
	StripMechanicalMCP bool
	Preamble           string
	ResultDir          string
	Dir                string
	Cols               int
	Rows               int
	SizeFn             func() (cols, rows int)
	Timeout            time.Duration
	StallWindow        time.Duration
	TrustPromptWait    time.Duration
	Log                *event.Log
	Tokens             TokenSink
	Transcripts        TranscriptSink
	ModelFallback      *ModelFallbackNotice
	OnSessionStart     func(sessionID, label string)
	now                func() time.Time
	start              terminalStarter
	steerPoll          time.Duration
}

func (c *ClaudeInteractive) Provider() string { return "claude" }

// Route reports the resolved provider/model/effort for pre-call display.
func (c *ClaudeInteractive) Route(string) (string, string, string) {
	return "claude", c.model(), c.Effort
}

func (c *ClaudeInteractive) model() string {
	if c.Model == "" {
		return ClaudeDefaultModel
	}
	return c.Model
}

func (c *ClaudeInteractive) args(prompt, sessionID, label string) []string {
	args := append([]string{}, c.Flags...)
	args = append(args, "--model", c.model())
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	if c.DisallowedTools != "" {
		args = append(args, "--disallowedTools="+c.DisallowedTools)
	}
	// --strict-mcp-config with no --mcp-config loads zero MCP servers, so a
	// mechanical phase pays none of the repo MCP config's startup latency or
	// schema tokens. Claude-only; other providers have no equivalent.
	if c.StripMechanicalMCP && MechanicalPhase(label) {
		args = append(args, "--strict-mcp-config")
	}
	if sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	return append(args, prompt)
}

func (c *ClaudeInteractive) Run(ctx context.Context, prompt, label string) (Result, error) {
	if c.Model == "" {
		c.ModelFallback.announce(c.Log, label, ClaudeDefaultModel)
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	resultPath, err := c.resultPath(label)
	if err != nil {
		return Result{}, err
	}
	full := c.Preamble + "\n\n" + prompt + "\n\n" + resultInstruction(resultPath)

	sessionID, _ := newUUID()
	if c.OnSessionStart != nil {
		c.OnSessionStart(sessionID, label)
	}

	starter := c.start
	if starter == nil {
		starter = startPTY
	}

	cols, rows := c.resolveSize()
	stem := newTranscriptStem(label, c.clock())
	transcript, closeTranscript := c.openTranscript(stem, cols, rows)
	defer closeTranscript()

	start := c.clock()
	sess, err := starter(ctx, c.Bin, c.Dir, c.args(full, sessionID, label), cols, rows)
	if err != nil {
		res := Result{IsError: true}
		c.emit(label, res, c.clock().Sub(start), err)
		return res, fmt.Errorf("claude interactive run (%s): %w", label, err)
	}
	defer func() { _ = sess.Close() }()

	// lastActivity tracks the most recent transcript byte so the stall watchdog
	// can tell a working-but-quiet agent from one wedged before it ever produced
	// output (the COD-498 stall: 0 bytes for the full timeout). Seeded at start so
	// a process that never emits anything trips the window from launch.
	var lastActivity atomic.Int64
	lastActivity.Store(start.UnixNano())

	if c.Log != nil {
		c.Log.Emit(event.KindAgentStart, label, "", map[string]any{
			"transcript_id": stem,
			"cols":          cols,
			"rows":          rows,
		})
	}

	trustPrompt := make(chan struct{}, 1)
	authPrompt := make(chan struct{}, 1)
	go drainWithTrustSignal(transcript, sess, trustPrompt, authPrompt, func() {
		lastActivity.Store(c.clock().UnixNano())
	})

	wait := make(chan error, 1)
	go func() { wait <- sess.Wait() }()

	trustWait := c.TrustPromptWait
	if trustWait == 0 {
		trustWait = 3 * time.Second
	}
	if trustWait > 0 {
		if ok, err := c.maybeConfirmTrust(ctx, sess, trustPrompt, trustWait); err != nil {
			_ = sess.Kill()
			res := Result{IsError: true}
			c.emit(label, res, c.clock().Sub(start), err)
			return res, fmt.Errorf("claude interactive run (%s): %w", label, err)
		} else if ok {
			if err := sleepContext(ctx, time.Second); err != nil {
				_ = sess.Kill()
				res := Result{IsError: true}
				c.emit(label, res, c.clock().Sub(start), err)
				return res, fmt.Errorf("claude interactive run (%s): %w", label, err)
			}
		}
	}

	// Started after the trust window so the two PTY writers never overlap.
	if src, ok := SteerFrom(ctx); ok {
		steerCtx, stopSteer := context.WithCancel(ctx)
		defer stopSteer()
		go c.deliverSteer(steerCtx, sess, src, label)
	}

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for {
		if final, ok, err := readResultFile(resultPath); err != nil {
			_ = sess.Kill()
			res := Result{IsError: true}
			c.emit(label, res, c.clock().Sub(start), err)
			return res, fmt.Errorf("claude interactive run (%s): read result: %w", label, err)
		} else if ok {
			_ = sess.Kill()
			res := c.enrich(Result{Final: final}, sessionID)
			dur := c.clock().Sub(start)
			c.emit(label, res, dur, nil)
			c.record(label, res, dur)
			return res, nil
		}

		select {
		case err := <-wait:
			final, ok, readErr := readResultFile(resultPath)
			res := c.enrich(Result{Final: final, IsError: err != nil || readErr != nil || !ok}, sessionID)
			dur := c.clock().Sub(start)
			c.emit(label, res, dur, err)
			c.record(label, res, dur)
			switch {
			case readErr != nil:
				return res, fmt.Errorf("claude interactive run (%s): read result after exit: %w", label, readErr)
			case ok:
				return res, err
			case err != nil:
				return res, fmt.Errorf("claude interactive run (%s): %w", label, err)
			default:
				return res, fmt.Errorf("claude interactive run (%s): exited without writing result file %s", label, resultPath)
			}
		case <-ctx.Done():
			_ = sess.Kill()
			res := Result{IsError: true}
			c.emit(label, res, c.clock().Sub(start), ctx.Err())
			return res, fmt.Errorf("claude interactive run (%s): %w", label, ctx.Err())
		case <-authPrompt:
			// The agent hit a provider auth/login wall (403 / "Please run /login")
			// and would otherwise idle here until the stall watchdog kills it, only
			// for every retry to re-hit the same wall. Fail fast with a classifiable
			// error so the pipeline pauses blamelessly instead of faulting the ticket.
			_ = sess.Kill()
			res := Result{IsError: true}
			c.emit(label, res, c.clock().Sub(start), ErrAuthRequired)
			return res, fmt.Errorf("claude interactive run (%s): %w", label, ErrAuthRequired)
		case <-tick.C:
			if c.StallWindow > 0 {
				if idle := c.clock().Sub(time.Unix(0, lastActivity.Load())); idle >= c.StallWindow {
					_ = sess.Kill()
					res := Result{IsError: true}
					stallErr := fmt.Errorf("no agent output for %s — stalled before AGENT_TIMEOUT", c.StallWindow)
					c.emit(label, res, c.clock().Sub(start), stallErr)
					return res, fmt.Errorf("claude interactive run (%s): %w", label, stallErr)
				}
			}
		}
	}
}

func (c *ClaudeInteractive) resultPath(label string) (string, error) {
	root := c.ResultDir
	if root == "" {
		root = filepath.Join(os.TempDir(), "trau-agent-results")
	}
	dir := filepath.Join(root, ResultsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create result dir: %w", err)
	}
	name := fmt.Sprintf("%d-%s%s", c.clock().UnixNano(), safeLabel(label), resultExt)

	abs, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("resolve result path: %w", err)
	}
	return abs, nil
}

func (c *ClaudeInteractive) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *ClaudeInteractive) enrich(res Result, sessionID string) Result {
	if sessionID == "" {
		return res
	}
	stats, ok := readSessionStats(claudeConfigDir(), sessionID)
	if !ok {
		return res
	}
	res.Usage = stats.Usage
	res.NumTurns = stats.Turns
	res.Context = stats.Context
	res.Skills = stats.Skills
	res.Model = stats.Model
	if res.Model == "" {
		res.Model = c.model()
	}
	return res
}

func (c *ClaudeInteractive) emit(label string, res Result, dur time.Duration, runErr error) {
	if c.Log == nil {
		return
	}
	model := res.Model
	if model == "" {
		model = c.model()
	}
	fields := map[string]any{
		"provider":       "claude",
		"mode":           "interactive",
		"model":          model,
		"effort":         c.Effort,
		"is_error":       res.IsError || runErr != nil,
		"num_turns":      res.NumTurns,
		"input_tokens":   res.Usage.Input,
		"output_tokens":  res.Usage.Output,
		"total_tokens":   res.Usage.Input + res.Usage.Output + res.Usage.CacheRead + res.Usage.CacheCreation,
		"context_tokens": res.Context,
		"cost_usd":       res.CostUSD,
		"duration_ms":    dur.Milliseconds(),
	}
	if len(res.Skills) > 0 {
		fields["skills"] = res.Skills
	}
	if runErr != nil {
		fields["error"] = runErr.Error()
	}
	c.Log.Emit("agent_call", label, "", fields)
}

func (c *ClaudeInteractive) maybeConfirmTrust(ctx context.Context, sess terminalSession, trustPrompt <-chan struct{}, wait time.Duration) (bool, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-trustPrompt:
		_, err := sess.Write([]byte("\r"))
		if err != nil {
			return true, fmt.Errorf("confirm trust prompt: %w", err)
		}
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return false, nil
	}
}

func (c *ClaudeInteractive) record(label string, res Result, dur time.Duration) {
	if c.Tokens == nil {
		return
	}

	model := res.Model
	if model == "" {
		model = c.model()
	}
	cost := tokens.EstimateCost(model, res.Usage.Input, res.Usage.Output, res.Usage.CacheRead, res.Usage.CacheCreation)
	c.Tokens.Append(label, tokens.Record{
		Input:         res.Usage.Input,
		Output:        res.Usage.Output,
		CacheRead:     res.Usage.CacheRead,
		CacheCreation: res.Usage.CacheCreation,
		Reasoning:     res.Usage.Reasoning,
		CostUSD:       &cost,
		Turns:         res.NumTurns,
		IsError:       res.IsError,
		Provider:      "claude",
		Model:         model,
		Effort:        c.Effort,
		Context:       res.Context,
		Duration:      dur,
		Skills:        res.Skills,
	})
}

func resultInstruction(path string) string {
	return "Do not rely on stdout as the machine-readable response. When this phase is complete, write your result to exactly this file path, creating parent directories if needed: " + path + "\n" +
		"Write the result in the form the task requests — the requested sentinel/text, or a small JSON object when the task offers one (for a pick, either 'PICK=<ID>'/'PICK=NONE' or {\"pick\":\"<ID>\"}/{\"pick\":\"NONE\"} is accepted). After the file is written, stop working and leave the session idle; the loop will close the terminal."
}

// ResultsSubdir holds the ephemeral agent-interface result files
// (<stem>.result.json) the CLI writes and the loop reads back (ADR 0008 §6). PTY
// transcripts no longer land on disk — they stream to the hub (ADR 0008 §4).
const (
	ResultsSubdir = "_agent-results"
	resultExt     = ".result.json"
)

const (
	defaultCols = 80
	defaultRows = 24
)

func effectiveSize(cols, rows int) (int, int) {
	if cols <= 0 || rows <= 0 {
		return defaultCols, defaultRows
	}
	return cols, rows
}

func (c *ClaudeInteractive) resolveSize() (int, int) {
	return resolveSize(c.SizeFn, c.Cols, c.Rows)
}

func resolveSize(sizeFn func() (int, int), cols, rows int) (int, int) {
	if sizeFn != nil {
		if c, r := sizeFn(); c > 0 && r > 0 {
			return effectiveSize(c, r)
		}
	}
	return effectiveSize(cols, rows)
}

// newTranscriptStem names a transcript session <unix-nano>-<label>: the session
// id the hub keys chunks by and the consumers pin their replay to. The nanosecond
// prefix is the session-start time the follow-mode since bound orders on.
func newTranscriptStem(label string, now time.Time) string {
	return fmt.Sprintf("%d-%s", now.UnixNano(), safeLabel(label))
}

// openTranscript returns the writer the agent tees PTY output to for this session,
// plus a close. With no sink configured it discards, so a run without a hub still
// works (its live tail is simply unavailable).
func (c *ClaudeInteractive) openTranscript(stem string, cols, rows int) (io.Writer, func()) {
	if c.Transcripts == nil {
		return io.Discard, func() {}
	}
	w := c.Transcripts.Open(stem, cols, rows)
	return w, func() { _ = w.Close() }
}

// liveTranscript starts a transcript session for a stdout-piped backend (Codex,
// Kimi), emitting the agent-start event and returning the writer to tee output to.
// ok is false when no sink is configured, so the caller tees nothing.
func liveTranscript(sink TranscriptSink, log *event.Log, label string, cols, rows int, now time.Time) (io.WriteCloser, bool) {
	if sink == nil {
		return nil, false
	}
	stem := newTranscriptStem(label, now)
	w := sink.Open(stem, cols, rows)
	if log != nil {
		log.Emit(event.KindAgentStart, label, "", map[string]any{
			"transcript_id": stem,
			"cols":          cols,
			"rows":          rows,
		})
	}
	return w, true
}

func drainWithTrustSignal(dst io.Writer, src io.Reader, trustPrompt, authPrompt chan<- struct{}, onActivity func()) {
	buf := make([]byte, 4096)
	var seen strings.Builder
	trustSeen, authSeen := false, false
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if onActivity != nil {
				onActivity()
			}
			chunk := buf[:n]
			_, _ = dst.Write(chunk)
			if !trustSeen || !authSeen {
				seen.WriteString(string(chunk))
				text := seen.String()
				if !trustSeen && strings.Contains(text, "Quick") && strings.Contains(text, "safety") && strings.Contains(text, "trust") {
					trustSeen = true
					signalOnce(trustPrompt)
				}
				if !authSeen && hasAuthFailure(text) {
					authSeen = true
					signalOnce(authPrompt)
				}
				if seen.Len() > 8192 {
					trimmed := text[len(text)-4096:]
					seen.Reset()
					seen.WriteString(trimmed)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// signalOnce does a non-blocking send so a full (already-signaled) channel never
// wedges the transcript drain.
func signalOnce(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// ErrAuthRequired marks an agent run that failed because the provider needs
// re-authentication — an OAuth/login wall, a 403, an invalid key, or an exhausted
// credit balance — rather than because the work or the agent process went wrong.
// Claude Code surfaces these as "API Error: 403 Request not allowed / Please run
// /login" and then idles at a prompt, so the call would otherwise read as a
// generic output stall. The pipeline treats it as a blameless pause (no retries,
// ticket left resumable) instead of burning the recovery budget and faulting the
// ticket. See COD-596.
var ErrAuthRequired = errors.New("provider authentication required — re-login (e.g. run claude, then /login)")

// StripANSI removes ANSI escape/control sequences from s. It delegates to the
// shared sanitize package so the ANSI pattern has a single source of truth.
func StripANSI(s string) string { return sanitize.StripANSI(s) }

// hasAuthFailure reports whether the agent's terminal output shows a provider
// auth/login wall that won't clear without human re-authentication. It strips ANSI
// styling/cursor codes first so a marker drawn with interleaved color or
// positioning sequences still matches, and requires distinctive paired tokens for
// the 403 case to avoid pausing on incidental prose.
func hasAuthFailure(s string) bool {
	low := strings.ToLower(sanitize.StripANSI(s))
	switch {
	case strings.Contains(low, "please run /login"):
		return true
	case strings.Contains(low, "invalid api key"):
		return true
	case strings.Contains(low, "credit balance is too low"):
		return true
	case strings.Contains(low, "oauth token") && strings.Contains(low, "expired"):
		return true
	case strings.Contains(low, "403") && strings.Contains(low, "not allowed"):
		return true
	default:
		return false
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func readResultFile(path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	text := strings.TrimRight(string(b), "\n")
	if strings.TrimSpace(text) == "" {
		return "", false, nil
	}
	return text, true, nil
}

func safeLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "phase"
	}
	return b.String()
}

// Codex runs `codex exec --json -o <msgfile> "<prompt>"`, the second backend
// behind the seam. It diverges from Claude in three ways, all confined
// here: the final agent message is read from the -o file (not stdout); the --json
// event stream on stdout carries token usage which is summed and dropped; and
// codex reports input_tokens INCLUDING cached, so usage is renormalized to the
// shared non-cached schema. There is no disallowed-tools field: codex exec is a
// single-agent runner with no Agent/Workflow fan-out tool, so Claude's
// --disallowedTools block has no codex equivalent — the Preamble covers fan-out
// denial instead.
type Codex struct {
	Bin         string
	Flags       []string
	Profile     string
	Model       string
	Effort      string
	Preamble    string
	Dir         string
	ResultDir   string
	Cols        int
	Rows        int
	SizeFn      func() (cols, rows int)
	Log         *event.Log
	Tokens      TokenSink
	Transcripts TranscriptSink
	now         func() time.Time
}

// Provider names the backend for logging and routing attribution.
func (c *Codex) Provider() string { return "codex" }

// Route reports the configured provider/model/effort for pre-call display.
func (c *Codex) Route(string) (string, string, string) { return "codex", c.Model, c.Effort }

func (c *Codex) args(prompt, msgPath string) []string {
	args := []string{"exec"}
	args = append(args, c.Flags...)
	args = append(args, "--json", "-o", msgPath)
	if c.Profile != "" {
		args = append(args, "--profile", c.Profile)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+c.Effort)
	}
	return append(args, prompt)
}

type codexEvent struct {
	Type  string `json:"type"`
	Usage struct {
		Input     int `json:"input_tokens"`
		Cached    int `json:"cached_input_tokens"`
		Output    int `json:"output_tokens"`
		Reasoning int `json:"reasoning_output_tokens"`
	} `json:"usage"`
}

func parseCodexUsage(stream []byte) (Usage, int) {
	var u Usage
	turns := 0
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal(b, &ev); err != nil || ev.Type != "turn.completed" {
			continue
		}
		turns++
		u.Input += ev.Usage.Input - ev.Usage.Cached
		u.Output += ev.Usage.Output
		u.CacheRead += ev.Usage.Cached
		u.Reasoning += ev.Usage.Reasoning
	}
	return u, turns
}

// Run executes one fresh codex process and returns its final message. The prompt
// is Preamble + blank line + the caller's prompt, identical to Claude. codex
// streams --json events (token usage) to stdout, which we parse then drop, and
// writes the final agent message to a temp -o file we read and clean up. If that
// file is missing or empty (a crash before codex wrote it), Final is empty rather
// than the raw event JSON — `codex exec` itself prints no event noise as its
// message, so dumping stdout here would be wrong; the -o file is the only source
// of the final message.
func (c *Codex) Run(ctx context.Context, prompt, label string) (Result, error) {
	full := c.Preamble + "\n\n" + prompt

	msg, err := os.CreateTemp("", "trau-codex-msg-*")
	if err != nil {
		return Result{}, fmt.Errorf("codex run (%s): create message file: %w", label, err)
	}
	msgPath := msg.Name()
	_ = msg.Close()
	defer func() { _ = os.Remove(msgPath) }()

	var stdout bytes.Buffer
	sink := io.Writer(&stdout)
	cols, rows := resolveSize(c.SizeFn, c.Cols, c.Rows)
	if live, ok := liveTranscript(c.Transcripts, c.Log, label, cols, rows, c.clock()); ok {
		defer func() { _ = live.Close() }()
		sink = io.MultiWriter(&stdout, live)
	}

	cmd := exec.CommandContext(ctx, c.Bin, c.args(full, msgPath)...)
	cmd.Dir = c.Dir
	cmd.Stdout = sink
	cmd.Stderr = nil

	start := c.clock()
	runErr := cmd.Run()
	dur := c.clock().Sub(start)

	res := Result{}
	res.Usage, res.NumTurns = parseCodexUsage(stdout.Bytes())

	final, _ := os.ReadFile(msgPath)
	res.Final = strings.TrimRight(string(final), "\n")

	c.emit(label, res, dur, runErr)
	c.record(label, res, dur)

	if runErr != nil {
		return res, fmt.Errorf("codex run (%s): %w", label, runErr)
	}
	return res, nil
}

func (c *Codex) emit(label string, res Result, dur time.Duration, runErr error) {
	if c.Log == nil {
		return
	}
	fields := map[string]any{
		"provider":       "codex",
		"model":          c.Model,
		"effort":         c.Effort,
		"is_error":       res.IsError || runErr != nil,
		"num_turns":      res.NumTurns,
		"input_tokens":   res.Usage.Input,
		"output_tokens":  res.Usage.Output,
		"total_tokens":   res.Usage.Input + res.Usage.Output + res.Usage.CacheRead + res.Usage.CacheCreation,
		"context_tokens": res.Context,
		"cost_usd":       res.CostUSD,
		"duration_ms":    dur.Milliseconds(),
	}
	if runErr != nil {
		fields["error"] = runErr.Error()
	}
	c.Log.Emit("agent_call", label, "", fields)
}

func (c *Codex) record(label string, res Result, dur time.Duration) {
	if c.Tokens == nil {
		return
	}
	c.Tokens.Append(label, tokens.Record{
		Input:         res.Usage.Input,
		Output:        res.Usage.Output,
		CacheRead:     res.Usage.CacheRead,
		CacheCreation: res.Usage.CacheCreation,
		Reasoning:     res.Usage.Reasoning,
		CostUSD:       nil,
		Turns:         res.NumTurns,
		IsError:       res.IsError,
		Provider:      "codex",
		Model:         c.Model,
		Effort:        c.Effort,
		Duration:      dur,
	})
}

func (c *Codex) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Kimi runs Kimi Code CLI in print mode (`kimi -p <prompt> --output-format
// stream-json`) and returns the final assistant message parsed from the stream.
// Each call is a fresh process — never -S/--continue session reuse — so phases
// stay isolated. Print mode auto-executes tools (no approval prompt) and rejects
// --yolo/--auto, so KIMI_FLAGS is normally empty.
//
// Token accounting: Kimi prints no usage on stdout, but it persists per-turn
// usage.record events to the session's wire.jsonl. Run recovers them via the
// session id echoed in the stream's session.resume_hint line, normalizing the
// counts into the shared non-cached schema (inputOther→Input, inputCacheRead→
// CacheRead, inputCacheCreation→CacheCreation). CostUSD stays nil: Kimi exposes
// tokens but no per-call dollar cost (a subscription plan, like codex), so spend
// is reported as unmetered rather than a misleading $0.
type Kimi struct {
	Bin         string
	Flags       []string
	Model       string
	Preamble    string
	Dir         string
	ResultDir   string
	Cols        int
	Rows        int
	SizeFn      func() (cols, rows int)
	Timeout     time.Duration
	SessionsDir string
	Log         *event.Log
	Tokens      TokenSink
	Transcripts TranscriptSink
	now         func() time.Time
}

// Provider names the backend for logging and routing attribution.
func (c *Kimi) Provider() string { return "kimi" }

// Route reports the configured provider/model for pre-call display. Kimi Code's
// reasoning-effort knob (KIMI_MODEL_THINKING_EFFORT) only applies via the
// KIMI_MODEL_* env-provider mechanism, which trau does not drive, so effort is
// always empty here.
func (c *Kimi) Route(string) (string, string, string) { return "kimi", c.Model, "" }

func (c *Kimi) args(prompt string) []string {
	args := append([]string{}, c.Flags...)
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}

	return append(args, "--output-format", "stream-json", "-p", prompt)
}

type kimiEvent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func parseKimiFinal(stream []byte) string {
	final := ""
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ev kimiEvent
		if err := json.Unmarshal(b, &ev); err != nil || ev.Role != "assistant" {
			continue
		}
		if ev.Content != "" {
			final = ev.Content
		}
	}
	return final
}

type kimiResumeHint struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// parseKimiSessionID returns the session id Kimi echoes in its stream's
// session.resume_hint line, or "" if absent. The id locates the session's
// wire.jsonl, the only place Kimi records per-call token usage.
func parseKimiSessionID(stream []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ev kimiResumeHint
		if err := json.Unmarshal(b, &ev); err != nil {
			continue
		}
		if ev.Type == "session.resume_hint" && ev.SessionID != "" {
			return ev.SessionID
		}
	}
	return ""
}

type kimiUsageRecord struct {
	Type       string `json:"type"`
	Model      string `json:"model"`
	UsageScope string `json:"usageScope"`
	Usage      struct {
		InputOther         int `json:"inputOther"`
		Output             int `json:"output"`
		InputCacheRead     int `json:"inputCacheRead"`
		InputCacheCreation int `json:"inputCacheCreation"`
	} `json:"usage"`
}

// readKimiUsage sums the turn-scoped usage.record events Kimi writes to the
// session's wire.jsonl (one per LLM turn, across every sub-agent), normalizing
// them into the shared non-cached schema. ok is false when no usage file or
// record is found, so the caller can flag the call unrecovered instead of
// recording a false zero.
func readKimiUsage(sessionsDir, sessionID string) (u Usage, turns int, model string, ok bool) {
	if sessionsDir == "" || sessionID == "" {
		return Usage{}, 0, "", false
	}
	matches, _ := filepath.Glob(filepath.Join(sessionsDir, "*", sessionID, "agents", "*", "wire.jsonl"))
	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			b := bytes.TrimSpace(sc.Bytes())
			if len(b) == 0 {
				continue
			}
			var ev kimiUsageRecord
			if err := json.Unmarshal(b, &ev); err != nil || ev.Type != "usage.record" || ev.UsageScope != "turn" {
				continue
			}
			turns++
			ok = true
			if ev.Model != "" {
				model = ev.Model
			}
			u.Input += ev.Usage.InputOther
			u.Output += ev.Usage.Output
			u.CacheRead += ev.Usage.InputCacheRead
			u.CacheCreation += ev.Usage.InputCacheCreation
		}
		_ = f.Close()
	}
	return u, turns, model, ok
}

// kimiSessionsDir resolves where Kimi Code stores session transcripts:
// SessionsDir when set (tests/overrides), else ~/.kimi-code/sessions. Returns ""
// if the home directory can't be resolved, which readKimiUsage treats as
// "no usage available".
func (c *Kimi) kimiSessionsDir() string {
	if c.SessionsDir != "" {
		return c.SessionsDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kimi-code", "sessions")
}

// Run executes one fresh kimi process and returns its final assistant message. The
// prompt is Preamble + blank line + the caller's prompt, like the other backends.
// stdout carries the stream-json events; parseKimiFinal extracts the final message
// from them. stderr is progress, kept only for error context.
func (c *Kimi) Run(ctx context.Context, prompt, label string) (Result, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	full := c.Preamble + "\n\n" + prompt

	var stdout, stderr bytes.Buffer
	sink := io.Writer(&stdout)
	cols, rows := resolveSize(c.SizeFn, c.Cols, c.Rows)
	if live, ok := liveTranscript(c.Transcripts, c.Log, label, cols, rows, c.clock()); ok {
		defer func() { _ = live.Close() }()
		sink = io.MultiWriter(&stdout, live)
	}

	cmd := exec.CommandContext(ctx, c.Bin, c.args(full)...)
	cmd.Dir = c.Dir
	cmd.Stdout = sink
	cmd.Stderr = &stderr

	start := c.clock()
	runErr := cmd.Run()
	dur := c.clock().Sub(start)

	res := Result{Model: c.Model}
	res.Final = parseKimiFinal(stdout.Bytes())

	usageOK := false
	if sid := parseKimiSessionID(stdout.Bytes()); sid != "" {
		if u, turns, model, ok := readKimiUsage(c.kimiSessionsDir(), sid); ok {
			usageOK = true
			res.Usage = u
			res.NumTurns = turns
			if model != "" {
				res.Model = model
			}
		}
	}

	c.emit(label, res, dur, runErr, usageOK)
	c.record(label, res, dur)

	if runErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return res, fmt.Errorf("kimi run (%s): %w: %s", label, runErr, msg)
		}
		return res, fmt.Errorf("kimi run (%s): %w", label, runErr)
	}
	return res, nil
}

func (c *Kimi) emit(label string, res Result, dur time.Duration, runErr error, usageRecovered bool) {
	if c.Log == nil {
		return
	}
	fields := map[string]any{
		"provider":        "kimi",
		"model":           res.Model,
		"effort":          "",
		"is_error":        res.IsError || runErr != nil,
		"num_turns":       res.NumTurns,
		"input_tokens":    res.Usage.Input,
		"output_tokens":   res.Usage.Output,
		"total_tokens":    res.Usage.Input + res.Usage.Output + res.Usage.CacheRead + res.Usage.CacheCreation,
		"context_tokens":  res.Context,
		"cost_usd":        res.CostUSD,
		"usage_recovered": usageRecovered,
		"duration_ms":     dur.Milliseconds(),
	}
	if runErr != nil {
		fields["error"] = runErr.Error()
	}
	c.Log.Emit("agent_call", label, "", fields)
}

func (c *Kimi) record(label string, res Result, dur time.Duration) {
	if c.Tokens == nil {
		return
	}
	model := res.Model
	if model == "" {
		model = c.Model
	}
	c.Tokens.Append(label, tokens.Record{
		Input:         res.Usage.Input,
		Output:        res.Usage.Output,
		CacheRead:     res.Usage.CacheRead,
		CacheCreation: res.Usage.CacheCreation,
		CostUSD:       nil,
		Turns:         res.NumTurns,
		IsError:       res.IsError,
		Provider:      "kimi",
		Model:         model,
		Duration:      dur,
	})
}

func (c *Kimi) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
