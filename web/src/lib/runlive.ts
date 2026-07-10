import type { RunState } from '@/components/trau/status-pill'
import type { PhaseCost } from '@/lib/rundetail'
import type { FailureClass } from '@/lib/runs'
import { phasePill, phaseSteps } from '@/lib/overview'

export type RunVariant = 'live' | 'success' | 'failure' | 'paused'

export interface VariantInput {
  phase: string
  failureClass?: FailureClass
  working: boolean
}

export function deriveVariant({ phase, failureClass, working }: VariantInput): RunVariant {
  if (working) return 'live'
  if (phase === 'merged') return 'success'
  if (failureClass === 'paused') return 'paused'
  if (failureClass === 'faulted' || failureClass === 'gave_up') return 'failure'
  return 'live'
}

export type RunPhaseState = 'done' | 'active' | 'todo' | 'fail'

export interface RunPhaseStep {
  label: string
  state: RunPhaseState
}

export function runPhaseSteps(phase: string, variant: RunVariant): RunPhaseStep[] {
  const base = phaseSteps(phase)
  if (variant === 'success') return base.map((s) => ({ label: s.label, state: 'done' }))
  if (variant === 'failure') {
    return base.map((s) => ({
      label: s.label,
      state: s.state === 'active' ? 'fail' : s.state,
    }))
  }
  return base
}

export function headerPill(
  variant: RunVariant,
  phase: string,
  failureClass?: FailureClass,
): { state: RunState; label: string } {
  switch (variant) {
    case 'success':
      return { state: 'success', label: 'merged' }
    case 'paused':
      return { state: 'warn', label: 'paused' }
    case 'failure':
      return failureClass === 'gave_up'
        ? { state: 'fail', label: 'quarantined' }
        : { state: 'fail', label: 'fault' }
    default:
      return phasePill(phase)
  }
}

const PHASE_LABELS: Record<string, string> = {
  '': 'queued',
  building: 'build',
  built: 'build',
  handed_off: 'handoff',
  verified: 'verify',
  pr_open: 'pr',
  merged: 'merge',
  quarantined: 'quarantined',
}

export function phaseLabel(phase: string): string {
  return PHASE_LABELS[phase] ?? phase.replace(/_/g, ' ')
}

export type PauseKind = 'reauth' | 'usage_window' | 'other'

export function pauseKind(reason: string): PauseKind {
  const r = reason.toLowerCase()
  if (r.includes('auth') || r.includes('login')) return 'reauth'
  if (r.includes('rate') || r.includes('usage') || r.includes('limit')) return 'usage_window'
  return 'other'
}

export interface PauseBanner {
  headline: string
  hint: string
}

export function pauseBanner(reason: string): PauseBanner {
  const provider = reason.split(' ')[0] || 'the provider'
  switch (pauseKind(reason)) {
    case 'reauth':
      return {
        headline: `paused — ${provider} needs re-authentication`,
        hint: `This is not a failure. Re-login to ${provider}, then resume.`,
      }
    case 'usage_window':
      return {
        headline: `paused — ${provider} usage limit reached`,
        hint: 'This is not a failure. The limit resets on its own — resume once it clears.',
      }
    default:
      return {
        headline: reason ? `paused — ${reason}` : 'paused',
        hint: 'This is not a failure. Clear the block, then resume.',
      }
  }
}

export interface CostSummary {
  tokens: number
  usd: number
  metered: boolean
}

export function sumCosts(costs: PhaseCost[]): CostSummary {
  return costs.reduce<CostSummary>(
    (acc, c) => ({
      tokens: acc.tokens + c.total,
      usd: acc.usd + c.cost_usd,
      metered: acc.metered && c.metered,
    }),
    { tokens: 0, usd: 0, metered: true },
  )
}

export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`
  return String(n)
}

export function formatCostUSD(usd: number, metered: boolean): string {
  const money = `$${usd.toFixed(2)}`
  return metered ? money : `≥ ${money}`
}

const TERMINAL_STATES = new Set(['merged', 'faulted', 'quarantined', 'paused'])

interface TimedEvent {
  ts: string
  kind: string
  fields?: Record<string, unknown>
}

function field(ev: TimedEvent, key: string): string {
  const v = ev.fields?.[key]
  return typeof v === 'string' ? v : ''
}

// deriveElapsedMs recovers a run's wall-clock span from the repo event feed: the
// gap between its terminal state_change and the first event after the previous
// terminal state_change. The loop works one ticket at a time, so that window is
// the run. Returns null when the feed does not hold the run's terminal event.
export function deriveElapsedMs(events: TimedEvent[], ticket: string): number | null {
  const sorted = events
    .filter((e) => !Number.isNaN(Date.parse(e.ts)))
    .sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts))

  let endIdx = -1
  for (let i = sorted.length - 1; i >= 0; i--) {
    const e = sorted[i]
    if (
      e.kind === 'state_change' &&
      field(e, 'ticket') === ticket &&
      TERMINAL_STATES.has(field(e, 'state'))
    ) {
      endIdx = i
      break
    }
  }
  if (endIdx <= 0) return null

  let startIdx = 0
  for (let i = endIdx - 1; i >= 0; i--) {
    const e = sorted[i]
    if (e.kind === 'state_change' && TERMINAL_STATES.has(field(e, 'state'))) {
      startIdx = i + 1
      break
    }
  }

  const ms = Date.parse(sorted[endIdx].ts) - Date.parse(sorted[startIdx].ts)
  return ms > 0 ? ms : null
}

export function formatDuration(ms: number): string {
  const s = Math.floor(ms / 1000)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (h > 0) return `${h}h ${String(m).padStart(2, '0')}m`
  return `${m}m ${String(sec).padStart(2, '0')}s`
}
