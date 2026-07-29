package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

func grillServer(t *testing.T) (*httptest.Server, *hubstore.Stores, string) {
	t.Helper()
	ts, stores, repo, _ := grillHookServer(t)
	return ts, stores, repo
}

// grillHookServer hands back the hub itself as well, so a test can stand in for the
// runner and watch what a turn-resuming request spawns. It isolates HOME because
// session creation resolves the repo's grill model through the layered config, which
// reads ~/.trau.ini.
func grillHookServer(t *testing.T) (*httptest.Server, *hubstore.Stores, string, *Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	stores := testStoresAt(t, home)
	repo := registry.Repo{Name: "acme", Root: filepath.Join(home, "acme"), RunsDir: filepath.Join(home, "acme", ".trau", "runs")}
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}
	srv := New("1.2.3", "127.0.0.1", "", nil, false, stores)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, stores, repo.Name, srv
}

func createGrill(t *testing.T, ts *httptest.Server, repo, issue string) GrillSessionView {
	t.Helper()
	return createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: issue})
}

func createGrillWith(t *testing.T, ts *httptest.Server, repo string, req GrillCreateRequest) GrillSessionView {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", req)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return v
}

func TestGrillCreateAndList(t *testing.T) {
	ts, _, repo := grillServer(t)

	sess := createGrill(t, ts, repo, "COD-1")
	if sess.State != hubstore.GrillRunning || sess.IssueID != "COD-1" {
		t.Fatalf("created session = %+v", sess)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create status = %d, want 409", res.StatusCode)
	}

	_, body := get(t, ts, APIPrefix+"/repos/"+repo+"/grill")
	var list GrillListResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].ID != sess.ID {
		t.Fatalf("list = %+v, want session %s", list.Sessions, sess.ID)
	}
}

func TestGrillAwaitingAcrossRepos(t *testing.T) {
	ts, stores, repo := grillServer(t)
	asked := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(asked.ID, 10, 64)
	createGrill(t, ts, repo, "COD-2")

	if _, _, err := stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindQuestion,
		Payload: `{"text":"Which destination?\nPick one of the options."}`,
	}); err != nil {
		t.Fatalf("post question: %v", err)
	}
	if _, err := stores.Grill().Transition(sid, hubstore.GrillWaiting, ""); err != nil {
		t.Fatalf("pose question: %v", err)
	}

	// Parking normally happens mid-interview, so the reason must win over the
	// question the session already asked.
	elsewhere, err := stores.Grill().Create(hubstore.NewGrillSession{Repo: "/tmp/other", IssueID: "OTH-9"})
	if err != nil {
		t.Fatalf("create other repo session: %v", err)
	}
	if _, _, err := stores.Grill().AppendMessage(elsewhere.ID, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindQuestion,
		Payload: `{"text":"Should I retry the migration?"}`,
	}); err != nil {
		t.Fatalf("post other repo question: %v", err)
	}
	if _, err := stores.Grill().Transition(elsewhere.ID, hubstore.GrillParked, "needs a decision"); err != nil {
		t.Fatalf("park other repo session: %v", err)
	}

	_, body := get(t, ts, APIPrefix+"/grill")
	var list GrillAwaitingResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode awaiting: %v", err)
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("awaiting = %+v, want the waiting and parked sessions only", list.Sessions)
	}
	byID := map[string]GrillAwaitingView{}
	for _, sess := range list.Sessions {
		byID[sess.ID] = sess
	}
	waiting, ok := byID[asked.ID]
	if !ok {
		t.Fatalf("awaiting = %+v, want session %s", list.Sessions, asked.ID)
	}
	if waiting.Repo != repo || waiting.Question != "Which destination?" {
		t.Fatalf("waiting view = %+v, want repo %s and the question's first line", waiting, repo)
	}
	// The dock identifies a row by these alone, without loading the conversation.
	if waiting.IssueID != "COD-1" || waiting.State != hubstore.GrillWaiting || waiting.UpdatedAt == "" {
		t.Fatalf("waiting view = %+v, want the issue identifier, state and age", waiting)
	}
	parked := byID[strconv.FormatInt(elsewhere.ID, 10)]
	if parked.Repo != "other" || parked.Question != "needs a decision" {
		t.Fatalf("parked view = %+v, want the park reason as preview", parked)
	}
	if parked.State != hubstore.GrillParked {
		t.Fatalf("parked view state = %q, want %q", parked.State, hubstore.GrillParked)
	}
}

func awaitingGrillIDs(t *testing.T, ts *httptest.Server) map[string]bool {
	t.Helper()
	_, body := get(t, ts, APIPrefix+"/grill")
	var list GrillAwaitingResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode awaiting: %v", err)
	}
	ids := map[string]bool{}
	for _, sess := range list.Sessions {
		ids[sess.ID] = true
	}
	return ids
}

