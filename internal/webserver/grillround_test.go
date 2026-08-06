package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

func roundCall(questions ...map[string]any) map[string]any {
	return toolCall("ask_round", map[string]any{"questions": questions})
}

// askRoundInBackground poses a round on a blocking call and hands back the channels the
// test waits on, so it can answer the round while the call is still open.
func askRoundInBackground(t *testing.T, ts *httptest.Server, sid string, call map[string]any) (<-chan rpcMsg, <-chan error) {
	t.Helper()
	done := make(chan rpcMsg, 1)
	errc := make(chan error, 1)
	go func() {
		res, err := doMCPPost(mcpURL(ts, sid), call)
		if err != nil {
			errc <- err
			return
		}
		msg, err := readSSEResult(res)
		if err != nil {
			errc <- err
			return
		}
		done <- msg
	}()
	return done, errc
}

func postRoundAnswers(t *testing.T, ts *httptest.Server, sid string, answers ...GrillRoundAnswerInput) *http.Response {
	t.Helper()
	return postJSON(t, ts.URL+APIPrefix+"/grill/"+sid+"/answer", GrillAnswerRequest{Answers: answers})
}

func submitRoundAnswers(t *testing.T, ts *httptest.Server, sid string, answers ...GrillRoundAnswerInput) {
	t.Helper()
	res := postRoundAnswers(t, ts, sid, answers...)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("round answer status = %d, want 200", res.StatusCode)
	}
}

// roundResult decodes the ordered answer set a settled round returns.
func roundResult(t *testing.T, tr mcpToolResult) []string {
	t.Helper()
	var structured struct {
		Answers []string `json:"answers"`
	}
	if err := json.Unmarshal(mustJSON(t, tr.StructuredContent), &structured); err != nil {
		t.Fatalf("decode round result: %v", err)
	}
	return structured.Answers
}

func roundQuestions(t *testing.T, msg GrillMessageView) []grillRoundQuestion {
	t.Helper()
	questions, ok := grillRoundQuestions(string(msg.Payload))
	if !ok {
		t.Fatalf("message %s is not a round: %s", msg.ID, msg.Payload)
	}
	return questions
}

