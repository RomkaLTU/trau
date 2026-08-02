package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// grillStubBlocks holds its first turn open until the test releases it, so an
// interjection can be typed while the child is genuinely mid-flight.
const grillStubBlocks = `#!/bin/sh
which=first
sid=sid-one
for a in "$@"; do
  if [ "$a" = "--resume" ]; then which=resume; sid=sid-two; fi
done
: > "$GRILL_STUB_DIR/$which.args"
for a in "$@"; do printf '%s\000' "$a" >> "$GRILL_STUB_DIR/$which.args"; done
mkdir -p "$CLAUDE_CONFIG_DIR/projects/p"
: > "$CLAUDE_CONFIG_DIR/projects/p/$sid.jsonl"
printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$sid"
if [ "$which" = first ]; then
  : > "$GRILL_STUB_DIR/started"
  while [ ! -f "$GRILL_STUB_DIR/release" ]; do sleep 0.05; done
fi
printf '{"type":"result","subtype":"success","session_id":"%s","is_error":false,"result":"ok"}\n' "$sid"
`

func postGrillMessage(t *testing.T, ts *httptest.Server, sid, text string) GrillAnswerResponse {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sid+"/answer", GrillAnswerRequest{Text: text})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d, want 200", res.StatusCode)
	}
	var v GrillAnswerResponse
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	return v
}

func grillMessageKinds(msgs []GrillMessageView) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Kind)
	}
	return out
}

// The composer posts to one endpoint whatever the session is doing: a running turn
// takes the text as an interjection and keeps working, every other state keeps the
// answer semantics it had.
func TestGrillAnswerByState(t *testing.T) {
	tests := []struct {
		name       string
		states     []string
		wantStatus int
		wantKind   string
		wantState  string
	}{
		{
			name:       "running queues an interjection",
			wantStatus: http.StatusOK,
			wantKind:   hubstore.GrillKindInterjection,
			wantState:  hubstore.GrillRunning,
		},
		{
			name:       "waiting answers the question",
			states:     []string{hubstore.GrillWaiting},
			wantStatus: http.StatusOK,
			wantKind:   hubstore.GrillKindAnswer,
			wantState:  hubstore.GrillRunning,
		},
		{
			name:       "parked answers and resumes",
			states:     []string{hubstore.GrillParked},
			wantStatus: http.StatusOK,
			wantKind:   hubstore.GrillKindAnswer,
			wantState:  hubstore.GrillRunning,
		},
		{
			name:       "finished takes a follow-up",
			states:     []string{hubstore.GrillFinished},
			wantStatus: http.StatusOK,
			wantKind:   hubstore.GrillKindAnswer,
			wantState:  hubstore.GrillRunning,
		},
		{name: "abandoned is refused", states: []string{hubstore.GrillAbandoned}, wantStatus: http.StatusConflict},
		{
			name:       "applied is refused",
			states:     []string{hubstore.GrillFinished, hubstore.GrillApplied},
			wantStatus: http.StatusConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, stores, repo, srv := grillHookServer(t)
			srv.startGrill = func(context.Context, hubstore.GrillSession) {}

			sess := createGrill(t, ts, repo, "COD-1")
			sid, _ := strconv.ParseInt(sess.ID, 10, 64)
			walkGrillStates(t, stores, sid, tt.states)

			res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "use postgres"})
			_ = res.Body.Close()
			if res.StatusCode != tt.wantStatus {
				t.Fatalf("answer status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			detail := grillDetail(t, ts, sess.ID)
			if detail.Session.State != tt.wantState {
				t.Errorf("session = %q, want %q", detail.Session.State, tt.wantState)
			}
			kinds := grillMessageKinds(detail.Messages)
			if len(kinds) != 1 || kinds[0] != tt.wantKind {
				t.Errorf("transcript = %q, want a single %q", kinds, tt.wantKind)
			}
		})
	}
}

// The queue reaches the agent at its next question instead of it: the question is
// never stored, so nothing later treats it as one the session is waiting on, and the
// session never enters waiting.
func TestGrillMCPAskUserDeliversInterjections(t *testing.T) {
	tests := []struct {
		name  string
		texts []string
	}{
		{name: "one", texts: []string{"drop the schema thread"}},
		{name: "several deliver in order", texts: []string{"drop the schema thread", "and use postgres", "keep it one slice"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _, repo := grillServer(t)
			sess := createGrill(t, ts, repo, "COD-1")
			for _, text := range tt.texts {
				postGrillMessage(t, ts, sess.ID, text)
			}

			msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("ask_user", map[string]any{
				"question": "Which page is in scope?",
			}))
			result := toolResult(t, msg)
			if len(result.Content) != 1 {
				t.Fatalf("ask_user result = %+v, want one content block", result.Content)
			}
			text := result.Content[0].Text
			if !strings.Contains(text, "the user interjected") || !strings.Contains(text, "not an answer to any pending") {
				t.Errorf("result = %q, want the steering frame", text)
			}
			if !containsInOrder(text, tt.texts) {
				t.Errorf("result = %q, want %q joined in order", text, tt.texts)
			}

			detail := grillDetail(t, ts, sess.ID)
			if detail.Session.State != hubstore.GrillRunning {
				t.Errorf("session = %q, want it left running", detail.Session.State)
			}
			if kinds := grillMessageKinds(detail.Messages); len(kinds) != len(tt.texts) {
				t.Errorf("transcript = %q, want the interjections alone — no question stored", kinds)
			}

			// Finishing proves the queue was claimed exactly once: a second delivery
			// would refuse this call instead of settling the session.
			finish := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
				"disposition": "no_change", "summary": "nothing to write",
			})))
			if finish.IsError {
				t.Errorf("finish after delivery = %+v, want the queue already claimed", finish)
			}
		})
	}
}

