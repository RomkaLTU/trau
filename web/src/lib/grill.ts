import { queryOptions, type QueryClient } from '@tanstack/react-query'

import { apiFetch } from './api'
import { type Assignee } from './assignee'

// A grilling session's lifecycle (grilling-prd.md). running/waiting/parked/stalled
// are active; applied and abandoned are settled and stop counting against the
// one-active-session-per-issue rule.
export type GrillState =
  'running' | 'waiting' | 'parked' | 'stalled' | 'finished' | 'applied' | 'abandoned'

export type GrillRole = 'agent' | 'user' | 'system'
// interjection is a message the user typed while a turn was in flight: it steers the
// agent at its next step rather than answering anything it asked.
// proposal is one participant's draft outcome in a second-opinion session; the
// canonical decision stays a single outcome message, minted when the user picks one.
export type GrillKind =
  'question' | 'answer' | 'info' | 'outcome' | 'interjection' | 'proposal'

// GrillMode is the session type declared at create: an interview clarifies or
// authors an issue, research answers a question and delivers a findings report, and
// fix diagnoses a failed run and rewrites the ticket for the next attempt.
export type GrillMode = 'interview' | 'research' | 'fix'

// GrillSession mirrors the hub's GrillSessionView. issue_id is absent for an
// authoring session anchored to the repo alone; parked_reason carries the cause a
// parked or stalled session settled with. issue_title is the hub's join onto the
// grilled issue — the only title a settled session has, since applying drops the
// triage labels the board queries key on. issue_destination names where the apply
// wrote, so a review remounted on a settled session names the destination it used
// instead of the picker default, and apply_warnings carries the caveats that apply
// reported, so the same remount raises them again. auto_accept marks a session
// answering its own recommendations, so only a question needing the user's taste is
// ever asked. report_title is the title a research outcome gave its report, which the
// Research page names the session by; a session finished without one keeps the seed.
// applying marks an apply the hub is still writing, so a reload and a second tab read
// it from the hub rather than from a mutation they never ran. stopped separates the
// park the user made themselves from the one an idle window made for them: their next
// message steers the agent rather than answering whatever it left open. challengers
// names the second-opinion providers locked at create, so a session whose interview
// ends in a side-by-side review says so before it gets there.
export interface GrillSession {
  id: string
  repo: string
  issue_id?: string
  issue_destination?: GrillDestination
  issue_title?: string
  report_title?: string
  apply_warnings?: string[]
  state: GrillState
  session_chain?: string
  mode?: GrillMode
  provider?: string
  model?: string
  model_options?: string[]
  auto_accept?: boolean
  challengers?: string[]
  applying?: boolean
  stopped?: boolean
  parked_reason?: string
  created_at: string
  updated_at: string
}

// GrillMessage is one turn in the conversation. payload is the kind's JSON body
// embedded as-is: a QuestionPayload, a RoundPayload, an AnswerPayload, or an
// OutcomePayload. round_answers rides beside a round question rather than inside it:
// the answers fill in one at a time and the message itself never changes.
export interface GrillMessage {
  id: string
  role: GrillRole
  kind: GrillKind
  payload: unknown
  round_answers?: RoundAnswer[]
  created_at: string
}

// GrillDelta is one chunk of a running turn's reply, live-stream only — the hub never
// stores it. seq numbers a turn's deltas from one so a client can spot a dropped chunk
// rather than splice the reply back together across the gap.
export interface GrillDelta {
  seq: number
  text: string
}

// GrillActivity is one thing the agent did mid-turn — live-stream only on the same
// terms as a delta, and numbered on its own count. id ties a tool frame to the later
// frames that fill it in and close it out. detail only ever summarizes a call (a path,
// a query); the hub keeps whole tool inputs and results off the stream. text is the
// agent's thinking as it is written: a thinking frame carrying none opens a stretch,
// and every frame that follows grows it.
export interface GrillActivity {
  seq: number
  kind: string
  id?: string
  name?: string
  detail?: string
  text?: string
  ok?: boolean
}

export interface GrillDetail {
  session: GrillSession
  messages: GrillMessage[]
}

// GrillProviderOption is one provider a not-yet-started session can run on and the
// model catalog choosing it swaps to. disabled marks a provider the requested session
// type rules out, with reason saying why; note describes how a provider that does
// support it runs the type.
export interface GrillProviderOption {
  name: string
  model_options?: string[]
  disabled?: boolean
  reason?: string
  note?: string
}

// GrillDefaults is what a session of the requested type started right now would run
// on. It rides on the list resource so a start surface can offer the provider/model
// choice before any session exists; a cache write the panel makes itself may not carry
// it. providers carries every offered provider with its own catalog, so picking one
// swaps the model list without another round trip.
export interface GrillDefaults {
  provider: string
  model?: string
  model_options?: string[]
  providers?: GrillProviderOption[]
  // challengers is the GRILL_CHALLENGERS prefill for the second-opinion control,
  // already cut back to providers this machine can spawn. The interviewer is still in
  // it — the start surface owns that pick, so it drops the collision itself.
  challengers?: string[]
}

export interface GrillListResponse {
  repo: string
  tracker?: string
  defaults?: GrillDefaults
  sessions: GrillSession[]
}

// GrillAnswerResponse acknowledges an answer. message is absent for a round submission
// that left questions open: those answers land on the round they belong to rather than
// as a turn of their own.
export interface GrillAnswerResponse {
  session: GrillSession
  message?: GrillMessage
}

export type GrillStepStatus = 'ok' | 'failed'

// GrillDestination is where a create outcome files: the repo's configured tracker
// (the default) or the hub's internal issue store.
export type GrillDestination = 'tracker' | 'internal'

// GrillApplyStep is one tracker write's outcome — description, comment, or labels.
// A disposition only reports the steps it runs, so needs_split omits description.
export interface GrillApplyStep {
  step: string
  status: GrillStepStatus
  error?: string
}

// GrillApplyResponse mirrors the hub's apply result: the updated session, whether
// every step landed, and each step in the order it ran. A partial apply leaves the
// session finished so a retry re-runs the plan. warnings carry what the apply could
// not do but never gated on — the tracker note a detached ticket did not receive.
export interface GrillApplyResponse {
  session: GrillSession
  applied: boolean
  steps: GrillApplyStep[]
  warnings?: string[]
}

