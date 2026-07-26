package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// The hub's general-purpose MCP endpoint (POST /api/v1/mcp): an external MCP
// client — Claude Code, Codex — files a ticket, queues it, and starts the drain
// without going through the web UI. Every tool takes the repo it acts on and
// calls the same store and drain logic the REST routes do, so an MCP client and
// the Queue view can never disagree about what happened.
const hubMCPName = "trau"

var hubMCPTools = append([]mcpTool{
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
	{
		Name: "list_backlog",
		Description: "Read a repo's board: every issue the hub's store holds with its workflow status, normalized state " +
			"group, labels, source, epic relationship and blockers. This is the whole backlog, not what is runnable — call " +
			"list_eligible for that. state filters to one or more status groups (backlog, unstarted, started, completed, " +
			"canceled), label to one label name, parent to an epic's direct children. Rows are capped; total reports how " +
			"many matched so you can page with limit and offset.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "state": {"type": "array", "items": {"type": "string"}, "description": "Status groups to union, e.g. [\"backlog\", \"started\"]. Omit for every group."},
    "label": {"type": "string", "description": "Only issues carrying this label."},
    "source": {"type": "string", "enum": ["internal", "synced"], "description": "Only hub-filed issues, or only ones synced from the tracker."},
    "assignee": {"type": "string", "description": "\"me\", \"unassigned\", or an assignee id."},
    "q": {"type": "string", "description": "Substring match over the identifier and title."},
    "parent": {"type": "string", "description": "An epic identifier — list its direct sub-issues."},
    "limit": {"type": "integer", "description": "Rows to return. Defaults to 100, capped at 500."},
    "offset": {"type": "integer", "description": "Rows to skip, for paging through total."}
  },
  "required": ["repo"]
}`),
		Annotations: readOnlyTool,
	},
	{
		Name: "list_eligible",
		Description: "List what the picker would consider runnable in a repo right now, in the order it would pick them: " +
			"ready-labelled, unblocked tickets with their labels and epic parent. This is the answer to \"what happens if I " +
			"start the queue\" — list_backlog shows everything, this shows only what is eligible. It reads the repo through a " +
			"fresh trau, so only a Registered repo can be listed.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it. Must be a repo whose can_drain is true."}
  },
  "required": ["repo"]
}`),
		Annotations: readOnlyTool,
	},
	{
		Name: "get_epic",
		Description: "List an epic's direct sub-issues with their preview state — done, epic for a nested parent, or todo " +
			"for a buildable child. Call it before queuing an epic to see what queuing it would actually run.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it. Must be a repo whose can_drain is true."},
    "epic": {"type": "string", "description": "The epic identifier, e.g. ACME-12."}
  },
  "required": ["repo", "epic"]
}`),
		Annotations: readOnlyTool,
	},
	{
		Name: "list_runs",
		Description: "List the tickets this repo has run, each with the phase its checkpoint settled at, whether that phase " +
			"is terminal, the branch and PR it opened, the failure class and reason if it faulted, its cost, and when it last " +
			"moved. Rows come back in board order — earliest phase first, then ticket — so the stuck runs lead. Use it to find " +
			"a ticket worth drilling into, then call get_run for that one.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "limit": {"type": "integer", "description": "Rows to return. Defaults to 100, capped at 500."}
  },
  "required": ["repo"]
}`),
		Annotations: readOnlyTool,
	},
	{
		Name: "get_run",
		Description: "Drill into one ticket's run: its checkpoint phase and failure class/reason, the verifier's verdict with " +
			"the concrete failures, per-phase and total token spend, flagged cost anomalies, which artifacts the run produced, " +
			"and the tail of its events. This is how a settled ticket went and why it is stuck — for a run happening right now, " +
			"call list_instances instead, which reports what the live process is doing this second. The bulky artifacts (the " +
			"handoff brief, build notes, transcripts) are flagged as present but never inlined.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "ticket": {"type": "string", "description": "The ticket identifier as list_runs reports it, e.g. ACME-12."},
    "events": {"type": "integer", "description": "How many of the run's most recent events to return. Defaults to 20, capped at 100."}
  },
  "required": ["repo", "ticket"]
}`),
		Annotations: readOnlyTool,
	},
	{
		Name: "list_instances",
		Description: "List the loop processes alive on this machine right now: pid, repo, the ticket and phase each is on, " +
			"its session state and current activity. This is live state — what is running this second, across every repo. For " +
			"how a ticket's run turned out once it settled, call get_run.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		Annotations: readOnlyTool,
	},
	{
		Name: "steer_agent",
		Description: "Nudge the agent running a ticket without stopping it: the note — \"also update the changelog\" — is " +
			"queued for that ticket and injected into the live agent mid-phase. Delivery is asynchronous and never " +
			"guaranteed: the agent takes the note at its next injection point, and a note still waiting when the run settles " +
			"expires undelivered. What comes back is the queue receipt, always pending, not a delivery confirmation — call " +
			"list_steer_notes for the status it reached. Call queue_status or list_instances first to see which ticket is " +
			"actually running; a note queued for a ticket that is not waits for its next run.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "ticket": {"type": "string", "description": "The ticket whose agent to steer, e.g. ACME-12."},
    "body": {"type": "string", "description": "The note to hand the agent. It reaches it verbatim and may span lines, so write it as you would type it to the agent yourself."}
  },
  "required": ["repo", "ticket", "body"]
}`),
	},
	{
		Name: "list_steer_notes",
		Description: "Read a ticket's steer notes in delivery order, oldest first: the body of each, whether it is still " +
			"pending, was delivered — with the phase whose agent consumed it — or expired when the run settled with the note " +
			"still queued, and the timestamps for both. This is how a steer_agent call is followed up. pending_only narrows " +
			"to the notes no agent has taken yet.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "Repo name as list_repos reports it."},
    "ticket": {"type": "string", "description": "The ticket whose notes to list, e.g. ACME-12."},
    "pending_only": {"type": "boolean", "description": "Only the notes still waiting for an agent. Defaults to false, which returns every note the ticket has."}
  },
  "required": ["repo", "ticket"]
}`),
		Annotations: readOnlyTool,
	},
}, hubMCPAdminTools...)

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

// MCPRun is what get_run answers with. The bulky artifacts — the handoff brief,
// the build notes, a transcript — are named in Artifacts but never inlined, so
// the payload stays bounded whatever the run did.
type MCPRun struct {
	Repo string `json:"repo"`
	RunView
	Total     SpendResponse `json:"total"`
	Costs     []PhaseCost   `json:"costs"`
	Anomalies []AnomalyView `json:"anomalies,omitempty"`
	Verdict   *VerdictView  `json:"verdict,omitempty"`
	Artifacts ArtifactSet   `json:"artifacts"`
	Events    []FeedEvent   `json:"events"`
	NoSkills  bool          `json:"no_skills,omitempty"`
	NoBrowser bool          `json:"no_browser,omitempty"`
	Removed   bool          `json:"removed,omitempty"`
}

// MCPSteerNote is what steer_agent answers with: the repo the note was queued in
// and the note itself, pending until an agent takes it.
type MCPSteerNote struct {
	Repo string `json:"repo"`
	SteerNoteView
}

// MCPSteerNotes is what list_steer_notes answers with: a ticket's notes in
// delivery order, each carrying the status it reached.
type MCPSteerNotes struct {
	Repo   string `json:"repo"`
	Ticket string `json:"ticket"`
	SteerNotesResponse
}

// The read tools answer a page rather than a whole table: each listing takes a
// default row count and clamps whatever it is asked for.
const (
	mcpBacklogRows  = 100
	mcpRunRows      = 100
	maxMCPRunRows   = 500
	mcpEventTail    = 20
	maxMCPEventTail = 100
)

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	mcpServer{name: hubMCPName, version: s.version, tools: hubMCPTools, callTool: s.hubMCPToolsCall}.serve(w, r)
}

// hubMCPToolsCall runs one tool and maps its outcome onto the MCP result shape.
// Anything the agent could fix by calling again — an unknown repo, an
// observe-only one, a ticket that is not in the store — comes back as a tool
// error it can read, not a protocol error that aborts the call.
func (s *Server) hubMCPToolsCall(w http.ResponseWriter, r *http.Request, rpcID json.RawMessage, p toolsCallParams) {
	if p.Name == "restart_hub" {
		s.mcpRestartHub(w, rpcID)
		return
	}
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
	case "list_backlog":
		run = s.mcpListBacklog
	case "list_eligible":
		run = func(args json.RawMessage) (any, error) { return s.mcpListEligible(r.Context(), args) }
	case "get_epic":
		run = func(args json.RawMessage) (any, error) { return s.mcpGetEpic(r.Context(), args) }
	case "list_runs":
		run = s.mcpListRuns
	case "get_run":
		run = s.mcpGetRun
	case "list_instances":
		run = s.mcpListInstances
	case "steer_agent":
		run = s.mcpSteerAgent
	case "list_steer_notes":
		run = s.mcpListSteerNotes
	default:
		if run = s.hubMCPAdminTool(r.Context(), p.Name); run == nil {
			respondRPCError(w, rpcID, rpcInvalidParams, "unknown tool: "+p.Name)
			return
		}
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
	s.markQueued(context.Background(), root, item)
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

func (s *Server) mcpListBacklog(args json.RawMessage) (any, error) {
	var a struct {
		Repo     string   `json:"repo"`
		State    []string `json:"state"`
		Label    string   `json:"label"`
		Source   string   `json:"source"`
		Assignee string   `json:"assignee"`
		Text     string   `json:"q"`
		Parent   string   `json:"parent"`
		Limit    int      `json:"limit"`
		Offset   int      `json:"offset"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("list_backlog arguments were not valid JSON")
	}
	repo, err := s.mcpRepo(a.Repo)
	if err != nil {
		return nil, err
	}
	store := s.stores.Issues()
	items, total, counts, err := store.BacklogPage(repo.Root, hubstore.BacklogFilter{
		Groups:   stateGroups(a.State),
		Label:    strings.TrimSpace(a.Label),
		Source:   strings.TrimSpace(a.Source),
		Assignee: strings.TrimSpace(a.Assignee),
		Text:     strings.TrimSpace(a.Text),
		Parent:   strings.TrimSpace(a.Parent),
		Limit:    mcpRows(a.Limit, mcpBacklogRows, maxBacklogLimit),
		Offset:   max(a.Offset, 0),
	})
	if err != nil {
		return nil, fmt.Errorf("list backlog: %w", err)
	}
	syncState, err := store.SyncState(repo.Root)
	if err != nil {
		return nil, fmt.Errorf("read sync state: %w", err)
	}
	archivedCount, err := store.ArchivedCount(repo.Root)
	if err != nil {
		return nil, fmt.Errorf("count archived: %w", err)
	}
	s.syncer.refreshIfStale(repo.Root, syncState.LastSyncedAt)
	readyLabel, provider := s.backlogConfig(repo)
	return BacklogResponse{
		Repo:          repo.Name,
		Provider:      provider,
		Items:         toBacklogEntries(items, readyLabel, syncState.Me.ID),
		Total:         total,
		Counts:        counts,
		ArchivedCount: archivedCount,
		Freshness:     s.freshnessFrom(repo.Root, syncState),
	}, nil
}

