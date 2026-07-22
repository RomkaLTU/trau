package webserver

import (
	"context"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// purgeCleanupTimeout bounds one purged ticket's cleanup child: long enough for a
// round trip to a slow remote, short enough never to pin the hub on a dead one.
const purgeCleanupTimeout = 2 * time.Minute

// purgeGitFootprint drops the git footprint a hard-deleted family leaves behind.
// For every purged identifier it spawns `trau --reset-local <ID>`, which takes down
// the ticket's branch (local + remote) and run directory and nothing else: the run
// history stays browsable per Issues.Purge, and a tombstoned ticket's upstream issue
// is not trau's to touch. Spawning rather than running git in-process keeps the
// single-writer discipline the reset endpoint already follows.
//
// It runs in the background on the hub's own context so the DELETE that ordered it
// answers immediately; failures are logged and nothing else. The children run one at
// a time because each checks the working tree out, and a repo with a run in flight is
// skipped whole — that loop owns the working tree.
func (s *Server) purgeGitFootprint(repo registry.Repo, ids []string) {
	go func() {
		if s.drain.repoLive(repo.Root) {
			logger.Verbosef("purge %s: a run holds this repo — the branches and runs state of %s stay behind",
				repo.Name, strings.Join(ids, ", "))
			return
		}
		for _, id := range ids {
			s.resetLocalChild(repo, id)
		}
	}()
}

func (s *Server) resetLocalChild(repo registry.Repo, id string) {
	ctx, cancel := context.WithTimeout(s.drainCtx, purgeCleanupTimeout)
	defer cancel()
	if _, err := s.sup.Capture(ctx, SpawnSpec{
		Dir:  repo.Root,
		Args: []string{"--repo", repo.Root, "--reset-local", id, "--no-tui"},
		Env:  childEnv(s.home),
	}); err != nil {
		logger.Verbosef("purge %s: git cleanup for %s: %v", repo.Name, id, err)
	}
}