// GrillAppliedOutcome is what a landed apply reports to the host that mounted the
// review: the disposition that ran, the issue it wrote — for a create, the freshly
// filed issue the inbox's toast names and links — and whether any step failed on the
// way there.
export interface GrillAppliedOutcome {
  disposition: string
  issueId: string
  issueTitle: string
  hasFailures: boolean
}

// grillAppliedOutcome reads a full apply's response into the host's report. A create
// apply anchors the session to the created issue before settling, so issue_id names
// it; the title join can trail the write, so an empty issue_title falls back to the
// title that was filed. An apply lands even when a step it does not gate on fails —
// a tracker refusing the assignment — and the review raises those itself, so
// hasFailures is how the host keeps a plain success confirmation out of their way.
export function grillAppliedOutcome(
  res: GrillApplyResponse,
  disposition: string,
  filedTitle: string,
): GrillAppliedOutcome {
  return {
    disposition,
    issueId: res.session.issue_id ?? '',
    issueTitle: res.session.issue_title || filedTitle,
    hasFailures: res.steps.some((step) => step.status === 'failed'),
  }
}

export interface QuestionPayload {
  text: string
  options: string[]
  recommended?: string
  why?: string
  allow_free_text: boolean
}

// RoundQuestion is one question of a round — a set of independent questions the agent
// asks in one exchange and the user answers together. It carries exactly what a single
// question does, so one card renders either.
export type RoundQuestion = QuestionPayload

// RoundPayload is a round question message's body: text renders the whole round as one
// numbered string for the readers that only know single questions, and round holds the
// questions themselves.
export interface RoundPayload {
  text: string
  round: RoundQuestion[]
}

// RoundAnswer is one settled question of a round, named by the position the question
// holds in it. auto marks one auto-accept took from the agent's own recommendation, so
// only the questions needing the user's taste were ever asked.
export interface RoundAnswer {
  index: number
  text: string
  auto?: boolean
}

// AnswerPayload is an answer turn's body, and an interjection's. auto marks one the
// hub took from the agent's own recommendation on an auto-accept session rather than
// from the user; round marks the one that closes a whole round, whose answers the
// round's own cards already show.
export interface AnswerPayload {
  text: string
  auto?: boolean
  round?: boolean
}

// SubIssueProposal is one proposed slice of a split: a fully-specified child with
// optional labels (defaulting to ready-for-agent at apply) and blocked_by indices
// referencing sibling positions in the same array.
export interface SubIssueProposal {
  title: string
  description: string
  labels?: string[]
  blocked_by?: number[]
}

// ReportSource is one source a research report consulted. Research settled inside
// the repository consults none, so a report carrying no sources is normal.
export interface ReportSource {
  title: string
  url: string
  note?: string
}

export interface OutcomePayload {
  disposition: string
  // title and labels are carried by a create outcome — the new issue's title and its
  // labels (a single issue defaults to ready-for-agent server-side).
  title?: string
  proposed_description?: string
  // findings is the research outcome's Markdown report, sources the ones it cited.
  findings?: string
  sources?: ReportSource[]
  labels?: string[]
  sub_issues?: SubIssueProposal[]
  summary: string
  // disagreement rides on a consensus outcome alone: the mechanical record of how the
  // second-opinion panel got from its opening split to the single proposal below.
  disagreement?: GrillDisagreement
}

// GrillDisagreement is the panel's own history of the decision: each member's opening
// disposition, then every challenge round's endorsements and revisions, plus the notes
// for what no member's turn could record — a member that dropped out mid-round.
export interface GrillDisagreement {
  winner: string
  initial: GrillDisposition[]
  rounds: GrillDisagreementRound[]
  notes: string[]
}

export interface GrillDisposition {
  provider: string
  disposition: string
}

export interface GrillDisagreementRound {
  round: number
  turns: GrillDisagreementTurn[]
}

// GrillDisagreementTurn is one member's move in one round: the proposal it endorsed,
// or the disposition it revised to and the note saying what it disputes.
export interface GrillDisagreementTurn {
  provider: string
  endorse?: string
  disposition?: string
  note?: string
}

// The issue labels that qualify an issue for grilling (grilling-prd.md inbox).
export const GRILLABLE_LABELS = ['needs-triage', 'needs-info', 'needs-split']

export function isGrillable(labels: string[]): boolean {
  return labels.some((l) => GRILLABLE_LABELS.includes(l))
}

export function isSettled(state: GrillState): boolean {
  return state === 'applied' || state === 'abandoned'
}

// isOver reports whether a session has stopped for good: a settled one, and a
// finished one whose proposal is reviewed on the Inbox rather than answered.
export function isOver(state: GrillState): boolean {
  return isSettled(state) || state === 'finished'
}

// isAwaitingAnswer reports whether a session in state can take the user's answer —
// the states whose child is blocked on ask_user (waiting) or has parked with a
// pending answer or resume (parked, stalled).
export function isAwaitingAnswer(state: GrillState): boolean {
  return state === 'waiting' || state === 'parked' || state === 'stalled'
}

// canCompose reports whether the composer takes typing in state. running takes it as
// an interjection the agent reads at its next step, so a turn never has to be waited
// out. stalled takes it as the answer it was still owed: the hub accepts one on a
// stalled session and resumes off it, so typing is a way out of the stall beside the
// Resume button.
export function canCompose(state: GrillState): boolean {
  return state === 'running' || state === 'waiting' || state === 'parked' || state === 'stalled'
}

// composerPlaceholder is the composer's prompt — what the message will do in a state
// that takes typing on its own terms, and otherwise the reason the box is disabled.
export function composerPlaceholder(state: GrillState): string {
  switch (state) {
    case 'running':
      return 'Steer the agent — it will see this at its next step…'
    case 'waiting':
    case 'parked':
    case 'stalled':
      return 'Type your answer…'
    default:
      return 'This session has ended.'
  }
}

function base(repo: string): string {
  return `/api/v1/repos/${encodeURIComponent(repo)}/grill`
}

// A failure names the session type the surface asked for, so the Research page never
// reports on an interview it never ran.
function modeNoun(mode?: GrillMode): string {
  if (mode === 'research') return 'research'
  if (mode === 'fix') return 'propose fix'
  return 'interview'
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as {
    error?: string
  } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

async function fetchGrillSessions(
  repo: string,
  state?: GrillState,
  mode?: GrillMode,
): Promise<GrillListResponse> {
  const query = new URLSearchParams()
  if (state) query.set('state', state)
  if (mode) query.set('mode', mode)
  const q = query.toString()
  const res = await apiFetch(q ? `${base(repo)}?${q}` : base(repo))
  if (!res.ok)
    throw new Error(await errorMessage(res, `list ${modeNoun(mode)} sessions failed`))
  return res.json()
}

// The list polls because it is the only feed the queue rail has for sessions whose
// thread is not on screen — only the open conversation gets an SSE stream. It is the
// triage feed, so it asks for interviews alone (legacy rows with no mode included);
// research lives on its own page.
export const grillSessionsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['grill', repo],
    queryFn: () => fetchGrillSessions(repo, undefined, 'interview'),
    enabled: repo !== '',
    staleTime: 10_000,
    refetchInterval: 5_000,
  })

