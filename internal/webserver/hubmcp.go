package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// The hub's general-purpose MCP endpoint (POST /api/v1/mcp): an external MCP
// client — Claude Code, Codex — files a ticket, queues it, and starts the drain
// without going through the web UI. Every tool takes the repo it acts on and
// calls the same store and drain logic the REST routes do, so an MCP client and
// the Queue view can never disagree about what happened.
const hubMCPName = "trau"

var hubMCPTools = []mcpTool{
	{
		Name: "list_repos",
		Description: "List the repos this hub serves: the name every other tool takes, the absolute path of each, " +
			"and whether its queue can be drained. can_drain is false for an observe-only repo — the hub can read its " +
			"queue but will refuse to queue or run work in it until it is registered. Call this first to learn the repo names.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		Annotations: readOnlyTool,
	},
	{
		Name: "create_ticket",
		Description: "File a new ticket in a repo's issue store. It lives only in the hub — no external tracker is " +
			"involved — and lands in the backlog, visible on the board straight away. labels defaults to the repo's " +
			"ready-for-agent label so the ticket is eligible for the loop; pass labels to override that. parent nests the " +
			"ticket under an existing epic, given as that epic's identifier. Returns the allocated identifier (e.g. ACME-12) " +
			"— that is the id enqueue takes.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "title": {"type": "string", "description": "The ticket title."},
    "description": {"type": "string", "description": "The markdown body: the full, unambiguous slice an agent can implement without guessing."},
    "labels": {"type": "array", "items": {"type": "string"}, "description": "Labels for the ticket. Omit for the repo's ready-for-agent label, which is what makes the ticket eligible for the loop."},
    "parent": {"type": "string", "description": "Optional epic identifier to file this ticket under."}
  },
  "required": ["repo", "title"]
}`),
	},
	{
		Name: "enqueue",
		Description: "Register a ticket or epic for execution in a repo's queue. id is an identifier as the board shows " +
			"it (e.g. ACME-12); its title, source and kind are resolved from the hub's issue store, and an id that has " +
			"children is queued as an epic carrying them. front lands it in the first pending position instead of the back, " +
			"never displacing a running item; it defaults to false. Queuing runs nothing on its own — call start_queue to " +
			"arm the drain. Returns the queued row with its position.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it. Must be a repo whose can_drain is true."},
    "id": {"type": "string", "description": "The ticket or epic identifier to queue, e.g. ACME-12."},
    "front": {"type": "boolean", "description": "Queue it in the first pending position instead of the back. Defaults to false."}
  },
  "required": ["repo", "id"]
}`),
	},
	{
		Name: "start_queue",
		Description: "Arm a repo's queue: the hub runs its pending items one at a time, in order, spawning one child per " +
			"item and moving on when it finishes. on_fault decides what a failed item does to the rest — \"halt\" (the " +
			"default) stops the queue at the failure, \"skip\" settles it and continues with the next item. Only a repo " +
			"whose can_drain is true can be started. Returns the queue with draining true.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it. Must be a repo whose can_drain is true."},
    "on_fault": {"type": "string", "enum": ["halt", "skip"], "description": "What a failed item does to the rest of the queue. Defaults to halt."}
  },
  "required": ["repo"]
}`),
	},
	{
		Name: "pause_queue",
		Description: "Pause a repo's queue. The drain stops after the item currently running exits — nothing is killed " +
			"mid-run — and every row stays queued, so start_queue picks up where it left off. Returns the queue with " +
			"draining false.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it. Must be a repo whose can_drain is true."}
  },
  "required": ["repo"]
}`),
	},
	{
		Name: "queue_status",
		Description: "Read a repo's queue: every row in order with its position, kind, status and unresolved blockers, " +
			"whether the drain is armed (draining), which item the hub is running right now (current), and whether that " +
			"run's child process is still alive (child_live).",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."}
  },
  "required": ["repo"]
}`),
		Annotations: readOnlyTool,
	},
}

// MCPRepo is one repo as list_repos reports it: the name every other tool takes,
// its absolute path, and whether the workspace gate lets the hub queue and drain
// work in it.
type MCPRepo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	CanDrain bool   `json:"can_drain"`
}

// MCPQueueRow is what enqueue answers with: the repo the item landed in and the
// queued row as the Queue view reads it.
type MCPQueueRow struct {
	Repo string        `json:"repo"`
	Item QueueItemView `json:"item"`
}

// MCPQueueStatus is what queue_status and the drain tools answer with: the queue
// resource the Queue view reads, plus the drain's live state — the item the hub
// is running right now and whether that run's child is still alive.
type MCPQueueStatus struct {
	QueueResponse
	Current   string `json:"current,omitempty"`
	ChildLive bool   `json:"child_live"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	mcpServer{name: hubMCPName, version: s.version, tools: hubMCPTools, callTool: s.hubMCPToolsCall}.serve(w, r)
}