// grillRoundMessage returns the session's one round question, failing when the
// transcript holds anything but exactly one.
func grillRoundMessage(t *testing.T, ts *httptest.Server, sid string) GrillMessageView {
	t.Helper()
	var found []GrillMessageView
	for _, m := range grillDetail(t, ts, sid).Messages {
		if m.Role != hubstore.GrillRoleAgent || m.Kind != hubstore.GrillKindQuestion {
			continue
		}
		if _, ok := grillRoundQuestions(string(m.Payload)); ok {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("stored %d round questions, want 1", len(found))
	}
	return found[0]
}

func answeredIndexes(answers []GrillRoundAnswerView) []int {
	out := make([]int, len(answers))
	for i, a := range answers {
		out[i] = a.Index
	}
	return out
}

// A round poses every question at once: all of them reach the dock as one numbered
// message, the user answers them in whatever order they like, and the single tool call
// comes back with the whole answer set in the order it asked.
func TestGrillMCPAskRoundRoundTrip(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	done, errc := askRoundInBackground(t, ts, sess.ID, roundCall(
		map[string]any{"question": "Which page is in scope?", "options": []string{"login", "signup"}},
		map[string]any{"question": "Which auth flow?"},
		map[string]any{"question": "Ship behind a flag?", "allow_free_text": false, "options": []string{"yes", "no"}},
	))

	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)

	round := grillRoundMessage(t, ts, sess.ID)
	questions := roundQuestions(t, round)
	if len(questions) != 3 {
		t.Fatalf("posed %d questions, want the whole round of 3", len(questions))
	}
	if questions[0].Text != "Which page is in scope?" || !slices.Equal(questions[0].Options, []string{"login", "signup"}) {
		t.Errorf("first question = %+v, want its own options carried through", questions[0])
	}
	if !questions[1].AllowFreeText || questions[2].AllowFreeText {
		t.Errorf("allow_free_text = %v/%v, want the default on and the explicit off",
			questions[1].AllowFreeText, questions[2].AllowFreeText)
	}
	if len(round.RoundAnswers) != 0 {
		t.Errorf("round answers = %+v, want none before the user answers", round.RoundAnswers)
	}

	// Out of order on purpose: the round is a form, not a queue.
	submitRoundAnswers(t, ts, sess.ID,
		GrillRoundAnswerInput{Index: 2, Text: "no"},
		GrillRoundAnswerInput{Index: 0, Text: "login"},
		GrillRoundAnswerInput{Index: 1, Text: "magic link"},
	)

	select {
	case err := <-errc:
		t.Fatalf("ask_round call failed: %v", err)
	case msg := <-done:
		tr := toolResult(t, msg)
		if tr.IsError {
			t.Fatalf("ask_round returned an error result: %+v", tr)
		}
		if got := roundResult(t, tr); !slices.Equal(got, []string{"login", "magic link", "no"}) {
			t.Fatalf("round answers = %+v, want them in the order the round asked", got)
		}
		if len(tr.Content) != 1 || !strings.Contains(tr.Content[0].Text, "Which auth flow?") ||
			!strings.Contains(tr.Content[0].Text, "magic link") {
			t.Fatalf("round result body = %+v, want every question with its answer", tr.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask_round did not return after the round was answered")
	}

	detail := grillDetail(t, ts, sess.ID)
	if detail.Session.State != hubstore.GrillRunning {
		t.Fatalf("session state = %q, want running after the round", detail.Session.State)
	}
	answers := detail.Messages[len(detail.Messages)-1]
	if answers.Role != hubstore.GrillRoleUser || answers.Kind != hubstore.GrillKindAnswer {
		t.Fatalf("last message = %s/%s, want the answer that closed the round", answers.Role, answers.Kind)
	}
	var a struct {
		Round bool `json:"round"`
	}
	if err := json.Unmarshal(answers.Payload, &a); err != nil {
		t.Fatalf("decode answer payload: %v", err)
	}
	if !a.Round {
		t.Errorf("answer payload = %s, want it flagged as a round's own", answers.Payload)
	}
	if got := answeredIndexes(grillRoundMessage(t, ts, sess.ID).RoundAnswers); !slices.Equal(got, []int{0, 1, 2}) {
		t.Errorf("stored round answers = %+v, want one per question", got)
	}
}

// A round recommended throughout never reaches the user: every question is answered
// from the agent's own recommendation, the session never enters waiting, and nothing
// notifies anyone.
func TestGrillMCPAskRoundAutoAcceptsEveryRecommendation(t *testing.T) {
	ts, stores, repo := grillServer(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})

	tr := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), roundCall(
		map[string]any{"question": "Which page is in scope?", "options": []string{"login", "signup"}, "recommended": "login"},
		map[string]any{"question": "Ship behind a flag?", "recommended": "  yes  ", "why": "It is reversible."},
	)))
	if tr.IsError {
		t.Fatalf("ask_round returned an error result: %+v", tr)
	}
	if got := roundResult(t, tr); !slices.Equal(got, []string{"login", "yes"}) {
		t.Fatalf("round answers = %+v, want both recommendations, trimmed", got)
	}

	detail := grillDetail(t, ts, sess.ID)
	if detail.Session.State != hubstore.GrillRunning {
		t.Fatalf("session state = %q, want running throughout", detail.Session.State)
	}
	if len(detail.Messages) != 2 {
		t.Fatalf("stored %d messages, want the round and its auto answer: %+v", len(detail.Messages), detail.Messages)
	}
	var answer struct {
		Auto  bool `json:"auto"`
		Round bool `json:"round"`
	}
	if err := json.Unmarshal(detail.Messages[1].Payload, &answer); err != nil {
		t.Fatalf("decode answer payload: %v", err)
	}
	if !answer.Auto || !answer.Round {
		t.Errorf("answer payload = %s, want a round answered by the hub itself", detail.Messages[1].Payload)
	}
	for _, a := range detail.Messages[0].RoundAnswers {
		if !a.Auto {
			t.Errorf("round answer %d = %+v, want it flagged auto", a.Index, a)
		}
	}
	items, err := stores.Notifications().List(10)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("stored %d notifications, want none for a round the user never saw", len(items))
	}
}