// grillDefaultsQueryOptions reads what a session of one type would start on. Provider
// availability depends on the session type, so the mode is part of the key. It nests
// under the repo's grill key so the list's invalidations reach it.
export const grillDefaultsQueryOptions = (repo: string, mode: GrillMode) =>
  queryOptions({
    queryKey: ['grill', repo, 'defaults', mode],
    queryFn: async () => (await fetchGrillSessions(repo, undefined, mode)).defaults ?? null,
    enabled: repo !== '',
    staleTime: 60_000,
  })

// startModelOptions is the model catalog to offer for a provider before a session
// exists: the provider's own list from the defaults payload, falling back to the flat
// default catalog for the default provider.
export function startModelOptions(
  defaults: GrillDefaults | undefined,
  provider: string,
): string[] {
  const match = defaults?.providers?.find((p) => p.name === provider)
  if (match) return match.model_options ?? []
  return provider === (defaults?.provider ?? 'claude')
    ? (defaults?.model_options ?? [])
    : []
}

// researchGrillSessionsQueryOptions reads the repo's research sessions — the feed
// behind the Research page, where a report stays readable long after the day it was
// written, so it carries the settled ones as well as the active. The mode also makes
// the response's defaults research-appropriate, since provider availability depends
// on the session type.
export const researchGrillSessionsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['grill', repo, 'research'],
    queryFn: () => fetchGrillSessions(repo, undefined, 'research'),
    enabled: repo !== '',
    staleTime: 10_000,
    refetchInterval: 5_000,
  })

// appliedGrillSessionsQueryOptions reads the repo's applied sessions. They are the
// only trace of a triaged issue once apply drops its labels, so the inbox's "Done
// today" reads them here rather than from the board. The key nests under the repo's
// grill list so an apply invalidation reaches it, while the narrower key leaves the
// unfiltered list — and the auto-start that reads it — alone.
export const appliedGrillSessionsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['grill', repo, 'applied'],
    queryFn: () => fetchGrillSessions(repo, 'applied'),
    enabled: repo !== '',
    staleTime: 10_000,
  })

// GrillMachineResponse is a machine-wide session feed: every project's sessions in
// one list, each carrying the registry name of whichever repo it belongs to.
export interface GrillMachineResponse {
  sessions: GrillSession[]
}

const runningGrillsQueryKey = ['grill-running'] as const

async function fetchRunningGrills(): Promise<GrillMachineResponse> {
  const res = await apiFetch('/api/v1/grill?state=running')
  if (!res.ok) throw new Error(await errorMessage(res, 'list running interviews failed'))
  return res.json()
}

// The running feed is what turns a repo's switcher icon teal, so it polls at the
// per-repo list's cadence: a session that starts or settles shows within one poll.
export const runningGrillsQueryOptions = () =>
  queryOptions({
    queryKey: runningGrillsQueryKey,
    queryFn: fetchRunningGrills,
    staleTime: 10_000,
    refetchInterval: 5_000,
  })

async function fetchGrillDetail(sid: string): Promise<GrillDetail> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}`)
  if (!res.ok) throw new Error(await errorMessage(res, 'fetch interview session failed'))
  return res.json()
}

export const grillDetailQueryOptions = (sid: string) =>
  queryOptions({
    queryKey: ['grill-session', sid],
    queryFn: () => fetchGrillDetail(sid),
    enabled: sid !== '',
    staleTime: 5_000,
  })

// GrillStartError carries the refusal's status, so a start that raced another one
// can tell the hub's one-session-per-issue conflict from a real failure.
export class GrillStartError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = 'GrillStartError'
    this.status = status
  }
}

// isActiveSessionConflict reports whether a start was refused because the issue
// already has a live session — the one refusal a start surface can act on.
export function isActiveSessionConflict(err: unknown): boolean {
  return err instanceof GrillStartError && err.status === 409
}

// GrillStartOpening is what a session opens on: the text seeding the opening user
// turn, the provider to lock the session's backend to, the model to pin its first
// turn, the session type, and whether the agent's recommendations are auto-accepted.
// An omitted provider or model leaves the hub to resolve the repo's default; an
// omitted mode opens an interview and an omitted autoAccept asks every question.
// fromSession names the research session whose report seeds the interview — the hub
// writes the opening note and grounds the first turn in the report itself.
export interface GrillStartOpening {
  seed?: string
  model?: string
  provider?: string
  mode?: GrillMode
  autoAccept?: boolean
  fromSession?: string
  // challengers are the second-opinion providers the interview ends in a side-by-side
  // review against: at most two, never the interviewer itself. Empty is a solo session.
  challengers?: string[]
}

// startGrillSession opens a session. An empty issueId with a seed starts a
// from-scratch authoring session anchored to the repo alone, the seed becoming the
// first turn; a concrete issueId grills that issue. The provider, model, mode and
// auto-accept the session opens on all lock for its lifetime.
export async function startGrillSession(
  repo: string,
  issueId: string,
  opening: GrillStartOpening = {},
): Promise<GrillSession> {
  const res = await apiFetch(base(repo), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      issue_id: issueId,
      idea: opening.seed ?? '',
      model: opening.model ?? '',
      provider: opening.provider ?? '',
      mode: opening.mode ?? 'interview',
      auto_accept: opening.autoAccept ?? false,
      from_session: opening.fromSession ?? '',
      challengers: opening.challengers ?? [],
    }),
  })
  if (!res.ok) {
    throw new GrillStartError(
      await errorMessage(res, `start ${modeNoun(opening.mode)} session failed`),
      res.status,
    )
  }
  return res.json()
}

// publishGrillSession puts a freshly started session at the head of the repo's
// list. A surface that navigates on start then arrives at a live conversation
// instead of the preview the list would hold until its next poll.
export function publishGrillSession(
  client: QueryClient,
  repo: string,
  session: GrillSession,
): void {
  client.setQueryData<GrillListResponse>(['grill', repo], (prev) =>
    prev
      ? {
          ...prev,
          sessions: [session, ...prev.sessions.filter((s) => s.id !== session.id)],
        }
      : { repo, sessions: [session] },
  )
}

// PregrillOutcome is one issue's result from an AFK pre-grill pass: a question was
// parked for the user, a rewrite proposal was drafted, the issue was already clear,
// the turn errored, or the issue was skipped (active session or past the pass limit).
export type PregrillOutcome = 'question_parked' | 'rewrite_drafted' | 'clear' | 'error' | 'skipped'

export interface PregrillResult {
  issue_id: string
  session_id?: string
  outcome: PregrillOutcome
  detail?: string
}

export interface PregrillResponse {
  repo: string
  max: number
  results: PregrillResult[]
}

// pregrillIssues runs the bounded, sequential AFK pre-grill pass over issueIds. The
// hub caps the number of turns at GRILL_PREGRILL_MAX and skips issues that already
// have an active session; each grilled issue lands in the inbox as its outcome.
// provider and model pin every session the pass opens, the same choice a
// hand-started one takes.
export async function pregrillIssues(
  repo: string,
  issueIds: string[],
  model = '',
  provider = '',
): Promise<PregrillResponse> {
  const res = await apiFetch(`${base(repo)}/pregrill`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ issue_ids: issueIds, model, provider }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'ask ahead failed'))
  return res.json()
}

export async function answerGrill(sid: string, text: string): Promise<GrillAnswerResponse> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/answer`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'answer failed'))
  return res.json()
}

