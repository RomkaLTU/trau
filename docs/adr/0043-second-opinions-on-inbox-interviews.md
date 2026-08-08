# ADR 0043 — Second opinions on Inbox interviews

- **Status:** Accepted
- **Date:** 2026-08-08
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-1577, amended by COD-1578

## Context

An Inbox Interview is one provider's reading of one ticket. The interviewer asks
the questions, hears the answers, and proposes the outcome, so the proposal
carries that provider's judgement alone. The user reviews it, but they review it
against nothing: there is no second reading of the same conversation to weigh it
against, and a plausible-but-wrong disposition — a `rewrite` where the work is
epic-shaped, a `needs_split` where the slice is already thin — reads exactly like
a right one.

The pipeline already has the shape of an answer to this. `VERIFY_PANEL` runs
several verifiers over the same slice and merges their verdicts by policy, and
that cross-vendor disagreement is what catches a verdict one model was confident
and wrong about. The Inbox has no equivalent.

## Decision

**A shared interview, then independent post-interview drafts.** One interviewer
runs the interview exactly as today: one conversation, one provider, one set of
questions to the user. When it submits its outcome, each Challenger drafts its
own outcome from the same transcript in a headless run of its own, concurrently,
and never talks to the user. The session finishes when every Challenger has
submitted or failed.

**Unanimity is the consensus rule.** When every remaining panel member endorses
the same proposal, the session settles on that decision without asking the user
to pick. Anything short of unanimity falls back to the side-by-side review. The
drafts alone are almost never unanimous, so the rule is applied over challenge
rounds rather than over the drafts — see the amendment below.

**Side-by-side review is the fallback, and this slice always takes it.** With
Challengers present, this slice ends in a review that shows every proposal
labelled by the provider that wrote it, each in its disposition's own shape. The
user picks one, which promotes it to the session's canonical outcome; the
existing editable review and Apply then proceed on it unchanged.

**Mode stays `interview`; a `challengers` column carries the difference.** A
session with second opinions is an interview — same questions, same transcript,
same outcome contract — so it keeps `mode = interview` and gains a CSV column of
provider names locked at create, beside `provider` and `mode`.

**The canonical outcome stays a single `kind=outcome` row.** Proposals are a new
`kind=proposal` message kind carrying `{provider, round, outcome}`. Nothing that
reads a settled decision — `latestGrillOutcome`, the whole apply path — learns
that proposals exist; an Apply on a session that has proposals and no outcome is
refused with 409 rather than guessing.

**A lost Challenger degrades, never blocks.** A draft run that errors or stalls
is dropped and a system note records which provider was lost and why. If every
Challenger fails, the interviewer's proposal becomes the canonical outcome and
the session finishes exactly as a solo one would, with the notes above it.

## Considered options

- **A joint panel interview** — every provider in the same conversation, asking
  the user questions in turn. Rejected: it multiplies the questions the user
  answers by the number of participants, which is the opposite of what the Inbox
  is for, and the transcript stops being one conversation anybody can read.
- **Autonomous debate** — the participants argue with each other over rounds
  until they converge. Rejected for this slice: unbounded cost per interview and
  no guarantee of termination, for a decision the user reviews anyway. The
  bounded version — one contest round under unanimity — is the follow-up slice.
- **Majority wins** — settle on whatever most participants proposed. Rejected:
  with two or three participants a majority is one vote, and outcomes are not
  comparable enough for a vote to mean anything. Unanimity is the only merge that
  says something real; everything else goes to the user.
- **A fourth session mode** (`second_opinion` beside `interview`, `research`,
  `fix`) — rejected: the mode selects the first-turn prompt and the surface a
  session is listed on, and both are the interview's. A fourth value would fork
  every mode switch in the hub and the web to say the same thing twice.

## Consequences

- `grill_sessions` gains `challengers`; `hubstore.GrillSession` and
  `GrillSessionView` carry it, and the create path validates it (interview mode
  only, known and available providers, no duplicates, never the interviewer, at
  most two). Ask-ahead never sets it.
- The turn runner gains a draft phase that holds the session's single turn slot
  and fans out one headless run per Challenger. Challenger runs are one-shot:
  no session chain is persisted, so each phase starts fresh.
- A Challenger talks to a member-scoped MCP endpoint,
  `/api/v1/grill/{sid}/mcp/{member}`, exposing `submit_decision` alone — the same
  input schema as `finish_session`, so both decide against the same dispositions.
- `POST /api/v1/grill/{sid}/choose-proposal` promotes one proposal to the
  canonical outcome.
- `GRILL_CHALLENGERS` prefills the start surface's control (ADR 0011 catalog
  key, `Grilling & triage`, web-editable).
- The glossary gains **Second opinion** and **Challenger**.

## Amendment — challenge rounds (COD-1578)

The drafts phase alone almost never produces unanimity: two providers reading one
interview rarely word the same disposition the same way, so "unanimity or the user
picks" was in practice always the user picking. The bounded contest this ADR listed
under *Considered options* as the follow-up is now the path between them.

- **The panel is the interviewer's provider plus the challengers.** Every challenge
  turn is a fresh headless run, symmetric for every member: the interviewer's
  interactive session chain is deliberately not resumed, so its verdict is decided
  from the same material and against the same contract as everybody else's. Rounds
  run inside the turn slot the draft phase already holds.
- **`GRILL_CHALLENGE_ROUNDS` bounds the contest** (int, default 2, minimum 0;
  `Grilling & triage`, web-editable). Zero skips the rounds, which is exactly the
  drafts-only behaviour this ADR shipped. Unanimity stops them early.
- **`submit_decision` gains `endorse` and `challenge_note`**, and the member endpoint
  gains a round segment — `/api/v1/grill/{sid}/mcp/{member}/{round}` — so a turn can
  only record its own verdict on the round it was spawned for. A round turn either
  endorses one member's current proposal or revises with a note saying what it
  disputes; a revision implicitly endorses its own author. Proposal payloads gain
  `round`, `endorse` and `challenge_note`.
- **A consensus is written as the ordinary `kind=outcome` row**, so review and Apply
  stay a solo session's exactly. The disagreement summary — the opening split, each
  round's endorsements and revisions, the challenge notes, and any warning notes —
  is assembled mechanically from the recorded rounds (no extra model run) and rides
  in that payload as one more key the apply path ignores.
- **A member that fails a round is dropped** with a system note and unanimity is
  computed over the rest; a panel down to one member takes that member's proposal as
  the consensus, with the warning in the summary.
- **A reopen starts a new cycle.** A finished session that takes a follow-up answer
  re-runs the whole cycle — drafts and rounds — and the active cycle is everything
  after the latest reopening answer. Earlier cycles' proposal rows stay in the
  transcript as history and settle nothing.
- The glossary gains **Panel**, **Challenge round** and **Consensus proposal**.
