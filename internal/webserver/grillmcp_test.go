package webserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
			"question": "Which page is in scope?",
			"options":  []string{"login", "signup"},
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
		if m.Role == hubstore.GrillRoleAgent && m.Kind == hubstore.GrillKindQuestion {
			gotQuestion = true
		}
	}
	if !gotQuestion {
		t.Fatalf("no question message stored: %+v", detail.Messages)
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
