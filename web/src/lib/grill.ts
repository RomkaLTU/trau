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
// parked or stalled session settled with.
export interface GrillSession {
  id: string
  repo: string
  issue_id?: string
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

export interface QuestionPayload {
  text: string
  options: string[]
  allow_free_text: boolean
}

export interface OutcomePayload {
  disposition: string
  proposed_description?: string
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

function base(repo: string): string {
  return `/api/v1/repos/${encodeURIComponent(repo)}/grill`
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

async function fetchGrillSessions(repo: string): Promise<GrillListResponse> {
  const res = await apiFetch(base(repo))
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

export async function startGrillSession(repo: string, issueId: string): Promise<GrillSession> {
  const res = await apiFetch(base(repo), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ issue_id: issueId }),
  })
  if (!res.ok) throw new Error(await errorMessage(res, 'start grill session failed'))
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
    proposed_description:
      typeof p.proposed_description === 'string' ? p.proposed_description : undefined,
    summary: typeof p.summary === 'string' ? p.summary : '',
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

export function latestOutcome(messages: GrillMessage[]): GrillMessage | null {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].kind === 'outcome') return messages[i]
  }
  return null
}

// GrillLive is the panel's merged view of one session: the authoritative session
// plus its messages. live tracks whether a stream frame has set the session, so a
// late GET hydrate never reverts a state the stream already advanced.
export interface GrillLive {
  session: GrillSession
  live: boolean
  messages: GrillMessage[]
}

export type GrillAction =
  | { type: 'hydrate'; detail: GrillDetail }
  | { type: 'message'; message: GrillMessage }
  | { type: 'state'; session: GrillSession }

export function grillReducer(state: GrillLive, action: GrillAction): GrillLive {
  switch (action.type) {
    case 'hydrate':
      return {
        session: state.live ? state.session : action.detail.session,
        live: state.live,
        messages: mergeMessages(state.messages, action.detail.messages),
      }
    case 'message':
      return { ...state, messages: upsertMessage(state.messages, action.message) }
    case 'state':
      return { ...state, session: action.session, live: true }
  }
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
        headline: 'Session stalled',
        hint: reason || 'The agent hit a wall — clear it, then resume.',
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
