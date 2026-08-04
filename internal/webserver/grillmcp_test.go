package webserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

type rpcMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *jsonrpcError   `json:"error"`
}

func mcpURL(ts *httptest.Server, sid string) string {
	return ts.URL + APIPrefix + "/grill/" + sid + "/mcp"
}

func mcpJSON(t *testing.T, url string, body any) rpcMsg {
	t.Helper()
	res := postJSON(t, url, body)
	defer func() { _ = res.Body.Close() }()
	var msg rpcMsg
	if err := json.NewDecoder(res.Body).Decode(&msg); err != nil {
		t.Fatalf("decode rpc response: %v", err)
	}
	return msg
}

func toolResult(t *testing.T, msg rpcMsg) mcpToolResult {
	t.Helper()
	if msg.Error != nil {
		t.Fatalf("rpc error: %+v", msg.Error)
	}
	var tr mcpToolResult
	if err := json.Unmarshal(msg.Result, &tr); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	return tr
}

func toolCall(name string, args map[string]any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}
}

func doMCPPost(url string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return http.Post(url, "application/json", bytes.NewReader(buf))
}

// readSSEResult drains an ask_user SSE stream and returns the final JSON-RPC
// response, skipping keepalive comments and progress notifications.
func readSSEResult(res *http.Response) (rpcMsg, error) {
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var msg rpcMsg
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			continue
		}
		if len(msg.Result) > 0 || msg.Error != nil {
			return msg, nil
		}
	}
	if err := sc.Err(); err != nil {
		return rpcMsg{}, err
	}
	return rpcMsg{}, io.EOF
}

func waitForGrillState(t *testing.T, ts *httptest.Server, sid, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, body := get(t, ts, APIPrefix+"/grill/"+sid)
		var d GrillDetailResponse
		if err := json.Unmarshal([]byte(body), &d); err == nil && d.Session.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session %s did not reach state %q in time", sid, want)
}

func grillDetail(t *testing.T, ts *httptest.Server, sid string) GrillDetailResponse {
	t.Helper()
	_, body := get(t, ts, APIPrefix+"/grill/"+sid)
	var d GrillDetailResponse
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	return d
}

func TestGrillMCPInitializeAndToolsList(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	url := mcpURL(ts, sess.ID)

	init := mcpJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18"},
	})
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(init.Result, &initResult); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	if initResult.ProtocolVersion != "2025-06-18" || initResult.ServerInfo.Name != "trau-grill" {
		t.Fatalf("initialize result = %+v", initResult)
	}
	if _, ok := initResult.Capabilities["tools"]; !ok {
		t.Fatalf("initialize missing tools capability: %+v", initResult.Capabilities)
	}

	list := mcpJSON(t, url, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	var listResult struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &listResult); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range listResult.Tools {
		names[tool.Name] = true
	}
	if !names["ask_user"] || !names["finish_session"] {
		t.Fatalf("tools/list = %+v, want ask_user and finish_session", listResult.Tools)
	}
}