// answerGrillRound submits a round's answers, each naming the question it settles by
// position. A submission that leaves questions open is kept: the round stays open on
// the rest, which is what lets the user step away part way through one.
export async function answerGrillRound(
  sid: string,
  answers: RoundAnswer[],
): Promise<GrillAnswerResponse> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/answer`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ answers: answers.map(({ index, text }) => ({ index, text })) }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'answer failed'))
  return res.json()
}

// applyGrill writes a finished session's proposed outcome to the tracker. A rewrite,
// split, or create carries its (possibly edited) description in the body; a split or
// create-epic also carries the edited sub-issues, and a create carries the edited
// title. destination rides along when the user picked one: on a create it is where
// the issue files, and on an anchored rewrite or split "internal" converts the
// anchored ticket and applies to the internal issue it becomes. assignee rides along
// only when a person was picked, and every issue the apply creates lands on them.
// hierarchy rides along on an Azure DevOps create: the work-item type to file as
// and the Feature to nest it under. Other dispositions carry none and let the hub
// fall back to the proposal.
export interface AzureHierarchyChoice {
  workItemType: string
  parent: string
}

// GrillApplyError carries the refusal's status so a card can tell the hub's
// one-apply-at-a-time guard from a real failure.
export class GrillApplyError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = 'GrillApplyError'
    this.status = status
  }
}

// The hub's answer to a second apply while the first is still writing.
export const APPLY_IN_PROGRESS = 'apply already in progress'

// isApplyInProgress reports whether an apply lost the race to another tab's. It is not
// a failure to show: the settle arrives over the stream.
export function isApplyInProgress(err: unknown): boolean {
  return err instanceof GrillApplyError && err.status === 409 && err.message === APPLY_IN_PROGRESS
}

export async function applyGrill(
  sid: string,
  proposedDescription: string,
  subIssues?: SubIssueProposal[],
  title?: string,
  destination?: GrillDestination,
  assignee?: Assignee | null,
  hierarchy?: AzureHierarchyChoice,
): Promise<GrillApplyResponse> {
  const body: {
    proposed_description: string
    sub_issues?: SubIssueProposal[]
    title?: string
    destination?: GrillDestination
    assignee?: { id: string; name: string }
    work_item_type?: string
    parent?: string
  } = {
    proposed_description: proposedDescription,
  }
  if (subIssues) body.sub_issues = subIssues
  if (title !== undefined) body.title = title
  if (destination) body.destination = destination
  if (assignee) body.assignee = { id: assignee.id, name: assignee.name }
  if (hierarchy?.workItemType) body.work_item_type = hierarchy.workItemType
  if (hierarchy?.parent) body.parent = hierarchy.parent
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/apply`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new GrillApplyError(await errorMessage(res, 'apply failed'), res.status)
  return res.json()
}

// switchGrillModel points the session's next turn at model; an in-flight turn
// finishes on the old one. The hub echoes the change over the session's state
// frames, so callers need no optimistic update.
export async function switchGrillModel(sid: string, model: string): Promise<GrillSession> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/model`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'switch model failed'))
  return res.json()
}

// setGrillAutoAccept turns a live session's auto-accept on or off; switching it on
// while a recommended question is waiting answers that one too. The hub echoes the
// change over the session's state frames, so callers need no optimistic update.
export async function setGrillAutoAccept(sid: string, enabled: boolean): Promise<GrillSession> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/auto-accept`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'auto-accept switch failed'))
  return res.json()
}

// stopGrill kills the session's in-flight turn and parks it: the next message the
// user sends steers the conversation instead of answering a question. The hub echoes
// the park over the session's state frames, so callers need no optimistic update.
export async function stopGrill(sid: string): Promise<GrillSession> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/stop`, {
    method: 'POST',
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'stop failed'))
  return res.json()
}

// resumeGrill restarts the turn a stalled session died on. The hub composes that
// turn's prompt from the session itself, so nothing is sent with the request and
// nothing new lands in the transcript.
export async function resumeGrill(sid: string): Promise<GrillSession> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/resume`, {
    method: 'POST',
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'resume failed'))
  return res.json()
}

// abandonGrill settles a session as abandoned — the discard path, where the user
// rejects the proposal and nothing is written to the tracker.
export async function abandonGrill(sid: string): Promise<GrillSession> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/abandon`, {
    method: 'POST',
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'discard failed'))
  return res.json()
}

// renameGrillReport gives a research report the title the user chose. It outranks the
// one the agent proposed for good, so a follow-up turn never takes the name back.
export async function renameGrillReport(sid: string, title: string): Promise<GrillSession> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/title`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'rename report failed'))
  return res.json()
}

