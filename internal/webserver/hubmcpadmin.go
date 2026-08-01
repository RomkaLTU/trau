package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/proc"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// The destructive half of the hub's MCP surface: the external agent is a full
// operator, so it can drop queue rows, kill the running child, rewrite and delete
// tickets, throw a run away and restart the hub. There is no confirmation layer —
// trau is machine-trust by design — so each description states what the tool
// takes away in its first sentence and that honesty is the guardrail.
var hubMCPAdminTools = []mcpTool{
	{
		Name: "dequeue",
		Description: "Removes an item from a repo's queue for good — the row is gone and the drain will never run it. " +
			"It is a queue removal and nothing else: the ticket itself stays in the issue store, and an item that is " +
			"running right now is refused rather than orphaned, so no live agent is touched. Returns the queue without it.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "id": {"type": "string", "description": "The queued identifier to remove, e.g. ACME-12."}
  },
  "required": ["repo", "id"]
}`),
		Annotations: destructiveTool,
	},
	{
		Name: "move_queue_item",
		Description: "Reorders a repo's queue, changing which item the drain picks up next. The item shifts one slot: " +
			"\"up\" toward the front, \"down\" toward the back. The running item cannot be reordered and nothing may " +
			"displace it. Returns the reordered queue.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "id": {"type": "string", "description": "The queued identifier to move, e.g. ACME-12."},
    "direction": {"type": "string", "enum": ["up", "down"], "description": "Which way to shift it one slot."}
  },
  "required": ["repo", "id", "direction"]
}`),
		Annotations: destructiveTool,
	},
	{
		Name: "update_ticket",
		Description: "Overwrites a ticket's stored content — the fields you pass replace what is there, with no history " +
			"to recover the old text from. Fields you omit keep their current value, so passing title alone edits the " +
			"title and nothing else; passing labels replaces the whole label set rather than adding to it. Only a " +
			"hub-filed ticket can be edited: one synced from a tracker is tracker-owned and is refused.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "id": {"type": "string", "description": "The ticket identifier to edit, e.g. ACME-12."},
    "title": {"type": "string", "description": "Replacement title. Omit to keep the current one; it cannot be emptied."},
    "description": {"type": "string", "description": "Replacement markdown body. Omit to keep the current one."},
    "labels": {"type": "array", "items": {"type": "string"}, "description": "Replacement label set — the whole set, not additions. Omit to keep the current labels; use transition_ticket to add or remove individual ones."},
    "state": {"type": "string", "enum": ["backlog", "unstarted", "started", "done", "canceled"], "description": "Replacement workflow state. Omit or leave empty to keep the current one."},
    "parent": {"type": "string", "description": "Replacement epic identifier, or empty to unnest it. Omit to keep the current parent."}
  },
  "required": ["repo", "id"]
}`),
		Annotations: destructiveTool,
	},
	{
		Name: "transition_ticket",
		Description: "Moves a ticket's workflow state and labels, which is what decides whether the loop will run it — " +
			"removing the ready label takes it out of the running, adding it puts it back in. An empty state leaves the " +
			"state alone; add_labels and remove_labels apply case-insensitively; a comment is appended to the ticket. " +
			"This is the loop-style write: use it to nudge one ticket, and update_ticket to rewrite its content.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "id": {"type": "string", "description": "The ticket identifier to transition, e.g. ACME-12."},
    "state": {"type": "string", "enum": ["backlog", "unstarted", "started", "done", "canceled"], "description": "The workflow state to move it to. Omit to leave it unchanged."},
    "add_labels": {"type": "array", "items": {"type": "string"}, "description": "Labels to add."},
    "remove_labels": {"type": "array", "items": {"type": "string"}, "description": "Labels to remove."},
    "comment": {"type": "string", "description": "A comment to append to the ticket."}
  },
  "required": ["repo", "id"]
}`),
		Annotations: destructiveTool,
	},
	{
		Name: "delete_ticket",
		Description: "Irreversibly deletes a ticket and what the board holds for it — its comments, attachments, " +
			"relations and queue entries — plus its local and remote feature branch and run directory. Its run history " +
			"is what survives: the checkpoint, phase logs and artifacts stay browsable, so what ran outlives the ticket " +
			"it ran for; call clear_run first if the checkpoint must go too. Deleting an epic takes its children down " +
			"with it. There is no undo and no archive to restore from; to get a ticket off the board while keeping it, " +
			"transition it to canceled instead. A ticket in the family that is mid-run is refused — stop that run " +
			"first. Returns every identifier the purge removed.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "id": {"type": "string", "description": "The ticket or epic identifier to delete, e.g. ACME-12."}
  },
  "required": ["repo", "id"]
}`),
		Annotations: destructiveTool,
	},
	{
		Name: "reset_run",
		Description: "Throws a ticket's run away: it drops the feature branch and the checkpoint and re-queues the " +
			"ticket on the tracker, so the next run starts from nothing. Unmerged work on that branch goes with it. " +
			"Resetting a ticket whose code is already merged drops the shipped branch, so it needs force true to " +
			"confirm. It is refused while a loop is live in the repo — stop that instance first.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "ticket": {"type": "string", "description": "The ticket identifier as list_runs reports it, e.g. ACME-12."},
    "force": {"type": "boolean", "description": "Reset a ticket that is already merged, dropping the shipped branch. Defaults to false."}
  },
  "required": ["repo", "ticket"]
}`),
		Annotations: destructiveTool,
	},
	{
		Name: "clear_run",
		Description: "Forgets a ticket's checkpoint, so the hub loses all record of how far that run got and the next " +
			"run starts from scratch. Unlike reset_run it touches nothing else — no branch, no tracker — which is what a " +
			"ticket finished out of band needs. It is refused while a loop is live in the repo. Returns the phase the " +
			"checkpoint sat at.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "ticket": {"type": "string", "description": "The ticket identifier as list_runs reports it, e.g. ACME-12."}
  },
  "required": ["repo", "ticket"]
}`),
		Annotations: destructiveTool,
	},
	{
		Name: "stop_instance",
		Description: "Stops a live loop process, ending its run mid-flight and leaving the ticket unfinished at " +
			"whatever phase it reached. It asks the loop to stop the way Ctrl-C does, so the loop checkpoints what it has " +
			"and exits gracefully rather than being killed outright. Only a pid list_instances reports can be stopped.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pid": {"type": "integer", "description": "The process id as list_instances reports it."}
  },
  "required": ["pid"]
}`),
		Annotations: destructiveTool,
	},
	{
		Name: "restart_hub",
		Description: "Restarts the hub process, dropping every open connection including this one — the tool answers " +
			"first, then the hub goes down and comes back on the same port. Anything the hub was doing in the background " +
			"at that moment, a queue teardown included, is interrupted; a loop child already spawned keeps running on " +
			"its own. A hub not started by `trau serve` has no successor to spawn and refuses. Returns the version that " +
			"is on its way out.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		Annotations: destructiveTool,
	},
}

