# Stacked PRs — what GitHub's preview actually does

A spike against a live scratch repo to settle the open empirical questions about
GitHub's stacked-PR preview, so the defensive-detection and `EPIC_STACKED_PRS`
slices build on observed behavior rather than doc inference. **No Trau code is
changed by this ticket** — every claim below is a recorded observation from a real
repo, and the implication paragraphs are input to later slices, not filed work.

**The feature is enabled** for this account. The rollout probe passed, so all seven
questions were answered live; none are blocked.

## Setup

| | |
|---|---|
| Scratch repo | <https://github.com/RomkaLTU/stacked-pr-spike-2026-08> (private, left in place) |
| Account | `RomkaLTU` |
| `gh` | 2.97.0 (2026-07-31) |
| `gh-stack` | `github/gh-stack` v0.1.0 (`gh extension install github/gh-stack`) |
| Date | 2026-08-05 |
| CI | one always-pass workflow, `.github/workflows/ci.yml`, `run: echo ok`, on `push` + `pull_request` |

Three 3-deep stacks were built (`main ← A ← B ← C`), each with one trivial file per
layer, plus one unstacked control PR. Each stack answers a different question so no
result contaminates another:

| Stack | PRs | Used for | Final state |
|---|---|---|---|
| #4 | 1, 2, 3 (`s1-A/B/C`) | Q1, Q4, Q5, Q3, Q7 | whole-stack squash-merged, `open=false` |
| #8 | 5, 6, 7 (`s2-A/B/C`) | Q2 legacy-merge refusals | fully open, untouched |
| #12 | 9, 10, 11 (`s3-A/B/C`) | Q6 partial merge | #9/#10 merged, #11 open and rebased |
| — | 13 (`ctrl-1`) | unstacked control | merged normally |

The repo is left exactly in this state for inspection.

### Availability probe

`GET /repos/{owner}/{repo}/stacks` returns **HTTP 200** with `[]` on a repo that has
no stacks. That is the positive signal. A repo the token cannot see returns:

```json
{"message":"Not Found","documentation_url":"https://docs.github.com/rest/pulls/stacks#list-pull-request-stacks","status":"404"}
```

Note the `documentation_url` names the stacks endpoint even on the 404, so **a 404
here does not distinguish "feature off" from "repo not found"** — it is emitted by
the matched route either way. Detection must not read a bare 404 as "stacks
unsupported". No exit code 9 was ever observed; `gh api` exits 1 on the 404.

## Q1 — Stack creation from existing branches

**Procedure.** Built `main ← s1-A ← s1-B ← s1-C`, pushed all three, opened three
chained PRs the ordinary way (`gh pr create --base s1-A --head s1-B`, etc.), recorded
the full REST payload of each, then ran `gh stack link s1-A s1-B s1-C`.

**Observed.** Linking is **purely additive**, exactly as documented:

```
✓ Created stack with 3 PRs (stack #4)
```

Diffing each PR's REST payload before and after linking, the *only* change is a new
`stack` key in the response. Unchanged: `base.ref` (still `main`, `s1-A`, `s1-B`),
`head.sha`, `title`, `body`, `draft`, labels. No comment was posted to any PR
(`comments` stayed `0`). No branch was force-updated. Nothing about the PRs a human
or a bot had already set was rewritten.

Two side observations worth recording:

- **Stack numbers share the issue/PR number sequence.** PRs 1–3 were followed by
  stack **#4**; the next stack was **#8** after PRs 5–7. So a bare number is
  ambiguous to a human but never collides — `gh stack merge <n>` resolves it as a
  stack first, then as a PR.
- **`gh stack link` will create PRs for branches that have none**, and it creates
  them **as drafts**. Stacks #8 and #12 were created this way and all six PRs came
  out `draft: true`, requiring `gh pr ready` before they could be merged. Stack #4's
  PRs, opened by `gh pr create`, stayed non-draft.