// deleteGrillReport drops a settled research report and its transcript. Reports are
// exempt from the hub's retention pruning, so this is the only way one goes away.
export async function deleteGrillReport(sid: string): Promise<void> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}`, {
    method: 'DELETE',
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'delete report failed'))
}

export function grillStreamURL(sid: string): string {
  return `/api/v1/grill/${encodeURIComponent(sid)}/stream`
}

// activeSessionForIssue picks the issue's live session — the newest unsettled one
// the panel reopens instead of starting a second. The list arrives id-desc, so the
// first match is the newest.
export function activeSessionForIssue(
  sessions: GrillSession[] | undefined,
  issueId: string,
): GrillSession | undefined {
  return sessions?.find((s) => s.issue_id === issueId && !isSettled(s.state))
}

// activeFixSessionForIssue narrows that to the ticket's live fix session, the one a
// Propose fix surface reopens instead of starting a second diagnosis.
export function activeFixSessionForIssue(
  sessions: GrillSession[] | undefined,
  issueId: string,
): GrillSession | undefined {
  return sessions?.find(
    (s) => s.issue_id === issueId && s.mode === 'fix' && !isSettled(s.state),
  )
}

// abandonIssueSessions maps an issue's unsettled sessions to abandoned — the
// optimistic mirror of the abandon endpoint, so a discarded conversation flips to
// its preview without waiting on a refetch.
export function abandonIssueSessions(
  sessions: readonly GrillSession[] | undefined,
  issueId: string,
): GrillSession[] {
  return (sessions ?? []).map((s) =>
    s.issue_id === issueId && !isSettled(s.state) ? { ...s, state: 'abandoned' as const } : s,
  )
}

// applySessionModel points the list's copy of a session at model. The switch endpoint
// persists immediately but the list only re-polls every few seconds, and Start over
// reads the model off that copy — so a switch has to land here or starting again
// silently reverts to the model the session opened on.
export function applySessionModel(
  sessions: readonly GrillSession[] | undefined,
  sid: string,
  model: string,
): GrillSession[] {
  return (sessions ?? []).map((s) => (s.id === sid ? { ...s, model } : s))
}

function messageOrder(a: GrillMessage, b: GrillMessage): number {
  return Number(a.id) - Number(b.id)
}

// upsertMessage inserts msg or replaces the entry that shares its id, keeping the
// list ordered by id. The SSE reconnect backfill re-sends messages already held, so
// merging by id is how a dropped-and-reconnected stream heals without duplicates.
export function upsertMessage(list: GrillMessage[], msg: GrillMessage): GrillMessage[] {
  const at = list.findIndex((m) => m.id === msg.id)
  if (at !== -1) {
    if (list[at] === msg) return list
    const next = list.slice()
    next[at] = keepRoundAnswers(list[at], msg)
    return next
  }
  return [...list, msg].sort(messageOrder)
}

// keepRoundAnswers holds a round's answers against a copy of the message that has
// fewer. A round only ever gains answers, so the longer set is always the newer one:
// the frame that poses a round carries none, and a hydrate racing the stream can carry
// a set the stream has already grown past.
function keepRoundAnswers(held: GrillMessage, incoming: GrillMessage): GrillMessage {
  const before = held.round_answers ?? []
  const after = incoming.round_answers ?? []
  if (before.length <= after.length) return incoming
  return { ...incoming, round_answers: before }
}

export function mergeMessages(list: GrillMessage[], incoming: GrillMessage[]): GrillMessage[] {
  return incoming.reduce(upsertMessage, list)
}

// questionPayload reads a question message's body, defaulting a missing
// allow_free_text to true to match the hub's ask_user default.
export function questionPayload(msg: GrillMessage): QuestionPayload {
  const p = (msg.payload ?? {}) as Partial<QuestionPayload>
  return {
    text: typeof p.text === 'string' ? p.text : '',
    options: Array.isArray(p.options) ? p.options : [],
    recommended: typeof p.recommended === 'string' ? p.recommended : undefined,
    why: typeof p.why === 'string' ? p.why : undefined,
    allow_free_text: p.allow_free_text !== false,
  }
}

// roundQuestions reads a question message as a round, or null when it is the single
// question ask_user poses. Each question is parsed on questionPayload's own defensive
// terms, so one card renders either kind.
export function roundQuestions(msg: GrillMessage): RoundQuestion[] | null {
  const p = (msg.payload ?? {}) as Partial<RoundPayload>
  if (!Array.isArray(p.round) || p.round.length === 0) return null
  return p.round.map((q) => questionPayload({ ...msg, payload: q }))
}

// roundAnswers reads the answers a round has collected so far, ordered by the position
// of the question each one settles.
export function roundAnswers(msg: GrillMessage): RoundAnswer[] {
  const raw = Array.isArray(msg.round_answers) ? msg.round_answers : []
  return raw
    .filter((a) => Number.isInteger(a?.index) && typeof a?.text === 'string')
    .map((a) => ({ index: a.index, text: a.text, auto: a.auto === true }))
    .sort((a, b) => a.index - b.index)
}

// isRoundAnswer reports whether an answer message closed a round rather than a single
// question. The round's own cards already show every answer it carries, so the thread
// leaves it out instead of repeating the whole set as a bubble.
export function isRoundAnswer(msg: GrillMessage): boolean {
  const p = (msg.payload ?? {}) as Partial<AnswerPayload>
  return p.round === true
}

export function answerText(msg: GrillMessage): string {
  const p = (msg.payload ?? {}) as Partial<AnswerPayload>
  return typeof p.text === 'string' ? p.text : ''
}

// isAutoAnswer reports whether the hub answered the question itself from the agent's
// recommendation, which the transcript badges so the audit trail stays honest about
// who chose.
export function isAutoAnswer(msg: GrillMessage): boolean {
  const p = (msg.payload ?? {}) as Partial<AnswerPayload>
  return p.auto === true
}

export function outcomePayload(msg: GrillMessage): OutcomePayload {
  const p = (msg.payload ?? {}) as Partial<OutcomePayload>
  return {
    disposition: typeof p.disposition === 'string' ? p.disposition : '',
    title: typeof p.title === 'string' ? p.title : undefined,
    proposed_description:
      typeof p.proposed_description === 'string' ? p.proposed_description : undefined,
    findings: typeof p.findings === 'string' ? p.findings : undefined,
    sources: Array.isArray(p.sources) ? p.sources.map(parseSource) : undefined,
    labels: Array.isArray(p.labels)
      ? p.labels.filter((l): l is string => typeof l === 'string')
      : undefined,
    sub_issues: Array.isArray(p.sub_issues) ? p.sub_issues.map(parseSubIssue) : undefined,
    summary: typeof p.summary === 'string' ? p.summary : '',
    disagreement: parseDisagreement(p.disagreement),
  }
}

function parseDisagreement(raw: unknown): GrillDisagreement | undefined {
  if (!isRecord(raw)) return undefined
  const d = raw as Partial<GrillDisagreement>
  return {
    winner: typeof d.winner === 'string' ? d.winner : '',
    initial: Array.isArray(d.initial) ? d.initial.filter(isRecord).map((i) => ({
      provider: typeof i.provider === 'string' ? i.provider : '',
      disposition: typeof i.disposition === 'string' ? i.disposition : '',
    })) : [],
    rounds: Array.isArray(d.rounds) ? d.rounds.filter(isRecord).map((r) => ({
      round: Number.isInteger(r.round) ? (r.round as number) : 0,
      turns: Array.isArray(r.turns) ? r.turns.filter(isRecord).map((t) => ({
        provider: typeof t.provider === 'string' ? t.provider : '',
        endorse: typeof t.endorse === 'string' ? t.endorse : undefined,
        disposition: typeof t.disposition === 'string' ? t.disposition : undefined,
        note: typeof t.note === 'string' ? t.note : undefined,
      })) : [],
    })) : [],
    notes: Array.isArray(d.notes) ? d.notes.filter((n): n is string => typeof n === 'string') : [],
  }
}

function isRecord(raw: unknown): raw is Record<string, unknown> {
  return raw !== null && typeof raw === 'object'
}

// SessionProposal is the proposal one panel member currently stands behind, carrying
// the message id the choose call names it by and every challenge note that member
// raised while the rounds ran.
export interface SessionProposal {
  id: string
  provider: string
  round: number
  outcome: OutcomePayload
  challengeNotes: string[]
}

// sessionProposals reads the proposal each panel member currently stands behind — its
// latest revision — in the order the members first proposed, so the interviewer's is
// first. An endorsement carries no proposal of its own; it moves a member's vote, not
// its draft, so it never replaces the proposal the review shows.
export function sessionProposals(messages: GrillMessage[]): SessionProposal[] {
  const out: SessionProposal[] = []
  for (const msg of messages) {
    if (msg.kind !== 'proposal') continue
    const p = (msg.payload ?? {}) as {
      provider?: unknown
      round?: unknown
      outcome?: unknown
      challenge_note?: unknown
    }
    const provider = typeof p.provider === 'string' ? p.provider : ''
    const at = out.findIndex((c) => c.provider === provider)
    if (typeof p.challenge_note === 'string' && p.challenge_note !== '' && at >= 0)
      out[at].challengeNotes.push(p.challenge_note)
    if (p.outcome === null || typeof p.outcome !== 'object') continue
    const proposal: SessionProposal = {
      id: msg.id,
      provider,
      round: Number.isInteger(p.round) ? (p.round as number) : 0,
      outcome: outcomePayload({ ...msg, payload: p.outcome }),
      challengeNotes: at >= 0 ? out[at].challengeNotes : [],
    }
    if (at >= 0) out[at] = proposal
    else out.push(proposal)
  }
  return out
}

// activeGrillCycle narrows a conversation to the panel cycle running now. A finished
// second-opinion session that takes a follow-up answer reopens and runs the whole
// cycle again — drafts and challenge rounds — so the rows the earlier cycles left
// behind stay in the thread as history and decide nothing. The boundary is the latest
// user answer that follows a proposal or an outcome, which is the answer that reopened
// the session.
export function activeGrillCycle(messages: GrillMessage[]): GrillMessage[] {
  let start = 0
  let proposed = false
  messages.forEach((msg, i) => {
    if (msg.kind === 'proposal' || msg.kind === 'outcome') proposed = true
    else if (proposed && msg.role === 'user' && msg.kind === 'answer') {
      start = i
      proposed = false
    }
  })
  return messages.slice(start)
}

// chooseGrillProposal promotes one proposal to the session's canonical outcome. The
// hub copies the payload into a fresh outcome message, which the editable review and
// Apply then ride exactly as on a solo session.
export async function chooseGrillProposal(
  sid: string,
  messageId: string,
): Promise<{ session: GrillSession; message: GrillMessage }> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/choose-proposal`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message_id: messageId }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'choose proposal failed'))
  return res.json()
}