// hubMCPToolsCall runs one tool and maps its outcome onto the MCP result shape.
// Anything the agent could fix by calling again — an unknown repo, an
// observe-only one, a ticket that is not in the store — comes back as a tool
// error it can read, not a protocol error that aborts the call.
func (s *Server) hubMCPToolsCall(w http.ResponseWriter, _ *http.Request, rpcID json.RawMessage, p toolsCallParams) {
	var run func(json.RawMessage) (any, error)
	switch p.Name {
	case "list_repos":
		run = s.mcpListRepos
	case "create_ticket":
		run = s.mcpCreateTicket
	case "enqueue":
		run = s.mcpEnqueue
	case "start_queue":
		run = s.mcpStartQueue
	case "pause_queue":
		run = s.mcpPauseQueue
	case "queue_status":
		run = s.mcpQueueStatus
	default:
		respondRPCError(w, rpcID, rpcInvalidParams, "unknown tool: "+p.Name)
		return
	}
	result, err := run(p.Arguments)
	if err != nil {
		respondRPCJSON(w, rpcID, mcpToolError(err.Error()))
		return
	}
	respondRPCJSON(w, rpcID, mcpToolJSON(result))
}

func (s *Server) mcpListRepos(json.RawMessage) (any, error) {
	views := s.repoViews()
	repos := make([]MCPRepo, 0, len(views))
	for _, v := range views {
		repos = append(repos, MCPRepo{Name: v.Name, Path: v.Root, CanDrain: v.Allowed})
	}
	return map[string]any{"repos": repos}, nil
}

func (s *Server) mcpCreateTicket(args json.RawMessage) (any, error) {
	var a struct {
		Repo        string   `json:"repo"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
		Parent      string   `json:"parent"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("create_ticket arguments were not valid JSON")
	}
	repo, err := s.mcpRepo(a.Repo)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(a.Title)
	if title == "" {
		return nil, errors.New("title is required")
	}
	labels := cleanLabels(a.Labels)
	if len(labels) == 0 {
		if ready, _ := s.backlogConfig(repo); ready != "" {
			labels = []string{ready}
		}
	}
	iss, err := s.createInternalIssue(repo, hubstore.InternalDraft{
		Title:       title,
		Description: a.Description,
		Labels:      labels,
		Parent:      strings.TrimSpace(a.Parent),
	})
	if err != nil {
		return nil, err
	}
	return toInternalIssueResponse(repo.Name, iss), nil
}

