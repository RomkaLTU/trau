package webserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// grillTurnTimeout bounds one grilling turn end to end, including the time the
// child spends blocked on an ask_user call (that block already self-parks via the
// MCP idle timeout, so this is only a backstop against a wedged child). It is a
// var so tests can shorten it.
var grillTurnTimeout = 30 * time.Minute

// grillStdoutBuffer is the read buffer for a child's stream-json stdout. Partial
// message events are small and arrive at token rate; the buffer only has to keep the
// pipe from round-tripping per line.
const grillStdoutBuffer = 64 << 10

// Reasons a turn leaves on a session it had to settle without an agent-proposed
// outcome. Each one reads as a resumable state in the panel.
const (
	grillCrashReason     = "the grilling agent stopped unexpectedly before proposing an outcome; resume to continue"
	grillNoOutcomeReason = "the grilling agent ended its turn without asking a question or proposing an outcome; resume to continue"
	grillResumeNudge     = "Please continue."
)

// grillRunner is the process side of a grilling session: it spawns the headless
// claude child for a turn, tracks the child's session-id chain, and reconciles the
// session's state after the child exits — parking it on idle/crash and stalling it
// on an auth or rate wall. It runs in-process in the hub (ADR 0008), so it mutates
// state through the same store and broadcaster the API handlers use. One turn per
// session at a time: inflight guards a create and a resume from racing into two
// children for the same session.
type grillRunner struct {
	srv     *Server
	baseCtx context.Context
	baseURL string

	mu       sync.Mutex
	inflight map[int64]bool
}

// EnableGrilling wires the turn runner into the create/resume seams. baseCtx is the
// hub's lifetime — cancelling it kills any turn in flight, leaving the session
// resumable. baseURL is the hub's own address as the grill child can reach it
// (loopback for a loopback bind), used to point the child's MCP config at the
// per-session endpoint. Call it once, before Start.
func (s *Server) EnableGrilling(baseCtx context.Context, baseURL string) {
	r := &grillRunner{
		srv:      s,
		baseCtx:  baseCtx,
		baseURL:  strings.TrimRight(baseURL, "/"),
		inflight: map[int64]bool{},
	}
	s.startGrill = r.launch
	s.runGrillTurn = r.runPregrill
}

// runPregrill runs one pre-grill turn synchronously — the pass waits on it to read
// the settled outcome, unlike the fire-and-forget launch. It shares launch's
// inflight guard and hub-lifetime timeout; the caller's context is ignored so a
// disconnected pass request never kills a turn mid-flight.
func (r *grillRunner) runPregrill(_ context.Context, sess hubstore.GrillSession) {
	r.mu.Lock()
	if r.inflight[sess.ID] {
		r.mu.Unlock()
		return
	}
	r.inflight[sess.ID] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.inflight, sess.ID)
		r.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(r.baseCtx, grillTurnTimeout)
	defer cancel()
	r.runTurn(ctx, sess)
}

// launch runs a turn for sess in the background unless one is already in flight for
// it. The passed context is the request's and is ignored for the turn's lifetime —
// a turn outlives the HTTP call that started it and is bounded by the hub context
// instead.
func (r *grillRunner) launch(_ context.Context, sess hubstore.GrillSession) {
	r.mu.Lock()
	if r.inflight[sess.ID] {
		r.mu.Unlock()
		return
	}
	r.inflight[sess.ID] = true
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.inflight, sess.ID)
			r.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(r.baseCtx, grillTurnTimeout)
		defer cancel()
		r.runTurn(ctx, sess)
	}()
}

// runTurn spawns one claude child for sess, updates the session-id chain from the
// stream, and reconciles the session's state once the child exits.
func (r *grillRunner) runTurn(ctx context.Context, sess hubstore.GrillSession) {
	repo, ok := r.srv.findRepoByRoot(sess.Repo)
	if !ok {
		r.settle(sess.ID, hubstore.GrillParked, "the session's repository is no longer registered with the hub")
		return
	}
	cfg, err := r.srv.grillConfigFor(repo)
	if err != nil {
		r.settle(sess.ID, hubstore.GrillParked, "could not load the repository config: "+err.Error())
		return
	}

	spec := r.buildTurn(sess, repo, cfg)
	out, runErr := r.spawnClaude(ctx, spec, r.deltaSink(sess.ID))

	chainID, resultErr := parseGrillStream(out.stdout)
	if chainID != "" {
		if _, _, err := r.srv.stores.Grill().UpdateChain(sess.ID, chainID); err != nil {
			logger.Verbosef("grill %d: update chain: %v", sess.ID, err)
		}
	}
	r.reconcile(sess.ID, out, runErr, resultErr)
}

// grillTurnSpec is the resolved claude invocation for one turn.
type grillTurnSpec struct {
	bin  string
	dir  string
	args []string
	env  []string
}