func TestGrillRetiredWhenIssueCloses(t *testing.T) {
	ts, stores, repo := grillServer(t)
	_, issue := createInternal(t, ts, repo, InternalIssueRequest{Title: "Onboarding wizard", State: "started"})
	sess := createGrill(t, ts, repo, issue.ID)
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	if _, err := stores.Grill().Transition(sid, hubstore.GrillWaiting, ""); err != nil {
		t.Fatalf("pose question: %v", err)
	}
	if !awaitingGrillIDs(t, ts)[sess.ID] {
		t.Fatalf("awaiting lacks session %s while its issue is open", sess.ID)
	}

	res := postJSON(t,
		ts.URL+APIPrefix+"/repos/"+repo+"/issues/internal/"+issue.ID+"/transition",
		InternalTransitionRequest{State: "done"},
	)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("close status = %d, want 200", res.StatusCode)
	}

	if awaitingGrillIDs(t, ts)[sess.ID] {
		t.Fatalf("awaiting still serves session %s after its issue closed", sess.ID)
	}
	stored, _, err := stores.Grill().Session(sid)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	want := "retired: " + issue.ID + " closed"
	if stored.State != hubstore.GrillAbandoned || stored.ParkedReason != want {
		t.Fatalf("session = %q/%q, want abandoned carrying %q", stored.State, stored.ParkedReason, want)
	}
}

func TestGrillCreateUnknownRepo(t *testing.T) {
	ts, _, _ := grillServer(t)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/nope/grill", GrillCreateRequest{IssueID: "COD-1"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestGrillDetailAndAnswer(t *testing.T) {
	ts, stores, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)

	// Answering a running session (no question posed) is refused.
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "hi"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("premature answer status = %d, want 409", res.StatusCode)
	}

	// Simulate the runner posing a question and parking on the answer.
	if _, _, err := stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindQuestion, Payload: `{"text":"why?"}`}); err != nil {
		t.Fatalf("post question: %v", err)
	}
	if _, err := stores.Grill().Transition(sid, hubstore.GrillWaiting, ""); err != nil {
		t.Fatalf("pose question: %v", err)
	}

	res = postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "because"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d, want 200", res.StatusCode)
	}
	var ack GrillAnswerResponse
	if err := json.NewDecoder(res.Body).Decode(&ack); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if ack.Session.State != hubstore.GrillRunning || ack.Message.Kind != hubstore.GrillKindAnswer {
		t.Fatalf("answer ack = %+v", ack)
	}

	_, body := get(t, ts, APIPrefix+"/grill/"+sess.ID)
	var detail GrillDetailResponse
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if len(detail.Messages) != 2 || detail.Messages[1].Kind != hubstore.GrillKindAnswer {
		t.Fatalf("detail messages = %+v", detail.Messages)
	}
}

func TestGrillFollowUpOnFinished(t *testing.T) {
	ts, stores, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)

	// Simulate the runner proposing an outcome and finishing on it.
	if _, _, err := stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindOutcome, Payload: `{"disposition":"no_change","summary":"reads clear"}`}); err != nil {
		t.Fatalf("post outcome: %v", err)
	}
	if _, err := stores.Grill().Transition(sid, hubstore.GrillFinished, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "what about auth?"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("follow-up status = %d, want 200", res.StatusCode)
	}
	var ack GrillAnswerResponse
	if err := json.NewDecoder(res.Body).Decode(&ack); err != nil {
		t.Fatalf("decode follow-up: %v", err)
	}
	if ack.Session.State != hubstore.GrillRunning || ack.Message.Kind != hubstore.GrillKindAnswer {
		t.Fatalf("follow-up ack = %+v, want running/answer", ack)
	}

	// The door closes once the outcome lands: an applied session takes no follow-up.
	if _, err := stores.Grill().Transition(sid, hubstore.GrillFinished, ""); err != nil {
		t.Fatalf("refinish: %v", err)
	}
	if _, err := stores.Grill().Transition(sid, hubstore.GrillApplied, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	late := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "too late"})
	_ = late.Body.Close()
	if late.StatusCode != http.StatusConflict {
		t.Fatalf("answer after apply status = %d, want 409", late.StatusCode)
	}
}

