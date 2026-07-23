package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/sanitize"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// CodexInteractive drives the codex TUI in a real terminal session, which is what
// buys codex the same mid-phase steering claude has: a running session can be
// typed into, `codex exec` cannot. The prompt is the launch argument, completion
// is the shared result file, and stdout is terminal UI that is teed to the live
// view but never parsed.
//
// Usage cannot come from stdout here — the --json event stream only exists in
// exec mode — so it is recovered from the session rollout codex writes under
// $CODEX_HOME/sessions and normalized into the shared non-cached schema. CostUSD
// stays nil: codex bills a subscription, so spend is reported as unmetered rather
// than a misleading $0.
type CodexInteractive struct {
	Bin             string
	Flags           []string
	Profile         string
	Model           string
	Effort          string
	Preamble        string
	ResultDir       string
	Dir             string
	Cols            int
	Rows            int
	SizeFn          func() (cols, rows int)
	Timeout         time.Duration
	StallWindow     time.Duration
	TrustPromptWait time.Duration
	SessionsDir     string
	Log             *event.Log
	Tokens          TokenSink
	Transcripts     TranscriptSink
	now             func() time.Time
	start           terminalStarter
	steerPoll       time.Duration
	usageWait       time.Duration
}

// Provider names the backend for logging and routing attribution.
func (c *CodexInteractive) Provider() string { return "codex" }

// Route reports the configured provider/model/effort for pre-call display.
func (c *CodexInteractive) Route(string) (string, string, string) { return "codex", c.Model, c.Effort }

