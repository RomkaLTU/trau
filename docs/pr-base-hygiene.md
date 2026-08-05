# PR base hygiene

Two lessons from a run whose one-commit slice opened as a 19-commit, 301-file pull
request. Both are about the same thing: **GitHub diffs a PR against the base branch
as the remote has it**, not as your machine has it.

## A PR whose diff dwarfs the run means the remote base is behind

The symptom is unmistakable — trau pushed one commit, and the PR page says 19 commits
and hundreds of files, most of them untouched by the ticket. The cause is not the
branch: it is the base. Local `main` had an epic merged into it and was never pushed,
so `origin/main` sat 18 commits behind the commit the slice branch was cut from.
GitHub, comparing the branch against the older `origin/main`, attributed all of that
base drift to the PR.

The fix is a fast-forward push of the base:

```bash
git push origin main
```

GitHub recomputes the diff and the PR collapses back to the run's own work.

Trau now does this itself. Before any PR is opened against `BASE_BRANCH` it checks —
with `ls-remote`, never a remote-tracking ref, which a failed push leaves looking
exactly like a successful one — that the remote base contains the run's recorded fork
point. If it does not, the base is pushed (a plain push; trau never force-pushes) and
re-checked. A base that is still behind after that, because the branch is protected or
the histories have diverged, fails the phase **before the PR exists**, with a message
naming the remote branch and the missing commit; the slice's work stays on its branch
and the ticket resumes once an operator syncs the base. The same gate guards the epic
PR and each child PR in a Folder repo, and `EnsureCleanBase` pushes the fast-forward
best-effort at run start so the drift is usually gone before a branch is even cut.

The merge-time check (`foreignWorkInPR`) stays as defense in depth: it catches a PR
whose commit count dwarfs the run's own and refuses the merge.

## Never retarget a PR's base onto an orphan branch

Retargeting a PR's base is safe **only toward a branch that shares history with the
head branch**. Pointing a PR at an orphan branch — a proofs branch, or any branch
created with no common ancestor — makes GitHub close the PR, and **that close is
irreversible**: the reopen check validates against the base recorded at the moment of
closing, so grafting shared history onto the branch afterwards does not unblock it.
The only way forward is a new PR.

If a PR's diff needs re-anchoring after the base was pushed, do one of these instead:

- wait for GitHub's mergeability recompute (usually seconds), or
- push an empty commit on the **head** branch (`git commit --allow-empty`) to force it.

Base-toggling (switch the base to another branch and back) works only between branches
that share history with the head, and is never worth the risk when the two options
above cost nothing.

## Carve-out: stacked epics (`EPIC_STACKED_PRS`)

An epic running in the opt-in stacked shape puts one PR's base on another PR's head
branch: child N targets child N−1's branch, and only the bottom PR targets
`BASE_BRANCH`. Both rules above still hold, unchanged — **trau never force-pushes and
never retargets a PR** — but the reasoning needs one addition, because in this shape
GitHub itself will retarget and rebase.

What GitHub does to a stack, observed live (`docs/research/stacked-prs-spike.md`, Q6):
merging part of a stack retargets every PR above the merge point onto the new base
**and non-fast-forward rewrites their head branches**. A run holding one of those
branches would find its recorded head SHA gone from the remote, its next push rejected
as non-fast-forward, and its fork point unrecognizable. That is exactly the hazard
this document exists to prevent — except performed by the forge, with no involvement
from trau at all.

The stacked shape therefore merges **once, at the very end**: nothing lands until
every layer is green, and then the whole stack merges from its top PR in a single
all-or-nothing operation. The consequences:

- **GitHub owns retarget and rebase at merge time only.** While the epic is in
  flight there is no partial merge, so there is no rewrite for trau to absorb.
- **Every base under an open PR is a branch trau owns.** A slice branch does not
  move once its layer is complete, so the sibling-squash drift the base gate exists
  for cannot occur between layers. The gate still runs on every layer: it proves the
  remote copy of the layer below carries the commit the layer above was cut from,
  repaired — as everywhere else — by a plain push.
- **A stack trau cannot finish is handed to a human whole.** A red layer, a closed
  layer, or a stack merge GitHub refuses parks the epic; trau does not merge around
  the problem, because merging part of the stack is what triggers the rewrite.

The classic epic flow is unchanged, and it is what runs whenever the flag is off, the
repo is not on GitHub, the run delivers locally, an epic branch already owns the
epic's children, or GitHub's stacked-PRs preview does not answer. The shape is decided
once, at the epic's start.
