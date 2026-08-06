# ADR 0032 — A repository's forge and base branch are read from its own remote

- **Status:** Accepted
- **Date:** 2026-08-02
- **Deciders:** Romas (sole maintainer)
- **Ticket:** TRAU-33

## Context

A team keeping ~40 git repositories in one folder and its tickets in Azure
DevOps met four separate things onboarding told them that were not true.

Nothing anywhere identified which code host a repository was on. `inspectGit`
read `origin`'s URL and threw the host away, the tracker step asked for an Azure
DevOps organization URL and said nothing about the git remote, and delivery is
GitHub-only (`gh` CLI). A repo hosted elsewhere passed onboarding cleanly and
died at `gh pr create` — after the build, handoff and verify agents had been paid
for.

Default-branch detection was worse than absent. `inspectGit` and `inspectChild`
read `refs/remotes/origin/HEAD` and, when that ref was missing — `git clone`
writes it, plenty of working copies do not have it — fell back to `rev-parse
--abbrev-ref HEAD`, the branch that happened to be checked out. `api-administrators`
(default `master`) reported as `azure-devops-generic-pipeline`. That value skewed
`majorityBranch` and was prefilled straight into `BASE_BRANCH`.

And ADR 0030 §2 forced one `BASE_BRANCH` on every child of a Folder repo: in a
folder that is mostly `master` with one repo on `main`, that repo was permanently
unshippable, and every child parked on a feature branch was skipped even when
clean — the opposite of what `EnsureCleanBase` does for a plain Repo.

## Decision

**Where a repository is hosted and which branch it ships to are facts read from
that repository's own remote, per repository and per Child repo.**

- **`internal/forge` owns both facts and imports nothing else of trau's.** It
  identifies a forge from a remote URL across git's URL, scp-style and local-path
  forms, and reads a default branch from `refs/remotes/<remote>/HEAD` first,
  falling back to one budgeted `ls-remote --symref` for the working copies that
  ref is missing from. The checked-out branch is never consulted for either.
- **Nothing is inferred from the tracker and nothing is assumed.** GitHub code
  with Azure DevOps tickets is one combination among many. A host the table does
  not know is `unknown`, not GitHub; the `FORGE` key names it, per repo and — in
  its own `.trau.ini`, never inherited from the folder root — per child.
- **Delivery stays GitHub-only, and that is stated before a run spends
  anything.** `EnsureCleanBase` refuses a plain Repo hosted elsewhere before the
  ticket is picked; a Folder repo's off-limits sweep names such a child and ships
  to the rest. This ADR identifies forges; it does not add a second delivery path.
  *(Superseded by ADR 0036: delivery now also reaches Bitbucket Cloud, refused at
  the same point and by the same choke point. Everything else here stands.)*
- **Each Child repo ships to its own base** — its own `.trau.ini`
  `BASE_BRANCH`, else the branch its own remote calls default, else the folder's.
  The sweep records the bases on the checkpoint so a resumed run branches exactly
  where the run that started it did.
- **Standing off base stops being a reason to skip a child.** ADR 0030 §2's
  off-base condition is withdrawn: a clean child parked elsewhere is checked out
  onto its base by `sweepFolder`, and only a checkout git refuses puts it off
  limits. Dirty, unreadable and non-GitHub remain the reasons.

## Consequences

- A folder whose children span several forges is shippable for the GitHub part of
  it and honest about the rest, at onboarding, in doctor, and in the sweep.
- The sweep's cost per child rises by a remote URL read and, for a clone with no
  `origin/HEAD`, one `ls-remote`. Both are bounded by `SweepConcurrency`, and the
  result is checkpointed rather than re-read on resume.
- A repository trau cannot deliver to now fails doctor and the onboarding
  detection report rather than passing them and failing the run.