func (c *CodexInteractive) args(prompt string) []string {
	args := append([]string{}, c.Flags...)
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

func (c *CodexInteractive) Run(ctx context.Context, prompt, label string) (Result, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	resultPath, err := newResultPath(c.ResultDir, label, c.clock())
	if err != nil {
		return Result{}, err
	}
	full := c.Preamble + "\n\n" + prompt + "\n\n" + resultInstruction(resultPath)

	starter := c.start
	if starter == nil {
		starter = startPTY
	}

	cols, rows := resolveSize(c.SizeFn, c.Cols, c.Rows)
	stem := newTranscriptStem(label, c.clock())
	transcript, closeTranscript := openTranscript(c.Transcripts, stem, cols, rows)
	defer closeTranscript()

	start := c.clock()
	sess, err := starter(ctx, c.Bin, c.Dir, c.args(full), cols, rows)
	if err != nil {
		res := Result{IsError: true, Model: c.Model}
		c.emit(label, res, c.clock().Sub(start), err, false)
		return res, fmt.Errorf("codex interactive run (%s): %w", label, err)
	}
	defer func() { _ = sess.Close() }()

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
	go drainWithSignals(transcript, sess, codexWatch, terminalSignals{trust: trustPrompt, auth: authPrompt}, func() {
		lastActivity.Store(c.clock().UnixNano())
	})

	wait := make(chan error, 1)
	go func() { wait <- sess.Wait() }()

	fail := func(cause error) (Result, error) {
		_ = sess.Kill()
		res := Result{IsError: true, Model: c.Model}
		c.emit(label, res, c.clock().Sub(start), cause, false)
		return res, fmt.Errorf("codex interactive run (%s): %w", label, cause)
	}

	trustWait := c.TrustPromptWait
	if trustWait == 0 {
		trustWait = defaultTrustPromptWait
	}
	confirmed, err := confirmTrustPrompt(ctx, sess, trustPrompt, trustWait)
	if err != nil {
		return fail(err)
	}
	if confirmed {
		// The dialog dismissal replays the launch prompt; give the composer a beat
		// to submit it before a steer writer can interleave with it.
		if err := sleepContext(ctx, time.Second); err != nil {
			return fail(err)
		}
	}

	if src, ok := SteerFrom(ctx); ok {
		steerCtx, stopSteer := context.WithCancel(ctx)
		defer stopSteer()
		go deliverSteer(steerCtx, sess, src, label, c.steerPoll, c.Log)
	}

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for {
		if final, ok, err := readResultFile(resultPath); err != nil {
			_ = sess.Kill()
			res := Result{IsError: true, Model: c.Model}
			c.emit(label, res, c.clock().Sub(start), err, false)
			return res, fmt.Errorf("codex interactive run (%s): read result: %w", label, err)
		} else if ok {
			stats, usageOK := c.awaitUsage(ctx, start)
			_ = sess.Kill()
			res := c.enrich(Result{Final: final}, stats, usageOK)
			dur := c.clock().Sub(start)
			c.emit(label, res, dur, nil, usageOK)
			c.record(label, res, dur, usageOK)
			return res, nil
		}

		select {
		case err := <-wait:
			final, ok, readErr := readResultFile(resultPath)
			stats, usageOK := readCodexSessionStats(c.sessionsDir(), c.Dir, start)
			res := c.enrich(Result{Final: final, IsError: err != nil || readErr != nil || !ok}, stats, usageOK)
			dur := c.clock().Sub(start)
			c.emit(label, res, dur, err, usageOK)
			c.record(label, res, dur, usageOK)
			switch {
			case readErr != nil:
				return res, fmt.Errorf("codex interactive run (%s): read result after exit: %w", label, readErr)
			case ok:
				return res, err
			case err != nil:
				return res, fmt.Errorf("codex interactive run (%s): %w", label, err)
			default:
				return res, fmt.Errorf("codex interactive run (%s): exited without writing result file %s", label, resultPath)
			}
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-authPrompt:
			return fail(ErrAuthRequired)
		case <-tick.C:
			if c.StallWindow > 0 {
				if idle := c.clock().Sub(time.Unix(0, lastActivity.Load())); idle >= c.StallWindow {
					return fail(fmt.Errorf("no agent output for %s — stalled before AGENT_TIMEOUT", c.StallWindow))
				}
			}
		}
	}
}

// codexUsagePoll is how often the rollout is re-read while codex is finishing its
// accounting; defaultCodexUsageWait caps that wait.
const (
	codexUsagePoll        = 250 * time.Millisecond
	defaultCodexUsageWait = 10 * time.Second
)

// awaitUsage holds the session open until codex has flushed the turn's accounting
// into its rollout. Codex records token_count only once the tool call that wrote
// the result file has been fed back to the model, so killing the moment the file
// appears leaves the call with no accounting at all. The wait ends as soon as two
// consecutive reads agree; a session that wrote no rollout has nothing to wait for.
// Pinning the rollout up front also keeps a session started later in the same
// workspace from being picked up mid-wait.
func (c *CodexInteractive) awaitUsage(ctx context.Context, since time.Time) (codexRolloutStats, bool) {
	path, found := findCodexRollout(c.sessionsDir(), c.Dir, since)
	if !found {
		return codexRolloutStats{}, false
	}
	wait := c.usageWait
	if wait == 0 {
		wait = defaultCodexUsageWait
	}
	deadline := c.clock().Add(wait)

	var last codexRolloutStats
	recovered := false
	for {
		stats, ok := readCodexRollout(path)
		if ok {
			if recovered && stats == last {
				return stats, true
			}
			last, recovered = stats, true
		}
		if !c.clock().Before(deadline) {
			return last, recovered
		}
		if err := sleepContext(ctx, codexUsagePoll); err != nil {
			return last, recovered
		}
	}
}

// enrich folds a recovered rollout's accounting into res. An unrecovered call
// keeps its zero usage and is reported as unrecovered rather than as a real zero.
func (c *CodexInteractive) enrich(res Result, stats codexRolloutStats, recovered bool) Result {
	if recovered {
		res.Usage = stats.Usage
		res.NumTurns = stats.Turns
		res.Context = stats.Context
		res.Model = stats.Model
	}
	if res.Model == "" {
		res.Model = c.Model
	}
	return res
}

func (c *CodexInteractive) sessionsDir() string {
	if c.SessionsDir != "" {
		return c.SessionsDir
	}
	return filepath.Join(codexHome(), "sessions")
}

func codexHome() string {
	if d := strings.TrimSpace(os.Getenv("CODEX_HOME")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

// codexWatch is how a codex session's terminal output is recognized. Codex
// positions the cursor before every word on both of these screens, so neither
// sentence ever appears as one contiguous run in the byte stream — the markers are
// matched against the text with escape sequences and layout whitespace removed.
var codexWatch = terminalWatch{trusts: hasCodexTrustPrompt, auths: hasCodexAuthWall}

// hasCodexTrustPrompt reports whether codex's directory-trust dialog is on screen.
func hasCodexTrustPrompt(s string) bool {
	flat := squashSpace(strings.ToLower(sanitize.StripANSI(s)))
	return strings.Contains(flat, "doyoutrust") && strings.Contains(flat, "yes,continue")
}

// hasCodexAuthWall reports whether codex is blocked on authentication: its
// onboarding sign-in screen, or a refresh that could not renew the session. None
// of these clear without a human re-login, and the animated TUI keeps redrawing
// behind them, so the stall watchdog never sees the run go quiet.
func hasCodexAuthWall(s string) bool {
	flat := squashSpace(strings.ToLower(sanitize.StripANSI(s)))
	switch {
	case strings.Contains(flat, "signinwithchatgpt") && strings.Contains(flat, "provideyourownapikey"):
		return true
	case strings.Contains(flat, "pleasesigninagain"), strings.Contains(flat, "pleaselogoutandsigninagain"):
		return true
	default:
		return hasAuthFailure(s)
	}
}

func squashSpace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func (c *CodexInteractive) emit(label string, res Result, dur time.Duration, runErr error, usageRecovered bool) {
	if c.Log == nil {
		return
	}
	fields := map[string]any{
		"provider":        "codex",
		"mode":            "interactive",
		"model":           res.Model,
		"effort":          c.Effort,
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

// record appends the call to the token ledger. A call whose rollout never turned
// up is left out entirely: a zero row there is indistinguishable from a real free
// call, and the cost surfaces would read it as one.
func (c *CodexInteractive) record(label string, res Result, dur time.Duration, recovered bool) {
	if c.Tokens == nil || !recovered {
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
		Model:         res.Model,
		Effort:        c.Effort,
		Context:       res.Context,
		Duration:      dur,
	})
}

func (c *CodexInteractive) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
