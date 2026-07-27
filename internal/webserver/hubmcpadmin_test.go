package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/proc"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// hubMCPAdminServer builds the hub's MCP endpoint with the stop-and-wait timings
// compressed, so a shutdown_queue test never sleeps for real seconds.
func hubMCPAdminServer(t *testing.T) (*httptest.Server, *Server, string) {
	t.Helper()
	ts, s, root := hubMCPServer(t)

	prevPoll, prevConfirm := stopWaitPoll, stopKillConfirm
	stopWaitPoll, stopKillConfirm = 5*time.Millisecond, time.Second
	t.Cleanup(func() { stopWaitPoll, stopKillConfirm = prevPoll, prevConfirm })

	prevKill, prevRace := shutdownKillGrace, shutdownRaceWindow
	shutdownKillGrace, shutdownRaceWindow = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { shutdownKillGrace, shutdownRaceWindow = prevKill, prevRace })

	return ts, s, root
}

func queueIDs(items []QueueItemView) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

func addQueued(t *testing.T, s *Server, root string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := s.stores.Queue(root).Add(queue.Item{Kind: queue.KindTicket, ID: id}); err != nil {
			t.Fatalf("queue %s: %v", id, err)
		}
	}
}

// hubMCPToolList indexes the endpoint's declared tools by name.
func hubMCPToolList(t *testing.T, ts *httptest.Server) map[string]mcpTool {
	t.Helper()
	list := mcpJSON(t, hubMCPURL(ts), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
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
	return tools
}

// schemaEnum reads the values a tool's input schema advertises for one field.
func schemaEnum(t *testing.T, tool mcpTool, field string) []string {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("decode %s input schema: %v", tool.Name, err)
	}
	return schema.Properties[field].Enum
}

// Every tool in the destructive slice declares destructiveHint, so a client can
// tell them apart from the reads and writes already on the endpoint.
func TestHubMCPAdminToolsDeclareDestructiveHint(t *testing.T) {
	ts, _, _ := hubMCPServer(t)

	tools := hubMCPToolList(t, ts)
	destructive := []string{
		"dequeue", "move_queue_item", "shutdown_queue", "update_ticket", "transition_ticket",
		"delete_ticket", "reset_run", "clear_run", "stop_instance", "restart_hub",
	}
	for _, name := range destructive {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tools/list did not declare %s", name)
		}
		if tool.Annotations == nil || !tool.Annotations.DestructiveHint || tool.Annotations.ReadOnlyHint {
			t.Errorf("%s annotations = %+v, want destructiveHint and no read-only hint", name, tool.Annotations)
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
		if len(tool.Description) < 80 {
			t.Errorf("%s description = %q, want it to state the consequence", name, tool.Description)
		}
	}
}

func TestHubMCPDequeue(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	addQueued(t, s, root, "ACME-1", "ACME-2")

	var left MCPQueueStatus
	hubToolPayload(t, hubTool(t, ts, "dequeue", map[string]any{"repo": "acme", "id": "ACME-1"}), &left)
	if got := queueIDs(left.Items); !slices.Equal(got, []string{"ACME-2"}) {
		t.Fatalf("queue after dequeue = %v, want only ACME-2", got)
	}
	items, _, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		t.Fatalf("snapshot queue: %v", err)
	}
	if len(items) != 1 || items[0].ID != "ACME-2" {
		t.Errorf("stored queue = %+v, want the row gone from the store too", items)
	}

	tr := hubTool(t, ts, "dequeue", map[string]any{"repo": "acme", "id": "ACME-9"})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "is not in the queue") {
		t.Fatalf("dequeue of an unqueued id = %+v, want a tool error naming it", tr)
	}
}

// A dequeue is a queue-row removal by design: the running item is refused rather
// than removed, so its child is never orphaned.
func TestHubMCPDequeueRefusesRunningItem(t *testing.T) {
	ts, s, root := hubMCPAdminServer(t)
	addQueued(t, s, root, "ACME-1")
	if err := s.stores.Queue(root).MarkRunning("ACME-1", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	tr := hubTool(t, ts, "dequeue", map[string]any{"repo": "acme", "id": "ACME-1"})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "is running and cannot be removed") {
		t.Fatalf("dequeue of the running item = %+v, want a tool error", tr)
	}
	if items, _, _ := s.stores.Queue(root).Snapshot(); len(items) != 1 {
		t.Errorf("stored queue = %+v, want the running row untouched", items)
	}
	if fake := s.sup.(*fakeSupervisor); len(fake.stops) != 0 || len(fake.kills) != 0 {
		t.Errorf("dequeue signalled the child: stops=%v kills=%v", fake.stops, fake.kills)
	}
}

