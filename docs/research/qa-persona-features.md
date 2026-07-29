# Making Trau useful for QA people — research report

## The question

How could Trau grow features for a QA persona — concretely, a QA worker whose job is to check released features in the projects Trau builds — so Trau serves QA, not only developers?

## What I investigated

- **This repository** (primary source): the verify pipeline (`internal/pipeline`), the checks library (`internal/checks`), prompts (`internal/prompts`), the Inbox/Interview machinery (`internal/webserver/grill*`), the internal tracker (ADR 0007), the web UI (`web/src`), all 24 ADRs, `tasks/*.md` PRDs, `trau.ini.example`, and the label/status vocabulary.
- **External landscape** (web): agentic QA products ([mabl's Active Coverage announcement](https://www.mabl.com/blog/introducing-active-coverage-agentic-software-testing), April 23 2026; [Momentic on AI agents in QA](https://momentic.ai/blog/ai-agents-in-qa-testing), Dec 29 2025; [QA.tech's 2026 tool comparison](https://qa.tech/blog/the-13-best-ai-testing-tools-in-2026)), the canonical test-management entity model (TestRail/Qase), and tracker-side QA workflow conventions ([BrowserStack's Jira QA workflow guide](https://www.browserstack.com/guide/jira-qa-specific-workflows-a-practical-guide)).

## Finding 1 — Trau already *does* QA; it just has no surface for a QA *person*

Trau's pipeline is, internally, an agentic QA system with remarkably QA-shaped artifacts:

- **The handoff brief is literally addressed to a human QA tester.** The prompt (`internal/prompts/registry.go:7`) reads: *"Write a QA brief for {ID}: the concrete, checkable behaviors a manual QA tester must verify for this slice, in priority order."*
- **A structured acceptance rubric** per run (`internal/pipeline/rubric.go:19-23`): `acceptance_criteria`, `non_goals`, `required_tests`, `ui_paths`, `fail_conditions`.
- **A cold, adversarial verify phase** that grades the slice against brief + rubric and writes a JSON **verdict** (`pass`, `summary`, `failures`, per-check results, honest browser accounting — `internal/pipeline/pipeline.go:4288`).
- **Browser verification with proofs**: screenshots and video harvested to the hub (`VERIFY_PROOFS`, `internal/webserver/proofs.go`), a "QA proofs" gallery on the run detail page, and a "### QA proofs" section in PR bodies.
- **QA accounts**: full CRUD in web settings (`web/src/components/trau/qa-accounts-panel.tsx`), a credential roster injected into verify with write-only-secret handling, and auto-capture of accounts the verifier creates (`internal/pipeline/qacapture.go`).
- **A pluggable quality bar**: `.trau/checks/*.yaml` — a check is 3 keys plus an English sentence (`name`, `severity`, `prompt`), documented in `docs/verify-checks.md`. Error-severity failures block the merge.
- **A cross-vendor verify panel** (`docs/verify-panel.md`) and a lessons ledger feeding past failures into future verifies.

Everything above is consumed by *agents* and *developers*. Nothing is packaged for a QA person.

## Finding 2 — The gaps (verified absences)

1. **No QA vocabulary.** No `needs-qa`, `in-qa`, `qa-passed`, or `ready-for-qa` label or status exists anywhere in code or docs (grep-verified). The triage set is `needs-triage` / `needs-info` / `ready-for-agent` / `needs-human` / `wontfix` / `needs-split`.
2. **The Run ledger ends at "merged".** Buckets are Needs attention / active / stopped / merged — there is no state for *shipped but not yet human-verified*, which is exactly where your QA worker's job begins.
3. **No sign-off gesture.** Nobody can mark a shipped feature Verified or Regressed; `AUTO_MERGE=1` merges on green CI with no human quality gate (the only alternative is `AUTO_MERGE=0`, stop at PR — a developer flow).
4. **No verify-only run.** There is no `--phase`/`--verify-only` flag; queue item kinds are only `ticket` and `epic` (`internal/queue/queue.go:19-20`). Verify can only be reached by building first or via state-driven resume.
5. **No QA-shaped intake.** Interview modes are `interview` and `research` (`internal/hubstore/grill.go:25-26`); the first-turn prompts (`grillprompt.go`) cover spec-grilling, authoring, pre-grill, and research — nothing tuned for bug reports with repro steps.
6. **APP_URL means "locally-running app"** (`trau.ini.example:234`) — there is no notion of a staging/production environment to verify *released* features against.
7. **No QA persona PRD exists** in `tasks/` — this space is genuinely unclaimed.

Two useful foundations already in place: the hub is multi-repo (Registered repos; the Run ledger's "All projects" aggregation — `CONTEXT.md`), and the reconcile-sweep invariant ("never launches anything", ADR 0022) shows how to add passive QA states safely.

## Finding 3 — External landscape

- The agentic-QA market is converging on **"agents execute, humans review evidence and own the quality bar."** [mabl's Active Coverage](https://www.mabl.com/blog/introducing-active-coverage-agentic-software-testing) (Apr 2026) sells "coverage that builds itself, runs itself, recovers itself" with an MCP server wired into Jira/IDEs/coding agents ([PR Newswire release](https://www.prnewswire.com/news-releases/mabl-unveils-next-generation-agentic-testing-platform-for-the-ai-development-era-302751783.html)); their survey claims teams spend ~20% of the week manually verifying AI-generated work — that reviewing role *is* the emerging QA job. [Momentic](https://momentic.ai/blog/ai-agents-in-qa-testing) (Dec 2025) similarly: agents generate/maintain tests, humans review. (Caveat: both are vendor-voiced claims.)
- Traditional test management (TestRail, Qase) is built on **test cases → runs → plans → milestones** with defect links. I could not read TestRail's docs directly (HTTP 403); the entity model is corroborated only by secondary comparisons ([Qase vs TestRail](https://testomat.io/blog/qase-vs-testrail/), [TestRail's blog](https://www.testrail.com/blog/popular-test-management-tools/)).
- Tracker-side QA convention is **"Ready for QA" / "In Testing" / "Failed QA" statuses plus a formal sign-off** — sign-off meaning documented, accepted risk, not zero bugs ([BrowserStack guide](https://www.browserstack.com/guide/jira-qa-specific-workflows-a-practical-guide), [QA Sphere process guide](https://qasphere.com/blog/qa-process-complete-guide/)).

## The idea map — three directions, ranked

### 1st — Direction A: QA as consumer — a release-verification workspace *(recommended first bet)*

Repackage what every run already produces into the QA worker's actual workflow:

- **A "Shipped, awaiting QA" queue**: merged runs not yet human-verified, per-repo and across all Registered repos (the multi-repo ledger already exists). Implemented as a hub-side flag like Archive — sync-immune, no tracker coupling required.
- **A verification packet page** per shipped run: the QA brief (already written *for* them), the rubric's acceptance criteria and `ui_paths` rendered as a tick-off checklist, the verdict with browser notes, the proof screenshots/video, and the PR link. All of this data exists today on the developer-oriented run detail page (`web/src/routes/runs_.$repo.$ticket.tsx`) — this is recomposition, not new plumbing.
- **A sign-off gesture**: *Verified* (stamps who/when — identity machinery exists per ADR 0014) or *Regressed* (opens a pre-filled bug intake linking the run → Direction C). Optionally mirror a `qa-passed`/`needs-qa` label to the tracker via the existing explicit-gesture write pattern (ADR 0020).
- **An optional QA gate mode**: a middle setting between `AUTO_MERGE=1` and stop-at-PR — merge on green but hold the ticket in `needs-qa` until sign-off.

**Why first:** it is almost entirely a re-surface of existing artifacts; it matches your QA worker's stated job (checking released features across projects) exactly; and it makes every other direction more valuable because sign-off data accumulates.

### 2nd — Direction B: QA as operator — QA-launched verify runs against released builds

- **A verify-only queue kind**: a third `queue.Kind` ("verify") that runs the cold verifier + browser drive against a ticket's persisted rubric — no branch, no PR, just a fresh verdict and proofs. Fits ADR 0015 (queue stays the web's only start path). This is the largest build item: the pipeline has no phase-selective entry today.
- **Release recheck / regression sweep**: batch-recheck the last N merged tickets' rubrics against a deployed build before a release — Trau's own-artifacts version of what mabl/QA Wolf sell.
- **Environment targets**: extend `APP_URL`/`APP_URLS` with named environments (staging/production) so "verify the *released* feature" is expressible; reuse the QA accounts roster.
- **Guardrail (important):** autonomous browser agents against production need explicit read-only-flow constraints and dedicated QA accounts; the roster's write-only-secret handling is the right foundation.

### 3rd — Direction C: QA as author — intake and owning the quality bar

- **A bug-report Interview mode**: a new grill prompt kind tuned for defect intake — repro steps, expected vs. actual, environment, severity — ending in a `create` outcome labeled `Bug`, filed to tracker or internal. Reuses the entire existing Interview/outcome machinery; the attachments epic (`tasks/attachments-epic-corrected.md`) covers screenshots-as-input.
- **A checks editor in the web UI**: checks are already plain-English-authorable, but only by committing files to the target repo; a settings surface would let QA own the merge-blocking quality bar. (Fix regardless: custom checks currently *replace* the built-in defaults entirely — a sharp edge for non-developer authors.)
- **QA-authored acceptance criteria pre-build**: let QA attach acceptance criteria to a ticket (via Interview); the rubric prompt already draws from ticket/PRD, so QA-written criteria would flow through rubric → verdict → sign-off packet, closing the loop.

### Anti-recommendation

**Do not build test-case management** (suites/plans/milestones à la TestRail) inside Trau. It is a large, mature, off-thesis product category; Trau's thesis — agents execute verification, humans review evidence — makes per-run rubrics + sign-off the native unit, and the market (mabl's MCP-into-Jira/IDE move) says integrate, don't rebuild.

## Recommendation

Sequence: **A** (shipped-awaiting-QA queue → verification packet → sign-off) as the first epic — high value, low machinery; then **C's bug-report Interview mode** (small, reuses grill wholesale, and Regressed-sign-off needs it as its landing); then **B's release recheck** as the flagship agentic-QA feature once environments and a verify-only pipeline entry exist. Skip TMS entities entirely.

## Sources

**Repository (read directly):** `CONTEXT.md`, `AGENTS.md`, `trau.ini.example`, `internal/prompts/registry.go` + goldens, `internal/pipeline/{pipeline.go,rubric.go,qacapture.go,artifacts.go}`, `internal/checks/checks.go`, `docs/verify-checks.md`, `docs/verify-panel.md`, `internal/webserver/{grillprompt.go,grillmcp.go,grillapply.go}`, `internal/hubstore/{grill.go,qaaccounts.go}`, `internal/queue/queue.go`, `internal/tracker/internal.go`, `web/src/routes/runs_.$repo.$ticket.tsx`, `web/src/components/trau/qa-accounts-panel.tsx`, `docs/adr/` (all 24), `docs/agents/triage-labels.md`, `tasks/*.md`. Key claims (prompt wording, rubric/check fields, absence of QA labels, queue kinds) were grep-verified, not taken from summaries.

**External:** [mabl — Introducing Active Coverage](https://www.mabl.com/blog/introducing-active-coverage-agentic-software-testing) · [mabl PR Newswire release](https://www.prnewswire.com/news-releases/mabl-unveils-next-generation-agentic-testing-platform-for-the-ai-development-era-302751783.html) · [Momentic — AI Agents in QA Testing](https://momentic.ai/blog/ai-agents-in-qa-testing) · [QA.tech — 13 Best AI Testing Tools 2026](https://qa.tech/blog/the-13-best-ai-testing-tools-in-2026) · [BrowserStack — Jira QA workflows](https://www.browserstack.com/guide/jira-qa-specific-workflows-a-practical-guide) · [QA Sphere — QA process guide](https://qasphere.com/blog/qa-process-complete-guide/) · [Testomat — Qase vs TestRail](https://testomat.io/blog/qase-vs-testrail/) · [TestRail blog](https://www.testrail.com/blog/popular-test-management-tools/).

**Unconfirmed / caveats:** TestRail's official docs returned HTTP 403, so the test-management entity model rests on secondary sources; mabl's "20% of the week" figure and Momentic's capability claims are vendor-published and not independently verified.
