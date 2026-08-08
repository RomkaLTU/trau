# ADR 0044 — Runs are isolated in per-ticket git worktrees, not clones

- **Status:** Accepted
- **Date:** 2026-08-08
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-1581 (supersedes the de-queue verdict in `tasks/LOOP-61-worktree-qa-research.md`)

## Context

Until now every run of a repo shared one checkout: the registered root. That is
the source of the loop's most disruptive behaviour. A run that finds uncommitted
work in the tree has to do something with it, so `EnsureCleanBase` stashes it or
commits it back to whatever branch an interrupted run left behind, and then checks
the base out — in the directory a human may be standing in. It also makes
concurrency impossible in principle: two runs in one working tree cannot both hold
a branch.

`tasks/LOOP-61-worktree-qa-research.md` looked at exactly this in July and said
**no**: a fresh worktree lacks `.env`, `vendor/`, `node_modules/`, the project's
`.trau.ini`, `.trau/checks/`, `.gitconfig.repo` and `.agents/`, so verify would run
against a tree that cannot build and browser QA would drive an app still serving
the base branch. Its conclusion — "the missing piece is a whole
workspace-provisioning subsystem trau has zero concepts for today" — was correct
about the gap and wrong only about it being unbridgeable. This ADR is that
subsystem, and it supersedes the de-queue verdict.

Two shapes were on the table:

- **Copy-on-write clones** (the COD-1171 "lanes" framing): a full second checkout
  per run, cheap on APFS/btrfs, ordinary and expensive everywhere else.
- **Linked git worktrees**: one shared object store and ref namespace, one
  checked-out tree per run.

## Decision

**Runs are isolated in linked git worktrees, one per ticket, provisioned by the
run itself and removed when the ticket settles.** `WORKTREES=1` turns it on per
repo; `WORKTREES=0` — the default — changes nothing at all.

### Why worktrees beat CoW clones here

- **One ref namespace.** The whole pipeline is branch work: cut `feature/<id>`,
  push it, open the PR, merge, delete it. A worktree shares the repo's refs and
  objects, so a branch cut inside a tree is the same branch the registered root
  sees, and the merge that ships it needs no synchronisation step. Clones would
  need a push/fetch dance between two object stores for every one of those.
- **Portable, not filesystem-dependent.** CoW is fast on APFS and btrfs and is a
  full copy on ext4, on a network mount, and on CI. `git worktree add` costs one
  checkout everywhere.
- **git already polices the invariant we need.** A branch can be checked out in
  exactly one tree. That is precisely the concurrency rule this feature must
  enforce, and getting it from git rather than from our own bookkeeping means it
  cannot drift.
- **Already partly modelled.** `WorktreeHolding` and the detached-base path exist
  because the loop already had to survive a user's own worktree.

### The provisioning contract

A tree is not just a checkout; it is a checkout plus the things git deliberately
does not carry. Provisioning is, in order:

1. `git fetch <remote> <base>` at the registered root — best effort, so an
   unreachable remote narrows the run rather than failing it.
2. `git worktree add <path> <branch>` when the ticket's branch already exists
   (resume/adopt), else `git worktree add --detach <path> <base-tip>`, with the
   branch cut inside the tree by the existing `resolveBuildBranch` flow.
3. **Copy** what git ignores and the run cannot work without. Unconditionally:
   `.trau.ini`, `.trau/` minus `RUNS_DIR`, `.gitconfig.repo`, and `.agents/` when
   untracked. By glob (`WORKTREE_COPY`, default `.env,.env.*`): only files git
   *ignores* at the registered root, because tracked content already arrived with
   the checkout and copying it would shadow the commit the tree is on. Copying is
   Go, not `cp`, so it behaves the same on every platform.
4. **Setup** (`WORKTREE_SETUP_CMD`, `sh -c`, cwd = the fresh tree): the `npm ci` or
   `composer install` step that answers LOOP-61's `vendor/`/`node_modules`
   objection. A non-zero exit parks the run Faulted with the command's output kept
   as a run artifact, and **the tree is kept** — the output is only actionable next
   to the tree it failed in.

Steps 3 and 4 run **only on creation**. An adopted tree keeps its files and its
work in progress, and pays for setup once.