func TestHubMCPMoveQueueItem(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	addQueued(t, s, root, "ACME-1", "ACME-2", "ACME-3")

	var moved MCPQueueStatus
	hubToolPayload(t, hubTool(t, ts, "move_queue_item", map[string]any{
		"repo": "acme", "id": "ACME-3", "direction": "up",
	}), &moved)
	if got := queueIDs(moved.Items); !slices.Equal(got, []string{"ACME-1", "ACME-3", "ACME-2"}) {
		t.Fatalf("queue after move up = %v, want ACME-3 in second place", got)
	}

	var back MCPQueueStatus
	hubToolPayload(t, hubTool(t, ts, "move_queue_item", map[string]any{
		"repo": "acme", "id": "ACME-3", "direction": "down",
	}), &back)
	if got := queueIDs(back.Items); !slices.Equal(got, []string{"ACME-1", "ACME-2", "ACME-3"}) {
		t.Fatalf("queue after move down = %v, want ACME-3 back at the end", got)
	}
	items, _, _ := s.stores.Queue(root).Snapshot()
	if got := len(items); got != 3 {
		t.Errorf("stored queue = %d rows, want the three it started with", got)
	}

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown id", map[string]any{"repo": "acme", "id": "ACME-9", "direction": "up"}, "is not in the queue"},
		{"bad direction", map[string]any{"repo": "acme", "id": "ACME-1", "direction": "sideways"}, `must be "up" or "down"`},
		{"unknown repo", map[string]any{"repo": "stranger", "id": "ACME-1", "direction": "up"}, "unknown repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := hubTool(t, ts, "move_queue_item", tc.args)
			if !tr.IsError || !strings.Contains(tr.Content[0].Text, tc.want) {
				t.Fatalf("move result = %+v, want a tool error mentioning %q", tr, tc.want)
			}
		})
	}
}

// shutdown_queue disarms the drain synchronously and hands the rest to the same
// teardown the REST route runs: the running child is stopped for real, the
// checkpoints it and the paused items hold are dropped, and the queue is emptied.
func TestHubMCPShutdownQueueStopsChildAndClears(t *testing.T) {
	ts, s, root := hubMCPAdminServer(t)
	s.sup.(*fakeSupervisor).onKill = func(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }
	store := s.stores.Queue(root)

	runningPID := spawnTermIgnorer(t, "5")
	addQueued(t, s, root, "ACME-1", "ACME-2")
	if err := store.MarkRunning("ACME-1", runningPID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := store.Pause("ACME-2", "faulted"); err != nil {
		t.Fatalf("Pause ACME-2: %v", err)
	}
	for _, ticket := range []string{"ACME-1", "ACME-2"} {
		if err := s.stores.Checkpoints().Upsert(root, ticket, map[string]string{"PHASE": "building"}); err != nil {
			t.Fatalf("seed checkpoint %s: %v", ticket, err)
		}
	}
	if err := store.SetDraining(true); err != nil {
		t.Fatalf("arm drain: %v", err)
	}

	var ack MCPShutdown
	hubToolPayload(t, hubTool(t, ts, "shutdown_queue", map[string]any{"repo": "acme"}), &ack)
	if ack.Repo != "acme" || ack.Status != "shutting_down" || ack.Stopping != "ACME-1" {
		t.Fatalf("shutdown_queue = %+v, want acme tearing down with ACME-1 stopping", ack)
	}
	if _, meta, _ := store.Snapshot(); meta.Draining {
		t.Error("drain still armed after shutdown_queue answered")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		items, _, err := store.Snapshot()
		if err != nil {
			t.Fatalf("snapshot queue: %v", err)
		}
		if len(items) == 0 && !s.isShuttingDown(root) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue never emptied: %+v", items)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if registry.Alive(runningPID) {
		t.Error("running child still alive after shutdown_queue")
	}
	for _, ticket := range []string{"ACME-1", "ACME-2"} {
		if _, found, _ := s.stores.Checkpoints().One(root, ticket); found {
			t.Errorf("%s's checkpoint survived the teardown", ticket)
		}
	}

	tr := hubTool(t, ts, "shutdown_queue", map[string]any{"repo": "stranger"})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "observe-only") {
		t.Fatalf("shutdown_queue of an observe-only repo = %+v, want a tool error", tr)
	}
}