func TestGrillResumeSpawns(t *testing.T) {
	tests := []struct {
		name   string
		state  string
		active bool
		want   bool
	}{
		{name: "parked", state: hubstore.GrillParked, want: true},
		{name: "stalled", state: hubstore.GrillStalled, want: true},
		{name: "finished", state: hubstore.GrillFinished, want: true},
		{name: "waiting on a live child", state: hubstore.GrillWaiting, active: true, want: false},
		{name: "waiting after the child exited", state: hubstore.GrillWaiting, want: true},
		{name: "running", state: hubstore.GrillRunning, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{grillTurnActive: func(int64) bool { return tt.active }}
			if got := s.grillResumeSpawns(7, tt.state); got != tt.want {
				t.Errorf("grillResumeSpawns(%q) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

func TestGrillAbandon(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/abandon", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("abandon status = %d, want 200", res.StatusCode)
	}

	// Idempotent on an already-abandoned session.
	res = postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/abandon", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("repeat abandon status = %d, want 200", res.StatusCode)
	}

	// Settling frees the issue for a fresh session.
	res = postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("recreate status = %d, want 201", res.StatusCode)
	}
}

func TestGrillSessionNotFound(t *testing.T) {
	ts, _, _ := grillServer(t)
	res, _ := get(t, ts, APIPrefix+"/grill/999")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
	res, _ = get(t, ts, APIPrefix+"/grill/not-a-number")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

func TestGrillModelSwitch(t *testing.T) {
	ts, stores, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)

	// A running session takes the switch — the model is only read at next spawn.
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/model", GrillModelRequest{Model: "opus"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("switch status = %d, want 200", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode switch: %v", err)
	}
	if v.Model != "opus" || v.Provider != "claude" || len(v.ModelOptions) == 0 {
		t.Fatalf("switched view = %+v, want model opus, provider claude, options", v)
	}

	msgs, err := stores.Grill().Messages(sid, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want the switch notice alone", len(msgs))
	}
	if msgs[0].Role != hubstore.GrillRoleSystem || msgs[0].Kind != hubstore.GrillKindInfo {
		t.Fatalf("notice = %s/%s, want system/info", msgs[0].Role, msgs[0].Kind)
	}
	if msgs[0].Payload != `{"text":"Model switched to opus"}` {
		t.Fatalf("notice payload = %s", msgs[0].Payload)
	}

	// Re-sending the same model is a no-op: 200 with no second notice.
	res = postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/model", GrillModelRequest{Model: "opus"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("no-op status = %d, want 200", res.StatusCode)
	}
	if again, _ := stores.Grill().Messages(sid, 0); len(again) != 1 {
		t.Fatalf("no-op appended a notice: %d messages, want 1", len(again))
	}

	res = postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/model", GrillModelRequest{Model: "  "})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty model status = %d, want 400", res.StatusCode)
	}
}

func TestGrillModelSwitchSettled(t *testing.T) {
	ts, stores, repo := grillServer(t)
	cases := []struct {
		issue string
		path  []string
	}{
		{"COD-1", []string{hubstore.GrillFinished}},
		{"COD-2", []string{hubstore.GrillFinished, hubstore.GrillApplied}},
		{"COD-3", []string{hubstore.GrillAbandoned}},
	}
	for _, tc := range cases {
		state := tc.path[len(tc.path)-1]
		sess := createGrill(t, ts, repo, tc.issue)
		sid, _ := strconv.ParseInt(sess.ID, 10, 64)
		for _, next := range tc.path {
			if _, err := stores.Grill().Transition(sid, next, ""); err != nil {
				t.Fatalf("transition to %s: %v", next, err)
			}
		}

		res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/model", GrillModelRequest{Model: "opus"})
		_ = res.Body.Close()
		if res.StatusCode != http.StatusConflict {
			t.Fatalf("%s switch status = %d, want 409", state, res.StatusCode)
		}
		if after, _, _ := stores.Grill().Session(sid); after.Model == "opus" {
			t.Fatalf("%s switch persisted the model", state)
		}
	}
}

func poseGrillQuestion(t *testing.T, stores *hubstore.Stores, sid int64, payload, state string) {
	t.Helper()
	if _, _, err := stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleAgent,
		Kind:    hubstore.GrillKindQuestion,
		Payload: payload,
	}); err != nil {
		t.Fatalf("post question: %v", err)
	}
	if _, err := stores.Grill().Transition(sid, state, ""); err != nil {
		t.Fatalf("pose question: %v", err)
	}
}

func postAutoAccept(t *testing.T, ts *httptest.Server, sid string, enabled bool, want int) GrillSessionView {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sid+"/auto-accept", GrillAutoAcceptRequest{Enabled: enabled})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != want {
		t.Fatalf("auto-accept status = %d, want %d", res.StatusCode, want)
	}
	var v GrillSessionView
	if want == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
			t.Fatalf("decode auto-accept: %v", err)
		}
	}
	return v
}