func TestGrillMCPAskUserRoundTrip(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	done := make(chan rpcMsg, 1)
	errc := make(chan error, 1)
	go func() {
		res, err := doMCPPost(mcpURL(ts, sess.ID), toolCall("ask_user", map[string]any{
			"question":    "Which page is in scope?",
			"options":     []string{"login", "signup"},
			"recommended": "login",
			"why":         "It is the only page in scope.",
		}))
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

	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)

	ans := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "Just the login page."})
	_ = ans.Body.Close()
	if ans.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d, want 200", ans.StatusCode)
	}

	select {
	case err := <-errc:
		t.Fatalf("ask_user call failed: %v", err)
	case msg := <-done:
		tr := toolResult(t, msg)
		if tr.IsError {
			t.Fatalf("ask_user returned an error result: %+v", tr)
		}
		if len(tr.Content) != 1 || tr.Content[0].Text != "Just the login page." {
			t.Fatalf("ask_user result = %+v, want the answer text", tr.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask_user did not return after the answer")
	}

	detail := grillDetail(t, ts, sess.ID)
	if detail.Session.State != hubstore.GrillRunning {
		t.Fatalf("session state = %q, want running after answer", detail.Session.State)
	}
	var gotQuestion bool
	for _, m := range detail.Messages {
		if m.Role != hubstore.GrillRoleAgent || m.Kind != hubstore.GrillKindQuestion {
			continue
		}
		gotQuestion = true
		var q struct {
			Recommended string `json:"recommended"`
			Why         string `json:"why"`
		}
		if err := json.Unmarshal(m.Payload, &q); err != nil {
			t.Fatalf("decode question payload: %v", err)
		}
		if q.Recommended != "login" || q.Why != "It is the only page in scope." {
			t.Fatalf("stored recommended/why = %q/%q, want login and the reason", q.Recommended, q.Why)
		}
	}
	if !gotQuestion {
		t.Fatalf("no question message stored: %+v", detail.Messages)
	}
}

// A provider whose MCP client abandons the blocking ask_user call retries it with
// the same question. The retry must re-attach to the pending question — one bubble
// in the transcript, one wait — and still return the answer when it lands.
func TestGrillMCPAskUserRetryReattaches(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	call := toolCall("ask_user", map[string]any{"question": "Which page is in scope?"})

	ctx, abandon := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL(ts, sess.ID), bytes.NewReader(mustJSON(t, call)))
	if err != nil {
		t.Fatalf("build ask_user request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	first, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ask_user call failed: %v", err)
	}
	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)
	abandon()
	_ = first.Body.Close()

	// The retry's response headers land once the handler has re-attached and opened
	// its stream, so the answer below cannot race ahead of the wait.
	retry, err := doMCPPost(mcpURL(ts, sess.ID), call)
	if err != nil {
		t.Fatalf("retried ask_user call failed: %v", err)
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

	ans := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "Just the login page."})
	_ = ans.Body.Close()
	if ans.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d, want 200", ans.StatusCode)
	}

	select {
	case err := <-errc:
		t.Fatalf("retried ask_user call failed: %v", err)
	case msg := <-done:
		tr := toolResult(t, msg)
		if tr.IsError || len(tr.Content) != 1 || tr.Content[0].Text != "Just the login page." {
			t.Fatalf("retried ask_user result = %+v, want the answer text", tr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retried ask_user did not return after the answer")
	}

	questions := 0
	for _, m := range grillDetail(t, ts, sess.ID).Messages {
		if m.Role == hubstore.GrillRoleAgent && m.Kind == hubstore.GrillKindQuestion {
			questions++
		}
	}
	if questions != 1 {
		t.Fatalf("stored %d question messages, want 1", questions)
	}
}

func TestGrillMCPAskUserParkSentinel(t *testing.T) {
	defer swapGrillTimers(50*time.Millisecond, 10*time.Second)()

	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	res, err := doMCPPost(mcpURL(ts, sess.ID), toolCall("ask_user", map[string]any{
		"question": "Anyone there?",
	}))
	if err != nil {
		t.Fatalf("ask_user post: %v", err)
	}
	msg, err := readSSEResult(res)
	if err != nil {
		t.Fatalf("read ask_user stream: %v", err)
	}
	tr := toolResult(t, msg)
	var structured struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(mustJSON(t, tr.StructuredContent), &structured); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	if structured.Status != "parked" {
		t.Fatalf("park sentinel status = %q, want parked (result %+v)", structured.Status, tr)
	}

	waitForGrillState(t, ts, sess.ID, hubstore.GrillParked)
}

// A session started with auto-accept answers a recommendation-bearing question itself,
// in either mode: the pair lands in the transcript with the answer flagged auto, the
// tool result comes straight back, and the user is never pulled in — no waiting state
// and no needs-you notification. A recommendation matching no offered option is still
// the answer, trimmed.
func TestGrillMCPAskUserAutoAcceptsRecommendation(t *testing.T) {
	for _, mode := range []string{hubstore.GrillModeInterview, hubstore.GrillModeResearch} {
		t.Run(mode, func(t *testing.T) {
			ts, stores, repo := grillServer(t)
			sess := createGrillWith(t, ts, repo, GrillCreateRequest{
				IssueID:    "COD-1",
				Mode:       mode,
				AutoAccept: true,
			})
			if !sess.AutoAccept {
				t.Fatalf("created session = %+v, want auto_accept on", sess)
			}

			tr := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), toolCall("ask_user", map[string]any{
				"question":    "Which page is in scope?",
				"options":     []string{"login", "signup"},
				"recommended": "login",
				"why":         "It is the only page in scope.",
			})))
			if tr.IsError || len(tr.Content) != 1 || tr.Content[0].Text != "login" {
				t.Fatalf("ask_user result = %+v, want the recommendation as the answer", tr)
			}

			detail := grillDetail(t, ts, sess.ID)
			if detail.Session.State != hubstore.GrillRunning || !detail.Session.AutoAccept {
				t.Fatalf("session = %+v, want running throughout with auto_accept persisted", detail.Session)
			}
			if len(detail.Messages) != 2 {
				t.Fatalf("stored %d messages, want the question and its auto answer: %+v", len(detail.Messages), detail.Messages)
			}
			question, answer := detail.Messages[0], detail.Messages[1]
			if question.Role != hubstore.GrillRoleAgent || question.Kind != hubstore.GrillKindQuestion {
				t.Errorf("first message = %s/%s, want the agent's question", question.Role, question.Kind)
			}
			var q struct {
				Text        string   `json:"text"`
				Options     []string `json:"options"`
				Recommended string   `json:"recommended"`
				Why         string   `json:"why"`
			}
			if err := json.Unmarshal(question.Payload, &q); err != nil {
				t.Fatalf("decode question payload: %v", err)
			}
			if q.Text != "Which page is in scope?" || !slices.Equal(q.Options, []string{"login", "signup"}) ||
				q.Recommended != "login" || q.Why != "It is the only page in scope." {
				t.Errorf("stored question = %+v, want the full payload the user would have seen", q)
			}
			if answer.Role != hubstore.GrillRoleUser || answer.Kind != hubstore.GrillKindAnswer {
				t.Errorf("second message = %s/%s, want the user's answer", answer.Role, answer.Kind)
			}
			var a struct {
				Text string `json:"text"`
				Auto bool   `json:"auto"`
			}
			if err := json.Unmarshal(answer.Payload, &a); err != nil {
				t.Fatalf("decode answer payload: %v", err)
			}
			if a.Text != "login" || !a.Auto {
				t.Errorf("stored answer = %+v, want the recommendation flagged auto", a)
			}

			items, err := stores.Notifications().List(10)
			if err != nil {
				t.Fatalf("list notifications: %v", err)
			}
			if len(items) != 0 {
				t.Errorf("stored %d notifications, want none for a question the user never saw", len(items))
			}

			offList := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), toolCall("ask_user", map[string]any{
				"question":    "How wide should the fix reach?",
				"options":     []string{"login", "signup"},
				"recommended": "  both, plus the reset page  ",
			})))
			if offList.IsError || len(offList.Content) != 1 || offList.Content[0].Text != "both, plus the reset page" {
				t.Fatalf("ask_user result = %+v, want the off-list recommendation trimmed and accepted", offList)
			}
		})
	}
}

