package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// hubMCPServer builds a hub serving the general-purpose MCP endpoint with one
// Registered repo, "acme", and a fake supervisor standing in for the children a
// drain would spawn. Its tracker reader is stubbed unavailable so no tool call
// reaches the network. It returns the test server, the hub, and the repo root.
func hubMCPServer(t *testing.T) (*httptest.Server, *Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "acme")
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStoresAt(t, home))
	s.home = home
	s.newReader = func(config.Config) (tracker.Reader, error) { return nil, tracker.ErrReaderUnavailable }
	s.sup = &fakeSupervisor{}
	s.drain.repoLive = func(string) bool { return false }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.drainCtx = ctx
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, s, root
}

func hubMCPURL(ts *httptest.Server) string { return ts.URL + APIPrefix + "/mcp" }

func hubTool(t *testing.T, ts *httptest.Server, name string, args map[string]any) mcpToolResult {
	t.Helper()
	return toolResult(t, mcpJSON(t, hubMCPURL(ts), toolCall(name, args)))
}

// hubToolPayload decodes a successful tool result's JSON payload into v.
func hubToolPayload(t *testing.T, tr mcpToolResult, v any) {
	t.Helper()
	if tr.IsError {
		t.Fatalf("tool returned an error result: %+v", tr)
	}
	if len(tr.Content) != 1 {
		t.Fatalf("tool result content = %+v, want one text part", tr.Content)
	}
	if err := json.Unmarshal([]byte(tr.Content[0].Text), v); err != nil {
		t.Fatalf("decode tool payload: %v", err)
	}
}

func createMCPTicket(t *testing.T, ts *httptest.Server, args map[string]any) InternalIssueResponse {
	t.Helper()
	var ticket InternalIssueResponse
	hubToolPayload(t, hubTool(t, ts, "create_ticket", args), &ticket)
	return ticket
}

func TestHubMCPInitializeAndToolsList(t *testing.T) {
	ts, _, _ := hubMCPServer(t)

	init := mcpJSON(t, hubMCPURL(ts), map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": mcpProtocolVersion},
	})
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(init.Result, &initResult); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	if initResult.ServerInfo.Name != "trau" || initResult.ServerInfo.Version != "1.2.3" {
		t.Fatalf("serverInfo = %+v, want trau 1.2.3", initResult.ServerInfo)
	}
	if initResult.ProtocolVersion != mcpProtocolVersion {
		t.Errorf("protocolVersion = %q, want the version the client asked for", initResult.ProtocolVersion)
	}
	if _, ok := initResult.Capabilities["tools"]; !ok {
		t.Errorf("initialize missing tools capability: %+v", initResult.Capabilities)
	}

	if ping := mcpJSON(t, hubMCPURL(ts), map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"}); ping.Error != nil {
		t.Errorf("ping error = %+v, want an empty result", ping.Error)
	}

	list := mcpJSON(t, hubMCPURL(ts), map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"})
	var listResult struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &listResult); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	tools := map[string]mcpTool{}
	for _, tool := range listResult.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"list_repos", "create_ticket", "enqueue", "start_queue", "pause_queue", "queue_status"} {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tools/list = %v, want it to declare %s", listResult.Tools, name)
		}
		if tool.Description == "" {
			t.Errorf("%s carries no description", name)
		}
		var schema struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("decode %s input schema: %v", name, err)
		}
		if schema.Type != "object" {
			t.Errorf("%s input schema type = %q, want object", name, schema.Type)
		}
	}
	for _, name := range []string{"list_repos", "queue_status"} {
		if a := tools[name].Annotations; a == nil || !a.ReadOnlyHint {
			t.Errorf("%s annotations = %+v, want readOnlyHint", name, tools[name].Annotations)
		}
	}
	for _, name := range []string{"create_ticket", "enqueue", "start_queue", "pause_queue"} {
		if tools[name].Annotations != nil {
			t.Errorf("%s declares %+v, want no read-only hint on a writing tool", name, tools[name].Annotations)
		}
	}
}