func TestGrillAutoAcceptSwitch(t *testing.T) {
	ts, stores, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	if sess.AutoAccept {
		t.Fatalf("created session = %+v, want auto-accept off", sess)
	}

	if v := postAutoAccept(t, ts, sess.ID, true, http.StatusOK); !v.AutoAccept {
		t.Fatalf("switched view = %+v, want auto-accept on", v)
	}
	msgs, err := stores.Grill().Messages(sid, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want the switch notice alone", len(msgs))
	}
	if msgs[0].Role != hubstore.GrillRoleSystem || msgs[0].Kind != hubstore.GrillKindInfo {
		t.Fatalf("notice = %s/%s, want system/info", msgs[0].Role, msgs[0].Kind)
	}
	if msgs[0].Payload != `{"text":"Auto-accept recommendations turned on"}` {
		t.Fatalf("notice payload = %s", msgs[0].Payload)
	}

	// Re-sending the value already in effect is a no-op: 200 with no second notice.
	postAutoAccept(t, ts, sess.ID, true, http.StatusOK)
	if again, _ := stores.Grill().Messages(sid, 0); len(again) != 1 {
		t.Fatalf("no-op appended a notice: %d messages, want 1", len(again))
	}

	if v := postAutoAccept(t, ts, sess.ID, false, http.StatusOK); v.AutoAccept {
		t.Fatalf("switched-off view = %+v, want auto-accept off", v)
	}
	msgs, _ = stores.Grill().Messages(sid, 0)
	if len(msgs) != 2 || msgs[1].Payload != `{"text":"Auto-accept recommendations turned off"}` {
		t.Fatalf("messages after switching off = %+v", msgs)
	}
}

// Switching auto-accept on while a recommended question is waiting answers that
// question with its recommendation, so the flip lands on the one the user is looking
// at rather than only the next.
func TestGrillAutoAcceptAnswersPendingQuestion(t *testing.T) {
	ts, stores, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	poseGrillQuestion(t, stores, sid,
		`{"text":"which store?","options":["sqlite","postgres"],"recommended":"sqlite"}`, hubstore.GrillWaiting)

	v := postAutoAccept(t, ts, sess.ID, true, http.StatusOK)
	if v.State != hubstore.GrillRunning || !v.AutoAccept {
		t.Fatalf("view = %+v, want running with auto-accept on", v)
	}
	msgs, err := stores.Grill().Messages(sid, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want the question, the notice and the auto answer", len(msgs))
	}
	answer := msgs[2]
	if answer.Role != hubstore.GrillRoleUser || answer.Kind != hubstore.GrillKindAnswer {
		t.Fatalf("last message = %s/%s, want user/answer", answer.Role, answer.Kind)
	}
	if answer.Payload != `{"text":"sqlite","auto":true}` {
		t.Fatalf("auto answer payload = %s", answer.Payload)
	}
}

// A question the agent made no recommendation on needs the user's taste, so the flip
// leaves it standing.
func TestGrillAutoAcceptLeavesUnrecommendedQuestion(t *testing.T) {
	ts, stores, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	poseGrillQuestion(t, stores, sid, `{"text":"which of these reads better?"}`, hubstore.GrillWaiting)

	v := postAutoAccept(t, ts, sess.ID, true, http.StatusOK)
	if v.State != hubstore.GrillWaiting || !v.AutoAccept {
		t.Fatalf("view = %+v, want waiting with auto-accept on", v)
	}
	if msgs, _ := stores.Grill().Messages(sid, 0); len(msgs) != 2 {
		t.Fatalf("messages = %d, want the question and the notice alone", len(msgs))
	}
}

// A parked session has no live child to hand the answer to, so the auto-accepted one
// must spawn a resume turn the way a typed answer does.
func TestGrillAutoAcceptResumesParkedSession(t *testing.T) {
	ts, stores, repo, srv := grillHookServer(t)
	spawned := make(chan hubstore.GrillSession, 2)
	srv.startGrill = func(_ context.Context, sess hubstore.GrillSession) { spawned <- sess }

	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	<-spawned
	poseGrillQuestion(t, stores, sid, `{"text":"ship it?","recommended":"yes"}`, hubstore.GrillParked)

	if v := postAutoAccept(t, ts, sess.ID, true, http.StatusOK); v.State != hubstore.GrillRunning {
		t.Fatalf("view = %+v, want running", v)
	}
	select {
	case resumed := <-spawned:
		if resumed.ID != sid || resumed.State != hubstore.GrillRunning {
			t.Fatalf("resumed session = %+v, want %d running", resumed, sid)
		}
	default:
		t.Fatal("auto-accepting a parked session spawned no resume turn")
	}
}

func TestGrillAutoAcceptSettled(t *testing.T) {
	ts, stores, repo := grillServer(t)
	cases := []struct {
		issue string
		path  []string
	}{
		{"COD-1", []string{hubstore.GrillFinished}},
		{"COD-2", []string{hubstore.GrillFinished, hubstore.GrillApplied}},
		{"COD-3", []string{hubstore.GrillAbandoned}},
	}
	for _, tc := range cases {
		state := tc.path[len(tc.path)-1]
		sess := createGrill(t, ts, repo, tc.issue)
		sid, _ := strconv.ParseInt(sess.ID, 10, 64)
		for _, next := range tc.path {
			if _, err := stores.Grill().Transition(sid, next, ""); err != nil {
				t.Fatalf("transition to %s: %v", next, err)
			}
		}

		postAutoAccept(t, ts, sess.ID, true, http.StatusConflict)
		if after, _, _ := stores.Grill().Session(sid); after.AutoAccept {
			t.Fatalf("%s switch persisted auto-accept", state)
		}
	}
}