function parseSource(raw: unknown): ReportSource {
  const p = (raw ?? {}) as Partial<ReportSource>
  return {
    title: typeof p.title === 'string' ? p.title : '',
    url: typeof p.url === 'string' ? p.url : '',
    note: typeof p.note === 'string' ? p.note : undefined,
  }
}

function parseSubIssue(raw: unknown): SubIssueProposal {
  const p = (raw ?? {}) as Partial<SubIssueProposal>
  return {
    title: typeof p.title === 'string' ? p.title : '',
    description: typeof p.description === 'string' ? p.description : '',
    labels: Array.isArray(p.labels)
      ? p.labels.filter((l): l is string => typeof l === 'string')
      : undefined,
    blocked_by: Array.isArray(p.blocked_by)
      ? p.blocked_by.filter((n): n is number => Number.isInteger(n))
      : undefined,
  }
}

// pendingQuestion is the question awaiting an answer: the last question with nothing
// of the user's after it. An interjection retires it as an answer does — the hub reads
// one as the user moving the conversation on and never poses that question again, so a
// round left open behind an interjection must not hold the composer down forever. A
// parked crash or no-outcome turn leaves no pending question, so the panel falls back
// to a plain resume composer.
export function pendingQuestion(messages: GrillMessage[]): GrillMessage | null {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].kind === 'question') {
      const settled = messages.some(
        (m) =>
          (m.kind === 'answer' || m.kind === 'interjection') &&
          Number(m.id) > Number(messages[i].id),
      )
      return settled ? null : messages[i]
    }
  }
  return null
}

export interface GrillProgress {
  answered: number
  total: number
}

// grillProgress is how far the grilling has got: the questions asked, and how many
// the user has answered. A pending turn is the only one outstanding — the session
// cannot ask the next one until it is answered — and a round counts every question it
// poses, the ones it is still short of included.
export function grillProgress(messages: GrillMessage[]): GrillProgress {
  const pending = pendingQuestion(messages)
  let answered = 0
  let total = 0
  for (const m of messages) {
    if (m.kind !== 'question') continue
    const round = roundQuestions(m)
    total += round ? round.length : 1
    if (m.id !== pending?.id) answered += round ? round.length : 1
    else if (round) answered += roundAnswers(m).length
  }
  return { answered, total }
}

export function latestOutcome(messages: GrillMessage[]): GrillMessage | null {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].kind === 'outcome') return messages[i]
  }
  return null
}