// The headline flow: an MCP client lists the repos, files a ticket, queues it,
// and arms the drain, with every step landing in the same stores the web UI reads.
func TestHubMCPCreateEnqueueStartFlow(t *testing.T) {
	ts, s, root := hubMCPServer(t)

	var repos struct {
		Repos []MCPRepo `json:"repos"`
	}
	hubToolPayload(t, hubTool(t, ts, "list_repos", nil), &repos)
	idx := slices.IndexFunc(repos.Repos, func(r MCPRepo) bool { return r.Name == "acme" })
	if idx < 0 {
		t.Fatalf("list_repos = %+v, want the Registered acme repo", repos.Repos)
	}
	if repo := repos.Repos[idx]; repo.Path != root || !repo.CanDrain {
		t.Fatalf("acme = %+v, want its root path and can_drain true", repo)
	}

	ticket := createMCPTicket(t, ts, map[string]any{
		"repo":        "acme",
		"title":       "Add a dark-mode toggle",
		"description": "As a user I can toggle dark mode from settings.",
	})
	if ticket.ID == "" || ticket.State != "backlog" || ticket.Source != "internal" {
		t.Fatalf("created ticket = %+v, want an internal backlog issue with an identifier", ticket)
	}
	if !slices.Contains(ticket.Labels, "ready-for-agent") {
		t.Errorf("labels = %v, want the ready label so the ticket is eligible", ticket.Labels)
	}

	res, body := get(t, ts, APIPrefix+"/repos/acme/issues/internal/"+ticket.ID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("board read of %s = %d, want the ticket to exist: %s", ticket.ID, res.StatusCode, body)
	}

	var row MCPQueueRow
	hubToolPayload(t, hubTool(t, ts, "enqueue", map[string]any{"repo": "acme", "id": ticket.ID}), &row)
	if row.Repo != "acme" || row.Item.ID != ticket.ID || row.Item.Position != 1 {
		t.Fatalf("enqueue row = %+v, want %s first in acme's queue", row, ticket.ID)
	}
	if row.Item.Kind != string(queue.KindTicket) || row.Item.Title != "Add a dark-mode toggle" {
		t.Errorf("enqueue row = %+v, want a ticket carrying the stored title", row.Item)
	}

	var started MCPQueueStatus
	hubToolPayload(t, hubTool(t, ts, "start_queue", map[string]any{"repo": "acme", "on_fault": "skip"}), &started)
	if !started.Draining || len(started.Items) != 1 {
		t.Fatalf("start_queue = %+v, want the queue armed with its one row", started)
	}
	_, meta, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		t.Fatalf("snapshot queue: %v", err)
	}
	if !meta.Draining || meta.OnFault != queue.OnFaultSkip {
		t.Errorf("persisted meta = %+v, want draining with on_fault skip", meta)
	}
	s.drain.mu.Lock()
	_, active := s.drain.active[root]
	s.drain.mu.Unlock()
	if !active {
		t.Error("start_queue did not launch a drain loop for the repo")
	}
}

func TestHubMCPPauseQueueAndStatus(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	ticket := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Ship the toggle"})
	hubToolPayload(t, hubTool(t, ts, "enqueue", map[string]any{"repo": "acme", "id": ticket.ID, "front": true}), &MCPQueueRow{})

	var paused MCPQueueStatus
	hubToolPayload(t, hubTool(t, ts, "pause_queue", map[string]any{"repo": "acme"}), &paused)
	if paused.Draining {
		t.Errorf("pause_queue = %+v, want draining false", paused)
	}
	if _, meta, _ := s.stores.Queue(root).Snapshot(); meta.Draining {
		t.Error("draining flag not cleared after pause_queue")
	}

	var status MCPQueueStatus
	hubToolPayload(t, hubTool(t, ts, "queue_status", map[string]any{"repo": "acme"}), &status)
	if status.Repo != "acme" || len(status.Items) != 1 || status.Items[0].ID != ticket.ID {
		t.Fatalf("queue_status = %+v, want acme's single queued row", status)
	}
	if status.Draining || status.Current != "" || status.ChildLive {
		t.Errorf("queue_status = %+v, want an idle queue with no running item", status)
	}
}