// A row from before the model column was resolved at create shows the repo
// config's fallback chain — GRILL_MODEL over CLAUDE_MODEL — and stays empty when
// neither is set, which the panel renders as the Claude CLI default.
func TestGrillModelViewFallback(t *testing.T) {
	ts, stores, _ := grillServer(t)
	root := filepath.Join(os.Getenv("HOME"), "acme")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	sess, err := stores.Grill().Create(hubstore.NewGrillSession{Repo: root, IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create legacy session: %v", err)
	}

	fetch := func() GrillSessionView {
		t.Helper()
		_, body := get(t, ts, APIPrefix+"/grill/"+strconv.FormatInt(sess.ID, 10))
		var detail GrillDetailResponse
		if err := json.Unmarshal([]byte(body), &detail); err != nil {
			t.Fatalf("decode detail: %v", err)
		}
		return detail.Session
	}

	if got := fetch(); got.Model != "" || got.Provider != "claude" {
		t.Fatalf("unconfigured view = %+v, want empty model, provider claude", got)
	}

	cfgPath := config.ProjectConfigPath(root)
	if err := os.WriteFile(cfgPath, []byte("CLAUDE_MODEL=claude-model\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if got := fetch(); got.Model != "claude-model" {
		t.Fatalf("claude fallback model = %q, want claude-model", got.Model)
	}

	if err := os.WriteFile(cfgPath, []byte("GRILL_MODEL=grill-model\nCLAUDE_MODEL=claude-model\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if got := fetch(); got.Model != "grill-model" {
		t.Fatalf("grill fallback model = %q, want grill-model", got.Model)
	}

	// Posting the resolved fallback is a no-op: no notice, nothing persisted.
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+strconv.FormatInt(sess.ID, 10)+"/model", GrillModelRequest{Model: "grill-model"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("fallback no-op status = %d, want 200", res.StatusCode)
	}
	if msgs, _ := stores.Grill().Messages(sess.ID, 0); len(msgs) != 0 {
		t.Fatalf("fallback no-op appended %d messages, want 0", len(msgs))
	}
	if after, _, _ := stores.Grill().Session(sess.ID); after.Model != "" {
		t.Fatalf("fallback no-op persisted model %q", after.Model)
	}
}

func TestGrillStreamBackfillAndLive(t *testing.T) {
	ts, stores, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	if _, err := stores.Grill().Transition(sid, hubstore.GrillWaiting, ""); err != nil {
		t.Fatalf("pose question: %v", err)
	}

	r := openSSE(t, ts, APIPrefix+"/grill/"+sess.ID+"/stream", nil)

	event, data := readFrame(t, r)
	if event != "state" {
		t.Fatalf("first frame event = %q, want state", event)
	}
	var state GrillSessionView
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		t.Fatalf("decode state frame: %v", err)
	}
	if state.State != hubstore.GrillWaiting {
		t.Fatalf("state frame state = %q, want waiting", state.State)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "because"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d, want 200", res.StatusCode)
	}

	if event, _ := readFrame(t, r); event != "message" {
		t.Fatalf("live frame event = %q, want message", event)
	}
	if event, data := readFrame(t, r); event != "state" {
		t.Fatalf("live frame event = %q, want state (%s)", event, data)
	}
}

// A start-time model choice is what the session stores, so its very first turn spawns
// on it instead of the repo default.
func TestGrillCreateHonoursRequestedModel(t *testing.T) {
	ts, stores, repo := grillServer(t)
	writeGrillConfig(t, "GRILL_MODEL=grill-model\n")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1", Model: "opus"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if v.Model != "opus" {
		t.Fatalf("created model = %q, want the requested opus", v.Model)
	}
	sid, _ := strconv.ParseInt(v.ID, 10, 64)
	stored, _, err := stores.Grill().Session(sid)
	if err != nil {
		t.Fatalf("read back session: %v", err)
	}
	if stored.Model != "opus" {
		t.Fatalf("stored model = %q, want opus over the repo default", stored.Model)
	}
}

// The list resource carries what an interview started now would run on, so a start
// surface can offer the choice before any session exists.
func TestGrillListDefaults(t *testing.T) {
	ts, _, repo := grillServer(t)
	writeGrillConfig(t, "GRILL_MODEL=grill-model\n")

	_, body := get(t, ts, APIPrefix+"/repos/"+repo+"/grill")
	var list GrillListResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Defaults.Provider != "claude" {
		t.Fatalf("defaults provider = %q, want claude", list.Defaults.Provider)
	}
	if list.Defaults.Model != "grill-model" {
		t.Fatalf("defaults model = %q, want the repo default", list.Defaults.Model)
	}
	if len(list.Defaults.ModelOptions) == 0 {
		t.Fatal("defaults carry no model catalog")
	}
}

func TestGrillDefaultsUseConfiguredProvider(t *testing.T) {
	ts, stores, repo := grillServer(t)
	seedKimiConfig(t, "k3")
	writeGrillConfig(t, "GRILL_PROVIDER=kimi\nKIMI_MODEL=kimi-default\n")

	_, body := get(t, ts, APIPrefix+"/repos/"+repo+"/grill")
	var list GrillListResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Defaults.Provider != "kimi" {
		t.Fatalf("defaults provider = %q, want kimi", list.Defaults.Provider)
	}
	if list.Defaults.Model != "kimi-default" {
		t.Fatalf("defaults model = %q, want kimi-default", list.Defaults.Model)
	}
	if !contains(list.Defaults.ModelOptions, "k3") {
		t.Fatalf("defaults model options = %v, want the kimi catalog", list.Defaults.ModelOptions)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if v.Provider != "kimi" || v.Model != "kimi-default" {
		t.Fatalf("created session = %+v, want kimi/kimi-default", v)
	}
	sid, _ := strconv.ParseInt(v.ID, 10, 64)
	if stored, _, _ := stores.Grill().Session(sid); stored.Provider != "kimi" {
		t.Fatalf("stored provider = %q, want kimi", stored.Provider)
	}

	root := filepath.Join(os.Getenv("HOME"), "acme")
	if _, _, err := stores.Issues().Upsert(root, "linear", []hubstore.Issue{{
		Identifier:  "COD-2",
		Title:       "Pinned",
		StatusGroup: "unstarted",
	}}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, _, err := stores.Issues().SetProvider(root, "COD-2", "codex"); err != nil {
		t.Fatalf("pin issue provider: %v", err)
	}
	pinned := createGrill(t, ts, repo, "COD-2")
	if pinned.Provider != "kimi" {
		t.Fatalf("pinned issue grill provider = %q, want configured kimi", pinned.Provider)
	}
}

func TestGrillDefaultsIgnoreStaleConfiguredProvider(t *testing.T) {
	ts, stores, repo := grillServer(t)
	writeGrillConfig(t, "GRILL_PROVIDER=ollama\nGRILL_MODEL=grill-model\n")

	_, body := get(t, ts, APIPrefix+"/repos/"+repo+"/grill")
	var list GrillListResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Defaults.Provider != "claude" || list.Defaults.Model != "grill-model" {
		t.Fatalf("defaults = %+v, want claude/grill-model", list.Defaults)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if v.Provider != "claude" {
		t.Fatalf("created provider = %q, want claude", v.Provider)
	}
	sid, _ := strconv.ParseInt(v.ID, 10, 64)
	if stored, _, _ := stores.Grill().Session(sid); stored.Provider != "claude" {
		t.Fatalf("stored provider = %q, want claude", stored.Provider)
	}
}

func TestGrillCreateHonoursRequestedProvider(t *testing.T) {
	ts, stores, repo := grillServer(t)
	writeGrillConfig(t, "KIMI_MODEL=kimi-default\n")
	seedKimiConfig(t, "k3")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1", Provider: "kimi"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if v.Provider != "kimi" {
		t.Fatalf("created provider = %q, want kimi", v.Provider)
	}
	if v.Model != "kimi-default" {
		t.Fatalf("created model = %q, want the KIMI_MODEL default", v.Model)
	}
	if !contains(v.ModelOptions, "k3") {
		t.Fatalf("model options = %v, want the kimi catalog", v.ModelOptions)
	}
	sid, _ := strconv.ParseInt(v.ID, 10, 64)
	if stored, _, _ := stores.Grill().Session(sid); stored.Provider != "kimi" {
		t.Fatalf("stored provider = %q, want kimi", stored.Provider)
	}
}

func TestGrillCreateHonoursRequestedCodexProvider(t *testing.T) {
	ts, stores, repo := grillServer(t)
	writeGrillConfig(t, "CODEX_MODEL=codex-default\n")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1", Provider: "codex"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if v.Provider != "codex" {
		t.Fatalf("created provider = %q, want codex", v.Provider)
	}
	if v.Model != "codex-default" {
		t.Fatalf("created model = %q, want the CODEX_MODEL default", v.Model)
	}
	if !contains(v.ModelOptions, config.CodexDefaultModel) {
		t.Fatalf("model options = %v, want the codex catalog", v.ModelOptions)
	}
	sid, _ := strconv.ParseInt(v.ID, 10, 64)
	if stored, _, _ := stores.Grill().Session(sid); stored.Provider != "codex" {
		t.Fatalf("stored provider = %q, want codex", stored.Provider)
	}
}

func TestGrillCreateHonoursResearchMode(t *testing.T) {
	ts, stores, repo := grillServer(t)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1", Mode: hubstore.GrillModeResearch})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if v.Mode != hubstore.GrillModeResearch {
		t.Fatalf("created mode = %q, want research", v.Mode)
	}
	sid, _ := strconv.ParseInt(v.ID, 10, 64)
	if stored, _, _ := stores.Grill().Session(sid); stored.Mode != hubstore.GrillModeResearch {
		t.Fatalf("stored mode = %q, want research", stored.Mode)
	}
}

// A create that omits the field, and a session stored before the mode column
// existed, both read back as an interview.
func TestGrillModeDefaultsToInterview(t *testing.T) {
	ts, stores, repo := grillServer(t)

	sess := createGrill(t, ts, repo, "COD-1")
	if sess.Mode != hubstore.GrillModeInterview {
		t.Fatalf("created mode = %q, want interview", sess.Mode)
	}
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	if stored, _, _ := stores.Grill().Session(sid); stored.Mode != hubstore.GrillModeInterview {
		t.Fatalf("stored mode = %q, want interview", stored.Mode)
	}
	if got := grillEffectiveMode(""); got != hubstore.GrillModeInterview {
		t.Fatalf("legacy row mode = %q, want interview", got)
	}
}

func TestGrillCreateRejectsUnknownMode(t *testing.T) {
	ts, _, repo := grillServer(t)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1", Mode: "bogus"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400", res.StatusCode)
	}
}