// update_ticket replaces what it is given and leaves the rest alone, so an agent
// editing a title cannot blank the description it never sent.
func TestHubMCPUpdateTicket(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	ticket := createMCPTicket(t, ts, map[string]any{
		"repo": "acme", "title": "Ship the toggle", "description": "the original body",
		"labels": []string{"ready-for-agent", "ui"},
	})

	var retitled InternalIssueResponse
	hubToolPayload(t, hubTool(t, ts, "update_ticket", map[string]any{
		"repo": "acme", "id": ticket.ID, "title": "Ship the dark-mode toggle",
	}), &retitled)
	if retitled.Title != "Ship the dark-mode toggle" || retitled.Description != "the original body" {
		t.Fatalf("update_ticket = %+v, want the new title and the untouched body", retitled)
	}
	if !slices.Equal(retitled.Labels, ticket.Labels) {
		t.Errorf("labels = %v, want the stored %v kept", retitled.Labels, ticket.Labels)
	}

	var relabelled InternalIssueResponse
	hubToolPayload(t, hubTool(t, ts, "update_ticket", map[string]any{
		"repo": "acme", "id": ticket.ID, "labels": []string{"chore"}, "description": "a new body",
	}), &relabelled)
	if !slices.Equal(relabelled.Labels, []string{"chore"}) || relabelled.Description != "a new body" {
		t.Fatalf("update_ticket = %+v, want the replacement labels and body", relabelled)
	}
	if relabelled.Title != "Ship the dark-mode toggle" {
		t.Errorf("title = %q, want the earlier edit kept", relabelled.Title)
	}

	stored, found, err := s.stores.Issues().Internal(root, ticket.ID)
	if err != nil || !found {
		t.Fatalf("read stored issue: found=%v err=%v", found, err)
	}
	if stored.Title != "Ship the dark-mode toggle" || stored.Description != "a new body" {
		t.Errorf("stored issue = %+v, want the edits persisted", stored)
	}

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown id", map[string]any{"repo": "acme", "id": "ACME-999", "title": "x"}, "is not a hub-filed issue"},
		{"unknown repo", map[string]any{"repo": "stranger", "id": ticket.ID, "title": "x"}, "unknown repo"},
		{"emptied title", map[string]any{"repo": "acme", "id": ticket.ID, "title": "   "}, "title cannot be emptied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := hubTool(t, ts, "update_ticket", tc.args)
			if !tr.IsError || !strings.Contains(tr.Content[0].Text, tc.want) {
				t.Fatalf("update_ticket = %+v, want a tool error mentioning %q", tr, tc.want)
			}
		})
	}
}

func TestHubMCPTransitionTicket(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	ticket := createMCPTicket(t, ts, map[string]any{
		"repo": "acme", "title": "Ship the toggle", "labels": []string{"ready-for-agent"},
	})

	var moved InternalIssueResponse
	hubToolPayload(t, hubTool(t, ts, "transition_ticket", map[string]any{
		"repo": "acme", "id": ticket.ID, "state": "started",
		"add_labels": []string{"in-flight"}, "remove_labels": []string{"ready-for-agent"},
		"comment": "picked up over MCP",
	}), &moved)
	if moved.State != "started" {
		t.Fatalf("transition_ticket = %+v, want the started state", moved)
	}
	if !slices.Equal(moved.Labels, []string{"in-flight"}) {
		t.Errorf("labels = %v, want the ready label swapped for in-flight", moved.Labels)
	}

	stored, found, err := s.stores.Issues().Find(root, ticket.ID)
	if err != nil || !found {
		t.Fatalf("read stored issue: found=%v err=%v", found, err)
	}
	if stored.StatusGroup != "started" || len(stored.Comments) != 1 {
		t.Errorf("stored issue = %+v, want the state moved and the comment appended", stored)
	}
	if body := stored.Comments[0].Body; body != "picked up over MCP" {
		t.Errorf("comment = %q, want the one the transition carried", body)
	}

	tr := hubTool(t, ts, "transition_ticket", map[string]any{"repo": "acme", "id": "ACME-999", "state": "started"})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "is not a hub-filed issue") {
		t.Fatalf("transition of an unknown id = %+v, want a tool error naming it", tr)
	}
}

