# ADR 0030 — A folder of repositories registers as one Repo

- **Status:** Accepted
- **Date:** 2026-07-31
- **Deciders:** Romas (sole maintainer)
- **Ticket:** TRAU-20

## Context

`C:\PROJECTS\PortalProLT` holds 44 git repositories, `PortalPro` 49 and
`PortalProPL` 48, and all of them sit behind one Azure DevOps board. The only
shipped way to take such a folder was the add-project wizard's scan
(`internal/webserver/fsdiscover.go`): each child registers as its own Repo and
they are grouped into a hub Project.

That gives 44 mirrors of the same board, 44 queues, 44 Runs ledgers, and a
start-repo picker (`internal/webserver/projectstart.go`) asking which member a
ticket belongs in — and a run that can only ever touch the one member it was
started in. A work item spanning `api-apigateway` and `api-companies` could not
be delivered at all.

Everything below the registration was single-repo by construction:
`validateRepoPath` refused a root without `.git`, `ExecGit{Repo: root}` and the
`Git` interface bind one repository for a whole run, `checks.Load(repoRoot)` runs
once at pipeline construction, and `lintfix`'s `c.Dir` is the repo root.

## Decision

**A folder registers as one Repo — a Folder repo — and a run ships a branch and
a PR in each Child repo it changed.**

- **The folder is one Repo, not a project of members.** One board mirror, one
  queue, one Runs ledger, no start-repo picker. Child repos are never
  registered: `folderrepo.Children` finds the git repositories directly inside
  the folder at run time and they are used only as ship targets. Registering a
  folder neither requires nor removes any child's own registration.
- **Nothing is prepared up front beyond a cheap sweep.** At run start each child
  is read for a dirty tree and for sitting on its base branch — `status
  --porcelain` plus `rev-parse --abbrev-ref HEAD`, which across fifty children
  costs milliseconds where fifty fetches and checkouts would cost minutes and
  would trample checkouts the ticket never goes near.
- **A dirty or off-base child does not abort the run.** It is recorded by the
  sweep and named to the build agent as off limits. The run gives up only if a
  change actually lands in one of them, because that change is entangled with
  work trau does not own.
- **A child changed when its tree stops reading the way the sweep found it.**
  The sweep fingerprints the uncommitted work each child already carried and the
  ship set is the difference against that fingerprint, so a stray file an
  operator left behind is neither shipped as this run's work nor mistaken for
  the build reaching into an off-limits child. One definition of dirty serves
  both — untracked files included, since a build that only adds files changed
  the child as surely as one that edits it.
- **Feature branches are cut lazily, at commit time, only in the children that
  changed.** A child the ticket never reached is never left holding an empty
  branch.
- **Verify and lint run at the child's grain.** Each changed child's own
  `.trau/checks` and its own `.trau.ini` `LINT_FIX_CMD` apply inside that child,
  with the folder root's as the fallback — the ADR 0019 workspace grain with the
  Child repo as the workspace. Forty-four services do not share a build command.
- **Ship fans out: one branch and one PR per changed child, the same branch name
  in each.** Nothing merges until every PR is green; a red one goes through the
  existing repair loop and the ticket settles only once all of them merge. A
  half-merged cross-repo change is the one outcome that breaks deployed
  services.
- **Epics are refused, their sub-issues are not.** `epic/<id>-slug` stacking per
  child multiplies the most stateful part of the pipeline, so queueing an epic
  into a Folder repo answers 409 with `pipeline.ErrFolderRepoEpic` and a CLI epic
  run refuses with the same message before it starts. The remedy that refusal
  names has to work: a sub-issue queued on its own stays a ticket there — the
  queue's epic promotion is off in a Folder repo — and it is built off each
  child's base rather than stacked on its siblings, whatever `EPIC_FLOW` says.

The plural ship set rides the checkpoint as `SHIP_TARGETS` and `PR_URLS`, two new
free-form keys, with `PR` and `PR_URL` still naming the first target so every
surface built for one PR keeps reading. The sweep rides it as `OFF_LIMITS` and
`START_DIRT`: it is taken once, before the build, and a resumed run reads the
recorded census back instead of re-sweeping — by then the work the run itself
left in the children is indistinguishable from the WIP the sweep exists to
protect. `Pipeline.GitAt` and
`Pipeline.GitHubAt` bind the existing `Git`/`GitHub` seams to one child, so
nothing about the single-repo path changes.

## Consequences

- A Folder repo's ship phase always commits deterministically — one templated
  Conventional Commit per changed child, the same message in each — instead of
  running the commit agent. The agent commit assumes one worktree, and a
  mechanical fan-out is what a uniform cross-repo change wants anyway.
- `AUTO_MERGE=0` in a Folder repo stops at "green, merge these yourself" and
  lists every PR, rather than blocking in the single-PR manual-merge wait. The
  gate that matters — all green before anything lands — still runs.
- Verify proofs are not published for a Folder repo run: `internal/proofsbranch`
  pushes an orphan branch to *the* repo's remote, and there is no single one.
- The discovery scan's cap moves from 64 to `folderrepo.MaxChildren` (256) and
  `FSDiscoverResponse` gained `truncated`, so a folder of 300 is never reported
  as holding 64.
- `CONTEXT.md` gains **Folder repo** and **Child repo**. The **Repo** entry is
  unchanged: a Folder repo is a Repo, and the two new entries define themselves
  against it.
- Out of scope and unchanged: nested Folder repos, children deeper than the
  folder's immediate subdirectories, and migrating an existing 44-member Project
  into a Folder repo — register the folder and forget the members by hand.
