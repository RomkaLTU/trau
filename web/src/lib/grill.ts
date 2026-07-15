import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

// A grilling session's lifecycle (grilling-prd.md). running/waiting/parked/stalled
// are active; applied and abandoned are settled and stop counting against the
// one-active-session-per-issue rule.
export type GrillState =
  | 'running'
  | 'waiting'
  | 'parked'
  | 'stalled'
  | 'finished'
  | 'applied'
  | 'abandoned'

export type GrillRole = 'agent' | 'user' | 'system'
export type GrillKind = 'question' | 'answer' | 'info' | 'outcome'

// GrillSession mirrors the hub's GrillSessionView. issue_id is absent for an
// authoring session anchored to the repo alone; parked_reason carries the cause a
// parked or stalled session settled with. issue_title is the hub's join onto the
// grilled issue — the only title a settled session has, since applying drops the
// triage labels the board queries key on.
export interface GrillSession {
  id: string
  repo: string
  issue_id?: string
  issue_title?: string
  state: GrillState
  session_chain?: string
  model?: string
  parked_reason?: string
  created_at: string
  updated_at: string
}

// GrillMessage is one turn in the conversation. payload is the kind's JSON body
// embedded as-is: a QuestionPayload, an AnswerPayload, or an OutcomePayload.
export interface GrillMessage {
  id: string
  role: GrillRole
  kind: GrillKind
  payload: unknown
  created_at: string
}

// GrillDelta is one chunk of a running turn's reply, live-stream only — the hub never
// stores it. seq numbers a turn's deltas from one so a client can spot a dropped chunk
// rather than splice the reply back together across the gap.
export interface GrillDelta {
  seq: number
  text: string
}

export interface GrillDetail {
  session: GrillSession
  messages: GrillMessage[]
}

export interface GrillListResponse {
  repo: string
  sessions: GrillSession[]
}

export interface GrillAnswerResponse {
  session: GrillSession
  message: GrillMessage
}

export type GrillStepStatus = 'ok' | 'failed'

// GrillApplyStep is one tracker write's outcome — description, comment, or labels.
// A disposition only reports the steps it runs, so needs_split omits description.
export interface GrillApplyStep {
  step: string
  status: GrillStepStatus
  error?: string
}

// GrillApplyResponse mirrors the hub's apply result: the updated session, whether
// every step landed, and each step in the order it ran. A partial apply leaves the
// session finished so a retry re-runs the plan.
export interface GrillApplyResponse {
  session: GrillSession
  applied: boolean
  steps: GrillApplyStep[]
}

export interface QuestionPayload {
  text: string
  options: string[]
  recommended?: string
  why?: string
  allow_free_text: boolean
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

export interface OutcomePayload {
  disposition: string
  // title and labels are carried by a create outcome — the new issue's title and its
  // labels (a single issue defaults to ready-for-agent server-side).
  title?: string
  proposed_description?: string
  labels?: string[]
  sub_issues?: SubIssueProposal[]
  summary: string
}

// The issue labels that qualify an issue for grilling (grilling-prd.md inbox).
export const GRILLABLE_LABELS = ['needs-triage', 'needs-info', 'needs-split']

export function isGrillable(labels: string[]): boolean {
  return labels.some((l) => GRILLABLE_LABELS.includes(l))
}

export function isSettled(state: GrillState): boolean {
  return state === 'applied' || state === 'abandoned'
}

// isAwaitingAnswer reports whether a session in state can take the user's answer —
// the states whose child is blocked on ask_user (waiting) or has parked with a
// pending answer or resume (parked, stalled).
export function isAwaitingAnswer(state: GrillState): boolean {
  return state === 'waiting' || state === 'parked' || state === 'stalled'
}

// canCompose reports whether the composer takes typing in state. stalled awaits an
// answer but resumes through its banner's pre-filled Resume button, so the box stays
// shut until the session is moving again.
export function canCompose(state: GrillState): boolean {
  return state === 'waiting' || state === 'parked'
}

// composerPlaceholder is the composer's prompt — and, in a state canCompose refuses,
// the reason the box is disabled.
export function composerPlaceholder(state: GrillState): string {
  switch (state) {
    case 'running':
      return 'Agent is thinking…'
    case 'stalled':
      return 'Session stalled — resume to keep answering…'
    case 'waiting':
    case 'parked':
      return 'Type your answer…'
    default:
      return 'This session has ended.'
  }
}

// lastAnswer is the text of the user's most recent answer, the resume mechanic's
// pre-fill: a stalled session retries the turn it died on without retyping it.
export function lastAnswer(messages: GrillMessage[]): string {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].kind === 'answer') return answerText(messages[i])
  }
  return ''
}