func TestGrillCreateRejectsUnknownProvider(t *testing.T) {
	ts, _, repo := grillServer(t)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1", Provider: "bogus"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400", res.StatusCode)
	}
}

func TestGrillListDefaultsProviders(t *testing.T) {
	ts, _, repo := grillServer(t)
	seedKimiConfig(t, "k3")
	writeGrillConfig(t, "KIMI_BIN="+installedStub(t, "kimi")+"\nCODEX_BIN="+codexInstalledStub(t)+"\n")

	byName := grillDefaultProviders(t, ts, repo)
	claude, hasClaude := byName["claude"]
	codex, hasCodex := byName["codex"]
	kimi, hasKimi := byName["kimi"]
	if !hasClaude || !hasCodex || !hasKimi {
		t.Fatalf("defaults providers = %v, want claude, codex, and kimi", byName)
	}
	if len(claude) == 0 {
		t.Fatal("claude offered with no model catalog")
	}
	if !contains(codex, config.CodexDefaultModel) {
		t.Fatalf("codex catalog = %v, want the codex catalog", codex)
	}
	if !contains(kimi, "k3") {
		t.Fatalf("kimi catalog = %v, want the seeded alias", kimi)
	}
}

func TestGrillListDefaultsSkipsUninstalledProvider(t *testing.T) {
	ts, _, repo := grillServer(t)
	seedKimiConfig(t, "k3")
	writeGrillConfig(t, strings.Join([]string{
		"KIMI_BIN=" + filepath.Join(t.TempDir(), "no-such-kimi"),
		"CODEX_BIN=" + filepath.Join(t.TempDir(), "no-such-codex"),
		"",
	}, "\n"))

	byName := grillDefaultProviders(t, ts, repo)
	if _, offered := byName["kimi"]; offered {
		t.Errorf("kimi offered without an installed CLI: %v", byName)
	}
	if _, offered := byName["codex"]; offered {
		t.Errorf("codex offered without a capable CLI: %v", byName)
	}
	if _, offered := byName["claude"]; !offered {
		t.Errorf("the default provider must stay offered: %v", byName)
	}
}