// PendingAnswer is an answer the user has sent that the hub has not echoed back yet.
// It rides beside messages so the thread shows the turn the moment it is sent; failed
// carries a send that errored, which the thread offers to retry rather than losing.
export interface PendingAnswer {
  id: string
  text: string
  failed: boolean
}

// StreamingReply is the running turn's reply so far, assembled from delta frames. It
// is a preview, never a message: the stored message frame replaces it. holed means the
// stream skipped a seq, so the thread shows the thinking row rather than holed prose.
export interface StreamingReply {
  seq: number
  text: string
  holed: boolean
}

export const NO_REPLY: StreamingReply = { seq: 0, text: '', holed: false }

// LiveActivity is what the running turn has been doing so far: a bounded ring of the
// most recent rows, one per call the agent made, following the reply's lifecycle — a
// message or state frame ends it.
export interface LiveActivity {
  seq: number
  items: GrillActivity[]
}

export const NO_ACTIVITY: LiveActivity = { seq: 0, items: [] }

const ACTIVITY_RING = 50

const THINKING_TEXT_MAX = 2000

// GrillLive is the panel's merged view of one session: the authoritative session
// plus its messages. live tracks whether a stream frame has set the session, so a
// late GET hydrate never reverts a state the stream already advanced; hydrated
// tracks whether the transcript itself has landed yet.
export interface GrillLive {
  session: GrillSession
  live: boolean
  hydrated: boolean
  messages: GrillMessage[]
  pending: PendingAnswer[]
  streaming: StreamingReply
  activity: LiveActivity
}

// GrillRoundFrame is a round's answer set as the hub pushes it, so a second tab on the
// same session watches each answer land. It names the round it belongs to by message
// id and carries no frame id: round progress is not transcript.
export interface GrillRoundFrame {
  message_id: string
  answers: RoundAnswer[]
}

export type GrillAction =
  | { type: 'hydrate'; detail: GrillDetail }
  | { type: 'message'; message: GrillMessage }
  | { type: 'round'; round: GrillRoundFrame }
  | { type: 'state'; session: GrillSession }
  | { type: 'delta'; delta: GrillDelta }
  | { type: 'activity'; activity: GrillActivity }
  | { type: 'send'; id: string; text: string }
  | { type: 'send-failed'; id: string; text: string }
  | { type: 'send-retry'; id: string }
  | { type: 'send-discard'; id: string }

export function grillReducer(state: GrillLive, action: GrillAction): GrillLive {
  switch (action.type) {
    case 'hydrate':
      return {
        ...state,
        session: state.live ? state.session : action.detail.session,
        hydrated: true,
        messages: mergeMessages(state.messages, action.detail.messages),
        pending: action.detail.messages
          .filter((m) => !holds(state.messages, m))
          .reduce(retirePending, state.pending),
      }
    // An interjection lands mid-turn and settles nothing: the reply it arrives beside
    // is still being written, so it is the one message frame that leaves it standing.
    case 'message': {
      const interjected = action.message.kind === 'interjection'
      return {
        ...state,
        messages: upsertMessage(state.messages, action.message),
        pending: holds(state.messages, action.message)
          ? state.pending
          : retirePending(state.pending, action.message),
        streaming: interjected ? state.streaming : NO_REPLY,
        activity: interjected ? state.activity : NO_ACTIVITY,
      }
    }
    // A round frame answers questions inside a message the thread already holds, and
    // settles nothing on its own: whatever the turn is streaming keeps streaming.
    case 'round': {
      const at = state.messages.findIndex((m) => m.id === action.round.message_id)
      if (at === -1) return state
      const messages = state.messages.slice()
      messages[at] = { ...messages[at], round_answers: action.round.answers }
      return { ...state, messages }
    }
    // Every state frame either settles the running turn or opens the next one, so it
    // ends whatever was streaming and rebases the seq for the turn ahead.
    case 'state':
      return {
        ...state,
        session: action.session,
        live: true,
        streaming: NO_REPLY,
        activity: NO_ACTIVITY,
      }
    case 'delta':
      return {
        ...state,
        streaming: appendDelta(state.streaming, action.delta, state.session.state),
      }
    case 'activity':
      return {
        ...state,
        activity: appendActivity(state.activity, action.activity, state.session.state),
      }
    case 'send':
      return {
        ...state,
        pending: [...state.pending, { id: action.id, text: action.text, failed: false }],
      }
    case 'send-failed':
      return {
        ...state,
        pending: markFailed(state.pending, action.id, action.text),
      }
    case 'send-retry':
      return {
        ...state,
        pending: state.pending.map((p) => (p.id === action.id ? { ...p, failed: false } : p)),
      }
    case 'send-discard':
      return {
        ...state,
        pending: state.pending.filter((p) => p.id !== action.id),
      }
  }
}

// appendDelta grows the reply by one chunk. Only a running turn streams, so a delta
// trailing a settled one never reopens a preview the session has moved past. A seq
// that skips means the broadcaster dropped a chunk: the reply stays holed for the
// turn, since every later chunk lands after the gap rather than filling it.
function appendDelta(reply: StreamingReply, delta: GrillDelta, state: GrillState): StreamingReply {
  if (state !== 'running') return reply
  if (delta.seq !== reply.seq + 1) return { seq: delta.seq, text: '', holed: true }
  return { seq: delta.seq, text: reply.text + delta.text, holed: reply.holed }
}

// appendActivity folds one frame into the turn's ring, dropping the oldest row once it
// is full. A seq that skips means what came before it is unaccounted for — a dropped
// frame, or a stream that opened mid-turn and is being replayed what it missed — so the
// ring starts over from this frame rather than reading as a complete account of the
// turn. It keeps the frame itself: activity is never stored, so one discarded here is
// gone for good.
function appendActivity(
  feed: LiveActivity,
  activity: GrillActivity,
  state: GrillState,
): LiveActivity {
  if (state !== 'running') return feed
  if (activity.seq !== feed.seq + 1) return { seq: activity.seq, items: foldActivity([], activity) }
  return { seq: activity.seq, items: foldActivity(feed.items, activity) }
}