func (s *Server) mcpListEligible(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("list_eligible arguments were not valid JSON")
	}
	root, err := s.mcpAllowedRoot(a.Repo, "have its eligible tickets listed")
	if err != nil {
		return nil, err
	}
	tickets, err := s.listEligibleTickets(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("listing eligible tickets failed: %w", err)
	}
	return EligibleResult{Repo: filepath.Base(root), RepoRoot: root, Tickets: tickets}, nil
}

func (s *Server) mcpGetEpic(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Repo string `json:"repo"`
		Epic string `json:"epic"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("get_epic arguments were not valid JSON")
	}
	epic := strings.TrimSpace(a.Epic)
	if !reTicketID.MatchString(epic) {
		return nil, fmt.Errorf("epic %q is not a valid ticket identifier", a.Epic)
	}
	root, err := s.mcpAllowedRoot(a.Repo, "have its epics previewed")
	if err != nil {
		return nil, err
	}
	subs, err := s.listEpicSubIssues(ctx, root, epic)
	if err != nil {
		return nil, fmt.Errorf("epic preview failed: %w", err)
	}
	return EpicPreviewResult{Repo: filepath.Base(root), RepoRoot: root, Epic: epic, SubIssues: subs}, nil
}

func (s *Server) mcpListRuns(args json.RawMessage) (any, error) {
	var a struct {
		Repo  string `json:"repo"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("list_runs arguments were not valid JSON")
	}
	repo, err := s.mcpRepo(a.Repo)
	if err != nil {
		return nil, err
	}
	s.importCheckpoints(repo)
	runs := s.collectRuns(repo.Root)
	if limit := mcpRows(a.Limit, mcpRunRows, maxMCPRunRows); len(runs) > limit {
		runs = runs[:limit]
	}
	return RunsResponse{Repo: repo.Name, Runs: runs}, nil
}

