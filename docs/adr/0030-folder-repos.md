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
- **A dirty or off-base child does not abort the run.** (The off-base half is
  withdrawn by ADR 0032: each child ships to its own base and a clean one parked
  elsewhere is checked out onto it.) It is recorded by the
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

## Amendment (2026-08-01): a Folder repo outside the hub's drain

TRAU-20 made a Folder repo runnable through the hub, which execs
`trau --repo <root>`. Reaching it any other way — from the working directory, from
`trau doctor`, from a reset — still assumed the root was a git repository. Four
decisions close that:

- **Resolution from the working directory.** A folder root, and any non-git
  directory under it, resolves to the nearest ancestor Folder repo. Standing
  inside a Child repo is the ambiguous case, and registration breaks the tie: the
  child when it is registered in its own right, otherwise the Folder repo that
  holds it. Registration is only ever the tie-breaker — a registered set that is
  empty or unreadable leaves the git top-level, so it never stands between an
  operator and an unregistered folder they are standing in.
- **`.gitconfig.repo` is wired per ship-target child.** A folder root has no git
  config to hold an `include.path`, so the identity is pinned inside each child
  the ticket reached, at commit time: the child's own `.gitconfig.repo` when it
  has one, the folder root's otherwise — the same child-overrides-folder grain as
  `.trau/checks` and `LINT_FIX_CMD`.
- **Reset acts on the recorded children.** Every `SHIP_TARGETS` entry loses its
  branch locally and on the remote. Branches are cut lazily, so a run reset while
  parked in build has none — it has loose work instead, and that is discarded in
  exactly the children that changed since `START_DIRT`. The start-of-run census is
  what tells this run's work from the operator's, so a child holding unrelated WIP
  is left as it was found. `PurgeLocal` drops the same branches and no loose work;
  `Requeue` closes every PR in `PR_URLS`, not only the first; `Clear` still
  touches nothing.
- **A Folder repo and a repo inside it never run at once.** They share a working
  tree, so a Folder repo run is refused while any live entry sits in one of its
  children, and a child's run while one sits in the folder. Every live entry
  blocks, `StateTakeover` included — a terminal takeover holds that tree just as
  firmly as a loop. The hub answers 409 at queue start so the board states the
  reason before spawning; the CLI refuses again just before the run starts, which
  is the guard nothing bypasses.

The web diff pane runs the existing per-root diff machinery once per changed child
and merges the results into one flat `files[]`, each path rooted under the child's
name and each entry carrying `repo`. `head_sha` and `base_sha` are empty for a
folder run — N repositories have N of each — while `base` and `branch` stay as
they are, one of each shared by every child.

## Amendment (2026-08-02): the fan-out is crash-safe and reviewable

TRAU-23 keeps the fan-out as it is and closes what a killed run and a reviewer
each ran into.

- **Proofs are published, to the first changed child in sorted order.** The
  consequence above ("Verify proofs are not published for a Folder repo run") is
  superseded. A folder has no remote of its own, but its ship set does, and one of
  them is as good a host as any as long as the choice is deterministic — every PR
  body links that one branch. Picking a host is strictly better than dropping the
  QA gallery from a cross-repo change.
- **The ship set is stamped incrementally, and a resume unions three readings.**
  `SHIP_TARGETS`, `PR_URLS` and the new `FORK_POINTS` are written after each child
  rather than after the loop, so a run killed mid-fan-out leaves nothing it cut or
  opened outside the checkpoint. A resumed run then ships the children still
  carrying loose work ∪ the recorded set ∪ every child holding the ticket's branch:
  a child committed just before the death reads clean again, and dropping it is how
  half a cross-repo change ships. The last two readings are a resume's alone — a
  fresh run ships only what its own build changed. Both a branch name and the
  checkpoint keys belong to the ticket rather than to the attempt, so what an
  abandoned earlier run of the same ticket left in a child must never be branched,
  pushed and PR'd by a run that never touched that child.
- **Sibling links are a second pass.** A PR's URL exists only after `gh pr create`
  returns, so the first body written cannot carry the last one's link. Once every
  PR exists each body is rewritten through `UpdatePRBody` (`gh pr edit --body`) with
  a **Ships with** list. A rewrite gh refuses warns and does not fail the run — it
  is cosmetic, and stranding a green cross-repo change over a body edit is worse.
- **Fork points are per child.** Each child's branch pins the commit it was cut at
  at commit time — the merge base with the base branch when the branch was already
  there and the attempt that cut it never got as far as recording a pin — so every
  shipped child has one. The diff pane resolves that child's pin instead of the
  single `BASE_SHA` a plain Repo's run records. A folder holds as many bases as
  repositories and each advances on its own while a long run works the others, so a
  shared pin would report a sibling's merges as this run's work in most panes.
- **Settling asks about every PR.** The hub's reconcile sweep read the singular `PR`
  and ran `gh` in the repo root, which for a folder is not a git repository at all.
  It now walks `PR_URLS`, asks in each child, and settles the item only when all of
  them merged — one merged sibling must not close a queue item over PRs still open.
- **The Runs ledger carries the plural set.** `RunView` gained `ships[]`, one entry
  per changed child with its PR, projected off the checkpoint the same way
  `pr_status` and `release` are. `pr`/`pr_url` still name the first target.
