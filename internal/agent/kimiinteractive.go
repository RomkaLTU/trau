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

// KimiInteractive drives the kimi TUI in a real terminal session, which is what
// buys kimi the same mid-phase steering claude has: a running session can be typed
// into, `kimi -p` cannot. Kimi accepts no prompt argument, so unlike the other
// backends the launch prompt is itself typed in — pasted into the composer once the
// TUI says it is reading input. Completion is the shared result file, and stdout is
// terminal UI that is teed to the live view but never parsed.
//
// Usage still comes from the session's wire.jsonl, parsed exactly as print mode
// parses it. Only the way back to that file changes: there is no stdout stream to
// carry a session.resume_hint, so the session is recovered from the index kimi
// keeps beside its sessions dir. CostUSD stays nil: kimi bills a subscription, so
// spend is reported as unmetered rather than a misleading $0.
type KimiInteractive struct {
	Bin          string
	Flags        []string
	Model        string
	Preamble     string
	ResultDir    string
	Dir          string
	Cols         int
	Rows         int
	SizeFn       func() (cols, rows int)
	Timeout      time.Duration
	StallWindow  time.Duration
	ComposerWait time.Duration
	SessionsDir  string
	Log          *event.Log
	Tokens       TokenSink
	Transcripts  TranscriptSink
	now          func() time.Time
	start        terminalStarter
	steerPoll    time.Duration
	usageWait    time.Duration
	settle       time.Duration
}

// Provider names the backend for logging and routing attribution.
func (c *KimiInteractive) Provider() string { return "kimi" }

// Route reports the configured provider/model for pre-call display. Kimi exposes
// no reasoning-effort knob trau can drive, so effort is always empty.
func (c *KimiInteractive) Route(string) (string, string, string) { return "kimi", c.Model, "" }

// args launches the TUI with tool approval turned off — the interactive
// equivalent of the auto-execution print mode does on its own, without which every
// tool call would block on a dialog no one is there to answer.
func (c *KimiInteractive) args() []string {
	args := append([]string{}, c.Flags...)
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return append(args, "--yolo")
}