// Every state the write tools advertise has to be one the store keeps: a value it
// does not know normalizes to backlog, so an enum that drifts from the store would
// refile the ticket instead of moving it.
func TestHubMCPWriteToolStatesRoundTrip(t *testing.T) {
	ts, s, root := hubMCPServer(t)

	tools := hubMCPToolList(t, ts)
	for _, name := range []string{"update_ticket", "transition_ticket"} {
		states := schemaEnum(t, tools[name], "state")
		if len(states) == 0 {
			t.Fatalf("%s advertises no state enum", name)
		}
		for _, want := range states {
			ticket := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Ship the toggle"})

			var moved InternalIssueResponse
			hubToolPayload(t, hubTool(t, ts, name, map[string]any{
				"repo": "acme", "id": ticket.ID, "state": want,
			}), &moved)
			if moved.State != want {
				t.Errorf("%s to %q = %q, want the state it advertises", name, want, moved.State)
			}

			stored, found, err := s.stores.Issues().Internal(root, ticket.ID)
			if err != nil || !found {
				t.Fatalf("read stored issue: found=%v err=%v", found, err)
			}
			if stored.StatusGroup != want {
				t.Errorf("%s to %q stored %q, want the state it advertises", name, want, stored.StatusGroup)
			}
		}
	}
}

// A state outside the advertised set is refused rather than normalized, so a
// mistaken value cannot quietly demote a started ticket to the backlog.
func TestHubMCPWriteToolsRefuseUnknownState(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	ticket := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Ship the toggle"})

	var started InternalIssueResponse
	hubToolPayload(t, hubTool(t, ts, "transition_ticket", map[string]any{
		"repo": "acme", "id": ticket.ID, "state": "started",
	}), &started)

	for _, name := range []string{"update_ticket", "transition_ticket"} {
		t.Run(name, func(t *testing.T) {
			tr := hubTool(t, ts, name, map[string]any{"repo": "acme", "id": ticket.ID, "state": "completed"})
			if !tr.IsError || !strings.Contains(tr.Content[0].Text, "is not a workflow state") {
				t.Fatalf("%s to a state the store has no room for = %+v, want a tool error", name, tr)
			}
			stored, found, err := s.stores.Issues().Internal(root, ticket.ID)
			if err != nil || !found {
				t.Fatalf("read stored issue: found=%v err=%v", found, err)
			}
			if stored.StatusGroup != "started" {
				t.Errorf("stored state = %q, want the started state untouched", stored.StatusGroup)
			}
		})
	}

	var retitled InternalIssueResponse
	hubToolPayload(t, hubTool(t, ts, "update_ticket", map[string]any{
		"repo": "acme", "id": ticket.ID, "state": "", "title": "Ship the dark-mode toggle",
	}), &retitled)
	if retitled.State != "started" || retitled.Title != "Ship the dark-mode toggle" {
		t.Errorf("update_ticket with an empty state = %+v, want the title edited and the state left alone", retitled)
	}
}

// delete_ticket takes the whole family down: the epic, its children, and every
// row the hub held for them.
func TestHubMCPDeleteTicket(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	epic := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Checkout redesign"})
	child := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Cart page", "parent": epic.ID})

	var deleted MCPDeleted
	hubToolPayload(t, hubTool(t, ts, "delete_ticket", map[string]any{"repo": "acme", "id": epic.ID}), &deleted)
	if deleted.Repo != "acme" || !slices.Contains(deleted.Deleted, epic.ID) || !slices.Contains(deleted.Deleted, child.ID) {
		t.Fatalf("delete_ticket = %+v, want the epic and its child removed", deleted)
	}
	for _, id := range []string{epic.ID, child.ID} {
		if _, found, err := s.stores.Issues().Internal(root, id); err != nil || found {
			t.Errorf("%s survived the delete: found=%v err=%v", id, found, err)
		}
		res, body := get(t, ts, APIPrefix+"/repos/acme/issues/internal/"+id)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("board read of %s = %d, want 404: %s", id, res.StatusCode, body)
		}
	}

	tr := hubTool(t, ts, "delete_ticket", map[string]any{"repo": "acme", "id": "ACME-999"})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "is not in repo") {
		t.Fatalf("delete of an unknown id = %+v, want a tool error naming it", tr)
	}
}

