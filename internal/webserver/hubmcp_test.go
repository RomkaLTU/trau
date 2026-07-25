package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
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
	declared := []string{
		"list_repos", "create_ticket", "enqueue", "start_queue", "pause_queue", "queue_status",
		"list_backlog", "list_eligible", "get_epic", "list_runs", "get_run", "list_instances",
	}
	for _, name := range declared {
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
	readers := []string{
		"list_repos", "queue_status", "list_backlog", "list_eligible", "get_epic",
		"list_runs", "get_run", "list_instances",
	}
	for _, name := range readers {
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

// list_backlog answers the board the REST route serves, and honours the same
// state and label filters the board's controls send.
func TestHubMCPListBacklog(t *testing.T) {
	ts, _, _ := hubMCPServer(t)
	ready := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Ship the toggle"})
	chore := createMCPTicket(t, ts, map[string]any{
		"repo": "acme", "title": "Bump deps", "labels": []string{"chore"},
	})

	var all BacklogResponse
	hubToolPayload(t, hubTool(t, ts, "list_backlog", map[string]any{"repo": "acme"}), &all)
	if all.Repo != "acme" || all.Total != 2 {
		t.Fatalf("list_backlog = %+v, want acme's two issues", all)
	}

	res, body := get(t, ts, APIPrefix+"/repos/acme/backlog")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET backlog = %d: %s", res.StatusCode, body)
	}
	var rest BacklogResponse
	if err := json.Unmarshal([]byte(body), &rest); err != nil {
		t.Fatalf("decode REST backlog: %v", err)
	}
	if !reflect.DeepEqual(all, rest) {
		t.Errorf("list_backlog = %+v, want the REST backlog %+v", all, rest)
	}

	var labelled BacklogResponse
	hubToolPayload(t, hubTool(t, ts, "list_backlog", map[string]any{"repo": "acme", "label": "chore"}), &labelled)
	if len(labelled.Items) != 1 || labelled.Items[0].ID != chore.ID {
		t.Errorf("label filter = %+v, want only %s", labelled.Items, chore.ID)
	}

	var started BacklogResponse
	hubToolPayload(t, hubTool(t, ts, "list_backlog", map[string]any{"repo": "acme", "state": []string{"started"}}), &started)
	if len(started.Items) != 0 {
		t.Errorf("state filter = %+v, want nothing started", started.Items)
	}
	if started.Counts["backlog"] != 2 {
		t.Errorf("counts = %v, want the backlog group counted with the state filter ignored", started.Counts)
	}

	var paged BacklogResponse
	hubToolPayload(t, hubTool(t, ts, "list_backlog", map[string]any{"repo": "acme", "limit": 1, "offset": 1}), &paged)
	if len(paged.Items) != 1 || paged.Total != 2 {
		t.Errorf("paged = %+v, want one of two rows", paged)
	}
	if paged.Items[0].ID == ready.ID && all.Items[0].ID == ready.ID {
		t.Errorf("offset ignored: page = %+v, want the second row", paged.Items)
	}
}

// list_eligible reports what the picker would run next, decoded from the same
// --list-eligible capture the REST route reads.
func TestHubMCPListEligible(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	s.sup.(*fakeSupervisor).captureOut = []byte(
		`[{"id":"ACME-1","title":"Ship the toggle","labels":["ready-for-agent"],"parent":"","has_children":false}]`,
	)

	var eligible EligibleResult
	hubToolPayload(t, hubTool(t, ts, "list_eligible", map[string]any{"repo": "acme"}), &eligible)
	if eligible.Repo != "acme" || eligible.RepoRoot != root {
		t.Fatalf("list_eligible = %+v, want acme and its root", eligible)
	}
	if len(eligible.Tickets) != 1 || eligible.Tickets[0].ID != "ACME-1" {
		t.Fatalf("tickets = %+v, want the one eligible ticket", eligible.Tickets)
	}

	res, body := get(t, ts, APIPrefix+"/repos/acme/eligible")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET eligible = %d: %s", res.StatusCode, body)
	}
	var rest EligibleResult
	if err := json.Unmarshal([]byte(body), &rest); err != nil {
		t.Fatalf("decode REST eligible: %v", err)
	}
	if !reflect.DeepEqual(eligible, rest) {
		t.Errorf("list_eligible = %+v, want the REST listing %+v", eligible, rest)
	}
}