// A mixed round only pulls the user in for the questions the agent would not choose
// for itself; the recommended ones are already answered when they arrive, and the tool
// still comes back with the whole set in order.
func TestGrillMCPAskRoundAutoAcceptPosesOnlyTasteQuestions(t *testing.T) {
	ts, stores, repo := grillServer(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})

	done, errc := askRoundInBackground(t, ts, sess.ID, roundCall(
		map[string]any{"question": "Which page is in scope?", "recommended": "login"},
		map[string]any{"question": "What should the empty state say?"},
	))
	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)

	round := grillRoundMessage(t, ts, sess.ID)
	if got := answeredIndexes(round.RoundAnswers); !slices.Equal(got, []int{0}) {
		t.Fatalf("answered questions = %+v, want only the recommended one", got)
	}
	items, err := stores.Notifications().List(10)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("stored %d notifications, want one for the question needing the user", len(items))
	}
	if strings.Contains(items[0].Body, "Which page is in scope?") ||
		!strings.Contains(items[0].Body, "What should the empty state say?") {
		t.Errorf("notification body = %q, want only the question still open", items[0].Body)
	}

	submitRoundAnswers(t, ts, sess.ID, GrillRoundAnswerInput{Index: 1, Text: "Nothing here yet."})

	select {
	case err := <-errc:
		t.Fatalf("ask_round call failed: %v", err)
	case msg := <-done:
		if got := roundResult(t, toolResult(t, msg)); !slices.Equal(got, []string{"login", "Nothing here yet."}) {
			t.Fatalf("round answers = %+v, want the recommendation and the user's answer in order", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask_round did not return after the last question was answered")
	}
}

// A queued interjection outranks a round exactly as it outranks a single question: the
// round is neither stored nor posed, and the agent is handed the steering instead.
func TestGrillMCPAskRoundInterjectionOutranks(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})

	postGrillMessage(t, ts, sess.ID, "Forget the login page, do signup.")

	tr := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), roundCall(
		map[string]any{"question": "Which page is in scope?", "recommended": "login"},
		map[string]any{"question": "Which auth flow?", "recommended": "password"},
	)))
	if tr.IsError || len(tr.Content) != 1 || !strings.Contains(tr.Content[0].Text, "Forget the login page, do signup.") {
		t.Fatalf("ask_round result = %+v, want the interjection as steering", tr)
	}
	for _, m := range grillDetail(t, ts, sess.ID).Messages {
		if _, ok := grillRoundQuestions(string(m.Payload)); ok {
			t.Fatalf("stored a round the interjection outranked: %+v", m)
		}
	}
}