// The run history is what a delete leaves, per Issues.Purge, so the tool's
// description promises the checkpoint stays and clear_run for dropping it.
func TestHubMCPDeleteTicketKeepsRunHistory(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	ticket := createMCPTicket(t, ts, map[string]any{"repo": "acme", "title": "Ship the toggle"})
	if err := s.stores.Checkpoints().Upsert(root, ticket.ID, map[string]string{"PHASE": state.PROpen}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	var deleted MCPDeleted
	hubToolPayload(t, hubTool(t, ts, "delete_ticket", map[string]any{"repo": "acme", "id": ticket.ID}), &deleted)
	if !slices.Equal(deleted.Deleted, []string{ticket.ID}) {
		t.Fatalf("delete_ticket = %+v, want just the ticket removed", deleted)
	}
	if _, found, err := s.stores.Checkpoints().One(root, ticket.ID); err != nil || !found {
		t.Errorf("checkpoint after the delete: found=%v err=%v, want the run history kept", found, err)
	}
}

func TestHubMCPClearRun(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	seedMCPRun(t, s, root, "ACME-1", 1)

	var cleared MCPRunReset
	hubToolPayload(t, hubTool(t, ts, "clear_run", map[string]any{"repo": "acme", "ticket": "ACME-1"}), &cleared)
	if cleared.Status != "cleared" || cleared.Ticket != "ACME-1" || cleared.Was != state.PROpen {
		t.Fatalf("clear_run = %+v, want ACME-1 cleared from its pr_open phase", cleared)
	}
	if _, found, _ := s.stores.Checkpoints().One(root, "ACME-1"); found {
		t.Error("checkpoint survived clear_run")
	}
	if captures := s.sup.(*fakeSupervisor).captures; len(captures) != 0 {
		t.Errorf("clear_run spawned %+v, want it to touch the checkpoint alone", captures)
	}

	tr := hubTool(t, ts, "clear_run", map[string]any{"repo": "acme", "ticket": "ACME-999"})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "has never run") {
		t.Fatalf("clear_run of an unknown ticket = %+v, want a tool error naming it", tr)
	}
}

func TestHubMCPResetRun(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	seedMCPRun(t, s, root, "ACME-1", 1)
	fake := s.sup.(*fakeSupervisor)

	var reset MCPRunReset
	hubToolPayload(t, hubTool(t, ts, "reset_run", map[string]any{"repo": "acme", "ticket": "ACME-1"}), &reset)
	if reset.Status != "reset" || reset.Ticket != "ACME-1" {
		t.Fatalf("reset_run = %+v, want ACME-1 reset", reset)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %+v, want the one reset child", fake.captures)
	}
	assertArgs(t, fake.captures[0].Args, []string{"--repo", root, "--reset", "ACME-1", "--no-tui"})
	if _, found, _ := s.stores.Checkpoints().One(root, "ACME-1"); found {
		t.Error("checkpoint survived reset_run")
	}
}

// A merged ticket's reset drops the shipped branch, so it is refused until the
// caller says force — the same override the CLI and the REST route require.
func TestHubMCPResetRunMergedNeedsForce(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	if err := s.stores.Checkpoints().Upsert(root, "ACME-1", map[string]string{"PHASE": state.Merged}); err != nil {
		t.Fatalf("seed merged checkpoint: %v", err)
	}
	fake := s.sup.(*fakeSupervisor)

	tr := hubTool(t, ts, "reset_run", map[string]any{"repo": "acme", "ticket": "ACME-1"})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "already merged") {
		t.Fatalf("reset of a merged ticket = %+v, want a tool error asking for force", tr)
	}
	if len(fake.captures) != 0 {
		t.Fatalf("refused reset still spawned %+v", fake.captures)
	}

	var forced MCPRunReset
	hubToolPayload(t, hubTool(t, ts, "reset_run", map[string]any{
		"repo": "acme", "ticket": "ACME-1", "force": true,
	}), &forced)
	if forced.Was != state.Merged {
		t.Errorf("reset_run = %+v, want the merged phase reported", forced)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %+v, want the one forced reset child", fake.captures)
	}
	assertArgs(t, fake.captures[0].Args, []string{"--repo", root, "--reset", "ACME-1", "--no-tui", "--force"})
}

