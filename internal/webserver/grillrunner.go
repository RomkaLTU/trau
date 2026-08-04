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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/proc"
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
	grillStoppedReason   = "you stopped the agent — your next message steers the conversation"
	grillResumeNudge     = "Please continue."
)

// grillRunner is the process side of a grilling session. One turn per session at a
// time: inflight guards a create and a resume from racing into two children, and
// holds each turn's cancel so a stop can kill the child it spawned.
type grillRunner struct {
	srv     *Server
	baseCtx context.Context
	baseURL string

	mu       sync.Mutex
	inflight map[int64]context.CancelFunc
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
		inflight: map[int64]context.CancelFunc{},
	}
	s.startGrill = r.launch
	s.runGrillTurn = r.runPregrill
	s.grillTurnActive = r.active
	s.stopGrillTurn = r.stop
}

func (r *grillRunner) active(sid int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.inflight[sid]
	return ok
}

// stop ends the session's in-flight turn by cancelling its context, which kills the
// child and everything it spawned.
func (r *grillRunner) stop(sid int64) {
	r.mu.Lock()
	cancel := r.inflight[sid]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// begin claims the session's one turn slot and hands back the turn's context, or
// reports that a turn is already in flight for it.
func (r *grillRunner) begin(sid int64) (context.Context, context.CancelFunc, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.inflight[sid]; ok {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(r.baseCtx, grillTurnTimeout)
	r.inflight[sid] = cancel
	return ctx, cancel, true
}

func (r *grillRunner) end(sid int64, cancel context.CancelFunc) {
	r.mu.Lock()
	delete(r.inflight, sid)
	r.mu.Unlock()
	cancel()
}

// runPregrill runs one pre-grill turn synchronously — the pass waits on it to read
// the settled outcome, unlike the fire-and-forget launch. It shares launch's
// inflight guard and hub-lifetime timeout; the caller's context is ignored so a
// disconnected pass request never kills a turn mid-flight.
func (r *grillRunner) runPregrill(_ context.Context, sess hubstore.GrillSession) {
	ctx, cancel, ok := r.begin(sess.ID)
	if !ok {
		return
	}
	defer r.end(sess.ID, cancel)
	r.runTurn(ctx, sess)
}

// launch runs a turn for sess in the background unless one is already in flight for
// it. The passed context is the request's and is ignored for the turn's lifetime —
// a turn outlives the HTTP call that started it and is bounded by the hub context
// instead. A turn that ended on an undelivered interjection goes round again, which
// only happens once: the resume turn consumes the queue that asked for it.
func (r *grillRunner) launch(_ context.Context, sess hubstore.GrillSession) {
	ctx, cancel, ok := r.begin(sess.ID)
	if !ok {
		return
	}
	go func() {
		if !r.turn(ctx, cancel, sess) {
			return
		}
		// Resuming the chain the turn just minted is the point of going round again,
		// and the struct it started from does not carry it.
		next, found, err := r.srv.stores.Grill().Session(sess.ID)
		if err != nil || !found {
			return
		}
		r.launch(r.baseCtx, next)
	}()
}

// turn runs one turn and releases the session's turn slot, reporting whether the
// session has to run again. The slot has to be free before the caller relaunches.
func (r *grillRunner) turn(ctx context.Context, cancel context.CancelFunc, sess hubstore.GrillSession) bool {
	defer r.end(sess.ID, cancel)
	return r.runTurn(ctx, sess)
}

func (r *grillRunner) runTurn(ctx context.Context, sess hubstore.GrillSession) bool {
	repo, ok := r.srv.findRepoByRoot(sess.Repo)
	if !ok {
		r.settle(sess.ID, hubstore.GrillParked, "the session's repository is no longer registered with the hub")
		return false
	}
	cfg, err := r.srv.grillConfigFor(repo)
	if err != nil {
		r.settle(sess.ID, hubstore.GrillParked, "could not load the repository config: "+err.Error())
		return false
	}
	adapter, ok := grillAdapterFor(r, sess.Provider)
	if !ok {
		r.settle(sess.ID, hubstore.GrillParked, "interviews do not yet support the "+sess.Provider+" provider")
		return false
	}
	if reason := grillProviderUnavailableReason(cfg, sess.Provider, sess.Mode); reason != "" {
		r.settle(sess.ID, hubstore.GrillParked, reason)
		return false
	}

	spec, err := r.buildTurn(ctx, sess, repo, cfg, adapter)
	if err != nil {
		r.settle(sess.ID, hubstore.GrillParked, "could not prepare the interview turn: "+err.Error())
		return false
	}
	out, runErr := r.spawnGrill(ctx, spec, r.deltaSink(sess.ID, adapter))

	chainID, resultErr := adapter.parseResult(out.stdout)
	if chainID != "" {
		if _, _, err := r.srv.stores.Grill().UpdateChain(sess.ID, chainID); err != nil {
			logger.Verbosef("grill %d: update chain: %v", sess.ID, err)
		}
	}
	return r.reconcile(sess.ID, adapter, out, runErr, resultErr)
}

type grillTurnSpec struct {
	bin  string
	dir  string
	args []string
	env  []string
}

// A stale chain self-heals by falling back to the first prompt; the next stream
// result becomes the authoritative session id.
func (r *grillRunner) buildTurn(ctx context.Context, sess hubstore.GrillSession, repo registry.Repo, cfg config.Config, adapter grillAdapter) (grillTurnSpec, error) {
	model := sess.Model
	if model == "" {
		model = grillModelDefault(cfg, sess.Provider)
	}
	resume, prompt := "", ""
	if sess.SessionChain != "" && adapter.resumable(sess.SessionChain) {
		resume = sess.SessionChain
		prompt = r.answerPrompt(ctx, repo, sess)
	} else {
		prompt = r.firstPrompt(ctx, repo, sess)
	}
	return adapter.turnSpec(sess.ID, repo, cfg, sess.Mode, model, resume, prompt)
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

// grillChildEnv is the environment a grilling child inherits: agent.ChildEnv
// minus TRAU_ACTIVE, the nested-loop guard the hub may carry from the loop that
// started it. Same lesson as the hub-spawn poisoning fix.
func grillChildEnv() []string {
	env := agent.ChildEnv(os.Environ())
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TRAU_ACTIVE=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Kimi rejects claude's explicit transport type, so the shared server shape leaves
// it to each provider-specific config writer.
func (r *grillRunner) grillMCPServer(sid int64) map[string]any {
	server := map[string]any{
		"url": fmt.Sprintf("%s%s/grill/%d/mcp", r.baseURL, APIPrefix, sid),
	}
	if r.srv.token != "" {
		server["headers"] = map[string]string{"Authorization": "Bearer " + r.srv.token}
	}
	return server
}

func (r *grillRunner) mcpConfigJSON(sid int64) string {
	server := r.grillMCPServer(sid)
	server["type"] = "http"
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"trau-grill": server}})
	return string(b)
}

func (r *grillRunner) firstPrompt(ctx context.Context, repo registry.Repo, sess hubstore.GrillSession) string {
	renderer := r.srv.promptRenderer(repo.Root)
	research := sess.Mode == hubstore.GrillModeResearch
	if sess.IssueID == "" {
		note := r.srv.withPastedFiles(ctx, repo, sess, r.openingNote(sess.ID))
		if research {
			return grillResearchIdeaPrompt(renderer, note, sess.AutoAccept)
		}
		return grillAuthoringPrompt(renderer, note, sess.AutoAccept)
	}
	title, description := "", ""
	if iss, found, err := r.srv.stores.Issues().Get(repo.Root, sess.IssueID); err == nil && found {
		title, description = iss.Title, iss.Description
	}
	in := grillPromptInput{
		issueID:     sess.IssueID,
		title:       title,
		description: description,
		files:       r.srv.materializeIssueAttachments(ctx, repo, sess.IssueID),
	}
	if r.srv.isPregrill(sess.ID) {
		return grillPregrillPrompt(renderer, in)
	}
	in.focus = r.openingNote(sess.ID)
	in.autoAccept = sess.AutoAccept
	if research {
		return grillResearchPrompt(renderer, in)
	}
	return grillIssuePrompt(renderer, in)
}

// answerPrompt is the resume-turn prompt: the user's latest answer with any image
// they pasted mid-interview materialized locally and referenced by path, so a
// screenshot dropped into an answer is something the child can open on this turn. An
// interjection queued while the previous turn worked outranks it — the answer it
// trails is one the agent has already had.
func (r *grillRunner) answerPrompt(ctx context.Context, repo registry.Repo, sess hubstore.GrillSession) string {
	if queued := r.srv.grillTakeInterjections(sess.ID); len(queued) > 0 {
		return r.srv.withPastedFiles(ctx, repo, sess, grillSteerFrame(queued))
	}
	text, steer := r.latestAnswer(sess.ID)
	if text == "" {
		return grillResumeNudge
	}
	text = r.srv.withPastedFiles(ctx, repo, sess, text)
	if steer {
		return grillSteerPrompt(text)
	}
	return text
}

func grillSteerPrompt(text string) string {
	return "The user stopped your previous turn mid-flight to redirect the interview. " +
		"Their steering message: " + text +
		"\n\nFollow this direction; do not resume your interrupted line of questioning unless it still applies."
}

// grillAttachTicket names the directory a session's files materialize into: the
// issue for an issue grilling, the session itself when it is authoring one.
func grillAttachTicket(sess hubstore.GrillSession) string {
	if sess.IssueID != "" {
		return sess.IssueID
	}
	return "grill-" + strconv.FormatInt(sess.ID, 10)
}

// openingNote returns the line a session was opened with, stored as its first info
// message: an authoring session's seed idea, or an issue grilling's focus note. It
// grounds the first-turn prompt. A system info message is hub bookkeeping (a model
// switch) and never the user's opener.
func (r *grillRunner) openingNote(sid int64) string {
	msgs, err := r.srv.stores.Grill().Messages(sid, 0)
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		if m.Role == hubstore.GrillRoleUser && m.Kind == hubstore.GrillKindInfo {
			return grillMessageText(m.Payload)
		}
	}
	return ""
}