func TestGrillResearchDisablesKimi(t *testing.T) {
	ts, _, repo := grillServer(t)
	seedKimiConfig(t, "k3")
	writeGrillConfig(t, "KIMI_BIN="+installedStub(t, "kimi")+"\nCODEX_BIN="+codexInstalledStub(t)+"\n")

	interview := grillDefaultProviderOptions(t, ts, repo, hubstore.GrillModeInterview)
	if kimi := interview["kimi"]; kimi.Disabled || kimi.Reason != "" {
		t.Fatalf("interview kimi = %+v, want it offered", kimi)
	}

	research := grillDefaultProviderOptions(t, ts, repo, hubstore.GrillModeResearch)
	kimi, offered := research["kimi"]
	if !offered {
		t.Fatalf("research providers = %v, want kimi listed disabled", research)
	}
	if !kimi.Disabled || kimi.Reason != "kimi has no web research support in trau yet" {
		t.Fatalf("research kimi = %+v, want it disabled with the reason", kimi)
	}
	for _, name := range []string{"claude", "codex"} {
		if p := research[name]; p.Disabled || p.Note == "" {
			t.Errorf("research %s = %+v, want it offered with a note", name, p)
		}
	}
}

func TestGrillCreateRejectsResearchOnKimi(t *testing.T) {
	ts, _, repo := grillServer(t)
	seedKimiConfig(t, "k3")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{
		IssueID:  "COD-1",
		Mode:     hubstore.GrillModeResearch,
		Provider: "kimi",
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400", res.StatusCode)
	}
	var detail struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&detail); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if detail.Error != "kimi has no web research support in trau yet" {
		t.Fatalf("error = %q, want the research reason", detail.Error)
	}
}