// Abandoning a session does not cancel the child, so the ask_user call in flight is
// how the agent is told to stop. Auto-accept must not answer over that: an ended
// session gets the stop sentinel, never its own recommendation.
func TestGrillMCPAskUserAutoAcceptStopsOnEndedSession(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})

	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/abandon", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("abandon status = %d, want 200", res.StatusCode)
	}

	tr := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), toolCall("ask_user", map[string]any{
		"question":    "Which page is in scope?",
		"recommended": "login",
	})))
	var structured struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(mustJSON(t, tr.StructuredContent), &structured); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	if structured.Status != "ended" {
		t.Fatalf("ask_user result = %+v, want the ended sentinel", tr)
	}
	for _, m := range grillDetail(t, ts, sess.ID).Messages {
		if m.Role == hubstore.GrillRoleUser && m.Kind == hubstore.GrillKindAnswer {
			t.Fatalf("auto-answered an ended session: %+v", m)
		}
	}
}

// Auto-accept reaches only the questions carrying a recommendation, and only the
// sessions that asked for it: everything else still poses the question and blocks.
func TestGrillMCPAskUserWaitsWithoutAutoAcceptedRecommendation(t *testing.T) {
	cases := []struct {
		name string
		req  GrillCreateRequest
		args map[string]any
	}{
		{
			name: "auto-accept without a recommendation",
			req:  GrillCreateRequest{IssueID: "COD-1", AutoAccept: true},
			args: map[string]any{"question": "Which page is in scope?", "options": []string{"login", "signup"}},
		},
		{
			name: "recommendation without auto-accept",
			req:  GrillCreateRequest{IssueID: "COD-1"},
			args: map[string]any{"question": "Which page is in scope?", "recommended": "login"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _, repo := grillServer(t)
			sess := createGrillWith(t, ts, repo, tc.req)

			done := make(chan rpcMsg, 1)
			errc := make(chan error, 1)
			go func() {
				res, err := doMCPPost(mcpURL(ts, sess.ID), toolCall("ask_user", tc.args))
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

			waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)

			ans := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "Just the login page."})
			_ = ans.Body.Close()
			if ans.StatusCode != http.StatusOK {
				t.Fatalf("answer status = %d, want 200", ans.StatusCode)
			}

			select {
			case err := <-errc:
				t.Fatalf("ask_user call failed: %v", err)
			case msg := <-done:
				tr := toolResult(t, msg)
				if tr.IsError || len(tr.Content) != 1 || tr.Content[0].Text != "Just the login page." {
					t.Fatalf("ask_user result = %+v, want the user's own answer", tr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ask_user did not return after the answer")
			}
		})
	}
}