func (s *Server) mcpGetRun(args json.RawMessage) (any, error) {
	var a struct {
		Repo   string `json:"repo"`
		Ticket string `json:"ticket"`
		Events int    `json:"events"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("get_run arguments were not valid JSON")
	}
	repo, err := s.mcpRepo(a.Repo)
	if err != nil {
		return nil, err
	}
	ticket := strings.TrimSpace(a.Ticket)
	s.importCheckpoints(repo)
	s.importArtifacts(repo)
	view, ok := s.runViewFor(repo.Root, ticket)
	if !ok {
		return nil, fmt.Errorf("%s has never run in repo %q — call list_runs for the tickets it has", a.Ticket, repo.Name)
	}
	detail := s.runDetail(repo, ticket, view)
	total, err := s.stores.Tokens().Total(repo.Root, ticket)
	if err != nil {
		return nil, fmt.Errorf("read spend: %w", err)
	}
	return MCPRun{
		Repo:      repo.Name,
		RunView:   detail.RunView,
		Total:     spendResponse(total),
		Costs:     detail.Costs,
		Anomalies: detail.Anomalies,
		Verdict:   detail.Verdict,
		Artifacts: detail.Artifacts,
		Events:    s.mcpRunEvents(repo, ticket, a.Events),
		NoSkills:  detail.NoSkills,
		NoBrowser: detail.NoBrowser,
		Removed:   detail.Removed,
	}, nil
}

// mcpRunEvents reads the capped tail of a ticket's events. A store error degrades
// to an empty tail rather than failing an otherwise-readable run.
func (s *Server) mcpRunEvents(repo registry.Repo, ticket string, want int) []FeedEvent {
	rows, err := s.stores.Events().Query(repo.Root, hubstore.EventFilter{
		Ticket: ticket,
		Limit:  mcpRows(want, mcpEventTail, maxMCPEventTail),
	})
	if err != nil {
		logger.Verbosef("run events %s/%s: %v", repo.Name, ticket, err)
		return []FeedEvent{}
	}
	return feedList(rows)
}

func (s *Server) mcpListInstances(json.RawMessage) (any, error) {
	return map[string]any{"instances": s.instanceViews()}, nil
}

func (s *Server) mcpSteerAgent(args json.RawMessage) (any, error) {
	var a struct {
		Repo   string `json:"repo"`
		Ticket string `json:"ticket"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("steer_agent arguments were not valid JSON")
	}
	repo, err := s.mcpRepo(a.Repo)
	if err != nil {
		return nil, err
	}
	ticket := strings.TrimSpace(a.Ticket)
	if ticket == "" {
		return nil, errors.New("ticket is required — a steer note is queued against the ticket whose agent reads it")
	}
	body := strings.TrimSpace(a.Body)
	if body == "" {
		return nil, errors.New("a steer note needs a body — that text is what reaches the agent")
	}
	note, err := s.steerNote(repo, ticket, body)
	if err != nil {
		return nil, fmt.Errorf("queue steer note: %w", err)
	}
	return MCPSteerNote{Repo: repo.Name, SteerNoteView: steerNoteView(note)}, nil
}