Provisioning is idempotent by construction: the path is a pure function of
configuration, so a resume finds the tree it made last time and adopts it. A branch
some *other* tree holds — the user's own checkout, or a tree a crash left behind —
parks the ticket naming that tree, rather than surfacing git's refusal from three
layers down.

### The repo-identity rule

**The tree is where the work happens; the registered root is who the work belongs
to.** Git, the agent phases, the run directory and every tracked-file lookup follow
`WorkTree`. Checkpoints, artifacts, phase logs, events, presence, proofs, lessons,
the queue and the worktrees table itself are all keyed by `RepoRoot`. Nothing the
hub keys a record by may read the tree path. This is what lets a run move into a
tree and out of it again — and lets the tree be deleted on settle — without any run
history following it.

The corollary is that `WORKTREES_DIR` must never sit inside a registered repo or a
folder repo. A tree there would be untracked content in the checkout it exists to
stay out of, and `folderrepo.IsRepo` — which reads a `.git` *file* as a repo,
exactly what a linked worktree has — would take every tree for another repository.
Provisioning refuses such a directory outright; the default,
`<TRAU_HOME>/worktrees`, is outside every repo by construction.

### Lifecycle

The hub keeps one row per `(repo, ticket)` in `worktrees`, state ∈
`active | settled | orphaned`. The child reports on creation and on adoption. The
path is computed on both sides, so the row is a lifecycle record and never the
source of truth for where a tree is.

Removal happens **wherever a settle already happens** — a merged drain outcome, a
queue reconcile that finds shipped work, Reset, Requeue, PurgeLocal, and the manual
Remove on the run detail. Each runs `git worktree remove --force` plus
`git worktree prune` against the registered root and marks the row settled. The
CLI `--reset` / `--requeue` / `--reset-local` paths remove the tree themselves for
the same reasons, so the CLI and the hub agree without either having to trust the
other to have run first. Order matters: the tree goes before the branch, because
git refuses to delete a branch a worktree still holds — and with worktrees on the
branch-drop paths no longer check anything out in the registered root, since the
branch lives in a tree that has just been removed and the user's checkout is not
ours to switch.

On boot the hub reconciles rows against the disk: an active row whose directory is
gone becomes `orphaned`, a settled row whose directory survived is pruned now, and
`git worktree prune` runs per repo so git's registry cannot keep a record that would
make the next `worktree add` refuse the path.

A give-up does **not** give up its tree. It settles the queue row, but the tree is
what a human needs to read afterwards.

An **Epic takes one tree for the whole epic**, keyed by the epic id rather than by
each sub-issue. Its children are serial by construction — one process, each child
building on the epic branch — and the release merges that same branch, so the first
child provisions the tree, every later child and the finalize adopt it, and it
settles with the epic rather than with any child. A hand-off keeps it until the epic
PR lands, since that is the tree the human's merge is about. The merge happening in
that tree, and not in the registered checkout, is what lets a releasing epic hold
only its own lane: a repo without worktrees still freezes whole, because there the
release really does own the checkout every other run would share.

## Consequences

- With `WORKTREES=1`, a dirty checkout neither blocks a run nor gets stashed. The
  pre-run clean-base flow is replaced by a base fetch and nothing else.
- The concurrency ceiling stays at 1 in this slice. Nothing here assumes it: the
  drain is otherwise untouched, and the per-ticket path and the branch-holding
  check are the two pieces a higher ceiling needs.
- A folder repo ignores `WORKTREES=1` with a logged notice: a folder has no
  repository at its root for git to add a tree to. Folder runs keep today's
  behaviour.
- `WORKTREES=0` is byte-for-byte today's behaviour. Every worktree code path is
  behind `worktreesOn()`.
- Disk cost is one checkout per in-flight ticket plus whatever the setup command
  installs. Trees are removed on settle, so the steady state is bounded by the
  number of unsettled tickets — and by the orphan reconcile, which is what stops a
  crash from leaking one forever.
- LOOP-61's objections are answered rather than dismissed: `.env` and friends by
  the copy step, `vendor/`/`node_modules` by the setup command, trau's own
  machinery by the unconditional copy set. Its browser-QA objection — a dev server
  parked on the registered checkout still serves the base branch — is real and
  outlives this slice; serving an app out of a tree is the next one, which is why
  the schema leaves room for port/app columns.
