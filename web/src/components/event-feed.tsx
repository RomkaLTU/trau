import { type ComponentType } from 'react'
import {
  Activity,
  Bot,
  CircleCheck,
  CircleDot,
  CircleX,
  Coins,
  Gauge,
  GitPullRequest,
  KeyRound,
  Play,
  Radio,
  TriangleAlert,
  type LucideProps,
} from 'lucide-react'

import { cn } from '@/lib/utils'
import {
  useEventFeed,
  type FeedEvent,
  type FeedStatus,
} from '@/lib/events'

type Tone = 'flow' | 'info' | 'success' | 'warn' | 'danger'

const toneText: Record<Tone, string> = {
  flow: 'text-muted-foreground',
  info: 'text-sky-600 dark:text-sky-400',
  success: 'text-emerald-600 dark:text-emerald-400',
  warn: 'text-amber-600 dark:text-amber-400',
  danger: 'text-destructive',
}

interface KindMeta {
  label: string
  icon: ComponentType<LucideProps>
  tone: Tone
}

const KIND_META: Record<string, KindMeta> = {
  agent_call: { label: 'Agent call', icon: Bot, tone: 'flow' },
  agent_start: { label: 'Agent start', icon: Play, tone: 'info' },
  usage_window: { label: 'Usage window', icon: Gauge, tone: 'info' },
  cost_anomaly: { label: 'Cost anomaly', icon: Coins, tone: 'warn' },
  build_no_skills: { label: 'No skills loaded', icon: TriangleAlert, tone: 'warn' },
  verify_no_browser: { label: 'Browser verify skipped', icon: TriangleAlert, tone: 'warn' },
  qa_roster: { label: 'QA roster', icon: KeyRound, tone: 'info' },
  model_fallback: { label: 'Built-in model used', icon: TriangleAlert, tone: 'warn' },
  pr_open: { label: 'PR opened', icon: GitPullRequest, tone: 'info' },
  ci: { label: 'CI', icon: CircleDot, tone: 'flow' },
  phase_start: { label: 'Phase', icon: CircleDot, tone: 'flow' },
  state_change: { label: 'State', icon: Activity, tone: 'flow' },
}

function kindMeta(kind: string): KindMeta {
  return (
    KIND_META[kind] ?? {
      label: kind.replace(/_/g, ' '),
      icon: Activity,
      tone: 'flow',
    }
  )
}

function str(fields: FeedEvent['fields'], key: string): string {
  const v = fields?.[key]
  return typeof v === 'string' ? v : ''
}

function num(fields: FeedEvent['fields'], key: string): number | undefined {
  const v = fields?.[key]
  return typeof v === 'number' ? v : undefined
}

function isError(ev: FeedEvent): boolean {
  return ev.fields?.is_error === true
}