// An Ask-ahead turn is a whole interview, not one parked question: its session
// auto-accepts, so a recommended question is answered before the pre-grill park check
// ever sees it and the turn carries on without pulling the user in.
func TestGrillMCPAskUserPregrillAutoAnswersRecommendation(t *testing.T) {
	ts, stores, repo, srv := grillHookServer(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	srv.markPregrill(sid)

	tr := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), toolCall("ask_user", map[string]any{
		"question":    "Which page is in scope?",
		"options":     []string{"login", "signup"},
		"recommended": "login",
	})))
	if tr.IsError || len(tr.Content) != 1 || tr.Content[0].Text != "login" {
		t.Fatalf("ask_user result = %+v, want the recommendation as the answer", tr)
	}

	detail := grillDetail(t, ts, sess.ID)
	if detail.Session.State != hubstore.GrillRunning {
		t.Fatalf("session = %+v, want the turn still running rather than parked", detail.Session)
	}
	if len(detail.Messages) != 2 {
		t.Fatalf("stored %d messages, want the question and its auto answer: %+v", len(detail.Messages), detail.Messages)
	}
	var a struct {
		Text string `json:"text"`
		Auto bool   `json:"auto"`
	}
	if err := json.Unmarshal(detail.Messages[1].Payload, &a); err != nil {
		t.Fatalf("decode answer payload: %v", err)
	}
	if a.Text != "login" || !a.Auto {
		t.Errorf("stored answer = %+v, want the recommendation flagged auto", a)
	}

	items, err := stores.Notifications().List(10)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("stored %d notifications, want none for a question the user never saw", len(items))
	}
}