// A repo that grills on kimi still gets a research session — on claude, since kimi
// cannot run one and an unrequested provider must not open a session that only parks.
func TestGrillResearchFallsBackFromConfiguredKimi(t *testing.T) {
	ts, _, repo := grillServer(t)
	seedKimiConfig(t, "k3")
	writeGrillConfig(t, "GRILL_PROVIDER=kimi\nKIMI_BIN="+installedStub(t, "kimi")+"\n")

	_, body := get(t, ts, APIPrefix+"/repos/"+repo+"/grill?mode="+hubstore.GrillModeResearch)
	var list GrillListResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Defaults.Provider != "claude" {
		t.Fatalf("research defaults provider = %q, want claude", list.Defaults.Provider)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: "COD-1", Mode: hubstore.GrillModeResearch})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if v.Provider != "claude" {
		t.Fatalf("created provider = %q, want claude", v.Provider)
	}
}

func TestGrillListRejectsUnknownMode(t *testing.T) {
	ts, _, repo := grillServer(t)

	res, _ := get(t, ts, APIPrefix+"/repos/"+repo+"/grill?mode=bogus")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("list status = %d, want 400", res.StatusCode)
	}
}

// The Research page reads the list with mode=research; the Inbox reads it without one
// and must keep seeing every session, research drafts included.
func TestGrillListNarrowsToMode(t *testing.T) {
	ts, _, repo := grillServer(t)
	interview := createGrill(t, ts, repo, "COD-1")
	research := createGrillWith(t, ts, repo, GrillCreateRequest{
		IssueID: "COD-2",
		Mode:    hubstore.GrillModeResearch,
	})

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{query: "", want: []string{research.ID, interview.ID}},
		{query: "?mode=" + hubstore.GrillModeInterview, want: []string{interview.ID}},
		{query: "?mode=" + hubstore.GrillModeResearch, want: []string{research.ID}},
	} {
		_, body := get(t, ts, APIPrefix+"/repos/"+repo+"/grill"+tc.query)
		var list GrillListResponse
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			t.Fatalf("decode list%s: %v", tc.query, err)
		}
		got := make([]string, len(list.Sessions))
		for i, sess := range list.Sessions {
			got[i] = sess.ID
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("list%s = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func grillDefaultProviders(t *testing.T, ts *httptest.Server, repo string) map[string][]string {
	t.Helper()
	byName := map[string][]string{}
	for name, opt := range grillDefaultProviderOptions(t, ts, repo, "") {
		byName[name] = opt.ModelOptions
	}
	return byName
}

func grillDefaultProviderOptions(t *testing.T, ts *httptest.Server, repo, mode string) map[string]GrillProviderOption {
	t.Helper()
	_, body := get(t, ts, APIPrefix+"/repos/"+repo+"/grill?mode="+mode)
	var list GrillListResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	byName := map[string]GrillProviderOption{}
	for _, p := range list.Defaults.Providers {
		byName[p.Name] = p
	}
	return byName
}

func installedStub(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s stub: %v", name, err)
	}
	return path
}

func codexInstalledStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
if [ "$1" = "exec" ] && [ "$2" = "--help" ]; then
  printf 'Usage: codex exec [OPTIONS] [PROMPT]\nCommands:\n  resume\nOptions:\n  --json\n'
  exit 0
fi
if [ "$1" = "mcp" ] && [ "$2" = "add" ] && [ "$3" = "--help" ]; then
  printf 'Options:\n  --url <URL>\n'
  exit 0
fi
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write codex stub: %v", err)
	}
	return path
}

func seedKimiConfig(t *testing.T, aliases ...string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".kimi-code")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir kimi home: %v", err)
	}
	var b strings.Builder
	for _, a := range aliases {
		fmt.Fprintf(&b, "[models.%q]\nmodel = %q\n\n", a, a)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write kimi config: %v", err)
	}
}

// writeGrillConfig lays down the repo config grillServer's repo resolves against.
func writeGrillConfig(t *testing.T, body string) {
	t.Helper()
	root := filepath.Join(os.Getenv("HOME"), "acme")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