// A checkpoint mutation is refused while a loop holds the repo, so an MCP client
// cannot corrupt a running session's state out from under it.
func TestHubMCPRunMutationsRefuseLiveRepo(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	seedMCPRun(t, s, root, "ACME-1", 1)
	entry := registry.Entry{
		PID: os.Getpid(), RepoRoot: root, StartedAt: time.Now(), Heartbeat: time.Now(),
		SessionState: registry.StateWorking, Ticket: "ACME-1", Phase: "build",
	}
	if err := s.stores.Instances().Upsert(entry); err != nil {
		t.Fatalf("upsert instance: %v", err)
	}

	for _, tool := range []string{"reset_run", "clear_run"} {
		t.Run(tool, func(t *testing.T) {
			tr := hubTool(t, ts, tool, map[string]any{"repo": "acme", "ticket": "ACME-1"})
			if !tr.IsError || !strings.Contains(tr.Content[0].Text, "has a live loop") {
				t.Fatalf("%s = %+v, want a tool error naming the holding loop", tool, tr)
			}
		})
	}
	if _, found, _ := s.stores.Checkpoints().One(root, "ACME-1"); !found {
		t.Error("a refused mutation dropped the checkpoint anyway")
	}
}

func TestHubMCPStopInstance(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	entry := registry.Entry{
		PID: os.Getpid(), RepoRoot: root, StartedAt: time.Now(), Heartbeat: time.Now(),
		SessionState: registry.StateWorking, Ticket: "ACME-1", Phase: "build",
	}
	if err := s.stores.Instances().Upsert(entry); err != nil {
		t.Fatalf("upsert instance: %v", err)
	}
	fake := s.sup.(*fakeSupervisor)

	var stopped MCPStop
	hubToolPayload(t, hubTool(t, ts, "stop_instance", map[string]any{"pid": os.Getpid()}), &stopped)
	if stopped.PID != os.Getpid() || stopped.Status != "stopping" || stopped.Signal != proc.StopName {
		t.Fatalf("stop_instance = %+v, want a stop on its way to this pid", stopped)
	}
	if len(fake.stops) != 1 || fake.stops[0] != os.Getpid() {
		t.Fatalf("stops = %v, want one graceful stop of the registered pid", fake.stops)
	}

	tr := hubTool(t, ts, "stop_instance", map[string]any{"pid": 999999})
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "no live loop with pid") {
		t.Fatalf("stop of an unregistered pid = %+v, want a tool error", tr)
	}
	if len(fake.stops) != 1 {
		t.Errorf("stops = %+v, want an unregistered pid never signalled", fake.stops)
	}
}

// restart_hub answers over a connection the restart is about to close, so the
// result has to be written before the hook fires — and only one successor is
// spawned however many times it is called.
func TestHubMCPRestartHub(t *testing.T) {
	ts, s, _ := hubMCPServer(t)
	restarts := make(chan struct{}, 4)
	s.EnableRestart(func() { restarts <- struct{}{} })

	var ack RestartAck
	hubToolPayload(t, hubTool(t, ts, "restart_hub", nil), &ack)
	if !ack.Restarting || ack.Version != "1.2.3" {
		t.Fatalf("restart_hub = %+v, want the outgoing version acknowledged", ack)
	}
	select {
	case <-restarts:
	case <-time.After(time.Second):
		t.Fatal("restart_hub answered but never triggered the restart")
	}

	hubToolPayload(t, hubTool(t, ts, "restart_hub", nil), &RestartAck{})
	select {
	case <-restarts:
		t.Error("a second restart_hub spawned a second successor")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubMCPRestartHubWithoutHook(t *testing.T) {
	ts, _, _ := hubMCPServer(t)

	tr := hubTool(t, ts, "restart_hub", nil)
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "cannot restart itself") {
		t.Fatalf("restart_hub = %+v, want a tool error from a hub with no successor", tr)
	}
}