// A provider whose MCP client abandons the blocking call retries it with the same
// round. The retry re-attaches to the round already posed — one message in the
// transcript, one wait — and still returns the answers when they land.
func TestGrillMCPAskRoundRetryReattaches(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	call := roundCall(
		map[string]any{"question": "Which page is in scope?"},
		map[string]any{"question": "Which auth flow?"},
	)

	ctx, abandon := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL(ts, sess.ID), bytes.NewReader(mustJSON(t, call)))
	if err != nil {
		t.Fatalf("build ask_round request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	first, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ask_round call failed: %v", err)
	}
	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)
	abandon()
	_ = first.Body.Close()

	retry, err := doMCPPost(mcpURL(ts, sess.ID), call)
	if err != nil {
		t.Fatalf("retried ask_round call failed: %v", err)
	}
	done := make(chan rpcMsg, 1)
	errc := make(chan error, 1)
	go func() {
		msg, err := readSSEResult(retry)
		if err != nil {
			errc <- err
			return
		}
		done <- msg
	}()

	submitRoundAnswers(t, ts, sess.ID,
		GrillRoundAnswerInput{Index: 0, Text: "login"},
		GrillRoundAnswerInput{Index: 1, Text: "magic link"},
	)

	select {
	case err := <-errc:
		t.Fatalf("retried ask_round call failed: %v", err)
	case msg := <-done:
		if got := roundResult(t, toolResult(t, msg)); !slices.Equal(got, []string{"login", "magic link"}) {
			t.Fatalf("retried round answers = %+v, want both answers in order", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retried ask_round did not return after the round was answered")
	}
	grillRoundMessage(t, ts, sess.ID)
}

// Stepping away mid-round parks the session on what is left of it. The answers already
// given are kept, the resume prompt names only the remainder, and the round the agent
// poses again re-attaches to the one the user is part way through rather than starting
// it over.
func TestGrillMCPAskRoundParkKeepsAnswersAndReposesRemainder(t *testing.T) {
	restore := swapGrillTimers(50*time.Millisecond, 10*time.Second)
	ts, _, repo, srv := grillHookServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	call := roundCall(
		map[string]any{"question": "Which page is in scope?"},
		map[string]any{"question": "Which auth flow?"},
		map[string]any{"question": "Ship behind a flag?"},
	)

	res, err := doMCPPost(mcpURL(ts, sess.ID), call)
	if err != nil {
		t.Fatalf("ask_round post: %v", err)
	}
	msg, err := readSSEResult(res)
	if err != nil {
		t.Fatalf("read ask_round stream: %v", err)
	}
	var structured struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(mustJSON(t, toolResult(t, msg).StructuredContent), &structured); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	if structured.Status != "parked" {
		t.Fatalf("round park sentinel status = %q, want parked", structured.Status)
	}
	waitForGrillState(t, ts, sess.ID, hubstore.GrillParked)
	restore()

	// The user comes back and answers one question of the three, which picks the
	// session up: the agent has to run again to collect the rest.
	submitRoundAnswers(t, ts, sess.ID, GrillRoundAnswerInput{Index: 1, Text: "magic link"})
	waitForGrillState(t, ts, sess.ID, hubstore.GrillRunning)
	if got := answeredIndexes(grillRoundMessage(t, ts, sess.ID).RoundAnswers); !slices.Equal(got, []int{1}) {
		t.Fatalf("answered questions = %+v, want the one the user gave", got)
	}

	prompt, ok := srv.grillRoundResumePrompt(sid)
	if !ok {
		t.Fatal("no round resume prompt for a session picked up mid-round")
	}
	if !strings.Contains(prompt, "magic link") || !strings.Contains(prompt, "Still unanswered:") ||
		!strings.Contains(prompt, "Ship behind a flag?") {
		t.Fatalf("round resume prompt = %q, want the answer given and the remainder", prompt)
	}

	done, errc := askRoundInBackground(t, ts, sess.ID, call)
	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)
	if got := answeredIndexes(grillRoundMessage(t, ts, sess.ID).RoundAnswers); !slices.Equal(got, []int{1}) {
		t.Fatalf("answered questions after the re-pose = %+v, want the earlier answer kept", got)
	}

	submitRoundAnswers(t, ts, sess.ID,
		GrillRoundAnswerInput{Index: 0, Text: "login"},
		GrillRoundAnswerInput{Index: 2, Text: "yes"},
	)
	select {
	case err := <-errc:
		t.Fatalf("re-posed ask_round call failed: %v", err)
	case msg := <-done:
		if got := roundResult(t, toolResult(t, msg)); !slices.Equal(got, []string{"login", "magic link", "yes"}) {
			t.Fatalf("round answers = %+v, want the remainder joined to the answer already given", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("re-posed ask_round did not return after the remainder was answered")
	}
}

// Stopping the agent mid-round leaves the user steering rather than stuck: the round
// stops being the only way in, so their next line redirects the turn they killed
// exactly as it does when a single question is the one left hanging.
func TestGrillStopMidRoundTakesSteeringText(t *testing.T) {
	ts, _, repo, srv := grillHookServer(t)
	var live atomic.Bool
	live.Store(true)
	srv.grillTurnActive = func(int64) bool { return live.Load() }
	srv.stopGrillTurn = func(int64) { live.Store(false) }

	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	done, errc := askRoundInBackground(t, ts, sess.ID, roundCall(
		map[string]any{"question": "Which page is in scope?"},
		map[string]any{"question": "Which auth flow?"},
	))
	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)
	submitRoundAnswers(t, ts, sess.ID, GrillRoundAnswerInput{Index: 0, Text: "login"})

	stopped := postGrillStop(t, ts, sess.ID, http.StatusOK)
	if !stopped.Stopped || stopped.ParkedReason != grillStoppedReason {
		t.Fatalf("stopped session = %+v, want the user's own park", stopped)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "drop the auth thread"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("steering a stopped round status = %d, want 200", res.StatusCode)
	}
	var answered GrillAnswerResponse
	if err := json.NewDecoder(res.Body).Decode(&answered); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	var payload struct {
		Text  string `json:"text"`
		Steer bool   `json:"steer"`
	}
	if answered.Message == nil {
		t.Fatal("steering a stopped round returned no message")
	}
	if err := json.Unmarshal(answered.Message.Payload, &payload); err != nil {
		t.Fatalf("decode answer payload: %v", err)
	}
	if payload.Text != "drop the auth thread" || !payload.Steer {
		t.Fatalf("answer payload = %+v, want the message marked as steering", payload)
	}
	if answered.Session.State != hubstore.GrillRunning {
		t.Fatalf("session after the steer = %q, want running", answered.Session.State)
	}
	// The round no longer commandeers the resume turn: the agent is told where the user
	// steered it, not to pose the round again.
	if prompt, ok := srv.grillRoundResumePrompt(sid); ok {
		t.Fatalf("resume prompt = %q, want the steer rather than a re-pose of the round", prompt)
	}
	if got := answeredIndexes(grillRoundMessage(t, ts, sess.ID).RoundAnswers); !slices.Equal(got, []int{0}) {
		t.Fatalf("round answers after the steer = %+v, want the one already given kept", got)
	}

	// A round the user overtook returns what they said, not an answer set with holes in
	// it where the questions they never got to sit.
	select {
	case err := <-errc:
		t.Fatalf("ask_round call failed: %v", err)
	case msg := <-done:
		tr := toolResult(t, msg)
		if len(tr.Content) != 1 || tr.Content[0].Text != "drop the auth thread" {
			t.Fatalf("overtaken ask_round result = %+v, want the user's steering message", tr.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask_round did not return once the user steered the stopped session")
	}
}

// A round answered a second time keeps the answer it has: the submission that races a
// question already settled by auto-accept must not overwrite it.
func TestGrillRoundAnswerKeepsTheAnswerAlreadyGiven(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})

	done, errc := askRoundInBackground(t, ts, sess.ID, roundCall(
		map[string]any{"question": "Which page is in scope?", "recommended": "login"},
		map[string]any{"question": "What should the empty state say?"},
	))
	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)

	res := postRoundAnswers(t, ts, sess.ID, GrillRoundAnswerInput{Index: 0, Text: "signup"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("re-answer status = %d, want 400 — nothing was left to answer", res.StatusCode)
	}

	submitRoundAnswers(t, ts, sess.ID, GrillRoundAnswerInput{Index: 1, Text: "Nothing here yet."})
	select {
	case err := <-errc:
		t.Fatalf("ask_round call failed: %v", err)
	case msg := <-done:
		if got := roundResult(t, toolResult(t, msg)); !slices.Equal(got, []string{"login", "Nothing here yet."}) {
			t.Fatalf("round answers = %+v, want the recommendation kept", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask_round did not return after the round was answered")
	}
}

func TestGrillRoundAnswerRefusals(t *testing.T) {
	ts, stores, repo := grillServer(t)

	single := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(single.ID, 10, 64)
	if _, _, err := stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindQuestion,
		Payload: `{"text":"Which page is in scope?"}`,
	}); err != nil {
		t.Fatalf("post question: %v", err)
	}
	if _, err := stores.Grill().Transition(sid, hubstore.GrillWaiting, ""); err != nil {
		t.Fatalf("pose question: %v", err)
	}
	res := postRoundAnswers(t, ts, single.ID, GrillRoundAnswerInput{Index: 0, Text: "login"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("round answer to a single question = %d, want 409", res.StatusCode)
	}

	round := createGrill(t, ts, repo, "COD-2")
	done, errc := askRoundInBackground(t, ts, round.ID, roundCall(
		map[string]any{"question": "Which page is in scope?"},
	))
	waitForGrillState(t, ts, round.ID, hubstore.GrillWaiting)

	out := postRoundAnswers(t, ts, round.ID, GrillRoundAnswerInput{Index: 3, Text: "login"})
	_ = out.Body.Close()
	if out.StatusCode != http.StatusBadRequest {
		t.Fatalf("out-of-range index status = %d, want 400", out.StatusCode)
	}
	blank := postRoundAnswers(t, ts, round.ID, GrillRoundAnswerInput{Index: 0, Text: "   "})
	_ = blank.Body.Close()
	if blank.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank answer status = %d, want 400", blank.StatusCode)
	}

	// One line cannot settle a round: the questions it left out would close with
	// nothing.
	line := postJSON(t, ts.URL+APIPrefix+"/grill/"+round.ID+"/answer", GrillAnswerRequest{Text: "login"})
	_ = line.Body.Close()
	if line.StatusCode != http.StatusConflict {
		t.Fatalf("plain answer to a round status = %d, want 409", line.StatusCode)
	}

	submitRoundAnswers(t, ts, round.ID, GrillRoundAnswerInput{Index: 0, Text: "login"})
	select {
	case err := <-errc:
		t.Fatalf("ask_round call failed: %v", err)
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ask_round did not return after the round was answered")
	}
}

func TestGrillAskRoundValidation(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	empty := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), roundCall()))
	if !empty.IsError || !strings.Contains(empty.Content[0].Text, "at least one question") {
		t.Fatalf("empty round result = %+v, want a correctable tool error", empty)
	}
	blank := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), roundCall(
		map[string]any{"question": "Which page is in scope?"},
		map[string]any{"question": "   "},
	)))
	if !blank.IsError || !strings.Contains(blank.Content[0].Text, "question 2") {
		t.Fatalf("blank question result = %+v, want the offending position named", blank)
	}
	for _, m := range grillDetail(t, ts, sess.ID).Messages {
		t.Fatalf("stored a message for a refused round: %+v", m)
	}
}

