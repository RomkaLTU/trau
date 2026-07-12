package webserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// ingestInterval is how often the hub folds newly-appended run artifacts into the
// derived tables while a loop is live. A package var so tests can tighten it.
var ingestInterval = time.Second

// runIngest keeps the derived tables current for the life of ctx: an immediate
// pass rebuilds from whatever is already on disk, then a tick picks up appends. It
// sits alongside sweepKnownRepos as the write side of the derived projection, off
// every request path.
func (s *Server) runIngest(ctx context.Context) {
	s.ingestPass()
	t := time.NewTicker(ingestInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ingestPass()
		}
	}
}

// ingestPass tails every known repo's events and token files into the derived
// tables. Ingestion is best-effort per repo and per source: a torn line,
// rewritten file, or read error is skipped or resynced, never propagated, so it
// can never take the hub down (ADR 0007 §3). Checkpoints are no longer tailed —
// the loop writes them straight to the authoritative table over HTTP (ADR 0008).
func (s *Server) ingestPass() {
	d := s.stores.Derived()
	for _, repo := range s.streamRepos() {
		ingestEvents(d, repo)
		ingestTokens(d, repo)
	}
}

func ingestEvents(d *hubstore.Derived, repo registry.Repo) {
	path := eventsPath(repo.RunsDir)
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	cur, err := d.EventCursor(repo.Root)
	if err != nil {
		logger.Verbosef("ingest events cursor %s: %v", repo.Name, err)
		return
	}
	resync := cur > info.Size()
	from := cur
	if resync {
		from = 0
	}
	if !resync && from >= info.Size() {
		return
	}
	feed, next, ok := scanEventsFrom(path, from)
	if !ok || (!resync && len(feed) == 0) {
		return
	}
	rows := make([]hubstore.EventRow, 0, len(feed))
	for _, fe := range feed {
		seq, _ := parseOffset(fe.ID)
		rows = append(rows, hubstore.EventRow{
			Seq:    seq,
			TS:     fe.Time,
			Kind:   fe.Kind,
			Phase:  fe.Phase,
			Msg:    fe.Msg,
			Fields: marshalMap(fe.Fields),
		})
	}
	if err := d.IngestEvents(repo.Root, resync, rows, next); err != nil {
		logger.Verbosef("ingest events %s: %v", repo.Name, err)
	}
}

func scanEventsFrom(path string, from int64) ([]FeedEvent, int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, 0, false
	}
	feed, next := scanFeed(bufio.NewReader(f), from)
	return feed, next, true
}

func ingestTokens(d *hubstore.Derived, repo registry.Repo) {
	matches, _ := filepath.Glob(filepath.Join(repo.RunsDir, "*", "tokens.jsonl"))
	for _, path := range matches {
		ingestTokenFile(d, repo, filepath.Base(filepath.Dir(path)), path)
	}
}

func ingestTokenFile(d *hubstore.Derived, repo registry.Repo, ticket, path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	cur, err := d.TokenCursor(repo.Root, ticket)
	if err != nil {
		logger.Verbosef("ingest tokens cursor %s/%s: %v", repo.Name, ticket, err)
		return
	}
	resync := cur > info.Size()
	from := cur
	if resync {
		from = 0
	}
	if !resync && from >= info.Size() {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return
	}
	calls, next := tokens.ScanCalls(bufio.NewReader(f), from)
	if !resync && len(calls) == 0 {
		return
	}
	rows := make([]hubstore.TokenRow, 0, len(calls))
	for _, c := range calls {
		rows = append(rows, hubstore.TokenRow{
			Seq:           c.Offset,
			TS:            c.TS,
			Phase:         c.Phase,
			Input:         c.Input,
			Output:        c.Output,
			CacheRead:     c.CacheRead,
			CacheCreation: c.CacheCreation,
			Reasoning:     c.Reasoning,
			Total:         c.Total,
			CostUSD:       c.CostUSD,
			Turns:         c.Turns,
			IsError:       c.IsError,
			Provider:      c.Provider,
			Model:         c.Model,
			Context:       c.Context,
			Skills:        marshalStrings(c.Skills),
		})
	}
	if err := d.IngestTokens(repo.Root, ticket, resync, rows, next); err != nil {
		logger.Verbosef("ingest tokens %s/%s: %v", repo.Name, ticket, err)
	}
}

func marshalMap(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

func marshalStrings(s []string) string {
	if len(s) == 0 {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}