function compact(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}k`
  return String(n)
}

function iconFor(ev: FeedEvent, base: ComponentType<LucideProps>) {
  if (ev.kind === 'agent_call' && isError(ev)) return CircleX
  if (ev.kind === 'cost_anomaly') return TriangleAlert
  if (ev.kind === 'ci') {
    const s = str(ev.fields, 'state')
    if (s === 'green' || s === 'merged') return CircleCheck
    if (s === 'failing') return CircleX
  }
  if (ev.kind === 'state_change') {
    const s = str(ev.fields, 'state')
    if (s === 'merged') return CircleCheck
    if (s === 'faulted' || s === 'quarantined') return CircleX
    if (s === 'paused') return TriangleAlert
  }
  return base
}

function toneFor(ev: FeedEvent, base: Tone): Tone {
  if (ev.kind === 'agent_call' && isError(ev)) return 'danger'
  if (ev.kind === 'ci') {
    const s = str(ev.fields, 'state')
    if (s === 'green' || s === 'merged') return 'success'
    if (s === 'failing') return 'danger'
    return 'flow'
  }
  if (ev.kind === 'state_change') {
    const s = str(ev.fields, 'state')
    if (s === 'merged') return 'success'
    if (s === 'paused') return 'warn'
    if (s === 'faulted' || s === 'quarantined') return 'danger'
  }
  return base
}

export function summarize(ev: FeedEvent): string {
  const f = ev.fields
  switch (ev.kind) {
    case 'agent_call': {
      const parts: string[] = []
      const provider = str(f, 'provider')
      const model = str(f, 'model')
      if (provider) parts.push(model ? `${provider} · ${model}` : provider)
      const inTok = num(f, 'input_tokens')
      const outTok = num(f, 'output_tokens')
      if (inTok !== undefined || outTok !== undefined) {
        parts.push(`${compact(inTok ?? 0)}→${compact(outTok ?? 0)} tok`)
      }
      const cost = num(f, 'cost_usd')
      if (cost) parts.push(`$${cost.toFixed(2)}`)
      if (isError(ev)) {
        const err = str(f, 'error')
        parts.push(err ? `error: ${err}` : 'error')
      }
      return parts.join(' · ')
    }
    case 'usage_window': {
      const provider = str(f, 'provider')
      const label = str(f, 'label')
      const util = num(f, 'utilization')
      const balance = num(f, 'balance_usd')
      if (util !== undefined) return `${provider} ${label} ${Math.round(util)}%`.trim()
      if (balance !== undefined) return `${provider} balance $${balance.toFixed(2)}`.trim()
      return provider
    }
    case 'ci':
      return str(f, 'state')
    case 'pr_open': {
      const n = num(f, 'number')
      return n ? `#${n}` : str(f, 'url')
    }
    case 'state_change': {
      const ticket = str(f, 'ticket')
      const state = str(f, 'state')
      const reason = str(f, 'reason')
      const label = reason ? `${state} · ${reason}` : state
      return ticket ? `${ticket} · ${label}` : label
    }
    default:
      return ev.msg ?? ''
  }
}

function clock(ts: string): string {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

function EventRow({ ev }: { ev: FeedEvent }) {
  const meta = kindMeta(ev.kind)
  const Icon = iconFor(ev, meta.icon)
  const tone = toneFor(ev, meta.tone)
  const summary = summarize(ev)
  return (
    <li className="flex items-start gap-3 px-3 py-2">
      <Icon className={cn('mt-0.5 size-4 shrink-0', toneText[tone])} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className={cn('text-sm font-medium', toneText[tone])}>
            {meta.label}
          </span>
          {ev.phase && (
            <span className="font-mono text-xs text-muted-foreground">
              {ev.phase}
            </span>
          )}
          <span className="ml-auto shrink-0 font-mono text-xs tabular-nums text-muted-foreground">
            {clock(ev.ts)}
          </span>
        </div>
        {summary && (
          <p className="mt-0.5 truncate text-xs text-muted-foreground">
            {summary}
          </p>
        )}
      </div>
    </li>
  )
}

function LiveDot({ status }: { status: FeedStatus }) {
  const meta: Record<FeedStatus, { label: string; dot: string }> = {
    connecting: { label: 'connecting', dot: 'bg-amber-500' },
    live: { label: 'live', dot: 'bg-emerald-500' },
    error: { label: 'reconnecting', dot: 'bg-destructive' },
  }
  const { label, dot } = meta[status]
  return (
    <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <span
        className={cn(
          'size-1.5 rounded-full',
          dot,
          status === 'live' && 'animate-pulse',
        )}
      />
      {label}
    </span>
  )
}

export function EventFeed({
  repo,
  limit,
  bordered = true,
  className,
}: {
  repo: string
  limit?: number
  bordered?: boolean
  className?: string
}) {
  const { events, status, error } = useEventFeed(repo)
  const shown = limit ? events.slice(0, limit) : events

  return (
    <section className={cn('flex flex-col gap-2', className)}>
      <div className="flex items-center justify-between px-1">
        <h2 className="flex items-center gap-2 text-sm font-medium">
          <Radio className="size-4 text-muted-foreground" />
          Live events
        </h2>
        <LiveDot status={status} />
      </div>
      {error != null && (
        <p className="px-1 text-sm text-destructive">{String(error)}</p>
      )}
      <div
        className={cn(
          'max-h-96 overflow-y-auto',
          bordered && 'rounded-lg border bg-card',
        )}
      >
        {shown.length === 0 ? (
          <p className="px-3 py-6 text-center text-sm text-muted-foreground">
            {status === 'error' ? 'Reconnecting…' : 'Waiting for events…'}
          </p>
        ) : (
          <ul className="divide-y">
            {shown.map((ev) => (
              <EventRow key={ev.id} ev={ev} />
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}