// buildTurn resolves the child invocation for sess. A session whose chain id still
// has a transcript on disk resumes it with the user's latest answer as the prompt;
// a fresh or stale-chained session runs the first-turn grilling prompt (the next
// result event mints the authoritative id, so a stale chain self-heals).
func (r *grillRunner) buildTurn(sess hubstore.GrillSession, repo registry.Repo, cfg config.Config) grillTurnSpec {
	model := sess.Model
	if model == "" {
		model = cfg.GrillModel
	}
	if model == "" {
		model = cfg.ClaudeModel
	}
	bin := cfg.ClaudeBin
	if bin == "" {
		bin = "claude"
	}

	resume, prompt := "", ""
	if sess.SessionChain != "" && agent.SessionExists(sess.SessionChain) {
		resume = sess.SessionChain
		prompt = r.latestAnswer(sess.ID)
		if prompt == "" {
			prompt = grillResumeNudge
		}
	} else {
		prompt = r.firstPrompt(repo, sess)
	}

	return grillTurnSpec{
		bin:  bin,
		dir:  repo.Root,
		args: grillTurnArgs(strings.Fields(cfg.ClaudeFlags), model, r.mcpConfigJSON(sess.ID), resume, prompt),
		env:  grillChildEnv(),
	}
}

// grillTurnArgs assembles the claude argument vector: the configured flags, the
// resolved model, the headless stream-json contract, the strict per-session MCP
// config, an optional resume, and the prompt last. stream-json in print mode
// requires --verbose, so it is always present; --include-partial-messages is what
// breaks each assistant message into the text deltas the panel streams.
func grillTurnArgs(flags []string, model, mcpConfig, resumeID, prompt string) []string {
	args := append([]string{}, flags...)
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--strict-mcp-config", "--mcp-config", mcpConfig)
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	return append(args, "-p", prompt)
}

// grillChildEnv is the environment a grilling child inherits, stripped of the
// markers that would poison a hub-spawned claude: TRAU_ACTIVE (the nested-loop
// guard) and CLAUDECODE (the already-inside-Claude-Code marker). Same lesson as the
// hub-spawn poisoning fix.
func grillChildEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TRAU_ACTIVE=") || strings.HasPrefix(kv, "CLAUDECODE=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// mcpConfigJSON is the --mcp-config the child gets: one Streamable-HTTP server
// pointing at this session's own MCP endpoint, so the child can reach ask_user and
// finish_session but cannot address another session. A bearer token is forwarded as
// an Authorization header when the hub gates its API.
func (r *grillRunner) mcpConfigJSON(sid int64) string {
	server := map[string]any{
		"type": "http",
		"url":  fmt.Sprintf("%s%s/grill/%d/mcp", r.baseURL, APIPrefix, sid),
	}
	if r.srv.token != "" {
		server["headers"] = map[string]string{"Authorization": "Bearer " + r.srv.token}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"trau-grill": server}})
	return string(b)
}

func (r *grillRunner) firstPrompt(repo registry.Repo, sess hubstore.GrillSession) string {
	if sess.IssueID == "" {
		return grillAuthoringPrompt(r.seedIdea(sess.ID))
	}
	title, description := "", ""
	if iss, found, err := r.srv.stores.Issues().Get(repo.Root, sess.IssueID); err == nil && found {
		title, description = iss.Title, iss.Description
	}
	if r.srv.isPregrill(sess.ID) {
		return grillPregrillPrompt(sess.IssueID, title, description)
	}
	return grillIssuePrompt(sess.IssueID, title, description)
}

// seedIdea returns the one-line idea an authoring session was opened with, stored as
// its first info message. It grounds the first-turn authoring prompt.
func (r *grillRunner) seedIdea(sid int64) string {
	msgs, err := r.srv.stores.Grill().Messages(sid, 0)
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		if m.Kind == hubstore.GrillKindInfo {
			return grillMessageText(m.Payload)
		}
	}
	return ""
}

// latestAnswer returns the text of the session's most recent user answer, the
// prompt for a resume turn.
func (r *grillRunner) latestAnswer(sid int64) string {
	msgs, err := r.srv.stores.Grill().Messages(sid, 0)
	if err != nil {
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == hubstore.GrillRoleUser && msgs[i].Kind == hubstore.GrillKindAnswer {
			return grillMessageText(msgs[i].Payload)
		}
	}
	return ""
}

// grillOutput is a finished child's captured output.
type grillOutput struct {
	stdout []byte
	stderr string
}

// spawnClaude runs one turn to completion, handing every stdout line to onLine as it
// lands rather than buffering the stream whole, so a turn's text can leave the hub
// while the child is still producing it.
func (r *grillRunner) spawnClaude(ctx context.Context, spec grillTurnSpec, onLine func([]byte)) (grillOutput, error) {
	cmd := exec.CommandContext(ctx, spec.bin, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	stderr := newTailWriter(spawnStderrTailBytes)
	cmd.Stderr = stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return grillOutput{stderr: stderr.String()}, err
	}
	if err := cmd.Start(); err != nil {
		return grillOutput{stderr: stderr.String()}, err
	}
	stdout := drainGrillStdout(pipe, onLine)
	err = cmd.Wait()
	return grillOutput{stdout: stdout, stderr: stderr.String()}, err
}

