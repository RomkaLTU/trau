package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// grillServer isolates HOME because session creation resolves the repo's grill
// model through the layered config, which reads ~/.trau.ini.
func grillServer(t *testing.T) (*httptest.Server, *hubstore.Stores, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	stores := testStoresAt(t, home)
	repo := registry.Repo{Name: "acme", Root: filepath.Join(home, "acme"), RunsDir: filepath.Join(home, "acme", ".trau", "runs")}
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}
	ts := httptest.NewServer(New("1.2.3", "127.0.0.1", "", nil, false, stores).Handler())
	t.Cleanup(ts.Close)
	return ts, stores, repo.Name
}

func createGrill(t *testing.T, ts *httptest.Server, repo, issue string) GrillSessionView {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: issue})
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
		state string
		want  bool
	}{
		{hubstore.GrillParked, true},
		{hubstore.GrillStalled, true},
		{hubstore.GrillFinished, true},
		{hubstore.GrillWaiting, false},
		{hubstore.GrillRunning, false},
	}
	for _, tt := range tests {
		if got := grillResumeSpawns(tt.state); got != tt.want {
			t.Errorf("grillResumeSpawns(%q) = %v, want %v", tt.state, got, tt.want)
		}
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