func (s *Server) mcpEnqueue(args json.RawMessage) (any, error) {
	var a struct {
		Repo  string `json:"repo"`
		ID    string `json:"id"`
		Front bool   `json:"front"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("enqueue arguments were not valid JSON")
	}
	root, err := s.mcpQueueRoot(a.Repo)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(a.ID)
	if !reTicketID.MatchString(id) {
		return nil, fmt.Errorf("id %q is not a valid ticket identifier", a.ID)
	}
	item, err := s.storeQueueItem(root, id)
	if err != nil {
		return nil, err
	}
	if a.Front {
		_, _, err = s.stores.Queue(root).AddFront(item)
	} else {
		_, err = s.stores.Queue(root).Add(item)
	}
	if errors.Is(err, queue.ErrAlreadyQueued) {
		return nil, fmt.Errorf("%s is already in the queue", id)
	}
	if err != nil {
		return nil, fmt.Errorf("enqueue: %w", err)
	}
	view, err := s.queueView(root)
	if err != nil {
		return nil, err
	}
	for _, row := range view.Items {
		if row.ID == id {
			return MCPQueueRow{Repo: view.Repo, Item: row}, nil
		}
	}
	return nil, fmt.Errorf("%s was queued but is no longer in the queue", id)
}

func (s *Server) mcpStartQueue(args json.RawMessage) (any, error) {
	var a struct {
		Repo    string `json:"repo"`
		OnFault string `json:"on_fault"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("start_queue arguments were not valid JSON")
	}
	root, err := s.mcpQueueRoot(a.Repo)
	if err != nil {
		return nil, err
	}
	onFault, err := normalizeOnFault(a.OnFault)
	if err != nil {
		return nil, err
	}
	if err := s.setDraining(root, true, false, onFault); err != nil {
		return nil, err
	}
	return s.mcpDrainState(root)
}

func (s *Server) mcpPauseQueue(args json.RawMessage) (any, error) {
	var a struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("pause_queue arguments were not valid JSON")
	}
	root, err := s.mcpQueueRoot(a.Repo)
	if err != nil {
		return nil, err
	}
	if err := s.setDraining(root, false, false, ""); err != nil {
		return nil, err
	}
	return s.mcpDrainState(root)
}

func (s *Server) mcpQueueStatus(args json.RawMessage) (any, error) {
	var a struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("queue_status arguments were not valid JSON")
	}
	root, ok := s.queueRoot(strings.TrimSpace(a.Repo))
	if !ok {
		return nil, unknownMCPRepo(a.Repo)
	}
	return s.mcpDrainState(root)
}

func (s *Server) mcpRepo(name string) (registry.Repo, error) {
	repo, ok := s.findRepo(strings.TrimSpace(name))
	if !ok {
		return registry.Repo{}, unknownMCPRepo(name)
	}
	return repo, nil
}

// mcpQueueRoot resolves a tool's repo argument to a root the hub may queue and
// drain work in, applying the workspace gate the REST queue routes apply.
func (s *Server) mcpQueueRoot(name string) (string, error) {
	root, ok := s.allowedRoot(strings.TrimSpace(name))
	if !ok {
		return "", fmt.Errorf("repo %q is observe-only; only a Registered repo can have work queued or drained — register it first, or call list_repos for the repos that can", name)
	}
	return root, nil
}

func unknownMCPRepo(name string) error {
	return fmt.Errorf("unknown repo %q — call list_repos for the repos this hub serves", name)
}

// storeQueueItem builds the queue row for an identifier from the hub's issue
// store alone — no tracker call — resolving kind the way the web queue path does.
func (s *Server) storeQueueItem(root, id string) (queue.Item, error) {
	iss, found, err := s.stores.Issues().Get(root, id)
	if err != nil {
		return queue.Item{}, fmt.Errorf("read issue: %w", err)
	}
	if !found {
		return queue.Item{}, fmt.Errorf("%s is not in this repo's issue store — file it with create_ticket, or sync the repo to pull it from the tracker", id)
	}
	item := queue.Item{Kind: queue.KindTicket, ID: iss.Identifier, Title: iss.Title, Source: iss.Source}
	children, err := s.stores.Issues().Children(root, id)
	if err != nil {
		return queue.Item{}, fmt.Errorf("resolve item: %w", err)
	}
	if len(children) > 0 {
		item.Kind = queue.KindEpic
		item.SubIssues = internalSubIssues(children)
	}
	return item, nil
}

// mcpDrainState reads the queue resource and folds in what only the drainer
// knows: the item currently running and whether its child is still alive.
func (s *Server) mcpDrainState(root string) (MCPQueueStatus, error) {
	view, err := s.queueView(root)
	if err != nil {
		return MCPQueueStatus{}, err
	}
	status := MCPQueueStatus{QueueResponse: view, ChildLive: s.drain.repoLive(root)}
	for _, row := range view.Items {
		if row.Status == queue.StatusRunning {
			status.Current = row.ID
			break
		}
	}
	return status, nil
}