// latestAnswer returns the text of the session's most recent user answer, the prompt
// for a resume turn, and whether it was sent to steer a stopped turn.
func (r *grillRunner) latestAnswer(sid int64) (string, bool) {
	msgs, err := r.srv.stores.Grill().Messages(sid, 0)
	if err != nil {
		return "", false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == hubstore.GrillRoleUser && msgs[i].Kind == hubstore.GrillKindAnswer {
			return grillMessageText(msgs[i].Payload), grillMessageSteer(msgs[i].Payload)
		}
	}
	return "", false
}

// grillOutput is a finished child's captured output.
type grillOutput struct {
	stdout []byte
	stderr string
}

// spawnGrill runs one turn to completion, handing every stdout line to onLine as it
// lands rather than buffering the stream whole, so a turn's text can leave the hub
// while the child is still producing it. The child leads its own process group and a
// cancelled turn ends that group, so a provider CLI's own children die with it.
func (r *grillRunner) spawnGrill(ctx context.Context, spec grillTurnSpec, onLine func([]byte)) (grillOutput, error) {
	cmd := exec.CommandContext(ctx, spec.bin, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	cmd.SysProcAttr = proc.Detached()
	cmd.Cancel = func() error { return proc.KillGroup(cmd.Process.Pid) }
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

// deltaSink publishes the agent's text as the child produces it, and the tool work
// it does between replies so a turn spent working is not a silent one.
func (r *grillRunner) deltaSink(sid int64, adapter grillAdapter) func([]byte) {
	return func(line []byte) {
		if text := adapter.deltaText(line); text != "" {
			r.srv.publishGrillDelta(sid, text)
			return
		}
		for _, act := range adapter.activityEvent(line) {
			r.srv.publishGrillActivity(sid, act)
		}
	}
}

// reconcile settles the session after its child exits and reports whether it must run
// again. A turn that reached ask_user or finish_session has already moved the session
// (parked/waiting/finished) through the MCP layer; this only has to catch the cases
// that layer did not: an auth or rate wall (stall), a crash (park), or an agent that
// ended without asking or proposing anything (park). Waiting sessions are left alone
// because some provider MCP clients exit after leaving a question pending.
func (r *grillRunner) reconcile(sid int64, adapter grillAdapter, out grillOutput, runErr error, resultErr bool) bool {
	sess, found, err := r.srv.stores.Grill().Session(sid)
	if err != nil || !found {
		return false
	}
	reason := grillStallReason(adapter, out.stdout, out.stderr)
	switch sess.State {
	case hubstore.GrillWaiting, hubstore.GrillFinished, hubstore.GrillApplied, hubstore.GrillAbandoned, hubstore.GrillStalled:
		return false
	case hubstore.GrillParked:
		// A stopped session is already settled by the user's own hand: what its
		// killed child left on stdout describes the kill, not a wall the session hit.
		if reason != "" && !grillStopped(sess) {
			r.settle(sid, hubstore.GrillStalled, reason)
		}
	default:
		switch {
		case reason != "":
			r.settle(sid, hubstore.GrillStalled, reason)
		case runErr != nil || resultErr:
			r.settle(sid, hubstore.GrillParked, grillCrashReason)
		// A turn that ended without reaching the queue the user typed into is not a
		// session that needs picking up: it stays running and resumes on that queue.
		case sess.SessionChain != "" && r.srv.grillHasInterjection(sid):
			return true
		default:
			r.settle(sid, hubstore.GrillParked, grillNoOutcomeReason)
		}
	}
	return false
}

func (r *grillRunner) settle(sid int64, state, reason string) {
	sess, err := r.srv.stores.Grill().Transition(sid, state, reason)
	if err != nil {
		logger.Verbosef("grill %d: settle %s: %v", sid, state, err)
		return
	}
	r.srv.publishGrillState(sess)
	r.srv.notifyGrillAwaiting(sess, "")
}

// grillStallReason classifies a turn's output for an auth or rate wall, reusing the
// pipeline's pause classification. It excludes the agent's reply text so a user-facing
// discussion of usage limits does not stall the session.
func grillStallReason(adapter grillAdapter, stdout []byte, stderr string) string {
	text := grillProviderText(adapter, stdout) + "\n" + stderr
	switch {
	case agent.AuthWallText(text):
		return "the grilling agent needs re-authentication — re-login (run claude, then /login), then resume"
	case agent.RateLimitedText(text):
		return "the grilling agent hit a provider usage or rate limit — resume once it clears"
	}
	return ""
}

func grillProviderText(adapter grillAdapter, stdout []byte) string {
	var b strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if adapter.deltaText(sc.Bytes()) != "" {
			continue
		}
		b.Write(sc.Bytes())
		b.WriteByte('\n')
	}
	return b.String()
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
// runner reads: the assistant's reply as it is written, one text delta at a time,
// and each block as it opens, which is where a tool call names itself.
type grillPartialEvent struct {
	Type  string `json:"type"`
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
	} `json:"event"`
}

// grillToolResultEvent is the slice of the top-level user event claude emits when a
// tool comes back: which call it closes and whether that call failed, never what it
// returned.
type grillToolResultEvent struct {
	Message struct {
		Content []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
}

// grillToolCallEvent is the slice of the top-level assistant event claude emits when
// a block closes: each tool_use block whole, which is the first point the call's
// input is known.
type grillToolCallEvent struct {
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
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

// grillActivityEvent returns the work one stream-json line reports the agent doing:
// a block start naming the tool it reached for or opening a stretch of thinking, the
// assistant event that fills in what the call was about, and the user event that
// closes it out. Only a capped summary of one input field rides along — the rest of
// the input, and everything the tool returned, stays in the child.
func grillActivityEvent(line []byte) []GrillActivityView {
	var ev grillPartialEvent
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "user":
		return grillToolResultActivity(line)
	case "assistant":
		return grillToolCallActivity(line)
	}
	if ev.Type != "stream_event" || ev.Event.Type != "content_block_start" {
		return nil
	}
	block := ev.Event.ContentBlock
	switch block.Type {
	case "tool_use":
		return []GrillActivityView{{Kind: grillActivityTool, ID: block.ID, Name: block.Name}}
	case "thinking":
		return []GrillActivityView{{Kind: grillActivityThinking}}
	}
	return nil
}

// An assistant event carries the blocks that just closed, so each tool_use block in it
// updates the row its block start already opened with a summary of what it was called
// on. Blocks that are not tool traffic repeat text the deltas already carried.
func grillToolCallActivity(line []byte) []GrillActivityView {
	var ev grillToolCallEvent
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	out := []GrillActivityView{}
	for _, blk := range ev.Message.Content {
		if blk.Type != "tool_use" {
			continue
		}
		out = append(out, GrillActivityView{
			Kind:   grillActivityTool,
			ID:     blk.ID,
			Name:   blk.Name,
			Detail: grillToolInputSummary(blk.Name, blk.Input),
		})
	}
	return out
}

// A user event is a tool coming back on a headless turn: every tool_result block it
// carries closes out the call it names, since a turn running tools in parallel gets
// them back together. Anything else is not tool traffic.
func grillToolResultActivity(line []byte) []GrillActivityView {
	var ev grillToolResultEvent
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	out := []GrillActivityView{}
	for _, blk := range ev.Message.Content {
		if blk.Type == "tool_result" {
			out = append(out, grillToolResult(blk.ToolUseID, !blk.IsError))
		}
	}
	return out
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
