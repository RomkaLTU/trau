# trau

Autonomous ticket loop: trau picks a ready tracker ticket, runs it through the
Build → Verify → Ship pipeline via an AI coding agent, and merges the PR.

## Language

**Repo**:
The target codebase trau builds, branches, and opens PRs against — resolved at launch, not necessarily the shell's cwd. Identified on screen by its folder name. Several Repos can share one Project (e.g. the m4c repos), so the Repo is the only unambiguous "where am I" signal.
_Avoid_: project (that's the tracker binding), workspace, target, directory, cwd

**Folder repo**:
A registered folder that is not itself a git repository — the git repositories directly inside it are its Child repos. It carries the tracker binding, the Queue and the board like any Repo, so a folder of forty-four services is one board mirror and one Queue rather than forty-four, and a single run may change several Child repos at once (ADR 0030). Registering a folder neither requires nor removes any child's own registration: a separately registered child stays an independent Repo with its own Queue and board.
_Avoid_: monorepo (the children are separate repositories), project (that's the tracker binding), parent repo, umbrella repo, workspace

**Child repo**:
A git repository inside a Folder repo, found by a bounded scan of the folder at run time rather than registered. It is a ship target: a run that changes it cuts the ticket's branch there and opens its pull request there, and one that leaves it alone never touches it. Each Child repo has its own Forge and its own base branch, both read from its own remote (ADR 0032). A Child repo that was dirty, unreadable, or on a Forge trau cannot deliver to when the run started is named to the build agent as off limits, and a change landing in one anyway aborts the run; a clean one merely standing off its base is moved onto it instead.
_Avoid_: member (that's a Project's registered repo), submodule, sub-repo, nested repo, workspace

**Forge**:
The code host a repository's own git remote points at — who would receive a push and open the pull request. Identified per Repo and per Child repo from that repository's remote, never from the tracker and never assumed; the `FORGE` key overrides it for a host trau does not recognize. Delivery is GitHub-only, so any other Forge is named and left alone before a run spends anything (ADR 0032).
_Avoid_: tracker (that's where tickets live), remote (that's the ref name, `origin`), host, provider (that's the AI backend), git server

**Project**:
The tracker (Linear/Jira) project a Repo is bound to via the `PROJECT` config key — it scopes the ready queue and guards cross-project runs. May be empty; never use it to identify which Repo trau is operating on.
_Avoid_: repo, board

**Provider**:
The AI coding backend that executes a phase — a vendor + its CLI. The known set is `claude`, `codex`, `kimi`. Selected by the `PROVIDER` config key or the `--provider` flag.
_Avoid_: agent, model, vendor, backend (when you mean the named provider)

**Model**:
The specific model a provider runs (e.g. `claude-opus-5`). One provider has one active model at a time, resolved per-provider (`ClaudeModel`/`CodexModel`/`KimiModel`). A Model never spans Providers — switching Provider switches which Model applies.
_Avoid_: provider, engine

**Route**:
A per-phase override that sends one pipeline phase to a specific provider/model instead of the default (e.g. run `verify` on codex while the default is claude). Distinct from the default Provider.
_Avoid_: override (bare), phase provider

**Fallback provider**:
The ordered failover chain (`FALLBACK_PROVIDERS`) tried when the primary Provider fails transiently mid-run. Not a user choice per run — an automatic recovery path.
_Avoid_: backup, secondary provider

**Provider override**:
An ephemeral, single-run swap of the default Provider, chosen at launch — at queue time on the web (each Queue item carries its own), or from the Run once screen in the TUI. Applies to that one run and reverts to the config default afterward. Changes only the default Provider — Routes and Fallback providers are unaffected.
_Avoid_: route (that's per-phase), fallback (that's failover), setting the provider (that's persisted config)

**Hub**:
The single machine-local web daemon (`trau serve`) that exposes the JSON API and embedded web UI on `127.0.0.1:8728`. Exactly one runs per machine — started explicitly with `trau serve`, or auto-started by the first interactive TUI session — and it outlives the loops it observes: they send it their Run data over HTTP and never open a database, so it can serve a run's history whether that loop is live or dead. The "web UI" is the Hub's browser front-end, not a separate thing. When a ticket says "one instance for the web UI, many for the CLI", the one is the Hub and the many are loops.
_Avoid_: server (bare), web UI (that's the Hub's front-end, not the process), instance (that's any live trau process in the registry), serve (that's the command that starts it)

**Instance**:
Any live interactive trau process registered with the Hub — a loop, a Run once, or a TUI merely open at the menu. Identified by PID, kept fresh by a heartbeat, gone when the process exits. Not necessarily a loop: an idle TUI is an Instance too.
_Avoid_: loop (bare — an idle TUI isn't one), process (bare), session (that's what the instance is currently doing)

**Session state**:
What an Instance is doing right now, reported by the instance itself alongside its heartbeat: **Idle** (TUI open, nothing live), **Grazing** (loop picking its next ready ticket), **Working** (executing a ticket's Activity), **Parked** (a run stopped short; the process is alive on the recap waiting for a human), **Stopping** (graceful stop in flight). Ephemeral — dies with the process. It says only *that* a session parked, never why: the why (Failure class, reason) belongs to the ticket's Checkpoint, and surfaces join the two. Nothing may derive a Session state from run artifacts when a reported one exists.
_Avoid_: phase (that's pipeline progress, lives in the Checkpoint), status (bare, overloaded), activity (that's the work inside Working — its own term), faulted/paused (as session states — those are Failure classes on the Checkpoint)

**Activity**:
The pipeline work a Working Instance is executing right now — one of `build`, `lintfix`, `cleanup`, `handoff`, `verify`, `repair`, `bugfix`, `commit`, `pr`, `ci-wait`, `merge` — reported present-tense in the instance's heartbeat with an optional detail (the raw call label, e.g. `repair2`). Exists only while Working; dies with the session. Its durable trace is the `activity_change` event stream, from which per-activity wall-clock is derived. Distinct from the Checkpoint's phase, which is past-tense — what last *completed*, kept for resume.
_Avoid_: phase (that's the Checkpoint's past-tense progress), step (that's the display grouping), session state (that's Idle/Grazing/Working/…)

**Step**:
The display grouping of Activities — exactly three, identical in web and TUI: **Build** (build, lintfix, cleanup, handoff), **Verify** (verify, repair, bugfix), **Ship** (commit, pr, ci-wait, merge). The active Step shows its live Activity as a sub-label ("Verify · repair 2"). The Activity→Step map lives at the display edge, never in the protocol; the handoff brief sits in Build because it runs concurrently with the cleanup chain and concurrent work cannot straddle sequential Steps.
_Avoid_: phase (that's the Checkpoint), stage, macro-step (just Step)

**Registered repo**:
A Repo the Hub is allowed to start loops in — added statically via `SERVE_WORKSPACE` or registered from the web UI. Contrast with an observe-only repo, which the hub has merely seen a loop run in: browsable, never startable. Registering is reversible and never touches the Repo's run artifacts.
_Avoid_: project (that's the tracker binding), workspace repo, allowlisted repo (config-only framing), added repo

**Active repo**:
The single Registered repo the web UI is checked out to. Every screen is scoped to it; no other Repo's data is visible until the user switches. Switching happens in exactly one place — the app shell. One exception: under "All projects" the Run ledger aggregates every Repo's runs, marking each row with its Repo.
_Avoid_: current project, selected repo (that's per-page picker framing), workspace

**Epic**:
A tracker issue that has Sub-issues (`has_children`), treated everywhere as a unit of remaining work: queueing it queues its sub-issues, drain settles them together, the Loop timeline expands it, and the backlog board renders it as a collapsible row with Settled/total progress. A not-yet-closed Epic files under the board's In Progress section while any live Sub-issue is started — in-flight work lists the whole Epic as in progress. Run and queue semantics are one level deep — Epic → Sub-issue — but the board renders the tree recursively: a Sub-issue that has children of its own is an Epic to them, with its own chevron and Settled/total over its direct children.
_Avoid_: parent (that's the field on the child, not the concept), group, story (tracker jargon)

**Sub-issue**:
A tracker issue with a `parent` — a child of exactly one Epic. On the backlog board it nests under its Epic only when both are visible in the same status section (status-true nesting), at whatever depth that puts it; anywhere else it renders flat with a breadcrumb chip naming its Epic.
_Avoid_: child (bare, ambiguous with process children), subtask, slice (that's the act of splitting a PRD, not the resulting issue)

**Work item type**:
The tracker's own name for what an issue *is* — `Epic`, `Feature`, `User Story`, `Bug`, `Task` on an Azure DevOps board, and whatever a customized process renamed them to. It is what the board badge shows, because it is what the user recognises. Azure DevOps only: every other provider leaves it empty (ADR 0031).
_Avoid_: issue type, kind, category (that's the state category), level (that's the normalized rung, below)

**Backlog level**:
The rung a Work item type sits on, normalized away from the names any one process uses: `epic`, `feature`, `requirement` or `task` — or empty for a type the project's backlog configuration places nowhere. Read from the project and its team, never matched against a literal type name, and it is the level rather than the type that drives behaviour: the loop refuses to start anything above `requirement`, a create files at `requirement`, and its slices file as `task` (ADR 0031). Unrelated to the **Epic** entry above, which is trau's provider-neutral name for any issue that has Sub-issues.
_Avoid_: hierarchy level, rank (that's the portfolio backlog's own ordering field), tier, depth

**Settled**:
Done or canceled — nothing left for trau to run. The numerator of an Epic's board progress (settled/total) and what queue drain marks an epic's sub-issues: each one as its own Checkpoint reaches a terminal phase mid-drain — merged reads done, quarantined reads quarantined — and then all of them when a clean epic finish settles the parent; remaining work is always total − settled. A canceled Sub-issue settles its share: an Epic with one canceled child still reaches n/n.
_Avoid_: done (bare — canceled isn't done but is settled), finished, closed, resolved

**Archive**:
A per-issue flag that shelves a family off the backlog board and every path that reads it — picker, add-all, an Epic's Sub-issues — and drops its pending Queue entries, all without touching the tracker. Archiving an Epic hides its whole family, including children synced after it was archived; archiving a Sub-issue hides just that child and drops it from its Epic's progress. It is sync-immune: inbound sync never sets or clears the flag, so a re-synced or tombstone-revived issue stays archived until explicitly unarchived. The board's "Archived (N)" view lists exactly the shelved families. Distinct from Settled (done/canceled work that ran) and from a tracker-removed tombstone (gone upstream) — an archived issue is deliberately put aside, not finished or deleted.
_Avoid_: delete, tombstone (that's tracker-removal), hide (bare), close, settle

**Assignee**:
The tracker-side person an issue is assigned to, reflected into trau as an identity plus display name. Not a trau account — trau has no users of its own; it only mirrors the tracker's people. Arrives with the issue on sync, which stays authoritative; the one write back out is an explicit assign gesture, which lands on the tracker first and is mirrored only once the tracker accepts it (ADR 0020).
_Avoid_: user (implies trau-side accounts), owner, member

**Unassigned**:
The absence of an Assignee — a first-class filterable state, not a person. Internal issues are always Unassigned.
_Avoid_: nobody, no owner, null assignee

**Me**:
The per-Repo identity derived from the tracker credentials — whoever the Linear API key or Jira token belongs to. Wherever an issue's Assignee is Me, surfaces say "Me" instead of the display name (the real name stays in a tooltip). The word "You" never appears for this concept.
_Avoid_: you (banned copy), current user, viewer (Linear API jargon), self, whoami

**Queue**:
The per-Repo ordered list of tickets and epics deliberately registered from the web for execution; the hub drains it one run at a time. Adding an epic queues its sub-issues too. Distinct from the loop (which takes whatever is eligible) and from the tracker backlog (what exists but hasn't been queued).
_Avoid_: batch (informal), backlog (that's the tracker's), schedule, loop

**Loop timeline**:
The Loop screen's running view — a single card showing every ticket the running Queue covers, epics expanded into their sub-issues. Finished tickets appear in the order they actually completed, the running ticket shows its live Step and Activity, and remaining tickets are an unordered set — the future order is decided at pick time, never promised.
_Avoid_: task list (they're tickets, not tasks), plan/schedule (implies a promised order), queue view (the Queue is the list; the timeline is its running progress)

**Run ledger**:
The Runs screen's flat list of every tracked run in scope — Needs attention first, then active, stopped, and merged, newest first within each group. Scoped to the Active repo, or aggregated across all Registered repos under "All projects". A run's row shows its Step progress, never a phase-by-phase stepper.
_Avoid_: board (the retired phase-column view), run list (bare), history

**Stopped** (run):
A run that is not live, carries no Failure class, and hasn't merged — a checkpoint left behind by a Stop or an ended loop, waiting for Resume or the next pick. One of the ledger's four buckets (active, Needs attention, stopped, merged).
_Avoid_: parked (that's a live session's state), paused (that's a Failure class), idle (that's an Instance state), abandoned

**Run once**:
A single-ticket run launched from the TUI's Run once screen or `trau <ID>`; it ends after that ticket. A TUI/CLI mechanic only — the web's counterpart for one deliberate ticket is Run next, which goes through the Queue.
_Avoid_: task, single task, one-off, run next (that's the web gesture)

**Run next**:
The web gesture that launches one deliberate ticket or Epic: it joins the front of the Queue and the drain arms, so it runs as soon as the Repo is free — immediately when idle, after the live run otherwise. Pending items behind it still drain, and an Epic is taken whole; on an already-queued item it means move to the front. Running one ticket alone is a queue of one. Web Resume is Run next on a ticket with a Checkpoint.
_Avoid_: run now (over-promises when a run is live), run once (that's the TUI/CLI launch), start (bare)

**Stop**:
Ending a live run (loop or Run once) from the TUI. Interrupts the in-flight phase; progress is checkpointed and resumable.
_Avoid_: quit (that's exiting the app), cancel, kill

**Quit**:
Exiting the TUI when nothing is live (menu or summary). Harmless — no run is affected.
_Avoid_: stop (that's ending a live run), exit

**Force quit**:
The second ctrl+c during a live run — the emergency escape that abandons the graceful Stop. Always available, never intercepted.
_Avoid_: quit (bare), hard exit

**Run data**:
Everything a run produces — Checkpoints, events, token calls, cost anomalies, phase artifacts (handoff, rubric, verdict, build notes, phase logs), pty transcripts, presence heartbeats, and drain outcomes. It is DB-first through the Hub: the loop child sends Run data to the Hub over HTTP and never opens a database; the Hub is the sole writer, keeping run history in `trau.db` and pty transcripts in a separate `transcripts.db`. Distinct from what legitimately stays on disk — configuration, repo-owned content (skills, checks, CI), provider files (`~/.claude` etc.), and the ephemeral agent-interface temp files.
_Avoid_: run artifacts (implies files-on-disk — the pre-DB-first framing), run files, logs (bare)

**Checkpoint**:
The durable saved state of one ticket's run — the phase it reached plus its branch, PR, and any failure — persisted so the run can resume or be inspected after the process ends. One per ticket. Casual "last state" means the checkpoint.
_Avoid_: state (bare, overloaded), run (that's the execution), snapshot, save

**Releasing**:
The Epic-level phase between the last Sub-issue's Ship and the Epic's own merge to base: syncing the epic branch with the base, opening its PR, gating its CI, merging. It is a real Checkpoint phase on the Epic id, not a display state — it survives the loop that wrote it — and it is never picked up as unfinished ticket work: a release the run died inside is re-entered through that Epic's own finalize instead, on a tree exit hygiene left clean by aborting the half-merge rather than committing its conflict markers. It ends one of two ways: merged, or handed to a human (unresolvable drift, a gate that never went green, or a merge only the operator can make), which the checkpoint records beside the phase. While the release is still trau's to finish, the Repo's Queue starts no new run — only that Epic's own finalize, which the hub re-arms on its own a bounded number of times when the finalize keeps dying — since its working tree is mid-merge on the epic branch; the hand-off releases the hold, as does a release that outlived those re-attempts and now reads faulted, and the Queue carries on with its other items. Neither ending is silent and neither reads as the other: a hand-off notifies the operator with the PR and settles the Epic's Queue item **awaiting-merge** — visibly not done, never re-attempted on its own, and settled done only once the PR actually lands — while a merge notifies it as delivered and settles done.
_Avoid_: finalizing (the code name — `FinalizeEpic`), shipping, deploying, publishing

**Failure class**:
Why a checkpoint stopped short — one of three, distinct from the phase (how far it got): **Paused** (blameless — a provider/rate/auth wall; work-in-progress intact, the fix is to Resume), **Faulted** (an unexpected error mid-run; WIP preserved and resumable), and **Gave up** (a verified dead end, surfaced as **Quarantined** — a human must decide).
_Avoid_: error, status (that's the phase), failed (bare)

**Needs attention**:
The bucket of checkpoints carrying any Failure class — Paused, Faulted, or Quarantined — floated to the top of the queue. Narrower **needs handling** is the subset a destructive Reset applies to: Faulted and Quarantined only, never Paused (which needs a Resume, not a reset).
_Avoid_: stuck (informal), broken, error queue

**Reset** (a ticket):
Destructively re-queue a ticket: delete its feature branch (local + remote) and artifacts, drop the Checkpoint, and re-open the tracker ticket to ready so the picker starts it clean. The recovery for a Quarantined dead end, or a Faulted ticket that Resume can't save.
_Avoid_: clear (that keeps branch + tracker), forget, resume, retry

**Clear** (aka forget):
Drop only the Checkpoint, leaving the git branch and tracker untouched; the next pick rebuilds from scratch with no git or tracker side effects. A lighter forget than Reset.
_Avoid_: reset (that's destructive + re-queues), delete, wipe

**Resume**:
Continue a ticket from its Checkpoint, keeping work-in-progress. The right move for a Paused ticket and the first thing to try on a Faulted one before Reset.
_Avoid_: retry, restart, rerun (bare), reset

**Config layer**:
One of the six sources a config value can resolve from — default, user, local, project, env var, CLI — in fixed precedence (default < user < local < project < env var < CLI; ADR 0016: the more specific file wins). A layer that states a key with an *empty* value states it: the empty value wins on precedence and the lower layer never applies. Every resolved value knows which layer it came from.
_Avoid_: scope, level, source (bare), origin

**Write target**:
The Config layer a settings edit persists to. Only project (`<repo>/.trau.ini`) and user (`~/.trau.ini`) are writable from settings surfaces; a user-layer write applies to every Repo on the machine.
_Avoid_: destination, save location

**Web-editable**:
Whether a config key may be written from the web settings surface. Fail-closed: keys are read-only over the web unless individually marked editable; tracker credential secrets are the write-only exception — settable and rotatable, never readable back.
_Avoid_: safe key (the old allowlist framing), editable (bare), unlocked

**Shadowed**:
A value or write outranked by a higher Config layer — persisted and legal, but without effect until the higher layer clears. An env var shadows both writable layers; a user value shadows a project write.
_Avoid_: overridden (collides with Provider override), masked, dead

**Section**:
A named group of related config keys, declared with the key itself and shared verbatim by the web and TUI settings surfaces.
_Avoid_: group (bare), category, panel

**Inbox**:
The web workspace where unclear or not-yet-written work is made ready to run — issues flagged for triage, questions waiting on an answer, proposed outcomes under review, and new drafts. Triage is one *source* of Inbox items, not the page's job; the tracker's triage labels keep their canonical names.
_Avoid_: triage inbox (triage is a source, not the surface), triage page

**Interview**:
The question-at-a-time session an agent runs over one Inbox item to make it ready, ending in a proposed outcome for approval. Starts only by a deliberate act — Start, typing a first message, Ask ahead, or Start over — never by merely viewing or browsing an item. The code-internal name is `grill`; grill/grilling never appears in UI copy.
_Avoid_: grilling (code name, not UI copy), chat (implies free-form talk), Q&A, session (bare — collides with Session state)

**Ask ahead**:
Running an item's Interview while the user is away, with the agent's own recommendations standing in for their answers: it either reaches a proposed outcome unaided or stops on the first question that needs their taste, waiting for them when they sit down. What moves an untouched item into the waiting-on-you group.
_Avoid_: pre-grill (code name `pregrill`), pre-interview, prep

**Draft**:
An Inbox item for an issue that does not exist yet — an authoring Interview with no tracker id, shown in the queue with a draft chip and titled by its seed. Purely client-side until the first message starts the session; it becomes a real issue only when its create outcome applies, and it evaporates if abandoned untouched.
_Avoid_: new issue (that's the entry action), authoring session (the mechanism, not the item), placeholder issue

**Start over** (an Interview):
Abandon the current Interview and immediately begin a fresh one on the same Inbox item, in one deliberate click — the old thread settles as abandoned and is never shown again. Touches only the session; the issue, its labels, and anything already applied are untouched.
_Avoid_: reset (that's the destructive ticket action), restart, retry, discard (that's abandoning without starting anew)

**Atlas**:
The per-Repo web page of agent-generated architecture Views, scoped to the Active repo like every screen. Everything it shows is derived from the Repo's code at a stamped commit — never hand-drawn, never edited in place. When merges land after that commit the Atlas is stale and says so; only a deliberate Regenerate refreshes it.
_Avoid_: diagram page (generic — any surface can render a diagram), map (informal planning jargon), blueprint (implies intent, not derived reality)

**View** (Atlas):
One named, independently generated perspective inside the Atlas — day one: **Data model** (entities and their relationships) and **App flows** (the significant runtime flows, each its own small graph). A View is produced by its own curated initialization prompt into a validated graph document; each View regenerates alone and carries the commit it was derived from.
_Avoid_: tab (display framing), diagram (bare), layer, perspective