func containsInOrder(text string, want []string) bool {
	for _, w := range want {
		at := strings.Index(text, w)
		if at == -1 {
			return false
		}
		text = text[at+len(w):]
	}
	return true
}

// An autopilot session is steerable mid-run: the interjection beats the recommendation
// the agent would have answered for itself, auto-accept survives it, and the next
// recommended question is taken for the user again.
func TestGrillMCPAskUserInterjectionBeatsAutoAccept(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})
	postGrillMessage(t, ts, sess.ID, "drop the schema thread")

	ask := toolCall("ask_user", map[string]any{
		"question":    "Which page is in scope?",
		"options":     []string{"login", "signup"},
		"recommended": "login",
	})
	steered := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), ask))
	if len(steered.Content) != 1 || !strings.Contains(steered.Content[0].Text, "drop the schema thread") {
		t.Fatalf("ask_user result = %+v, want the interjection", steered.Content)
	}

	detail := grillDetail(t, ts, sess.ID)
	if !detail.Session.AutoAccept {
		t.Errorf("session = %+v, want auto-accept still on", detail.Session)
	}
	if kinds := grillMessageKinds(detail.Messages); len(kinds) != 1 || kinds[0] != hubstore.GrillKindInterjection {
		t.Errorf("transcript = %q, want the interjection alone", kinds)
	}

	answered := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), ask))
	if len(answered.Content) != 1 || answered.Content[0].Text != "login" {
		t.Fatalf("second ask_user = %+v, want the recommendation auto-answered", answered.Content)
	}
	want := []string{hubstore.GrillKindInterjection, hubstore.GrillKindQuestion, hubstore.GrillKindAnswer}
	if kinds := grillMessageKinds(grillDetail(t, ts, sess.ID).Messages); !slices.Equal(kinds, want) {
		t.Errorf("transcript = %q, want %q", kinds, want)
	}
}

// A proposal drafted before the user spoke is not the proposal they asked for, so the
// finish is refused once — and only once.
func TestGrillMCPFinishRefusedByInterjection(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	postGrillMessage(t, ts, sess.ID, "the epic is the wrong shape")

	finish := toolCall("finish_session", map[string]any{
		"disposition": "rewrite", "proposed_description": "A tighter body", "summary": "sharpened the scope",
	})
	refused := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), finish))
	if !refused.IsError {
		t.Fatalf("finish result = %+v, want a refusal", refused)
	}
	if len(refused.Content) != 1 || !strings.Contains(refused.Content[0].Text, "the epic is the wrong shape") {
		t.Fatalf("refusal = %+v, want it to carry the interjection", refused.Content)
	}
	if state := grillDetail(t, ts, sess.ID).Session.State; state != hubstore.GrillRunning {
		t.Errorf("session = %q, want a refused finish to leave it running", state)
	}

	settled := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), finish))
	if settled.IsError {
		t.Fatalf("second finish = %+v, want it to go through", settled)
	}
	if state := grillDetail(t, ts, sess.ID).Session.State; state != hubstore.GrillFinished {
		t.Errorf("session = %q, want finished", state)
	}
}

// The distinct kind is what keeps a blocked ask_user honest: only a real answer
// resolves the question it is sitting on.
func TestGrillInterjectionNeverAnswersPendingQuestion(t *testing.T) {
	ts, stores, repo, srv := grillHookServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)

	question, _, err := stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindQuestion, Payload: `{"text":"Which page?"}`,
	})
	if err != nil {
		t.Fatalf("post question: %v", err)
	}
	if _, _, err := stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindInterjection, Payload: `{"text":"drop it"}`,
	}); err != nil {
		t.Fatalf("queue interjection: %v", err)
	}

	if answer, ok := srv.grillAnswerAfter(sid, question.ID); ok {
		t.Fatalf("interjection resolved the question as %q", answer)
	}
	if _, _, err := stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindAnswer, Payload: `{"text":"the login page"}`,
	}); err != nil {
		t.Fatalf("post answer: %v", err)
	}
	if answer, ok := srv.grillAnswerAfter(sid, question.ID); !ok || answer != "the login page" {
		t.Fatalf("answer = %q (ok=%v), want the real answer", answer, ok)
	}
}