// foldActivity keeps one row per call rather than one per frame: a tool frame either
// opens a row or fills in the one its id already opened, and a result frame resolves
// the row it closes. A result with no id — a provider that never named the call —
// settles the oldest row still running. Thinking is one row per stretch: the frame
// that carries no text opens it and the deltas that follow grow it, so a stretch
// attached to mid-stream grows the row it lands in rather than opening one per delta.
function foldActivity(rows: GrillActivity[], frame: GrillActivity): GrillActivity[] {
  if (frame.kind === 'result') {
    const at = frame.id
      ? rows.findIndex((row) => row.id === frame.id)
      : rows.findIndex((row) => row.kind === 'tool' && row.ok === undefined)
    if (at < 0) return rows
    return rows.map((row, i) => (i === at ? { ...row, ok: frame.ok } : row))
  }
  if (frame.kind === 'thinking' && frame.text) {
    const at = lastThinking(rows)
    if (at < 0) return [...rows, frame].slice(-ACTIVITY_RING)
    return rows.map((row, i) =>
      i === at ? { ...row, text: thinkingTail((row.text ?? '') + frame.text) } : row,
    )
  }
  const at = frame.id ? rows.findIndex((row) => row.id === frame.id) : -1
  if (at < 0) return [...rows, frame].slice(-ACTIVITY_RING)
  return rows.map((row, i) => (i === at ? { ...row, ...frame, seq: row.seq } : row))
}

function lastThinking(rows: GrillActivity[]): number {
  for (let i = rows.length - 1; i >= 0; i--) {
    if (rows[i].kind === 'thinking') return i
  }
  return -1
}

// A stretch the agent spends thinking runs far past what one line shows, and the row
// displays its tail, so an overlong one drops from the front.
function thinkingTail(text: string): string {
  return text.length > THINKING_TEXT_MAX ? text.slice(-THINKING_TEXT_MAX) : text
}

function holds(list: GrillMessage[], msg: GrillMessage): boolean {
  return list.some((m) => m.id === msg.id)
}

// retirePending drops the optimistic twin of an echoed answer. The hub assigns the
// message its own id, so the echo cannot be matched by id — the oldest unfailed send
// carrying the same text is the one it settles. Only a message the reducer has not
// held before retires a twin: the hub delivers every answer twice, once in the POST
// response and once over the stream, and a re-hydrate replays the whole transcript.
function retirePending(pending: PendingAnswer[], msg: GrillMessage): PendingAnswer[] {
  if (msg.kind !== 'answer' && msg.kind !== 'interjection') return pending
  const text = answerText(msg)
  const at = pending.findIndex((p) => !p.failed && p.text === text)
  return at === -1 ? pending : pending.filter((_, i) => i !== at)
}

// markFailed flags a send that errored. Its own entry may already be gone, since an
// echo settles the oldest unfailed twin rather than the send that produced it, so a
// failure whose entry was retired lands on the newest unfailed send of the same text
// — the one no echo is coming for.
function markFailed(pending: PendingAnswer[], id: string, text: string): PendingAnswer[] {
  let at = pending.findIndex((p) => p.id === id)
  if (at === -1) at = lastUnfailed(pending, text)
  return at === -1 ? pending : pending.map((p, i) => (i === at ? { ...p, failed: true } : p))
}

function lastUnfailed(pending: PendingAnswer[], text: string): number {
  for (let i = pending.length - 1; i >= 0; i--) {
    if (!pending[i].failed && pending[i].text === text) return i
  }
  return -1
}

export type GrillBannerTone = 'thinking' | 'parked' | 'stalled' | 'finished' | 'applied' | 'ended'

export interface GrillBanner {
  tone: GrillBannerTone
  headline: string
  hint?: string
  showResume?: boolean
}

// grillBanner is the state banner above the composer. waiting returns null — its
// question card is the banner. A normally-parked session (idle timeout) carries no
// reason and a pending question below; a crash or stall carries the reason as the
// hint.
export function grillBanner(session: GrillSession): GrillBanner | null {
  const reason = session.parked_reason?.trim() ?? ''
  switch (session.state) {
    case 'running':
      return {
        tone: 'thinking',
        headline: 'Thinking…',
        hint: 'The agent is working on your issue.',
      }
    case 'waiting':
      return null
    case 'parked':
      return reason
        ? { tone: 'parked', headline: 'Waiting for you', hint: reason }
        : {
            tone: 'parked',
            headline: 'Waiting for you',
            hint: 'Pick up anytime — answer below and the session resumes.',
          }
    // The cause lives in the stored reason — an auth wall and a usage wall both
    // stall, and naming one of them in the headline mislabelled every stall as a
    // rate limit regardless of why the session actually stopped.
    case 'stalled':
      return {
        tone: 'stalled',
        headline: 'Session stalled',
        hint: reason || 'Clear it, then resume — the interview picks up where it stopped.',
        showResume: true,
      }
    case 'finished':
      return {
        tone: 'finished',
        headline: 'Proposal ready',
        hint: 'Review the outcome before it is applied.',
      }
    case 'applied':
      return {
        tone: 'applied',
        headline: 'Applied',
        hint: 'The outcome was written to the tracker.',
      }
    case 'abandoned':
      return {
        tone: 'ended',
        headline: 'Session ended',
        hint: 'This session was abandoned.',
      }
  }
}

export type DiffOp = 'equal' | 'insert' | 'delete'

export interface DiffLine {
  op: DiffOp
  text: string
}

// diffLines is a line-level old→new diff for the rewrite review: an unchanged run
// renders once, an edit shows as a delete run followed by an insert run. It walks a
// longest-common-subsequence table, which is cheap on the short issue bodies a
// grilling rewrite produces and keeps the panel free of a diff dependency.
export function diffLines(before: string, after: string): DiffLine[] {
  const a = splitLines(before)
  const b = splitLines(after)
  const lcs = lcsLengths(a, b)
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ op: 'equal', text: a[i] })
      i++
      j++
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      out.push({ op: 'delete', text: a[i] })
      i++
    } else {
      out.push({ op: 'insert', text: b[j] })
      j++
    }
  }
  for (; i < a.length; i++) out.push({ op: 'delete', text: a[i] })
  for (; j < b.length; j++) out.push({ op: 'insert', text: b[j] })
  return out
}

export function diffHasChanges(lines: DiffLine[]): boolean {
  return lines.some((l) => l.op !== 'equal')
}

function splitLines(s: string): string[] {
  if (s === '') return []
  return s.replace(/\r\n/g, '\n').split('\n')
}

function lcsLengths(a: string[], b: string[]): number[][] {
  const table = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0))
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      table[i][j] =
        a[i] === b[j] ? table[i + 1][j + 1] + 1 : Math.max(table[i + 1][j], table[i][j + 1])
    }
  }
  return table
}