// A pre-grill question carrying no recommendation needs the user's taste, and nobody
// is there to give it: the session parks at once, still auto-accepting for the live
// session that resumes it.
func TestGrillMCPAskUserPregrillParksTasteQuestion(t *testing.T) {
	ts, _, repo, srv := grillHookServer(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{IssueID: "COD-1", AutoAccept: true})
	sid, _ := strconv.ParseInt(sess.ID, 10, 64)
	srv.markPregrill(sid)

	tr := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), toolCall("ask_user", map[string]any{
		"question": "How playful should the empty-state copy read?",
		"options":  []string{"playful", "plain"},
	})))
	var structured struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(mustJSON(t, tr.StructuredContent), &structured); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	if structured.Status != "parked" {
		t.Fatalf("ask_user result = %+v, want the park sentinel", tr)
	}
	if detail := grillDetail(t, ts, sess.ID); detail.Session.State != hubstore.GrillParked || !detail.Session.AutoAccept {
		t.Fatalf("session = %+v, want parked with auto-accept still on", detail.Session)
	}
}

func TestGrillMCPFinishSessionValidation(t *testing.T) {
	ts, _, repo := grillServer(t)

	cases := []struct {
		name    string
		args    map[string]any
		isError bool
	}{
		{"rewrite without description", map[string]any{"disposition": "rewrite", "summary": "s"}, true},
		{"unknown disposition", map[string]any{"disposition": "bogus", "summary": "s"}, true},
		{"missing summary", map[string]any{"disposition": "no_change"}, true},
		{"valid no_change", map[string]any{"disposition": "no_change", "summary": "already clear"}, false},
		{
			"split without description",
			map[string]any{"disposition": "split", "sub_issues": []any{map[string]any{"title": "A", "description": "da"}}, "summary": "s"},
			true,
		},
		{
			"split without sub_issues",
			map[string]any{"disposition": "split", "proposed_description": "epic", "summary": "s"},
			true,
		},
		{
			"split sub_issue missing description",
			map[string]any{"disposition": "split", "proposed_description": "epic", "sub_issues": []any{map[string]any{"title": "A"}}, "summary": "s"},
			true,
		},
		{
			"split out-of-range dep",
			map[string]any{"disposition": "split", "proposed_description": "epic", "sub_issues": []any{map[string]any{"title": "A", "description": "da", "blocked_by": []any{5}}}, "summary": "s"},
			true,
		},
		{
			"split self dep",
			map[string]any{"disposition": "split", "proposed_description": "epic", "sub_issues": []any{map[string]any{"title": "A", "description": "da", "blocked_by": []any{0}}}, "summary": "s"},
			true,
		},
		{
			"valid split",
			map[string]any{
				"disposition":          "split",
				"proposed_description": "epic",
				"sub_issues": []any{
					map[string]any{"title": "A", "description": "da"},
					map[string]any{"title": "B", "description": "db", "blocked_by": []any{0}},
				},
				"summary": "s",
			},
			false,
		},
		{"research without findings", map[string]any{"disposition": "research", "title": "Retry policy", "summary": "s"}, true},
		{"research with blank findings", map[string]any{"disposition": "research", "title": "Retry policy", "findings": "   ", "summary": "s"}, true},
		{"research without title", map[string]any{"disposition": "research", "findings": "## Question\n\nWhich API?", "summary": "s"}, true},
		{
			"valid research",
			map[string]any{"disposition": "research", "title": "Retry policy", "findings": "## Question\n\nWhich API?", "summary": "s"},
			false,
		},
		{
			"research source without a title",
			map[string]any{
				"disposition": "research", "title": "Retry policy", "findings": "## Question\n\nWhich API?", "summary": "s",
				"sources": []any{map[string]any{"url": "https://sdk.example/retries"}},
			},
			true,
		},
		{
			"research source without an http url",
			map[string]any{
				"disposition": "research", "title": "Retry policy", "findings": "## Question\n\nWhich API?", "summary": "s",
				"sources": []any{map[string]any{"title": "Retry docs", "url": "sdk.example/retries"}},
			},
			true,
		},
		{
			"valid research with sources",
			map[string]any{
				"disposition": "research", "title": "Retry policy", "findings": "## Question\n\nWhich API?", "summary": "s",
				"sources": []any{map[string]any{"title": "Retry docs", "url": "https://sdk.example/retries", "note": "the backoff table"}},
			},
			false,
		},
		{"create without title", map[string]any{"disposition": "create", "proposed_description": "body", "summary": "s"}, true},
		{"create without description", map[string]any{"disposition": "create", "title": "New feature", "summary": "s"}, true},
		{"valid create single", map[string]any{"disposition": "create", "title": "New feature", "proposed_description": "body", "summary": "s"}, false},
		{
			"create epic bad sub_issue",
			map[string]any{"disposition": "create", "title": "New epic", "proposed_description": "epic", "sub_issues": []any{map[string]any{"title": "A"}}, "summary": "s"},
			true,
		},
		{
			"valid create epic",
			map[string]any{
				"disposition":          "create",
				"title":                "New epic",
				"proposed_description": "epic",
				"sub_issues": []any{
					map[string]any{"title": "A", "description": "da"},
					map[string]any{"title": "B", "description": "db", "blocked_by": []any{0}},
				},
				"summary": "s",
			},
			false,
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := createGrill(t, ts, repo, "COD-"+string(rune('A'+i)))
			msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", tc.args))
			tr := toolResult(t, msg)
			if tr.IsError != tc.isError {
				t.Fatalf("isError = %v, want %v (result %+v)", tr.IsError, tc.isError, tr)
			}
			if tc.isError {
				return
			}
			detail := grillDetail(t, ts, sess.ID)
			if detail.Session.State != hubstore.GrillFinished {
				t.Fatalf("session state = %q, want finished", detail.Session.State)
			}
			last := detail.Messages[len(detail.Messages)-1]
			if last.Kind != hubstore.GrillKindOutcome {
				t.Fatalf("last message kind = %q, want outcome", last.Kind)
			}
		})
	}
}

