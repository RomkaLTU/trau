package hubstore

import (
	"database/sql"
	"path/filepath"
)

// Stores is the hub's set of SQLite-backed stores. Every store but transcripts is
// over the one authoritative database the serve process opens; transcripts live in
// a separate transcripts database (ADR 0008 §4) so their bulk never bloats the hot
// store, held here for one hub-owned object rather than raw handles. The caller
// owns both databases' lifecycles.
type Stores struct {
	db          *sql.DB
	repos       *Registrations
	issues      *Issues
	tokens      *Tokens
	checkpoints *Checkpoints
	events      *Events
	artifacts   *Artifacts
	lessons     *Lessons
	drains      *DrainOutcomes
	phaseLogs   *PhaseLogs
	instances   *Instances
	transcripts *Transcripts
	grill       *Grill
	atlas       *AtlasDocuments
	notifs      *Notifications
	prompts     *PromptOverrides
	attachments *Attachments
	qa          *QAAccounts
	routing     *Routing
	steer       *SteerNotes
}

// NewStores builds the hub store set over the authoritative database db and the
// separate transcripts database, each authoritative store pruned to its retention
// window. A nil transcriptsDB yields an inert transcript store (tests). home is
// the trau home the databases were opened under; attachment bytes live beside
// them, so a test home keeps a test's blobs out of the real one.
func NewStores(home string, db, transcriptsDB *sql.DB, retention Retention) *Stores {
	return &Stores{
		db:          db,
		repos:       NewRegistrations(db),
		issues:      NewIssues(db),
		tokens:      NewTokens(db, retention.TokenCalls),
		checkpoints: NewCheckpoints(db),
		events:      NewEvents(db, retention.Events),
		artifacts:   NewArtifacts(db),
		lessons:     NewLessons(db),
		drains:      NewDrainOutcomes(db),
		phaseLogs:   NewPhaseLogs(db),
		instances:   NewInstances(db),
		transcripts: NewTranscripts(transcriptsDB, retention.Transcripts),
		grill:       NewGrill(db, retention.Grill),
		atlas:       NewAtlasDocuments(db),
		notifs:      NewNotifications(db),
		prompts:     NewPromptOverrides(db),
		attachments: NewAttachments(db, filepath.Join(home, AttachmentsDir), retention.AttachmentCacheBytes),
		qa:          NewQAAccounts(db),
		routing:     NewRouting(db),
		steer:       NewSteerNotes(db),
	}
}

// Registrations returns the registration store.
func (s *Stores) Registrations() *Registrations { return s.repos }

// Issues returns the issue store.
func (s *Stores) Issues() *Issues { return s.issues }

// Tokens returns the authoritative token-call and anomaly store.
func (s *Stores) Tokens() *Tokens { return s.tokens }

// Checkpoints returns the authoritative per-ticket checkpoint store.
func (s *Stores) Checkpoints() *Checkpoints { return s.checkpoints }

// Events returns the authoritative event store.
func (s *Stores) Events() *Events { return s.events }

// Artifacts returns the authoritative per-run artifact store.
func (s *Stores) Artifacts() *Artifacts { return s.artifacts }

// Lessons returns the authoritative per-repo lessons ledger.
func (s *Stores) Lessons() *Lessons { return s.lessons }

// DrainOutcomes returns the store of how each queued child exited.
func (s *Stores) DrainOutcomes() *DrainOutcomes { return s.drains }

// PhaseLogs returns the authoritative per-run phase-log store.
func (s *Stores) PhaseLogs() *PhaseLogs { return s.phaseLogs }

// Instances returns the store of the live loops' presence.
func (s *Stores) Instances() *Instances { return s.instances }

// Transcripts returns the chunked transcript store over the transcripts database.
func (s *Stores) Transcripts() *Transcripts { return s.transcripts }

// Grill returns the web grilling session store.
func (s *Stores) Grill() *Grill { return s.grill }

// Atlas returns the store of agent-generated Atlas View documents.
func (s *Stores) Atlas() *AtlasDocuments { return s.atlas }

// Notifications returns the durable needs-attention notification store.
func (s *Stores) Notifications() *Notifications { return s.notifs }

// Prompts returns the prompt override store.
func (s *Stores) Prompts() *PromptOverrides { return s.prompts }

// Attachments returns the issue attachment index and its blob store.
func (s *Stores) Attachments() *Attachments { return s.attachments }

// QA returns the per-repo QA accounts and notes store.
func (s *Stores) QA() *QAAccounts { return s.qa }

// Routing returns the store of the routing fingerprint each repo last ran under.
func (s *Stores) Routing() *Routing { return s.routing }

// Steer returns the per-ticket steer-note queue.
func (s *Stores) Steer() *SteerNotes { return s.steer }

// Queue returns the queue store for a repo root.
func (s *Stores) Queue(root string) *Queue { return NewQueue(s.db, root) }

// ImportLegacyQueues imports the file-era queue.json of every repo the hub
// already tracks — known and web-registered — into the queue tables, removing
// each file after its rows commit. A failed import returns an error naming the
// file and leaves it in place, so serve startup can abort without losing a
// queue. Repos registered later import their queue.json lazily on first touch.
func (s *Stores) ImportLegacyQueues() error {
	roots, err := s.queueRoots()
	if err != nil {
		return err
	}
	for _, root := range roots {
		if err := s.Queue(root).ImportLegacy(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stores) queueRoots() ([]string, error) {
	known, err := s.repos.Known()
	if err != nil {
		return nil, err
	}
	registered, err := s.repos.Registered()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(known)+len(registered))
	roots := make([]string, 0, len(known)+len(registered))
	add := func(root string) {
		if root == "" {
			return
		}
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	for _, repo := range known {
		add(repo.Root)
	}
	for _, root := range registered {
		add(root)
	}
	return roots, nil
}