func (s *Server) mcpListSteerNotes(args json.RawMessage) (any, error) {
	var a struct {
		Repo        string `json:"repo"`
		Ticket      string `json:"ticket"`
		PendingOnly bool   `json:"pending_only"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errors.New("list_steer_notes arguments were not valid JSON")
	}
	repo, err := s.mcpRepo(a.Repo)
	if err != nil {
		return nil, err
	}
	ticket := strings.TrimSpace(a.Ticket)
	if ticket == "" {
		return nil, errors.New("ticket is required — steer notes are listed one ticket at a time")
	}
	read := s.stores.Steer().List
	if a.PendingOnly {
		read = s.stores.Steer().Pending
	}
	notes, err := read(repo.Root, ticket)
	if err != nil {
		return nil, fmt.Errorf("list steer notes: %w", err)
	}
	return MCPSteerNotes{Repo: repo.Name, Ticket: ticket, SteerNotesResponse: steerNotesResponse(notes)}, nil
}

func mcpRows(want, fallback, ceiling int) int {
	if want <= 0 {
		return fallback
	}
	return min(want, ceiling)
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
	return s.mcpAllowedRoot(name, "have work queued or drained")
}

// mcpAllowedRoot resolves a tool's repo argument to a root the hub may run the
// binary in, applying the workspace gate the REST routes apply. The refusal names
// the action that was blocked, so the agent knows what registering would buy.
func (s *Server) mcpAllowedRoot(name, action string) (string, error) {
	root, ok := s.allowedRoot(strings.TrimSpace(name))
	if !ok {
		return "", fmt.Errorf("repo %q is observe-only; only a Registered repo can %s — register it first, or call list_repos for the repos that can", name, action)
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