// MCPDeleted names every identifier delete_ticket removed: the ticket alone for a
// leaf, the epic and its children for a family.
type MCPDeleted struct {
	Repo    string   `json:"repo"`
	Deleted []string `json:"deleted"`
}

// MCPRunReset is what reset_run and clear_run answer with: what was done to the
// ticket, and the phase its checkpoint sat at before it went.
type MCPRunReset struct {
	Repo   string `json:"repo"`
	Ticket string `json:"ticket"`
	Status string `json:"status"`
	Was    string `json:"was,omitempty"`
}

// MCPStop is what stop_instance answers with: the stop request is on its way to
// the pid, and the loop exits once it has checkpointed.
type MCPStop struct {
	PID    int    `json:"pid"`
	Status string `json:"status"`
	Signal string `json:"signal"`
}

// hubMCPAdminTool resolves one of the destructive tools to its runner, reporting
// nil for a name this endpoint does not serve. restart_hub is not here: it needs
// the response writer, so hubMCPToolsCall runs it ahead of the dispatch.
func (s *Server) hubMCPAdminTool(ctx context.Context, name string) func(json.RawMessage) (any, error) {
	switch name {
	case "dequeue":
		return s.mcpDequeue
	case "move_queue_item":
		return s.mcpMoveQueueItem
	case "update_ticket":
		return s.mcpUpdateTicket
	case "transition_ticket":
		return s.mcpTransitionTicket
	case "delete_ticket":
		return s.mcpDeleteTicket
	case "reset_run":
		return func(args json.RawMessage) (any, error) { return s.mcpResetRun(ctx, args) }
	case "clear_run":
		return s.mcpClearRun
	case "stop_instance":
		return s.mcpStopInstance
	}
	return nil
}

