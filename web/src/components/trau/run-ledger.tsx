import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Play, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { PhaseStepper } from '@/components/trau/phase-stepper'
import { StatusPill } from '@/components/trau/status-pill'
import { TerminalCard } from '@/components/trau/terminal-card'
import {
  RunActionsMenu,
  RunResetButton,
  type CheckpointNotice,
} from '@/components/trau/checkpoint-actions'
import { ATTENTION_META } from '@/components/trau/overview'
import { summarize } from '@/components/event-feed'
import { CheckpointError } from '@/lib/checkpoints'
import { useEventFeed } from '@/lib/events'
import { instancesQueryOptions, startInstance } from '@/lib/instances'
import {
  attentionReason,
  bucketCounts,
  capMerged,
  checkpointLabel,
  formatAge,
  joinInstances,
  rowsForTab,
  type LedgerRow,
  type LedgerTab,
} from '@/lib/ledger'
import { boardPill } from '@/lib/overview'
import { formatDuration } from '@/lib/runlive'
import { runsQueryOptions, type Run } from '@/lib/runs'
import { checkpointSteps, liveSteps, type Step } from '@/lib/steps'
import { cn } from '@/lib/utils'

function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

const TABS: { key: LedgerTab; label: string }[] = [
  { key: 'all', label: 'all' },
  { key: 'active', label: 'active' },
  { key: 'needs-you', label: 'needs you' },
  { key: 'stopped', label: 'stopped' },
  { key: 'merged', label: 'merged' },
]

function rowStepper(row: LedgerRow): { steps: Step[]; label: string } {
  const { run, instance } = row
  if (instance) {
    const live = liveSteps(instance.activity, instance.detail, instance.phase ?? '')
    const label = (live.subLabel ?? checkpointLabel(instance.phase ?? '')).toLowerCase()
    return { steps: live.steps, label }
  }
  return {
    steps: checkpointSteps(run.phase, Boolean(run.failure_class)),
    label: checkpointLabel(run.phase),
  }
}

function rowCost(run: Run): string {
  return run.cost_usd ? `$${run.cost_usd.toFixed(2)}` : '—'
}

function rowAge(row: LedgerRow, now: number): string {
  if (row.instance) {
    return formatDuration(Math.max(0, now - Date.parse(row.instance.started_at)))
  }
  if (!row.run.updated_at) return '—'
  return formatAge(Math.max(0, now - Date.parse(row.run.updated_at)))
}

function EmptyRuns() {
  return (
    <TerminalCard title="runs" bodyClassName="p-0">
      <div className="furrow-grid relative flex flex-col items-center justify-center gap-4 px-6 py-20 text-center">
        <div className="hero-glow pointer-events-none absolute inset-0" aria-hidden="true" />
        <p className="relative font-sans text-sm text-muted-foreground">No runs yet.</p>
        <div className="relative flex flex-wrap items-center justify-center gap-2">
          <Button asChild className="font-mono" size="sm">
            <Link to="/run-once">Run once</Link>
          </Button>
          <Button asChild variant="outline" className="font-mono" size="sm">
            <Link to="/loop">Start loop</Link>
          </Button>
        </div>
      </div>
    </TerminalCard>
  )
}

function RowItem({
  repo,
  row,
  activity,
  now,
  onNotice,
  onConflict,
}: {
  repo: string
  row: LedgerRow
  activity?: string
  now: number
  onNotice: (notice: CheckpointNotice) => void
  onConflict: () => void
}) {
  const { run, instance } = row
  const pill = boardPill(run)
  const { steps, label } = rowStepper(row)
  const to = instance ? '/live/$repo/$ticket' : '/runs/$repo/$ticket'

  return (
    <li>
      <Link
        to={to}
        params={{ repo, ticket: run.ticket }}
        className="group flex flex-col gap-1.5 px-4 py-3 transition-colors hover:bg-secondary/40"
      >
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <span className="w-20 shrink-0 font-mono text-sm text-primary">{run.ticket}</span>
          <span className="min-w-0 flex-1 truncate font-sans text-sm text-foreground">
            {run.title ?? run.ticket}
          </span>
          <PhaseStepper compact steps={steps} subLabel={label} />
          <StatusPill state={pill.state} label={pill.label} />
          <span className="w-16 text-right font-mono text-[0.7rem] text-foreground">
            {rowCost(run)}
          </span>
          <span className="w-16 text-right font-mono text-[0.7rem] text-muted-foreground">
            {rowAge(row, now)}
          </span>
          <div onClick={(e) => e.preventDefault()}>
            <RunActionsMenu
              repo={repo}
              ticket={run.ticket}
              phase={run.phase}
              onNotice={onNotice}
              onConflict={onConflict}
            />
          </div>
        </div>
        {instance && activity && (
          <p className="pl-[5.75rem] font-mono text-xs text-muted-foreground">
            <span aria-hidden="true">→ </span>
            {activity}
          </p>
        )}
      </Link>
    </li>
  )
}