**Implication for Trau.** Confirms the epic's additive-linking assumption. A user who
stacks Trau's PRs via GitHub's recommendation banner does **not** cause Trau's
recorded `PR_URL`, `PR` number, base branch or head SHA to drift — every checkpoint
field Trau stamps survives linking untouched. Nothing in the *creation* path needs
defending. The draft-by-default behavior is a trap only if Trau ever calls
`gh stack link` itself for branches without PRs; it does not today.

## Q2 — Legacy merge on a stacked PR

This is the question that decides whether Trau's existing `mergePR` path
(`internal/pipeline/pipeline.go:3105`) breaks. Trau runs
`gh pr merge <pr> --squash --delete-branch`.

**Procedure.** Ran that exact command against stack #8 — bottom (#5), mid (#6) and
top (#7) — first as drafts, then again after `gh pr ready` to rule out the draft
state as the cause. Also tried `--merge` and `--rebase`, and the classic REST merge
endpoint. Control: the same command on unstacked PR #13.

**Observed — every layer is refused, including the bottom:**

```
$ gh pr merge 5 --squash --delete-branch
GraphQL: This pull request is part of a stack and must be merged using the
asynchronous merge REST API. For more information, see
https://docs.github.com/rest/pulls/pulls#merge-a-pull-request-asynchronously
(mergePullRequest)
$ echo $?
1
```

Byte-identical message and **exit code 1** for:

| Target | `--squash` | `--merge` | `--rebase` |
|---|---|---|---|
| bottom (#5) | refused, exit 1 | refused, exit 1 | refused, exit 1 |
| mid (#6) | refused, exit 1 | — | — |
| top (#7) | refused, exit 1 | — | — |

Draft state is irrelevant — the stack check fires *before* the draft check, and the
message was identical before and after `gh pr ready`. The classic REST endpoint
refuses too, with a different message and a distinct status:

```
$ gh api -X PUT repos/<o>/<r>/pulls/5/merge -f merge_method=squash
{"message":"Merging stacked PRs via this endpoint is not supported. Use the
asynchronous merge endpoint instead.",
 "documentation_url":"https://docs.github.com/rest/pulls/pulls#merge-a-pull-request-asynchronously",
 "status":"403"}
```

The control PR merged normally (`exit 0`), confirming the refusal is stack-specific
and not an artifact of the repo or token.

**Implication for Trau — this contradicts the "bottom PR still merges" assumption.**
There is no legacy escape hatch at any position: once a PR is in a stack, *the whole
stack must be merged through the stack API*. Trau's `mergePR` breaks outright.

Worse, the failure lands in the worst branch of Trau's classifier. The refusal text
contains none of `retryableGH`'s deterministic markers (`internal/pipeline/pipeline.go:3453`)
— no `not found`, no `forbidden`, no `http 403`, no `not mergeable` — so
`retryableGH` returns **true** and Trau burns all three attempts with 1s/2s backoff
against a permanently deterministic refusal. And `unmergeablePR`
(`internal/pipeline/pipeline.go:3481`) returns **false**, so the error never reaches
`recoverUnmergeablePR` and never becomes a clean give-up. It falls through to
`fmt.Errorf("merge %s: %w", id, err)` at `pipeline.go:2925` — an *unexpected error*
fault that stops the whole session, rather than a quarantine + `needs-human` on the
one ticket.

So slice 3's detection is not a nicety: without it, one user clicking GitHub's
stacking banner on a Trau PR faults the session. Two cheap mitigations fall straight
out of this, independent of the larger `EPIC_STACKED_PRS` work:

- add `"part of a stack"` to the `retryableGH` deterministic list so the three
  pointless retries stop, and
- route it to `giveUp` (like the policy-block case `unmergeablePR` already absorbs)
  so a stacked PR quarantines its ticket instead of faulting the session.

Note the REST refusal *is* already caught as deterministic — it carries `http 403`
— but Trau reaches GitHub through `gh pr merge`, i.e. the GraphQL path, so the
403 wording never appears in practice.

## Q3 — Whole-stack squash merge

**Procedure.** `gh stack merge 3 --squash --yes` on the **top** PR of stack #4, with
`main` at `3809eab` beforehand.

**Observed.**

```
Merging #1, #2, #3 into main via squash...
✓ Merged #1, #2, #3 into main (a6ca174)
```

`git log origin/main` afterwards:

```
a6ca174 feat: layer C (#3)
c3de69f feat: layer B (#2)
0752bf0 feat: layer A (#1)
3809eab feat(s3): layer B (#10)   <- pre-existing
```

**One squashed commit per layer**, in stack order, bottom first, each titled
`<PR title> (#N)` — the ordinary squash-merge subject. Not one combined commit, and
not a merge commit: `main` stays linear. Each layer's commit contains only that
layer's diff. The operation is atomic and all-or-nothing across the three PRs.

Notably the PRs' `base.ref` values were **not** retargeted before merging — #2 still
records `base=s1-A` and #3 still `base=s1-B` after merging — yet main came out
linear. GitHub replays the layers onto the base rather than merging each recorded
base in turn.

**Implication for Trau.** Confirms the epic's history-shape claim: a stacked epic
would land on `main` as one commit per slice, which is precisely the shape Trau's
current epic flow produces when slices squash-merge into an epic branch that then
merges to `main`. The history argument for `EPIC_STACKED_PRS` is therefore *neutral*
— it buys reviewability, not a cleaner main. That is worth stating plainly in the
epic, because "cleaner history" is not a reason to adopt it.

## Q4 — API surface

**Procedure.** Compared `gh pr view --json`, REST `pulls/{n}`, GraphQL and
`GET /stacks` across a stacked PR, an unstacked control PR, a merged stacked PR and
the survivor of a partial merge.

**Observed.**

**`gh pr view --json` has no `stack` field at all** in gh 2.97.0:

```
$ gh pr view 1 --json stack
Unknown JSON field: "stack"
```

The full field list (`additions … url`) contains no stack-related entry. This is the
single most important detection fact: **slice 2 cannot use `gh pr view --json`.**

**REST `GET /repos/{o}/{r}/pulls/{n}`** adds a top-level `stack` key, present only
when the PR is in a stack:

```json
"stack": {
  "base": {"ref": "main", "sha": "e864f256827b4888d89d7c2996550b6bb9327d47"},
  "id": 171306, "number": 8, "position": 1, "size": 3
}
```

| PR | `has("stack")` |
|---|---|
| unstacked control #13 | `false` — key absent entirely, not `null` |
| stacked open #5 | `true` |
| stacked merged #1 | `true` (with `stack.base.sha` → `null`) |
| partial-merge survivor #11 | `true`, still `position: 3, size: 3` |

Two subtleties: `stack.base` is the **stack's** base (`main`), *not* the PR's own
chained base — do not confuse it with `base.ref`. And `position`/`size` describe the
stack's original membership, so the survivor of a partial merge still reports
`position 3 of 3` with two layers already merged; they cannot be used to count
*remaining* work.

**GraphQL** exposes `PullRequest.stack` and `PullRequest.stackEntry`:

```
PullRequestStack:      baseRefName, entries, id, number, size
PullRequestStackEntry: id, position, pullRequest, stack
```

`stackEntry` is `null` for an unstacked PR and populated for a stacked one — a clean
boolean test, verified live on #5 (`position 1`, stack #8) versus #13 (`null`).

**`GET /repos/{o}/{r}/stacks`** returns every stack, closed ones included, each with
`id`, `number`, `node_id`, `url`, `base.ref`, `open`, `created_at` and an embedded
`pull_requests[]` array carrying each layer's `number`, `state`, `draft`, `merged_at`
and `head`. A `?state=closed` query parameter appeared to be **ignored** — the same
full list came back — so callers must filter on `open` themselves.

**Implication for Trau.** Detection is cheap but must not go through
`gh pr view --json`, which is what an implementer would reach for first. Two viable
routes, both one call:

- `gh api repos/{o}/{r}/pulls/{n} --jq 'has("stack")'`, or
- `gh api graphql` on `stackEntry` (null-or-not).

The first is simpler and matches Trau's existing `ExecGitHub.output` shape. Detection
should test **key presence**, not truthiness, and must tolerate the key being absent
on older/unenabled deployments — where its absence is indistinguishable from
"unstacked", which is the safe default and preserves today's behavior exactly.

## Q5 — Per-layer checks

**Procedure.** Read `gh pr checks` on all three layers of stack #4 and cross-checked
against `gh run list` and the `refs/pull/{n}/merge` trees.

**Observed.** Each layer gets its **own** checks and only its own — two runs per
branch (one `push`, one `pull_request`), all `pass`:

```
$ gh pr checks 3
ok  pass  5s  .../runs/31015375427/job/...   (push, s1-C)
ok  pass  4s  .../runs/31015389724/job/...   (pull_request, s1-C)
```

`gh pr checks` on an upper PR does **not** aggregate the lower layers' check runs.
Coverage of layers 1..N is nonetheless real, but it comes from ordinary git ancestry,
not from anything the stack feature does: the `pull_request` event builds the merge
ref of head into its *chained* base, and that tree contains every lower layer.
Verified directly:

```
refs/pull/2/merge → .github README.md base.txt layer-A.txt layer-B.txt
refs/pull/3/merge → .github README.md base.txt layer-A.txt layer-B.txt layer-C.txt
```

So layer N's CI does cover layers 1..N — because the head branch is built on them.

**Implication for Trau.** Confirms the epic's per-layer-CI assumption, and confirms
Trau's existing CI gate would keep working per layer unchanged: each stacked PR has
its own, complete, independently-pollable check rollup, and a green top layer really
does mean the cumulative tree is green. The caveat for slice 3 is the inverse — a
green upper layer says nothing about whether the *lower* PRs are still green after a
rebase, so a stack-aware merge gate would have to poll every layer, not just the one
it holds.

## Q6 — Partial-merge rebase behavior

This is the one that decides how firmly slice 3 must stay merge-once-at-the-end.

**Procedure.** On stack #12, merged the **middle** PR: `gh stack merge 10 --squash --yes`.
Recorded every head SHA before and after.

**Observed.**

```
Merging #9, #10 into main via squash...
✓ Merged #9, #10 into main (3809eab)
```

Merging the middle layer lands it *and* everything below it, as two separate squash
commits (consistent with Q3). The consequences for the untouched top PR are the
important part:

| | before | after |
|---|---|---|
| #11 `base.ref` | `s3-B` | **`main`** (auto-retargeted) |
| #11 `head.sha` | `d9aa825…` | **`31aef57…`** (rewritten) |
| `s3-A`, `s3-B` branches | exist | **still exist** — not deleted |
| #9, #10 | open | `state=closed`, `merged=true`, own `merge_commit_sha` each |

**The top PR's head branch was force-updated.** The old SHA is *not* an ancestor of
the new one:

```
$ git merge-base --is-ancestor d9aa825 31aef57 ; echo $?
1
```

GitHub rebased `s3-C` onto the new `main` and replayed the layer-C commit as a new
object. Old history was `d9aa825 → 1a216ce → 8574c99 → e864f25`; new history is
`31aef57 → 3809eab (#10) → 0156060 (#9) → fda4370 → e864f25`. Trees differ. The
rebase also fired a fresh round of CI on the rewritten head, which passed.

**Implication for Trau — this confirms the hazard `docs/pr-base-hygiene.md` warns
about, and it is worse than a retarget.** That doc's rule is that Trau *never*
force-pushes and that retargeting is safe only toward a branch sharing history with
the head. A partial stack merge does both things Trau refuses to do to itself:
GitHub retargets the PR base **and** non-fast-forward rewrites the head branch, with
no involvement from Trau at all.

Any Trau run holding that branch would find its recorded head SHA gone from the
remote: a subsequent `git push` of new work would be rejected as non-fast-forward,
and a `git pull` would produce a duplicate-commit mess. Trau's fork-point check
(`ls-remote` against the recorded fork point) would also see a base it does not
recognize.

**Slice 3 must stay merge-once-at-the-end, firmly.** There is no safe way for Trau to
absorb a partial stack merge while it has work in flight on an upper layer. The
defensive posture should be: detect the stack, refuse to merge, and hand the whole
stack to a human — not attempt to participate in it. If `EPIC_STACKED_PRS` is ever
built, it must own the entire stack lifecycle and merge only once, from the top, when
every layer is final.

## Q7 — Merged-state visibility after a stack merge

**Procedure.** After the Q3 whole-stack merge, polled each layer with the exact forms
Trau's `AUTO_MERGE=0` wait and `MergedPRURL` recovery use.

**Observed.** Every layer reads cleanly as merged, each with its own merge commit and
a distinct `mergedAt` a second apart:

```
#1 state=MERGED mergedAt=2026-08-05T14:34:35Z mergeCommit=0752bf0…
#2 state=MERGED mergedAt=2026-08-05T14:34:36Z mergeCommit=c3de69f…
#3 state=MERGED mergedAt=2026-08-05T14:34:37Z mergeCommit=a6ca174…
```

Trau's two exact invocations both behave normally:

```
$ gh pr view s1-A --json url,state        # ExecGitHub.MergedPRURL
{"state":"MERGED","url":".../pull/1"}
$ gh pr view 1 --json state -q .state     # ExecGitHub.PRState
MERGED
```

**Head branches are NOT deleted.** `s1-A`, `s1-B` and `s1-C` all still resolve to
their pre-merge SHAs after the whole-stack merge, and `s3-A`/`s3-B` survived the
partial merge too. The stack itself flips to `open=false` in `GET /stacks`.

**Implication for Trau.** Confirms the epic's assumption that merged-state polling
survives stacking. `PRState` returning `MERGED` means `mergePR`'s early-exit
(`if st == "MERGED" { return nil }`) correctly treats an externally stack-merged PR
as done, and the `AUTO_MERGE=0` manual-merge wait terminates normally — so a human
who resolves the Q2 refusal by stack-merging by hand *does* unblock a waiting Trau
run, cleanly. `MergedPRURL`'s branch lookup also still resolves, so the
already-merged reconcile path (COD-1158) keeps working.

The one contradicted sub-assumption is branch cleanup: Trau passes `--delete-branch`
to `gh pr merge`, but a stack merge leaves every head branch in place. Nothing breaks
— Trau does not depend on deletion — but a stacked epic would leave one stale remote
branch per slice for someone to sweep up.

## Summary — what this changes for the epic

| Q | Verdict on the `EPIC_STACKED_PRS` design |
|---|---|
| 1 | **Confirms** — linking is additive; no Trau checkpoint field drifts. |
| 2 | **Contradicts** — no legacy merge at *any* position, not even the bottom. Trau's `mergePR` faults the session, not just the ticket. |
| 3 | **Confirms** history shape, but it is one-commit-per-slice either way, so history is not an argument for the epic. |
| 4 | **Contradicts the likely implementation** — `gh pr view --json` has no `stack` field; detection must use REST `has("stack")` or GraphQL `stackEntry`. |
| 5 | **Confirms** — per-layer CI is real and cumulative via git ancestry, though `gh pr checks` never aggregates. |
| 6 | **Confirms the hazard** — partial merge force-rewrites upper head branches. Slice 3 must stay merge-once-at-the-end. |
| 7 | **Confirms** — merged-state polling works unchanged; only `--delete-branch` is a no-op. |

The cheapest next step is not the epic. It is the two-line defensive fix Q2 exposes:
teach `retryableGH` that `"part of a stack"` is deterministic, and route it to
`giveUp`. That converts a session-stopping fault into a single quarantined ticket,
and it is worth doing whether or not `EPIC_STACKED_PRS` is ever built.