func (s *Server) mcpDequeue(args json.RawMessage) (any, error) {
	var a struct {
		Repo string `json:"repo"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("dequeue arguments were not valid JSON")
	}
	root, ok := s.queueRoot(strings.TrimSpace(a.Repo))
	if !ok {
		return nil, unknownMCPRepo(a.Repo)
	}
	id := strings.TrimSpace(a.ID)
	item, queued := s.queuedItem(root, id)
	switch _, err := s.stores.Queue(root).Remove(id); {
	case errors.Is(err, queue.ErrNotQueued):
		return nil, fmt.Errorf("%s is not in the queue — call queue_status for the rows it has", id)
	case errors.Is(err, queue.ErrRunning):
		return nil, fmt.Errorf("%s is running and cannot be removed — stop its loop with stop_instance first", id)
	case err != nil:
		return nil, fmt.Errorf("dequeue: %w", err)
	}
	if queued {
		s.clearQueued(context.Background(), root, item)
	}
	return s.mcpDrainState(root)
}

func (s *Server) mcpMoveQueueItem(args json.RawMessage) (any, error) {
	var a struct {
		Repo      string `json:"repo"`
		ID        string `json:"id"`
		Direction string `json:"direction"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("move_queue_item arguments were not valid JSON")
	}
	root, ok := s.queueRoot(strings.TrimSpace(a.Repo))
	if !ok {
		return nil, unknownMCPRepo(a.Repo)
	}
	dir, err := moveDirection(a.Direction)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(a.ID)
	switch _, err := s.stores.Queue(root).Move(id, dir); {
	case errors.Is(err, queue.ErrNotQueued):
		return nil, fmt.Errorf("%s is not in the queue — call queue_status for the rows it has", id)
	case errors.Is(err, queue.ErrRunning):
		return nil, fmt.Errorf("%s is running and cannot be reordered", id)
	case err != nil:
		return nil, fmt.Errorf("reorder: %w", err)
	}
	return s.mcpDrainState(root)
}

func moveDirection(raw string) (int, error) {
	switch strings.TrimSpace(raw) {
	case "up":
		return -1, nil
	case "down":
		return 1, nil
	}
	return 0, fmt.Errorf("direction %q must be \"up\" or \"down\"", raw)
}