function ResumeAction({
  repo,
  ticket,
  onNotice,
  onConflict,
}: {
  repo: string
  ticket: string
  onNotice: (notice: CheckpointNotice) => void
  onConflict: () => void
}) {
  const queryClient = useQueryClient()
  const resume = useMutation({
    mutationFn: () => startInstance({ repo, ticket }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['instances'] })
      void queryClient.invalidateQueries({ queryKey: ['runs', repo] })
    },
    onError: (error) => {
      if (error instanceof CheckpointError && error.live) {
        onConflict()
        return
      }
      onNotice({
        tone: 'error',
        text: error instanceof Error ? error.message : String(error),
      })
    },
  })
  return (
    <Button
      variant="outline"
      size="sm"
      className="font-mono"
      disabled={resume.isPending}
      onClick={() => resume.mutate()}
    >
      <Play className="size-3.5" aria-hidden="true" />
      {resume.isPending ? 'Resuming…' : 'Resume'}
    </Button>
  )
}

function AttentionRow({
  repo,
  row,
  onNotice,
  onConflict,
}: {
  repo: string
  row: LedgerRow
  onNotice: (notice: CheckpointNotice) => void
  onConflict: () => void
}) {
  const { run } = row
  const pill = boardPill(run)
  const meta = run.failure_class ? ATTENTION_META[run.failure_class] : undefined
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 py-3">
      <Link
        to="/runs/$repo/$ticket"
        params={{ repo, ticket: run.ticket }}
        className="font-mono text-sm text-primary hover:underline"
      >
        {run.ticket}
      </Link>
      <span className="min-w-0 flex-1 truncate font-sans text-sm text-foreground">
        {run.title ?? run.ticket}
      </span>
      <StatusPill state={pill.state} label={pill.label} />
      <span className="font-mono text-xs text-muted-foreground">{attentionReason(run)}</span>
      {meta?.resume ? (
        <ResumeAction
          repo={repo}
          ticket={run.ticket}
          onNotice={onNotice}
          onConflict={onConflict}
        />
      ) : (
        <RunResetButton
          repo={repo}
          ticket={run.ticket}
          phase={run.phase}
          onNotice={onNotice}
          onConflict={onConflict}
        />
      )}
    </li>
  )
}

function NeedsYouStrip({
  repo,
  rows,
  onNotice,
  onConflict,
}: {
  repo: string
  rows: LedgerRow[]
  onNotice: (notice: CheckpointNotice) => void
  onConflict: () => void
}) {
  if (rows.length === 0) return null
  return (
    <section
      aria-label="Runs that need you"
      className="overflow-hidden rounded-lg border border-warn/50"
    >
      <header className="flex items-center gap-2 border-b border-warn/40 bg-warn/12 px-4 py-2">
        <span aria-hidden="true" className="font-mono text-sm text-warn">
          ⚠
        </span>
        <span className="font-mono text-xs uppercase tracking-[0.18em] text-warn">
          needs you ({rows.length})
        </span>
      </header>
      <ul className="flex flex-col divide-y divide-border/60">
        {rows.map((row) => (
          <AttentionRow
            key={row.run.ticket}
            repo={repo}
            row={row}
            onNotice={onNotice}
            onConflict={onConflict}
          />
        ))}
      </ul>
    </section>
  )
}

