# ADR 0036 — Delivery is forge-neutral, and Bitbucket Cloud is the second forge

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-1514

## Context

ADR 0032 identified every repository's forge and then refused all but one of
them: *"Delivery stays GitHub-only… This ADR identifies forges; it does not add a
second delivery path."* Bitbucket was already a first-class `forge.Forge` value
with host detection, a label and a `FORGE` name — and `Forge.Unsupported()` was
the single line that turned that identification into a refusal.

Everything below that line assumed one host. One 18-method `pipeline.GitHub`
interface, whose sole implementation shells to `gh`, carried the whole closing
chain; seven more places shelled `gh` outside it; the PR body built
`raw.githubusercontent.com` URLs; the merge gate's checkless-by-design detection
read `.github/workflows` and nothing else; doctor and the onboarding wizard made
an authenticated `gh` a hard gate for every repository, GitHub or not. There was
no forge credential plumbing at all — auth was entirely inherited from `gh auth`.

Atlassian ships no Bitbucket CLI. `acli` covers Jira and admin only, and the
Bitbucket CLIs that exist are unofficial or paid, so there is no `gh` equivalent
to shell out to and no way to reuse the shape the GitHub path is built in.

## Decision

**The delivery seam names no host, and Bitbucket Cloud ships through it.**

- **`pipeline.Delivery` is the forge-neutral pull-request contract.** It is the
  former `GitHub` interface with every host-specific method removed: open, read,
  re-describe, size, check, close and merge a pull request. A forge that speaks
  those satisfies it. GitLab and Azure DevOps become implementations of it, not
  new branches through the phases.
- **Stacked pull requests are an optional capability, not part of the contract.**
  `pipeline.Stacker` holds the four stack methods, and only `ExecGitHub`
  implements it. No other forge models a chain of PRs that merge as one unit, so
  a failed type assertion reads exactly as a failed preview probe already did:
  run the classic shape. Stacked epics stay GitHub-exclusive.
- **Bitbucket Cloud only, over a native Go REST client.** `internal/forge/
  bitbucketapi` talks to `api.bitbucket.org/2.0` and is modelled on
  `internal/tracker/jiraapi`, down to the 429 ladder and the typed auth sentinel.
  Bitbucket Data Center is a different product with a different API and is out of
  scope; it can follow without redoing this work.
- **Credentials are `BITBUCKET_EMAIL` + `BITBUCKET_API_TOKEN`, HTTP Basic.**
  Exactly the Jira pair, for exactly the reason: app passwords were removed in
  July 2026 and a scoped Atlassian API token is the only mechanism left. No OAuth
  app and no bearer access tokens. The token is a secret key, redacted everywhere
  config crosses the wire.
- **A missing credential is stated before a run spends anything.** `Delivery` may
  answer `DeliveryReady`; `assertDeliverable` asks it alongside the forge
  refusal, so a Bitbucket repo with no token is refused at the same point a repo
  on an unsupported host is — before the ticket is picked, not at PR time with
  three agents already paid for.
- **CI awareness is keyed on the files a repo has, not on its forge.**
  `ScanPullRequestCI` reads `.github/workflows` *and* `bitbucket-pipelines.yml`
  and unions them. A `pull-requests`, `branches` or `default` section is CI a PR
  can trigger; `custom` and `tags` are not. Bitbucket filters pipelines on a pull
  request's **source** branch and offers no destination filter at all, so a base
  branch cannot answer whether a pipeline could have run — and the gate's only
  safe reading of an unanswerable question is that CI is coming. It keeps
  waiting; it never waives.
- **Required-check auto-discovery is skipped on Bitbucket.** Its equivalent merge
  check, "required builds", is Premium-only, and its absence is indistinguishable
  from a repo that configures none. Onboarding falls back to explicit config or
  no required-check gate. CI polling still gates every merge.
- **`gh` is required only by repositories that ship through it.** Doctor and the
  onboarding wizard ask each forge a run would actually reach for its own
  credential: `gh auth status` for GitHub, one authenticated repository read for
  Bitbucket. A repo with no remote delivers locally and is asked for neither.
- **Proofs publish unchanged and are referenced per forge.** The orphan
  `trau-proofs` branch is plain git and was never GitHub-specific; only the
  `gh repo view` that reads owner/private was. `proofsbranch.Config.RepoInfo`
  replaces it where there is no CLI, and the PR body builds `bitbucket.org`
  raw/src URLs from the publication's own forge.
- **The tracker axis is untouched.** Bitbucket's built-in issue tracker is not
  added as a tracker provider. ADR 0032's separation holds: where a team's code
  lives says nothing about where its tickets do, and Bitbucket shops use Jira,
  which trau already supports.

This supersedes ADR 0032's "Delivery stays GitHub-only" clause. Every other
clause of ADR 0032 stands: forge identity is still read from a repository's own
remote, per repo and per Child repo, and still inferred from nothing else.

## Consequences

- A second forge costs one `Delivery` implementation and one credential pair. The
  phases, the checkpoint ranking, the merge gate and the epic flow are untouched
  by it, which is the whole point of the refactor landing first.
- `Forge.Unsupported()` remains the single refusal choke point, now naming two
  forges. `forgeDelivers` in `web/src/lib/onboarding.ts` mirrors it, as before.
- Bitbucket's merge strategies are not GitHub's. `squash` maps directly,
  `merge` to `merge_commit`, and `rebase` to `fast_forward` — the closest thing
  Bitbucket offers, since it has no rebase-and-merge.
- A Bitbucket repository cannot run a stacked epic. The existing per-forge gate
  refuses it by name before the epic commits to that shape.
- Delivery no longer proves a machine's GitHub auth for a repo that never touches
  GitHub. A Bitbucket-only user needs no `gh` installed at all.
- Bitbucket's pull-request states are normalized to GitHub's at the `Delivery`
  seam. `OPEN` and `MERGED` already match; `DECLINED` and `SUPERSEDED` both
  report as `CLOSED`, which is what every phase branching on a dead pull request
  — the manual-merge wait, the requeue sweep — actually compares against.
- The credential pair is collected by both onboarding wizards, for a Bitbucket
  repo only, and written to the user config: it authenticates an Atlassian
  account rather than one repository, so every Bitbucket repo on the machine
  shares it.