// mcpUpdateTicket edits a hub-filed ticket. The store replaces every editable
// field at once, so the update is merged onto the stored issue first: an argument
// the agent left out keeps the value it has rather than being blanked.
func (s *Server) mcpUpdateTicket(args json.RawMessage) (any, error) {
	var a struct {
		Repo        string    `json:"repo"`
		ID          string    `json:"id"`
		Title       *string   `json:"title"`
		Description *string   `json:"description"`
		Labels      *[]string `json:"labels"`
		State       *string   `json:"state"`
		Parent      *string   `json:"parent"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("update_ticket arguments were not valid JSON")
	}
	repo, err := s.mcpRepo(a.Repo)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(a.ID)
	iss, found, err := s.stores.Issues().Internal(repo.Root, id)
	if err != nil {
		return nil, fmt.Errorf("read issue: %w", err)
	}
	if !found {
		return nil, notHubFiled(id, repo.Name)
	}
	draft := hubstore.InternalDraft{
		Title:       iss.Title,
		Description: iss.Description,
		State:       iss.StatusGroup,
		Labels:      iss.Labels,
		Parent:      iss.Parent,
		Priority:    iss.Priority,
		DueDate:     iss.DueDate,
	}
	if a.Title != nil {
		draft.Title = strings.TrimSpace(*a.Title)
	}
	if a.Description != nil {
		draft.Description = *a.Description
	}
	if a.Labels != nil {
		draft.Labels = cleanLabels(*a.Labels)
	}
	if a.State != nil {
		group, err := mcpWorkflowState(*a.State, draft.State)
		if err != nil {
			return nil, err
		}
		draft.State = group
	}
	if a.Parent != nil {
		draft.Parent = strings.TrimSpace(*a.Parent)
	}
	if draft.Title == "" {
		return nil, errors.New("title cannot be emptied")
	}
	updated, err := s.stores.Issues().UpdateInternal(repo.Root, id, draft)
	if err != nil {
		return nil, fmt.Errorf("update issue: %w", err)
	}
	s.registerAttachments(repo.Root, updated.Identifier, scanIssueImages(updated.Description))
	s.bindUploadedAttachments(repo.Root, updated.Identifier, updated.Description)
	return toInternalIssueResponse(repo.Name, updated), nil
}

func (s *Server) mcpTransitionTicket(args json.RawMessage) (any, error) {
	var a struct {
		Repo         string   `json:"repo"`
		ID           string   `json:"id"`
		State        string   `json:"state"`
		AddLabels    []string `json:"add_labels"`
		RemoveLabels []string `json:"remove_labels"`
		Comment      string   `json:"comment"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("transition_ticket arguments were not valid JSON")
	}
	repo, err := s.mcpRepo(a.Repo)
	if err != nil {
		return nil, err
	}
	group, err := mcpWorkflowState(a.State, "")
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(a.ID)
	iss, err := s.stores.Issues().TransitionInternal(repo.Root, id, hubstore.InternalTransition{
		State:        group,
		AddLabels:    cleanLabels(a.AddLabels),
		RemoveLabels: cleanLabels(a.RemoveLabels),
		Comment:      a.Comment,
	})
	if errors.Is(err, hubstore.ErrInternalIssueNotFound) {
		return nil, notHubFiled(id, repo.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("transition issue: %w", err)
	}
	if group != "" {
		s.retireClosedGrill(repo.Root)
	}
	return toInternalIssueResponse(repo.Name, iss), nil
}

// mcpWorkflowStates are the state groups the issue store recognizes, and the set
// both write tools advertise.
var mcpWorkflowStates = []string{"backlog", "unstarted", "started", "done", "canceled"}

// mcpWorkflowState resolves a requested state, falling back to current when the
// request is empty — both write tools read that as "leave it where it is". The
// store normalizes a state it does not know to backlog, so one outside the set is
// refused here rather than quietly refiling the ticket.
func mcpWorkflowState(raw, current string) (string, error) {
	group := strings.ToLower(strings.TrimSpace(raw))
	if group == "" {
		return current, nil
	}
	if !slices.Contains(mcpWorkflowStates, group) {
		return "", fmt.Errorf("%q is not a workflow state — use one of %s", raw, strings.Join(mcpWorkflowStates, ", "))
	}
	return group, nil
}

func (s *Server) mcpDeleteTicket(args json.RawMessage) (any, error) {
	var a struct {
		Repo string `json:"repo"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("delete_ticket arguments were not valid JSON")
	}
	repo, err := s.mcpRepo(a.Repo)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(a.ID)
	res, found, err := s.stores.Issues().Purge(repo, id)
	var running *hubstore.IssueRunningError
	switch {
	case errors.As(err, &running):
		return nil, fmt.Errorf("%s is running — stop the run before deleting", running.Identifier)
	case err != nil:
		return nil, fmt.Errorf("delete issue: %w", err)
	case !found:
		return nil, fmt.Errorf("%s is not in repo %q's issue store — call list_backlog for the tickets it holds", id, repo.Name)
	}
	if err := s.stores.Attachments().CollectOrphans(res.OrphanedBlobs); err != nil {
		logger.Verbosef("delete %s: collect attachment blobs: %v", id, err)
	}
	s.purgeGitFootprint(repo, res.Deleted)
	return MCPDeleted{Repo: repo.Name, Deleted: res.Deleted}, nil
}

// resetTimeout bounds a reset: it drops the branch and re-queues the ticket on
// the tracker, so it must outlast a tracker write but never hang the call.
const resetTimeout = 2 * time.Minute

func (s *Server) mcpResetRun(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Repo   string `json:"repo"`
		Ticket string `json:"ticket"`
		Force  bool   `json:"force"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("reset_run arguments were not valid JSON")
	}
	repo, ticket, err := s.mcpMutableRun(a.Repo, a.Ticket)
	if err != nil {
		return nil, err
	}
	was := s.stores.Checkpoints().Phase(repo.Root, ticket)
	if was == state.Merged && !a.Force {
		return nil, fmt.Errorf("%s is already merged — resetting it drops the shipped branch; call again with force true to confirm", ticket)
	}
	cmd := []string{"--repo", repo.Root, "--reset", ticket, "--no-tui"}
	if a.Force {
		cmd = append(cmd, "--force")
	}
	ctx, cancel := context.WithTimeout(ctx, resetTimeout)
	defer cancel()
	if _, err := s.sup.Capture(ctx, SpawnSpec{Dir: repo.Root, Args: cmd, Env: childEnv(s.home)}); err != nil {
		return nil, fmt.Errorf("reset failed: %w", err)
	}
	if err := s.stores.Checkpoints().Remove(repo.Root, ticket); err != nil {
		logger.Verbosef("reset %s/%s: drop checkpoint: %v", repo.Name, ticket, err)
	}
	return MCPRunReset{Repo: repo.Name, Ticket: ticket, Status: "reset", Was: was}, nil
}

func (s *Server) mcpClearRun(args json.RawMessage) (any, error) {
	var a struct {
		Repo   string `json:"repo"`
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("clear_run arguments were not valid JSON")
	}
	repo, ticket, err := s.mcpMutableRun(a.Repo, a.Ticket)
	if err != nil {
		return nil, err
	}
	was := s.stores.Checkpoints().Phase(repo.Root, ticket)
	if err := s.stores.Checkpoints().Remove(repo.Root, ticket); err != nil {
		return nil, fmt.Errorf("clear failed: %w", err)
	}
	return MCPRunReset{Repo: repo.Name, Ticket: ticket, Status: "cleared", Was: was}, nil
}

func (s *Server) mcpStopInstance(args json.RawMessage) (any, error) {
	var a struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("stop_instance arguments were not valid JSON")
	}
	if a.PID <= 0 {
		return nil, fmt.Errorf("pid %d is not a process id", a.PID)
	}
	if !s.registered(a.PID) {
		return nil, fmt.Errorf("no live loop with pid %d — call list_instances for the ones running now", a.PID)
	}
	if err := s.sup.Stop(a.PID); err != nil {
		return nil, fmt.Errorf("signal loop: %w", err)
	}
	return MCPStop{PID: a.PID, Status: "stopping", Signal: proc.StopName}, nil
}

