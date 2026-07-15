package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

func grillServer(t *testing.T) (*httptest.Server, *hubstore.Stores, string) {
	t.Helper()
	home := t.TempDir()
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