function base(repo: string): string {
  return `/api/v1/repos/${encodeURIComponent(repo)}/grill`
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

async function fetchGrillSessions(repo: string, state?: GrillState): Promise<GrillListResponse> {
  const url = state ? `${base(repo)}?state=${state}` : base(repo)
  const res = await apiFetch(url)
  if (!res.ok) throw new Error(await errorMessage(res, 'list grill sessions failed'))
  return res.json()
}

export const grillSessionsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['grill', repo],
    queryFn: () => fetchGrillSessions(repo),
    enabled: repo !== '',
    staleTime: 10_000,
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

async function fetchGrillDetail(sid: string): Promise<GrillDetail> {
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}`)
  if (!res.ok) throw new Error(await errorMessage(res, 'fetch grill session failed'))
  return res.json()
}

export const grillDetailQueryOptions = (sid: string) =>
  queryOptions({
    queryKey: ['grill-session', sid],
    queryFn: () => fetchGrillDetail(sid),
    enabled: sid !== '',
    staleTime: 5_000,
  })

// startGrillSession opens a session. An empty issueId with an idea starts a
// from-scratch authoring session anchored to the repo alone, the idea seeding the
// first turn; a concrete issueId grills that issue.
export async function startGrillSession(
  repo: string,
  issueId: string,
  idea = '',
): Promise<GrillSession> {
  const res = await apiFetch(base(repo), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ issue_id: issueId, idea }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'start grill session failed'))
  return res.json()
}

// PregrillOutcome is one issue's result from an AFK pre-grill pass: a question was
// parked for the user, a rewrite proposal was drafted, the issue was already clear,
// the turn errored, or the issue was skipped (active session or past the pass limit).
export type PregrillOutcome =
  | 'question_parked'
  | 'rewrite_drafted'
  | 'clear'
  | 'error'
  | 'skipped'

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
export async function pregrillIssues(repo: string, issueIds: string[]): Promise<PregrillResponse> {
  const res = await apiFetch(`${base(repo)}/pregrill`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ issue_ids: issueIds }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'pre-grill failed'))
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

// applyGrill writes a finished session's proposed outcome to the tracker. A rewrite,
// split, or create carries its (possibly edited) description in the body; a split or
// create-epic also carries the edited sub-issues, and a create carries the edited
// title. Other dispositions carry none and let the hub fall back to the proposal.
export async function applyGrill(
  sid: string,
  proposedDescription: string,
  subIssues?: SubIssueProposal[],
  title?: string,
): Promise<GrillApplyResponse> {
  const body: {
    proposed_description: string
    sub_issues?: SubIssueProposal[]
    title?: string
  } = {
    proposed_description: proposedDescription,
  }
  if (subIssues) body.sub_issues = subIssues
  if (title !== undefined) body.title = title
  const res = await apiFetch(`/api/v1/grill/${encodeURIComponent(sid)}/apply`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'apply failed'))
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
    next[at] = msg
    return next
  }
  return [...list, msg].sort(messageOrder)
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

export function answerText(msg: GrillMessage): string {
  const p = (msg.payload ?? {}) as { text?: unknown }
  return typeof p.text === 'string' ? p.text : ''
}

export function outcomePayload(msg: GrillMessage): OutcomePayload {
  const p = (msg.payload ?? {}) as Partial<OutcomePayload>
  return {
    disposition: typeof p.disposition === 'string' ? p.disposition : '',
    title: typeof p.title === 'string' ? p.title : undefined,
    proposed_description:
      typeof p.proposed_description === 'string' ? p.proposed_description : undefined,
    labels: Array.isArray(p.labels)
      ? p.labels.filter((l): l is string => typeof l === 'string')
      : undefined,
    sub_issues: Array.isArray(p.sub_issues) ? p.sub_issues.map(parseSubIssue) : undefined,
    summary: typeof p.summary === 'string' ? p.summary : '',
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

// pendingQuestion is the question awaiting an answer: the last question with no
// answer after it. A parked crash or no-outcome turn leaves no pending question, so
// the panel falls back to a plain resume composer.
export function pendingQuestion(messages: GrillMessage[]): GrillMessage | null {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].kind === 'question') {
      const answered = messages.some(
        (m) => m.kind === 'answer' && Number(m.id) > Number(messages[i].id),
      )
      return answered ? null : messages[i]
    }
  }
  return null
}

export interface GrillProgress {
  answered: number
  total: number
}

// grillProgress is how far the grilling has got: the questions asked, and how many
// the user has answered. A question left pending is the only one outstanding — the
// session cannot ask the next one until the current one is answered.
export function grillProgress(messages: GrillMessage[]): GrillProgress {
  const total = messages.reduce((n, m) => (m.kind === 'question' ? n + 1 : n), 0)
  return { answered: pendingQuestion(messages) ? total - 1 : total, total }
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
}

export type GrillAction =
  | { type: 'hydrate'; detail: GrillDetail }
  | { type: 'message'; message: GrillMessage }
  | { type: 'state'; session: GrillSession }
  | { type: 'delta'; delta: GrillDelta }
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
    case 'message':
      return {
        ...state,
        messages: upsertMessage(state.messages, action.message),
        pending: holds(state.messages, action.message)
          ? state.pending
          : retirePending(state.pending, action.message),
        streaming: NO_REPLY,
      }
    // Every state frame either settles the running turn or opens the next one, so it
    // ends whatever was streaming and rebases the seq for the turn ahead.
    case 'state':
      return {
        ...state,
        session: action.session,
        live: true,
        streaming: NO_REPLY,
      }
    case 'delta':
      return {
        ...state,
        streaming: appendDelta(state.streaming, action.delta, state.session.state),
      }
    case 'send':
      return {
        ...state,
        pending: [...state.pending, { id: action.id, text: action.text, failed: false }],
      }
    case 'send-failed':
      return { ...state, pending: markFailed(state.pending, action.id, action.text) }
    case 'send-retry':
      return {
        ...state,
        pending: state.pending.map((p) => (p.id === action.id ? { ...p, failed: false } : p)),
      }
    case 'send-discard':
      return { ...state, pending: state.pending.filter((p) => p.id !== action.id) }
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

function holds(list: GrillMessage[], msg: GrillMessage): boolean {
  return list.some((m) => m.id === msg.id)
}

// retirePending drops the optimistic twin of an echoed answer. The hub assigns the
// message its own id, so the echo cannot be matched by id — the oldest unfailed send
// carrying the same text is the one it settles. Only a message the reducer has not
// held before retires a twin: the hub delivers every answer twice, once in the POST
// response and once over the stream, and a re-hydrate replays the whole transcript.
function retirePending(pending: PendingAnswer[], msg: GrillMessage): PendingAnswer[] {
  if (msg.kind !== 'answer') return pending
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

export type GrillBannerTone =
  | 'thinking'
  | 'parked'
  | 'stalled'
  | 'finished'
  | 'applied'
  | 'ended'

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
      return { tone: 'thinking', headline: 'Thinking…', hint: 'The agent is working on your issue.' }
    case 'waiting':
      return null
    case 'parked':
      return reason
        ? { tone: 'parked', headline: 'Waiting for you', hint: reason }
        : { tone: 'parked', headline: 'Waiting for you', hint: 'Pick up anytime — answer below and the session resumes.' }
    case 'stalled':
      return {
        tone: 'stalled',
        headline: 'Session stalled — the grilling agent hit a provider usage or rate limit',
        hint: reason || 'Clear it, then resume — your last answer is re-sent as-is.',
        showResume: true,
      }
    case 'finished':
      return { tone: 'finished', headline: 'Proposal ready', hint: 'Review the outcome before it is applied.' }
    case 'applied':
      return { tone: 'applied', headline: 'Applied', hint: 'The outcome was written to the tracker.' }
    case 'abandoned':
      return { tone: 'ended', headline: 'Session ended', hint: 'This session was abandoned.' }
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
  const table = Array.from({ length: a.length + 1 }, () =>
    new Array<number>(b.length + 1).fill(0),
  )
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      table[i][j] =
        a[i] === b[j] ? table[i + 1][j + 1] + 1 : Math.max(table[i + 1][j], table[i][j + 1])
    }
  }
  return table
}