// An id whose issue has children queues as an epic carrying them, the way the web
// queue path resolves kind from the store.
func TestHubMCPEnqueueEpicCarriesChildren(t *testing.T) {
	ts, _, _ := hubMCPServer(t)
	epic := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Checkout redesign"})
	child := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Cart page", "parent": epic.ID})

	var row MCPQueueRow
	hubToolPayload(t, hubTool(t, ts, "enqueue", map[string]any{"repo": "acme", "id": epic.ID}), &row)
	if row.Item.Kind != string(queue.KindEpic) {
		t.Fatalf("epic row kind = %q, want epic", row.Item.Kind)
	}
	if len(row.Item.SubIssues) != 1 || row.Item.SubIssues[0].ID != child.ID {
		t.Fatalf("epic sub-issues = %+v, want the stored child %s", row.Item.SubIssues, child.ID)
	}
}

func TestHubMCPRequeueConflicts(t *testing.T) {
	ts, _, _ := hubMCPServer(t)
	ticket := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Ship the toggle"})
	hubToolPayload(t, hubTool(t, ts, "enqueue", map[string]any{"repo": "acme", "id": ticket.ID}), &MCPQueueRow{})

	tr := hubTool(t, ts, "enqueue", map[string]any{"repo": "acme", "id": ticket.ID})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "already in the queue") {
		t.Fatalf("re-enqueue result = %+v, want a tool error naming the conflict", tr)
	}
}

func TestHubMCPObserveOnlyAndUnknownRepo(t *testing.T) {
	ts, _, _ := hubMCPServer(t)

	cases := []struct {
		name string
		tool string
		repo string
		want string
	}{
		{"start_queue on an observe-only repo", "start_queue", "stranger", "observe-only"},
		{"enqueue into an observe-only repo", "enqueue", "stranger", "observe-only"},
		{"create_ticket in an unknown repo", "create_ticket", "stranger", "unknown repo"},
		{"queue_status of an unknown repo", "queue_status", "stranger", "unknown repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := hubTool(t, ts, tc.tool, map[string]any{"repo": tc.repo, "id": "COD-1", "title": "x"})
			if !tr.IsError || !strings.Contains(tr.Content[0].Text, tc.want) {
				t.Fatalf("%s result = %+v, want a tool error mentioning %q", tc.tool, tr, tc.want)
			}
		})
	}
}

func TestHubMCPBadArguments(t *testing.T) {
	ts, _, _ := hubMCPServer(t)

	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"create_ticket without a title", "create_ticket", map[string]any{"repo": "acme"}, "title is required"},
		{"enqueue an unstored ticket", "enqueue", map[string]any{"repo": "acme", "id": "ACME-999"}, "is not in this repo's issue store"},
		{"enqueue a malformed id", "enqueue", map[string]any{"repo": "acme", "id": "not-a-ticket-id"}, "is not a valid ticket identifier"},
		{"start_queue with an unknown fault policy", "start_queue", map[string]any{"repo": "acme", "on_fault": "explode"}, `must be "halt" or "skip"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := hubTool(t, ts, tc.tool, tc.args)
			if !tr.IsError || !strings.Contains(tr.Content[0].Text, tc.want) {
				t.Fatalf("%s result = %+v, want a tool error mentioning %q", tc.tool, tr, tc.want)
			}
		})
	}
}

func TestHubMCPUnknownTool(t *testing.T) {
	ts, _, _ := hubMCPServer(t)
	msg := mcpJSON(t, hubMCPURL(ts), toolCall("delete_everything", map[string]any{"repo": "acme"}))
	if msg.Error == nil || !strings.Contains(msg.Error.Message, "unknown tool") {
		t.Fatalf("unknown tool response = %+v, want a JSON-RPC error naming it", msg)
	}
}

func TestHubMCPUnknownMethod(t *testing.T) {
	ts, _, _ := hubMCPServer(t)
	msg := mcpJSON(t, hubMCPURL(ts), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "resources/list"})
	if msg.Error == nil || msg.Error.Code != rpcMethodNotFound {
		t.Fatalf("unknown method response = %+v, want a method-not-found error", msg)
	}
}

func TestHubMCPRejectsNonPost(t *testing.T) {
	ts, _, _ := hubMCPServer(t)
	res, _ := get(t, ts, APIPrefix+"/mcp")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp = %d, want 405", res.StatusCode)
	}
}