// drainGrillStdout reads r to EOF line by line, calling onLine for each and returning
// everything it read. It never stops early: this runs before Wait, and a child whose
// stdout stops being drained blocks on a full pipe.
func drainGrillStdout(r io.Reader, onLine func([]byte)) []byte {
	var buf bytes.Buffer
	br := bufio.NewReaderSize(r, grillStdoutBuffer)
	for {
		line, err := br.ReadBytes('\n')
		buf.Write(line)
		if len(line) > 0 {
			onLine(bytes.TrimRight(line, "\r\n"))
		}
		if err != nil {
			return buf.Bytes()
		}
	}
}

// deltaSink publishes the agent's text as the child produces it. One child spans
// every turn of an interview, blocking inside ask_user between them, so the turn's
// numbering belongs to the broadcaster rather than to this closure.
func (r *grillRunner) deltaSink(sid int64) func([]byte) {
	return func(line []byte) {
		if text := grillDeltaText(line); text != "" {
			r.srv.publishGrillDelta(sid, text)
		}
	}
}

// reconcile settles the session after its child exits. A turn that reached ask_user
// or finish_session has already moved the session (parked/waiting/finished) through
// the MCP layer; this only has to catch the cases that layer did not: an auth or
// rate wall (stall), a crash (park), or an agent that ended without asking or
// proposing anything (park). A settled session is left alone, except a parked one
// the child stalled on, which is upgraded to stalled with the cause.
func (r *grillRunner) reconcile(sid int64, out grillOutput, runErr error, resultErr bool) {
	sess, found, err := r.srv.stores.Grill().Session(sid)
	if err != nil || !found {
		return
	}
	reason := grillStallReason(out.stdout, out.stderr)
	switch sess.State {
	case hubstore.GrillFinished, hubstore.GrillApplied, hubstore.GrillAbandoned:
		return
	case hubstore.GrillParked, hubstore.GrillStalled:
		if reason != "" && sess.State == hubstore.GrillParked {
			r.settle(sid, hubstore.GrillStalled, reason)
		}
	default:
		switch {
		case reason != "":
			r.settle(sid, hubstore.GrillStalled, reason)
		case runErr != nil || resultErr:
			r.settle(sid, hubstore.GrillParked, grillCrashReason)
		default:
			r.settle(sid, hubstore.GrillParked, grillNoOutcomeReason)
		}
	}
}

func (r *grillRunner) settle(sid int64, state, reason string) {
	sess, err := r.srv.stores.Grill().Transition(sid, state, reason)
	if err != nil {
		logger.Verbosef("grill %d: settle %s: %v", sid, state, err)
		return
	}
	r.srv.publishGrillState(sess)
}

// grillStallReason classifies a turn's output for an auth or rate wall, reusing the
// pipeline's pause classification. It returns the reason to stall the session with,
// or empty when the turn showed no stall.
func grillStallReason(stdout []byte, stderr string) string {
	text := string(stdout) + "\n" + stderr
	switch {
	case agent.AuthWallText(text):
		return "the grilling agent needs re-authentication — re-login (run claude, then /login), then resume"
	case agent.RateLimitedText(text):
		return "the grilling agent hit a provider usage or rate limit — resume once it clears"
	}
	return ""
}

// grillStreamEvent is the slice of a headless stream-json event the runner reads:
// the session id (chain update) and, on the terminal result event, whether the turn
// errored.
type grillStreamEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
}

// grillPartialEvent is the slice of an --include-partial-messages stream_event the
// runner reads: the assistant's reply as it is written, one text delta at a time.
type grillPartialEvent struct {
	Type  string `json:"type"`
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
}

// grillDeltaText returns the reply text one stream-json line carries. Only a partial
// message's text delta qualifies: thinking and tool-input deltas are not the reply,
// and the assistant event closing a block repeats text the deltas already carried.
func grillDeltaText(line []byte) string {
	var ev grillPartialEvent
	if json.Unmarshal(line, &ev) != nil || ev.Type != "stream_event" {
		return ""
	}
	if ev.Event.Type != "content_block_delta" || ev.Event.Delta.Type != "text_delta" {
		return ""
	}
	return ev.Event.Delta.Text
}

// parseGrillStream extracts the latest session id and the terminal result's error
// flag from a child's stream-json stdout. The last event carrying a session id wins
// — a crash-resumed turn mints a new id in its result event, so the chain is read
// fresh every turn and never assumed stable. Malformed lines are skipped.
func parseGrillStream(stream []byte) (sessionID string, resultErr bool) {
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ev grillStreamEvent
		if json.Unmarshal(b, &ev) != nil {
			continue
		}
		if ev.SessionID != "" {
			sessionID = ev.SessionID
		}
		if ev.Type == "result" {
			resultErr = ev.IsError
		}
	}
	return sessionID, resultErr
}

// grillConfigFor resolves the repo's layered config for a grilling turn.
func (s *Server) grillConfigFor(repo registry.Repo) (config.Config, error) {
	projectPath, userPath := s.repoConfigPaths(repo)
	return config.LoadLayered(projectPath, userPath, "", "")
}