// An ended session gets the stop sentinel rather than a round: the transcript the user
// discarded must not collect answers behind them.
func TestGrillAskRoundStopsOnEndedSession(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})

	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/abandon", nil)
	_ = res.Body.Close()

	tr := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), roundCall(
		map[string]any{"question": "Which page is in scope?", "recommended": "login"},
	)))
	var structured struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(mustJSON(t, tr.StructuredContent), &structured); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	if structured.Status != "ended" {
		t.Fatalf("ask_round result = %+v, want the ended sentinel", tr)
	}
	for _, m := range grillDetail(t, ts, sess.ID).Messages {
		if m.Role == hubstore.GrillRoleUser && m.Kind == hubstore.GrillKindAnswer {
			t.Fatalf("answered a round on an ended session: %+v", m)
		}
	}
}

// Turning auto-accept on mid-session lands on the round the user is looking at, not
// only the next one.
func TestGrillAutoAcceptSwitchAnswersPendingRound(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	done, errc := askRoundInBackground(t, ts, sess.ID, roundCall(
		map[string]any{"question": "Which page is in scope?", "recommended": "login"},
		map[string]any{"question": "Ship behind a flag?", "recommended": "yes"},
	))
	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)

	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/auto-accept", GrillAutoAcceptRequest{Enabled: true})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("auto-accept status = %d, want 200", res.StatusCode)
	}

	select {
	case err := <-errc:
		t.Fatalf("ask_round call failed: %v", err)
	case msg := <-done:
		if got := roundResult(t, toolResult(t, msg)); !slices.Equal(got, []string{"login", "yes"}) {
			t.Fatalf("round answers = %+v, want both recommendations", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask_round did not return after auto-accept was switched on")
	}
}