func TestGrillMCPRewriteFinish(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition":          "rewrite",
		"proposed_description": "As a user I can reset my password from the login page.",
		"summary":              "Clarified the reset flow.",
	}))
	if tr := toolResult(t, msg); tr.IsError {
		t.Fatalf("valid rewrite returned an error: %+v", tr)
	}

	detail := grillDetail(t, ts, sess.ID)
	last := detail.Messages[len(detail.Messages)-1]
	var outcome struct {
		Disposition         string `json:"disposition"`
		ProposedDescription string `json:"proposed_description"`
	}
	if err := json.Unmarshal(last.Payload, &outcome); err != nil {
		t.Fatalf("decode outcome payload: %v", err)
	}
	if outcome.Disposition != "rewrite" || outcome.ProposedDescription == "" {
		t.Fatalf("outcome payload = %+v", outcome)
	}
}

func TestGrillMCPSplitFinish(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition":          "split",
		"proposed_description": "Epic: deliver the checkout redesign.",
		"sub_issues": []any{
			map[string]any{"title": "Cart page", "description": "Rebuild the cart page."},
			map[string]any{"title": "Payment step", "description": "Wire the payment step.", "blocked_by": []any{0}, "labels": []any{"ready-for-agent", "frontend"}},
		},
		"summary": "Sliced the redesign into two.",
	}))
	if tr := toolResult(t, msg); tr.IsError {
		t.Fatalf("valid split returned an error: %+v", tr)
	}

	detail := grillDetail(t, ts, sess.ID)
	last := detail.Messages[len(detail.Messages)-1]
	var outcome struct {
		Disposition string `json:"disposition"`
		SubIssues   []struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Labels      []string `json:"labels"`
			BlockedBy   []int    `json:"blocked_by"`
		} `json:"sub_issues"`
	}
	if err := json.Unmarshal(last.Payload, &outcome); err != nil {
		t.Fatalf("decode outcome payload: %v", err)
	}
	if outcome.Disposition != "split" || len(outcome.SubIssues) != 2 {
		t.Fatalf("outcome = %+v, want split with 2 sub-issues", outcome)
	}
	if outcome.SubIssues[1].Title != "Payment step" || len(outcome.SubIssues[1].BlockedBy) != 1 || outcome.SubIssues[1].BlockedBy[0] != 0 {
		t.Fatalf("second sub-issue = %+v, want blocked_by [0]", outcome.SubIssues[1])
	}
	if !slices.Contains(outcome.SubIssues[1].Labels, "frontend") {
		t.Fatalf("second sub-issue labels = %v, want its proposed labels", outcome.SubIssues[1].Labels)
	}
}

