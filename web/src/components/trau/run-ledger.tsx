import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Play, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { PhaseStepper } from '@/components/trau/phase-stepper'
import { StatusPill } from '@/components/trau/status-pill'
import { TerminalCard } from '@/components/trau/terminal-card'
import { useActiveRepo } from '@/components/trau/active-repo'
import {
  RunActionsMenu,
  RunResetButton,
  type CheckpointNotice,
} from '@/components/trau/checkpoint-actions'
import { ATTENTION_META } from '@/components/trau/overview'
import { summarize } from '@/components/event-feed'
import { CheckpointError } from '@/lib/checkpoints'
import { useAllEvents, useEventFeed, type RepoFeedEvent } from '@/lib/events'
import { instancesQueryOptions, startInstance } from '@/lib/instances'
import {
  attentionReason,
  bucketCounts,
  capMerged,
  checkpointLabel,
  formatAge,
  mergeLedger,
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

function rowKey(row: LedgerRow): string {
  return `${row.repo}/${row.run.ticket}`
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

function RepoChip({ repo }: { repo: string }) {
  return (
    <span className="rounded border border-border bg-muted/60 px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground">
      {repo}
    </span>
  )
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

function RepoErrorNotices({ repos }: { repos: string[] }) {
  if (repos.length === 0) return null
  return (
    <div className="flex flex-col gap-1">
      {repos.map((repo) => (
        <p key={repo} className="font-mono text-xs text-muted-foreground">
          Couldn’t load {repo}’s runs.
        </p>
      ))}
    </div>
  )
}

function RowItem({
  row,
  showRepo,
  activity,
  now,
  onNotice,
  onConflict,
}: {
  row: LedgerRow
  showRepo: boolean
  activity?: string
  now: number
  onNotice: (notice: CheckpointNotice) => void
  onConflict: (repo: string) => void
}) {
  const { repo, run, instance } = row
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
          {showRepo && <RepoChip repo={repo} />}
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
              onConflict={() => onConflict(repo)}
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
  row,
  showRepo,
  onNotice,
  onConflict,
}: {
  row: LedgerRow
  showRepo: boolean
  onNotice: (notice: CheckpointNotice) => void
  onConflict: (repo: string) => void
}) {
  const { repo, run } = row
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
      {showRepo && <RepoChip repo={repo} />}
      <StatusPill state={pill.state} label={pill.label} />
      <span className="font-mono text-xs text-muted-foreground">{attentionReason(run)}</span>
      {meta?.resume ? (
        <ResumeAction
          repo={repo}
          ticket={run.ticket}
          onNotice={onNotice}
          onConflict={() => onConflict(repo)}
        />
      ) : (
        <RunResetButton
          repo={repo}
          ticket={run.ticket}
          phase={run.phase}
          onNotice={onNotice}
          onConflict={() => onConflict(repo)}
        />
      )}
    </li>
  )
}

function NeedsYouStrip({
  rows,
  showRepo,
  onNotice,
  onConflict,
}: {
  rows: LedgerRow[]
  showRepo: boolean
  onNotice: (notice: CheckpointNotice) => void
  onConflict: (repo: string) => void
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
            key={rowKey(row)}
            row={row}
            showRepo={showRepo}
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
  onNotice,
}: {
  onNotice: (notice: CheckpointNotice) => void
}) {
  const { repo, isAll, repos } = useActiveRepo()
  const repoNames = useMemo(
    () =>
      isAll
        ? repos.filter((r) => r.registered).map((r) => r.name)
        : repo
          ? [repo]
          : [],
    [isAll, repos, repo],
  )

  const runsResults = useQueries({
    queries: repoNames.map((name) => runsQueryOptions(name)),
  })
  const instancesQuery = useQuery(instancesQueryOptions)
  const singleFeed = useEventFeed(isAll ? '' : (repo ?? ''))
  const allEvents = useAllEvents(isAll)
  const now = useNow(1000)
  const [tab, setTab] = useState<LedgerTab>('all')
  const [expanded, setExpanded] = useState(false)
  const [conflict, setConflict] = useState<string | null>(null)

  const instances = instancesQuery.data?.instances ?? []

  const runsByRepo = useMemo(() => {
    const byRepo = new Map<string, Run[]>()
    repoNames.forEach((name, i) => {
      const data = runsResults[i]?.data
      if (data) byRepo.set(name, data.runs)
    })
    return byRepo
  }, [repoNames, runsResults])

  const rows = useMemo(
    () => mergeLedger(repoNames, runsByRepo, instances),
    [repoNames, runsByRepo, instances],
  )
  const counts = useMemo(() => bucketCounts(rows), [rows])
  const needsYou = useMemo(() => rowsForTab(rows, 'needs-you'), [rows])

  // The feed is sorted newest-first, so the first event carrying a ticket is that
  // (repo, ticket)'s latest activity line. Under "All projects" the frames span
  // every repo, so the key carries the repo to keep tickets from colliding.
  const activityByKey = useMemo(() => {
    const latest = new Map<string, string>()
    const events: RepoFeedEvent[] = isAll
      ? allEvents
      : singleFeed.events.map((ev) => ({ ...ev, repo: repo ?? '' }))
    for (const ev of events) {
      const ticket = typeof ev.fields?.ticket === 'string' ? ev.fields.ticket : ''
      if (!ticket) continue
      const key = `${ev.repo}/${ticket}`
      if (latest.has(key)) continue
      const text = summarize(ev)
      if (text) latest.set(key, text)
    }
    return latest
  }, [isAll, allEvents, singleFeed.events, repo])

  const failedRepos = repoNames.filter((_, i) => runsResults[i]?.isError)
  const anyPending = runsResults.some((result) => result.isPending)

  if (repoNames.length === 0) return <EmptyRuns />

  if (rows.length === 0) {
    if (anyPending) {
      return (
        <div className="flex flex-col gap-4">
          <RepoErrorNotices repos={failedRepos} />
          <p className="font-mono text-sm text-muted-foreground">Loading…</p>
        </div>
      )
    }
    if (failedRepos.length > 0) return <RepoErrorNotices repos={failedRepos} />
    return <EmptyRuns />
  }

  const visible = rowsForTab(rows, tab)
  const capped = tab === 'all' ? capMerged(visible, expanded) : { rows: visible, hidden: 0 }

  return (
    <div className="flex flex-col gap-6">
      <RepoErrorNotices repos={failedRepos} />

      {conflict && <ConflictBanner repo={conflict} onDismiss={() => setConflict(null)} />}

      <NeedsYouStrip
        rows={needsYou}
        showRepo={isAll}
        onNotice={onNotice}
        onConflict={setConflict}
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
              key={rowKey(row)}
              row={row}
              showRepo={isAll}
              activity={activityByKey.get(rowKey(row))}
              now={now}
              onNotice={onNotice}
              onConflict={setConflict}
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