func TestHubMCPGetEpic(t *testing.T) {
	ts, s, _ := hubMCPServer(t)
	s.sup.(*fakeSupervisor).captureOut = []byte(
		`[{"id":"ACME-2","title":"Cart page","state":"todo"},{"id":"ACME-3","title":"Checkout page","state":"done"}]`,
	)

	var epic EpicPreviewResult
	hubToolPayload(t, hubTool(t, ts, "get_epic", map[string]any{"repo": "acme", "epic": "ACME-1"}), &epic)
	if epic.Epic != "ACME-1" || len(epic.SubIssues) != 2 {
		t.Fatalf("get_epic = %+v, want ACME-1 with two children", epic)
	}
	if epic.SubIssues[0].State != "todo" || epic.SubIssues[1].State != "done" {
		t.Errorf("sub-issues = %+v, want each child's state carried through", epic.SubIssues)
	}

	tr := hubTool(t, ts, "get_epic", map[string]any{"repo": "acme", "epic": "not-an-epic"})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "is not a valid ticket identifier") {
		t.Fatalf("malformed epic result = %+v, want a tool error naming the identifier", tr)
	}
}

// seedMCPRun stamps one settled run into the hub's stores the way a loop child
// posts it — checkpoint, token calls, a verdict artifact and an event tail — so the
// run tools read the rows the REST routes serve rather than a mock.
func seedMCPRun(t *testing.T, s *Server, root, ticket string, events int) {
	t.Helper()
	err := s.stores.Checkpoints().Upsert(root, ticket, map[string]string{
		"PHASE": state.PROpen, "TITLE": "wire the read tools", "UPDATED": "2026-07-25T10:00:00Z",
		"BRANCH": "feature/" + ticket, "PR": "42", "PR_URL": "https://github.com/acme/acme/pull/42",
	})
	if err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	calls := []hubstore.TokenCall{
		{Ticket: ticket, TS: "2026-07-25T10:00:00", Phase: "build", Input: 100, Output: 50, Total: 150, CostUSD: usd(0.10), Turns: 3},
		{Ticket: ticket, TS: "2026-07-25T10:05:00", Phase: "verify", Input: 200, Output: 60, Total: 260, CostUSD: usd(0.20), Turns: 2},
	}
	if err := s.stores.Tokens().Append(root, calls); err != nil {
		t.Fatalf("seed token calls: %v", err)
	}
	verdict := `{"pass":false,"summary":"one criterion missed","failures":["the tail is unbounded"]}`
	if err := s.stores.Artifacts().Upsert(root, ticket, hubstore.ArtifactVerdict, verdict); err != nil {
		t.Fatalf("seed verdict: %v", err)
	}
	if err := s.stores.Artifacts().Upsert(root, ticket, hubstore.ArtifactHandoff, strings.Repeat("brief ", 5000)); err != nil {
		t.Fatalf("seed handoff: %v", err)
	}
	news := make([]hubstore.NewEvent, events)
	for i := range news {
		news[i] = hubstore.NewEvent{
			TS:     "2026-07-25T10:00:00Z",
			Kind:   "phase_end",
			Phase:  "build",
			Msg:    fmt.Sprintf("step %d", i),
			Fields: `{"ticket":"` + ticket + `"}`,
		}
	}
	if _, err := s.stores.Events().Append(root, news); err != nil {
		t.Fatalf("seed events: %v", err)
	}
}

func TestHubMCPListRuns(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	seedMCPRun(t, s, root, "ACME-1", 1)
	seedMCPRun(t, s, root, "ACME-2", 1)

	var runs RunsResponse
	hubToolPayload(t, hubTool(t, ts, "list_runs", map[string]any{"repo": "acme"}), &runs)
	if !reflect.DeepEqual(runs, getRuns(t, ts, "acme")) {
		t.Errorf("list_runs = %+v, want the REST runs board", runs)
	}
	if len(runs.Runs) != 2 || runs.Runs[0].Phase != state.PROpen || runs.Runs[0].PR != "42" {
		t.Fatalf("runs = %+v, want both checkpoints with their PR reference", runs.Runs)
	}

	var capped RunsResponse
	hubToolPayload(t, hubTool(t, ts, "list_runs", map[string]any{"repo": "acme", "limit": 1}), &capped)
	if len(capped.Runs) != 1 || capped.Runs[0].Ticket != runs.Runs[0].Ticket {
		t.Errorf("limited runs = %+v, want the first board row alone", capped.Runs)
	}
}