func TestGrillMCPCreateFinish(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "")

	msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition":          "create",
		"title":                "Add a dark-mode toggle",
		"proposed_description": "As a user I can toggle dark mode from settings.",
		"labels":               []any{"ready-for-agent", "frontend"},
		"summary":              "Specced the toggle.",
	}))
	if tr := toolResult(t, msg); tr.IsError {
		t.Fatalf("valid create returned an error: %+v", tr)
	}

	detail := grillDetail(t, ts, sess.ID)
	if detail.Session.State != hubstore.GrillFinished {
		t.Fatalf("session state = %q, want finished", detail.Session.State)
	}
	last := detail.Messages[len(detail.Messages)-1]
	var outcome struct {
		Disposition         string   `json:"disposition"`
		Title               string   `json:"title"`
		ProposedDescription string   `json:"proposed_description"`
		Labels              []string `json:"labels"`
	}
	if err := json.Unmarshal(last.Payload, &outcome); err != nil {
		t.Fatalf("decode outcome payload: %v", err)
	}
	if outcome.Disposition != "create" || outcome.Title != "Add a dark-mode toggle" || outcome.ProposedDescription == "" {
		t.Fatalf("outcome = %+v, want a create with title and description", outcome)
	}
	if !slices.Contains(outcome.Labels, "frontend") {
		t.Fatalf("outcome labels = %v, want the proposed labels", outcome.Labels)
	}
}

func TestGrillMCPResearchFinish(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")
	report := "## Question\n\nWhich retry policy does the SDK use?\n\n## Conclusion\n\nExponential backoff."

	msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition": "research",
		"title":       "SDK retry policy",
		"findings":    report,
		"summary":     "Answered the retry question.",
	}))
	if tr := toolResult(t, msg); tr.IsError {
		t.Fatalf("valid research returned an error: %+v", tr)
	}

	detail := grillDetail(t, ts, sess.ID)
	if detail.Session.State != hubstore.GrillFinished {
		t.Fatalf("session state = %q, want finished", detail.Session.State)
	}
	if detail.Session.ReportTitle != "SDK retry policy" {
		t.Fatalf("session report title = %q, want the outcome's own title", detail.Session.ReportTitle)
	}
	last := detail.Messages[len(detail.Messages)-1]
	var outcome grillOutcome
	if err := json.Unmarshal(last.Payload, &outcome); err != nil {
		t.Fatalf("decode outcome payload: %v", err)
	}
	if outcome.Disposition != grillDispResearch || outcome.Findings != report {
		t.Fatalf("outcome = %+v, want the research report round-tripped", outcome)
	}
	if outcome.ProposedDescription != "" {
		t.Fatalf("outcome carries a proposed_description %q, want none", outcome.ProposedDescription)
	}
}