// A turn that ended without reaching the queue must hand the session on rather than
// park it — but only when there is a chain to resume, and never in place of a wall or
// a crash the user still has to see.
func TestGrillReconcileResumesOnInterjection(t *testing.T) {
	tests := []struct {
		name      string
		chain     string
		queue     []string
		delivered bool
		out       grillOutput
		runErr    error
		wantAgain bool
		wantState string
		wantHint  string
	}{
		{
			name:      "queued interjection keeps the session running",
			chain:     "sid-one",
			queue:     []string{"drop the schema thread"},
			wantAgain: true,
			wantState: hubstore.GrillRunning,
		},
		{
			name:      "nothing queued parks as before",
			chain:     "sid-one",
			wantState: hubstore.GrillParked,
			wantHint:  grillNoOutcomeReason,
		},
		{
			name:      "an already-delivered interjection parks",
			chain:     "sid-one",
			queue:     []string{"drop the schema thread"},
			delivered: true,
			wantState: hubstore.GrillParked,
			wantHint:  grillNoOutcomeReason,
		},
		{
			name:      "no chain to resume parks",
			queue:     []string{"drop the schema thread"},
			wantState: hubstore.GrillParked,
			wantHint:  grillNoOutcomeReason,
		},
		{
			name:      "a crashed child still parks as a crash",
			chain:     "sid-one",
			queue:     []string{"drop the schema thread"},
			runErr:    context.Canceled,
			wantState: hubstore.GrillParked,
			wantHint:  grillCrashReason,
		},
		{
			name:      "a wall still stalls",
			chain:     "sid-one",
			queue:     []string{"drop the schema thread"},
			out:       grillOutput{stderr: "API Error: 403 Request not allowed. Please run /login"},
			wantState: hubstore.GrillStalled,
			wantHint:  "re-authentication",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, store, repo, _ := newGrillRunnerTest(t, grillStubScript)
			sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1"})
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			if tt.chain != "" {
				if _, _, err := store.UpdateChain(sess.ID, tt.chain); err != nil {
					t.Fatalf("update chain: %v", err)
				}
			}
			for _, text := range tt.queue {
				if _, _, err := store.AppendMessage(sess.ID, hubstore.NewGrillMessage{
					Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindInterjection,
					Payload: `{"text":"` + text + `"}`,
				}); err != nil {
					t.Fatalf("queue interjection: %v", err)
				}
			}
			if tt.delivered {
				if _, err := store.ConsumeInterjections(sess.ID); err != nil {
					t.Fatalf("pre-deliver interjection: %v", err)
				}
			}

			again := r.reconcile(sess.ID, claudeGrillAdapter{r: r}, tt.out, tt.runErr, false)

			if again != tt.wantAgain {
				t.Errorf("reconcile = %v, want %v", again, tt.wantAgain)
			}
			got, _, _ := store.Session(sess.ID)
			if got.State != tt.wantState || !strings.Contains(got.ParkedReason, tt.wantHint) {
				t.Errorf("session = %s (%q), want %s containing %q",
					got.State, got.ParkedReason, tt.wantState, tt.wantHint)
			}
		})
	}
}

// The whole path with a real child: the user types while the turn is in flight, the
// child ends without ever reaching the queue, and the session resumes on the same
// chain with the interjection as its prompt rather than parking on the user.
func TestGrillInterjectionResumesDeadChild(t *testing.T) {
	r, store, repo, stubDir := newGrillRunnerTest(t, grillStubBlocks)
	r.srv.startGrill = r.launch
	r.srv.grillTurnActive = r.active
	ts := httptest.NewServer(r.srv.Handler())
	t.Cleanup(ts.Close)

	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	r.launch(context.Background(), sess)
	waitForGrillStub(t, filepath.Join(stubDir, "started"))

	sid := strconv.FormatInt(sess.ID, 10)
	msg := postGrillMessage(t, ts, sid, "actually use postgres")
	if msg.Message.Kind != hubstore.GrillKindInterjection || msg.Session.State != hubstore.GrillRunning {
		t.Fatalf("interjection = %+v on session %q, want it queued mid-turn", msg.Message, msg.Session.State)
	}
	if err := os.WriteFile(filepath.Join(stubDir, "release"), nil, 0o644); err != nil {
		t.Fatalf("release stub: %v", err)
	}

	waitForGrillChain(t, store, sess.ID, "sid-two")
	waitForGrillIdle(t, r, sess.ID)

	resumeArgs := readNullArgs(t, filepath.Join(stubDir, "resume.args"))
	if !contains(resumeArgs, "--resume") || !contains(resumeArgs, "sid-one") {
		t.Errorf("resume turn must carry the dead child's chain: %v", resumeArgs)
	}
	prompt := lastArg(resumeArgs)
	if !strings.Contains(prompt, "the user interjected") || !strings.Contains(prompt, "actually use postgres") {
		t.Errorf("resume prompt = %q, want the steering frame around the interjection", prompt)
	}
	if left, err := store.PendingInterjections(sess.ID); err != nil || len(left) != 0 {
		t.Errorf("pending after the resume = %d (err=%v), want the queue claimed once", len(left), err)
	}
}