func (c *KimiInteractive) Run(ctx context.Context, prompt, label string) (Result, error) {
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
	sess, err := starter(ctx, c.Bin, c.Dir, c.args(), cols, rows)
	if err != nil {
		res := Result{IsError: true, Model: c.Model}
		c.emit(label, res, c.clock().Sub(start), err, false)
		return res, fmt.Errorf("kimi interactive run (%s): %w", label, err)
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

	composer := make(chan struct{}, 1)
	authPrompt := make(chan struct{}, 1)
	go drainWithSignals(transcript, sess, kimiWatch, terminalSignals{auth: authPrompt, ready: composer}, func() {
		lastActivity.Store(c.clock().UnixNano())
	})

	wait := make(chan error, 1)
	go func() { wait <- sess.Wait() }()

	fail := func(cause error) (Result, error) {
		_ = sess.Kill()
		res := Result{IsError: true, Model: c.Model}
		c.emit(label, res, c.clock().Sub(start), cause, false)
		return res, fmt.Errorf("kimi interactive run (%s): %w", label, cause)
	}

	if err := c.submitPrompt(ctx, sess, composer, full); err != nil {
		return fail(err)
	}

	// Started after the launch prompt so the two PTY writers never overlap.
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
			return res, fmt.Errorf("kimi interactive run (%s): read result: %w", label, err)
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
			stats, usageOK := c.sessionStats(start)
			res := c.enrich(Result{Final: final, IsError: err != nil || readErr != nil || !ok}, stats, usageOK)
			dur := c.clock().Sub(start)
			c.emit(label, res, dur, err, usageOK)
			c.record(label, res, dur, usageOK)
			switch {
			case readErr != nil:
				return res, fmt.Errorf("kimi interactive run (%s): read result after exit: %w", label, readErr)
			case ok:
				return res, err
			case err != nil:
				return res, fmt.Errorf("kimi interactive run (%s): %w", label, err)
			default:
				return res, fmt.Errorf("kimi interactive run (%s): exited without writing result file %s", label, resultPath)
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

// defaultComposerWait bounds how long a launching kimi is given to announce its
// composer before the prompt is typed regardless; defaultComposerSettle is the beat
// the mounted composer gets before the first keystrokes land.
const (
	defaultComposerWait   = 15 * time.Second
	defaultComposerSettle = time.Second
)

// submitPrompt types the launch prompt into the composer, which is the moment a
// kimi phase actually begins. The keystrokes have to land after the TUI has turned
// bracketed paste on, or its markers are read as stray escape keys and a
// multi-line prompt arrives mangled and half-submitted.
func (c *KimiInteractive) submitPrompt(ctx context.Context, sess terminalSession, composer <-chan struct{}, prompt string) error {
	wait := c.ComposerWait
	if wait == 0 {
		wait = defaultComposerWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-composer:
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	settle := c.settle
	if settle == 0 {
		settle = defaultComposerSettle
	}
	if err := sleepContext(ctx, settle); err != nil {
		return err
	}
	if _, err := sess.Write(pasteKeystrokes(prompt)); err != nil {
		return fmt.Errorf("submit prompt: %w", err)
	}
	return nil
}

// kimiUsagePoll is how often wire.jsonl is re-read while kimi is finishing its
// accounting; defaultKimiUsageWait caps that wait.
const (
	kimiUsagePoll        = 250 * time.Millisecond
	defaultKimiUsageWait = 10 * time.Second
)

// awaitUsage holds the session open until kimi has flushed the turn's accounting
// into the session's wire.jsonl. Kimi records a turn's usage only once that turn
// ends, and the turn that wrote the result file is still running at the moment the
// file appears, so killing right then loses the call's largest turn. The wait ends
// once the totals have moved and then stopped moving; a session the index never
// named has nothing to wait for. Pinning the session up front also keeps one
// started later in the same workspace from being picked up mid-wait.
func (c *KimiInteractive) awaitUsage(ctx context.Context, since time.Time) (kimiSessionStats, bool) {
	sessionID, found := findKimiSession(c.sessionsDir(), c.Dir, since)
	if !found {
		return kimiSessionStats{}, false
	}
	wait := c.usageWait
	if wait == 0 {
		wait = defaultKimiUsageWait
	}
	deadline := c.clock().Add(wait)

	var last kimiSessionStats
	have, moved := false, false
	for {
		stats, ok := readKimiSessionStats(c.sessionsDir(), sessionID)
		switch {
		case !ok:
		case !have:
			last, have = stats, true
		case stats != last:
			last, moved = stats, true
		case moved:
			return stats, true
		}
		if !c.clock().Before(deadline) {
			return last, have
		}
		if err := sleepContext(ctx, kimiUsagePoll); err != nil {
			return last, have
		}
	}
}

// sessionStats recovers what kimi recorded for the session it started in Dir. The
// process is already gone by the time this is called, so there is nothing left to
// wait for.
func (c *KimiInteractive) sessionStats(since time.Time) (kimiSessionStats, bool) {
	sessionID, found := findKimiSession(c.sessionsDir(), c.Dir, since)
	if !found {
		return kimiSessionStats{}, false
	}
	return readKimiSessionStats(c.sessionsDir(), sessionID)
}

// enrich folds a recovered session's accounting into res. An unrecovered call
// keeps its zero usage and is reported as unrecovered rather than as a real zero.
func (c *KimiInteractive) enrich(res Result, stats kimiSessionStats, recovered bool) Result {
	if recovered {
		res.Usage = stats.Usage
		res.NumTurns = stats.Turns
		res.Model = stats.Model
	}
	if res.Model == "" {
		res.Model = c.Model
	}
	return res
}

func (c *KimiInteractive) sessionsDir() string {
	if c.SessionsDir != "" {
		return c.SessionsDir
	}
	return filepath.Join(kimiHome(), "sessions")
}

func kimiHome() string {
	if d := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".kimi-code"
	}
	return filepath.Join(home, ".kimi-code")
}

// kimiWatch is how a kimi session's terminal output is read. Kimi opens straight
// into its composer with no directory-trust dialog to answer, so the only screens
// watched are the auth wall and the composer itself.
var kimiWatch = terminalWatch{auths: hasKimiAuthWall, ready: hasKimiComposer}

// hasKimiComposer reports whether kimi has turned bracketed paste on, which is the
// TUI telling the terminal it is now reading input the way the launch prompt is
// written. Matching that handshake rather than the drawn composer box keeps the
// launch from breaking every time the UI is restyled.
func hasKimiComposer(s string) bool { return strings.Contains(s, "\x1b[?2004h") }

// hasKimiAuthWall reports whether kimi is blocked on account access: a membership
// it cannot verify, or a provider rejection that needs a re-login. Neither clears
// without a human, and the composer keeps animating behind the error, so the stall
// watchdog never sees the run go quiet. Kimi wraps the message across the width of
// the pane, so the markers are matched with layout whitespace removed.
func hasKimiAuthWall(s string) bool {
	flat := squashSpace(strings.ToLower(sanitize.StripANSI(s)))
	switch {
	case strings.Contains(flat, "unabletoverifyyourmembership"):
		return true
	case strings.Contains(flat, "provider.api_error") && (strings.Contains(flat, "]401") || strings.Contains(flat, "]402")):
		return true
	default:
		return hasAuthFailure(s)
	}
}

func (c *KimiInteractive) emit(label string, res Result, dur time.Duration, runErr error, usageRecovered bool) {
	if c.Log == nil {
		return
	}
	fields := map[string]any{
		"provider":        "kimi",
		"mode":            "interactive",
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

// record appends the call to the token ledger. A call whose session never turned
// up is left out entirely: a zero row there is indistinguishable from a real free
// call, and the cost surfaces would read it as one.
func (c *KimiInteractive) record(label string, res Result, dur time.Duration, recovered bool) {
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
		Provider:      "kimi",
		Model:         res.Model,
		Context:       res.Context,
		Duration:      dur,
	})
}

func (c *KimiInteractive) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