func TestGrillMCPResearchSourcesRoundTrip(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition": "research",
		"title":       "SDK retry policy",
		"findings":    "## Conclusion\n\nExponential backoff.",
		"summary":     "Answered the retry question.",
		"sources": []any{
			map[string]any{"title": "  Retry docs  ", "url": " https://sdk.example/retries ", "note": " the backoff table "},
			map[string]any{"title": "Retry docs again", "url": "https://sdk.example/retries"},
			map[string]any{"title": "Release notes", "url": "http://sdk.example/changelog"},
		},
	}))
	if tr := toolResult(t, msg); tr.IsError {
		t.Fatalf("research with sources returned an error: %+v", tr)
	}

	detail := grillDetail(t, ts, sess.ID)
	last := detail.Messages[len(detail.Messages)-1]
	var outcome grillOutcome
	if err := json.Unmarshal(last.Payload, &outcome); err != nil {
		t.Fatalf("decode outcome payload: %v", err)
	}
	want := []grillSource{
		{Title: "Retry docs", URL: "https://sdk.example/retries", Note: "the backoff table"},
		{Title: "Release notes", URL: "http://sdk.example/changelog"},
	}
	if !slices.Equal(outcome.Sources, want) {
		t.Fatalf("sources = %+v, want %+v — trimmed and deduped by url", outcome.Sources, want)
	}
}

func TestGrillMCPResearchWithoutSourcesStoresNone(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition": "research",
		"title":       "How the picker resolves repos",
		"findings":    "## Conclusion\n\nIt reads the known set first.",
		"summary":     "Answered from the repository alone.",
	}))
	if tr := toolResult(t, msg); tr.IsError {
		t.Fatalf("research without sources returned an error: %+v", tr)
	}

	detail := grillDetail(t, ts, sess.ID)
	if detail.Session.State != hubstore.GrillFinished {
		t.Fatalf("session state = %q, want finished", detail.Session.State)
	}
	last := detail.Messages[len(detail.Messages)-1]
	if strings.Contains(string(last.Payload), "sources") {
		t.Fatalf("outcome payload = %s, want no sources field at all", last.Payload)
	}
}

func TestGrillMCPResearchWithoutFindingsExplainsItself(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition": "research",
		"title":       "Retry policy",
		"summary":     "s",
	}))
	tr := toolResult(t, msg)
	if !tr.IsError || len(tr.Content) != 1 || !strings.Contains(tr.Content[0].Text, "findings") {
		t.Fatalf("result = %+v, want a tool error naming findings", tr)
	}
	if detail := grillDetail(t, ts, sess.ID); detail.Session.State == hubstore.GrillFinished {
		t.Fatalf("session finished on a rejected research outcome: %+v", detail.Session)
	}
}

func TestGrillMCPResearchWithoutTitleExplainsItself(t *testing.T) {
	ts, _, repo := grillServer(t)
	sess := createGrill(t, ts, repo, "COD-1")

	msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition": "research",
		"findings":    "## Conclusion\n\nExponential backoff.",
		"summary":     "s",
	}))
	tr := toolResult(t, msg)
	if !tr.IsError || len(tr.Content) != 1 || !strings.Contains(tr.Content[0].Text, "title") {
		t.Fatalf("result = %+v, want a tool error naming title", tr)
	}
	if detail := grillDetail(t, ts, sess.ID); detail.Session.State == hubstore.GrillFinished {
		t.Fatalf("session finished on a titleless research outcome: %+v", detail.Session)
	}
}

func TestGrillMCPUnknownSession(t *testing.T) {
	ts, _, _ := grillServer(t)
	res := postJSON(t, mcpURL(ts, "999999"), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", res.StatusCode)
	}
}

func swapGrillTimers(idle, keepalive time.Duration) func() {
	prevIdle, prevKeepalive := grillAskIdleTimeout, grillAskKeepalive
	grillAskIdleTimeout, grillAskKeepalive = idle, keepalive
	return func() { grillAskIdleTimeout, grillAskKeepalive = prevIdle, prevKeepalive }
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