function ConflictBanner({ repo, onDismiss }: { repo: string; onDismiss: () => void }) {
  return (
    <div
      role="status"
      className="flex items-start justify-between gap-3 rounded-lg border border-warn/50 bg-warn/12 px-4 py-3"
    >
      <div className="flex items-start gap-2.5">
        <span aria-hidden="true" className="mt-0.5 font-mono text-sm text-warn">
          ⚠
        </span>
        <p className="font-mono text-sm leading-relaxed text-warn">
          {repo} is held by a live loop — try again after it stops.
        </p>
      </div>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss warning"
        className="flex size-6 shrink-0 items-center justify-center rounded-md text-warn/80 hover:bg-warn/12 hover:text-warn"
      >
        <X className="size-4" aria-hidden="true" />
      </button>
    </div>
  )
}

export function RunLedger({
  repo,
  onNotice,
}: {
  repo: string
  onNotice: (notice: CheckpointNotice) => void
}) {
  const runsQuery = useQuery(runsQueryOptions(repo))
  const instancesQuery = useQuery(instancesQueryOptions)
  const feed = useEventFeed(repo)
  const now = useNow(1000)
  const [tab, setTab] = useState<LedgerTab>('all')
  const [expanded, setExpanded] = useState(false)
  const [conflict, setConflict] = useState(false)

  const runs = runsQuery.data?.runs ?? []
  const instances = instancesQuery.data?.instances ?? []

  const rows = useMemo(
    () => joinInstances(runs, instances, repo),
    [runs, instances, repo],
  )
  const counts = useMemo(() => bucketCounts(rows), [rows])
  const needsYou = useMemo(() => rowsForTab(rows, 'needs-you'), [rows])

  // The feed is sorted newest-first, so the first event carrying a ticket is that
  // ticket's latest activity line.
  const activityByTicket = useMemo(() => {
    const latest = new Map<string, string>()
    for (const ev of feed.events) {
      const ticket = typeof ev.fields?.ticket === 'string' ? ev.fields.ticket : ''
      if (!ticket || latest.has(ticket)) continue
      const text = summarize(ev)
      if (text) latest.set(ticket, text)
    }
    return latest
  }, [feed.events])

  if (!repo) return <EmptyRuns />
  if (runsQuery.error) {
    return <p className="font-mono text-sm text-destructive">{String(runsQuery.error)}</p>
  }
  if (runsQuery.isPending) {
    return <p className="font-mono text-sm text-muted-foreground">Loading…</p>
  }
  if (rows.length === 0) return <EmptyRuns />

  const visible = rowsForTab(rows, tab)
  const capped = tab === 'all' ? capMerged(visible, expanded) : { rows: visible, hidden: 0 }

  return (
    <div className="flex flex-col gap-6">
      {conflict && <ConflictBanner repo={repo} onDismiss={() => setConflict(false)} />}

      <NeedsYouStrip
        repo={repo}
        rows={needsYou}
        onNotice={onNotice}
        onConflict={() => setConflict(true)}
      />

      <div className="flex flex-wrap items-center gap-1 self-start rounded-md border border-border bg-input p-0.5">
        {TABS.map((t) => {
          const count = t.key === 'all' ? rows.length : counts[t.key]
          return (
            <button
              key={t.key}
              type="button"
              onClick={() => setTab(t.key)}
              aria-pressed={tab === t.key}
              className={cn(
                'rounded-[calc(var(--radius)-6px)] px-3 py-1 font-mono text-xs transition-colors',
                tab === t.key
                  ? 'bg-primary text-primary-foreground'
                  : count === 0
                    ? 'text-faint hover:text-muted-foreground'
                    : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {t.label} ({count})
            </button>
          )
        })}
      </div>

      {capped.rows.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border/60 px-4 py-10 text-center font-mono text-xs text-faint">
          no runs match this filter
        </p>
      ) : (
        <ul className="flex flex-col divide-y divide-border/60 overflow-hidden rounded-lg border border-border/60 bg-card/40">
          {capped.rows.map((row) => (
            <RowItem
              key={row.run.ticket}
              repo={repo}
              row={row}
              activity={activityByTicket.get(row.run.ticket)}
              now={now}
              onNotice={onNotice}
              onConflict={() => setConflict(true)}
            />
          ))}
        </ul>
      )}

      {capped.hidden > 0 && (
        <button
          type="button"
          onClick={() => setExpanded(true)}
          className="self-start rounded-md border border-dashed border-border/60 px-3 py-2 font-mono text-xs text-muted-foreground hover:border-ring/40 hover:text-foreground"
        >
          + {capped.hidden} more merged
        </button>
      )}
    </div>
  )
}
