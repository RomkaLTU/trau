package main

import (
	"errors"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/folderrepo"
)

// folderCollisionRefusal refuses a run that would share a working tree with a
// live loop: a Folder repo run while one of its children is busy, and a child's
// run while the folder holding it is. It is the guard nothing bypasses — the hub
// spawns runs by exec'ing trau — so presence is read straight from the hub
// database rather than through an API that may not be answering. A takeover
// blocks like any other entry: a terminal session holds that working tree just as
// firmly. Presence that cannot be read refuses nothing.
func folderCollisionRefusal(repoRoot string) error {
	live, err := liveLoops()
	if err != nil {
		return nil
	}
	for _, e := range live {
		if !folderrepo.Collides(repoRoot, e.RepoRoot) {
			continue
		}
		return console.Actionable(
			errors.New(folderrepo.CollisionReason(e.Ticket, e.RepoRoot)),
			"start a run in "+repoRoot,
			"wait for that loop to finish, then start this run again")
	}
	return nil
}