// mcpRestartHub answers before restarting, the way the REST route flushes its 202
// first: the result has to be on the wire while the connection is still up,
// because the restart is what tears it down.
func (s *Server) mcpRestartHub(w http.ResponseWriter, rpcID json.RawMessage) {
	if s.restart == nil {
		respondRPCJSON(w, rpcID, mcpToolError("this hub cannot restart itself — it was not started by `trau serve`, so there is no successor to spawn"))
		return
	}
	respondRPCJSON(w, rpcID, mcpToolJSON(RestartAck{Restarting: true, Version: s.version}))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	s.triggerRestart()
}

// mcpMutableRun resolves a checkpoint mutation's repo and ticket, enforcing the
// invariants mutableCheckpoint enforces for the REST routes. The live check runs
// before the existence lookup for the same reason it does there: importCheckpoints
// no-ops while a loop is live, so an unimported checkpoint would otherwise read as
// an unknown run instead of reporting the conflict.
func (s *Server) mcpMutableRun(name, ticket string) (registry.Repo, string, error) {
	repo, err := s.mcpRepo(name)
	if err != nil {
		return registry.Repo{}, "", err
	}
	if e, ok := s.takeoverHolder(repo.Root); ok {
		return registry.Repo{}, "", fmt.Errorf("%s is taken over — pid %d holds %s in a terminal session; close it first", repo.Name, e.PID, e.Ticket)
	}
	if e, ok := s.liveInstance(repo.Root); ok {
		return registry.Repo{}, "", fmt.Errorf("%s has a live loop (pid %d) — stop it with stop_instance before changing its checkpoints", repo.Name, e.PID)
	}
	ticket = strings.TrimSpace(ticket)
	s.importCheckpoints(repo)
	if _, found, _ := s.stores.Checkpoints().One(repo.Root, ticket); !found {
		return registry.Repo{}, "", fmt.Errorf("%s has never run in repo %q — call list_runs for the tickets it has", ticket, repo.Name)
	}
	return repo, ticket, nil
}

func notHubFiled(id, repo string) error {
	return fmt.Errorf("%s is not a hub-filed issue in repo %q — a ticket synced from a tracker is tracker-owned and cannot be written here", id, repo)
}