// get_run carries what an agent needs to judge a settled run — phase, verdict,
// spend and a bounded event tail — without inlining the bulky artifacts.
func TestHubMCPGetRun(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	seedMCPRun(t, s, root, "ACME-1", maxMCPEventTail+40)

	var run MCPRun
	hubToolPayload(t, hubTool(t, ts, "get_run", map[string]any{"repo": "acme", "ticket": "ACME-1"}), &run)
	if run.Repo != "acme" || run.Ticket != "ACME-1" || run.Phase != state.PROpen {
		t.Fatalf("get_run = %+v, want acme's ACME-1 at its checkpoint phase", run)
	}
	if run.Total.Tokens != 410 || run.Total.CostUSD != 0.30 || !run.Total.Metered {
		t.Errorf("total = %+v, want the summed metered spend", run.Total)
	}
	if len(run.Costs) != 2 || run.Costs[0].Phase != "build" || run.Costs[1].Phase != "verify" {
		t.Errorf("costs = %+v, want the per-phase breakdown in first-seen order", run.Costs)
	}
	if run.Verdict == nil || run.Verdict.Pass || len(run.Verdict.Failures) != 1 {
		t.Errorf("verdict = %+v, want the failing verdict with its one failure", run.Verdict)
	}
	if !run.Artifacts.Handoff || !run.Artifacts.Verdict {
		t.Errorf("artifacts = %+v, want the stored handoff and verdict flagged present", run.Artifacts)
	}

	detail := getRunDetail(t, ts, "acme", "ACME-1")
	if !reflect.DeepEqual(run.RunView, detail.RunView) || !reflect.DeepEqual(run.Costs, detail.Costs) {
		t.Errorf("get_run = %+v, want the REST run detail's checkpoint and costs", run)
	}

	if len(run.Events) != mcpEventTail {
		t.Fatalf("event tail = %d events, want the default %d", len(run.Events), mcpEventTail)
	}
	if run.Events[len(run.Events)-1].Msg != fmt.Sprintf("step %d", maxMCPEventTail+39) {
		t.Errorf("tail ends at %q, want the newest event last", run.Events[len(run.Events)-1].Msg)
	}

	var greedy MCPRun
	hubToolPayload(t, hubTool(t, ts, "get_run", map[string]any{
		"repo": "acme", "ticket": "ACME-1", "events": maxMCPEventTail * 10,
	}), &greedy)
	if len(greedy.Events) != maxMCPEventTail {
		t.Errorf("event tail = %d events, want it clamped to %d", len(greedy.Events), maxMCPEventTail)
	}

	body, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal run: %v", err)
	}
	if strings.Contains(string(body), "brief brief") {
		t.Error("get_run inlined the handoff brief, want the payload bounded to its flag")
	}
}

func TestHubMCPListInstances(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	entry := registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateWorking,
		Ticket:       "ACME-1",
		Phase:        "build",
		Activity:     "editing files",
	}
	if err := s.stores.Instances().Upsert(entry); err != nil {
		t.Fatalf("upsert instance: %v", err)
	}

	var live struct {
		Instances []Instance `json:"instances"`
	}
	hubToolPayload(t, hubTool(t, ts, "list_instances", nil), &live)
	if len(live.Instances) != 1 {
		t.Fatalf("list_instances = %+v, want the one live loop", live.Instances)
	}
	inst := live.Instances[0]
	if inst.PID != os.Getpid() || inst.Repo != "acme" || inst.Ticket != "ACME-1" {
		t.Fatalf("instance = %+v, want this pid on acme's ACME-1", inst)
	}
	if inst.Phase != "build" || inst.SessionState != registry.StateWorking {
		t.Errorf("instance = %+v, want the reported phase and session state", inst)
	}
	if !reflect.DeepEqual(live.Instances, getInstances(t, ts).Instances) {
		t.Errorf("list_instances = %+v, want the REST instance rows", live.Instances)
	}
}

// An unknown repo or ticket is something the agent can fix by calling again, so
// every read tool answers with a tool error it can read rather than a protocol
// error that aborts the call.
func TestHubMCPReadToolsRejectUnknownTargets(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	seedMCPRun(t, s, root, "ACME-1", 1)

	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"list_backlog of an unknown repo", "list_backlog", map[string]any{"repo": "stranger"}, "unknown repo"},
		{"list_runs of an unknown repo", "list_runs", map[string]any{"repo": "stranger"}, "unknown repo"},
		{"get_run in an unknown repo", "get_run", map[string]any{"repo": "stranger", "ticket": "ACME-1"}, "unknown repo"},
		{"list_eligible in an observe-only repo", "list_eligible", map[string]any{"repo": "stranger"}, "observe-only"},
		{"get_epic in an observe-only repo", "get_epic", map[string]any{"repo": "stranger", "epic": "ACME-1"}, "observe-only"},
		{"get_run of a ticket that never ran", "get_run", map[string]any{"repo": "acme", "ticket": "ACME-999"}, "has never run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := mcpJSON(t, hubMCPURL(ts), toolCall(tc.tool, tc.args))
			if msg.Error != nil {
				t.Fatalf("%s = %+v, want a tool error, not a protocol error", tc.tool, msg.Error)
			}
			tr := toolResult(t, msg)
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
