import { useEffect, useMemo, useRef, useState } from 'react'
import { useQueries, useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'

import { Button } from '@/components/ui/button'
import { AuthorChip } from '@/components/trau/author-chip'
import { PhaseStepper } from '@/components/trau/phase-stepper'
import { PRStatusBadge } from '@/components/trau/pr-status-badge'
import { StatusPill } from '@/components/trau/status-pill'
import { TerminalCard } from '@/components/trau/terminal-card'
import { useActiveRepo } from '@/components/trau/active-repo'
import { summarize } from '@/components/event-feed'
import { useAllEvents, useEventFeed, type RepoFeedEvent } from '@/lib/events'
import { instancesQueryOptions } from '@/lib/instances'
import {
  attentionReason,
  bucketCounts,
  capMerged,
  checkpointLabel,
  formatAge,
  isMine,
  ledgerTotals,
  mergeLedger,
  rowPill,
  rowsForAuthor,
  rowsForTab,
  type LedgerAuthor,
  type LedgerRow,
  type LedgerTab,
} from '@/lib/ledger'
import { boardPill } from '@/lib/overview'
import { formatCostUSD, formatDuration } from '@/lib/runlive'
import {
  runsQueryOptions,
  teamRunsQueryOptions,
  type Run,
  type RunShip,
} from '@/lib/runs'
import { checkpointSteps, liveSteps, type Step } from '@/lib/steps'
import { useRovingList, type RovingRowProps } from '@/lib/use-roving-list'
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
  return `${row.repo}/${row.run.ticket}/${row.run.shared?.writer ?? ''}`
}

const TABS: { key: LedgerTab; label: string }[] = [
  { key: 'all', label: 'all' },
  { key: 'active', label: 'active' },
  { key: 'needs-you', label: 'needs you' },
  { key: 'stopped', label: 'stopped' },
  { key: 'merged', label: 'merged' },
]

const AUTHORS: { key: LedgerAuthor; label: string }[] = [
  { key: 'everyone', label: 'everyone' },
  { key: 'me', label: 'me' },
  { key: 'teammates', label: 'teammates' },
]

function rowStepper(row: LedgerRow): { steps: Step[]; label: string } {
  const { run, instance } = row
  if (instance?.session_state === 'working') {
    const live = liveSteps(instance.activity, instance.detail, instance.phase ?? '')
    const label = (live.subLabel ?? checkpointLabel(instance.phase ?? '')).toLowerCase()
    return { steps: live.steps, label }
  }
  const faulted =
    run.failure_class === 'faulted' || run.failure_class === 'gave_up'
  return {
    steps: checkpointSteps(run.phase, faulted),
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

// ShipsChip says a run delivered across several Child repos, which nothing else on
// the row can: PR and pr_url name only the first of them. A run with one target
// reads as any other row.
function ShipsChip({ ships }: { ships?: RunShip[] }) {
  if (!ships || ships.length < 2) return null
  return (
    <span className="shrink-0 rounded border border-border bg-muted/60 px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground">
      {ships.length} repos
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
  showAuthor,
  activity,
  now,
  rowProps,
}: {
  row: LedgerRow
  showRepo: boolean
  showAuthor: boolean
  activity?: string
  now: number
  rowProps: RovingRowProps
}) {
  const { repo, run, instance } = row
  const pill = rowPill(row)
  const { steps, label } = rowStepper(row)
  const shared = run.shared
  const to = shared
    ? '/team-runs/$repo/$writer/$ticket'
    : instance
      ? '/live/$repo/$ticket'
      : '/runs/$repo/$ticket'

  return (
    <li>
      <Link
        to={to}
        params={{ repo, ticket: run.ticket, writer: shared?.writer ?? '' }}
        {...rowProps}
        className="group flex flex-col gap-1.5 px-4 py-3 transition-colors hover:bg-secondary/40"
      >
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <span className="w-20 shrink-0 font-mono text-sm text-primary">{run.ticket}</span>
          <span className="min-w-0 flex-1 truncate font-sans text-sm text-foreground">
            {run.title ?? run.ticket}
          </span>
          {showRepo && <RepoChip repo={repo} />}
          {showAuthor && <AuthorChip run={run} />}
          <PhaseStepper compact steps={steps} subLabel={label} />
          <StatusPill state={pill.state} label={pill.label} />
          <ShipsChip ships={run.ships} />
          <PRStatusBadge status={run.pr_status} />
          <span className="w-16 text-right font-mono text-[0.7rem] text-foreground">
            {rowCost(run)}
          </span>
          <span className="w-16 text-right font-mono text-[0.7rem] text-muted-foreground">
            {rowAge(row, now)}
          </span>
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

function AttentionRow({ row, showRepo }: { row: LedgerRow; showRepo: boolean }) {
  const { repo, run } = row
  const pill = boardPill(run)
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
      <PRStatusBadge status={run.pr_status} />
      <span className="font-mono text-xs text-muted-foreground">{attentionReason(run)}</span>
    </li>
  )
}

function NeedsYouStrip({ rows, showRepo }: { rows: LedgerRow[]; showRepo: boolean }) {
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
          <AttentionRow key={rowKey(row)} row={row} showRepo={showRepo} />
        ))}
      </ul>
    </section>
  )
}

function TotalsLine({ rows }: { rows: LedgerRow[] }) {
  const totals = ledgerTotals(rows)
  return (
    <p className="font-mono text-xs tabular-nums text-muted-foreground">
      {totals.runs} runs ·{' '}
      <span className="text-foreground">{formatCostUSD(totals.costUsd, true)}</span>
    </p>
  )
}

export function RunLedger() {
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
  const teamResults = useQueries({
    queries: repoNames.map((name) => teamRunsQueryOptions(name)),
  })
  const instancesQuery = useQuery(instancesQueryOptions)
  const singleFeed = useEventFeed(isAll ? '' : (repo ?? ''))
  const allEvents = useAllEvents(isAll)
  const now = useNow(1000)
  const [tab, setTab] = useState<LedgerTab>('all')
  const [author, setAuthor] = useState<LedgerAuthor>('everyone')
  const [expanded, setExpanded] = useState(false)

  // Every row is a link, so the list keeps no activation of its own: Enter is
  // the browser's.
  const listRef = useRef<HTMLUListElement | null>(null)
  const walk = useRovingList({ container: listRef })

  const instances = instancesQuery.data?.instances ?? []

  const runsByRepo = useMemo(() => {
    const byRepo = new Map<string, Run[]>()
    repoNames.forEach((name, i) => {
      const data = runsResults[i]?.data
      const shared = teamResults[i]?.data?.runs ?? []
      if (data) byRepo.set(name, [...data.runs, ...shared])
    })
    return byRepo
  }, [repoNames, runsResults, teamResults])

  const all = useMemo(
    () => mergeLedger(repoNames, runsByRepo, instances),
    [repoNames, runsByRepo, instances],
  )
  // Team sync is opt-in, so the attribution chrome only appears once teammates
  // are actually sharing: without it the ledger is the one it has always been.
  const shared = all.some((row) => !isMine(row.run))
  const rows = useMemo(() => rowsForAuthor(all, author), [all, author])
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

  if (all.length === 0) {
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

      <NeedsYouStrip rows={needsYou} showRepo={isAll} />

      <div className="flex flex-wrap items-center gap-3">
        <div className="flex flex-wrap items-center gap-1 rounded-md border border-border bg-input p-0.5">
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

        {shared && (
          <div
            aria-label="Filter runs by author"
            className="flex flex-wrap items-center gap-1 rounded-md border border-border bg-input p-0.5"
          >
            {AUTHORS.map((a) => (
              <button
                key={a.key}
                type="button"
                onClick={() => setAuthor(a.key)}
                aria-pressed={author === a.key}
                className={cn(
                  'rounded-[calc(var(--radius)-6px)] px-3 py-1 font-mono text-xs transition-colors',
                  author === a.key
                    ? 'bg-primary text-primary-foreground'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                {a.label}
              </button>
            ))}
          </div>
        )}

        {shared && <TotalsLine rows={visible} />}
      </div>

      {capped.rows.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border/60 px-4 py-10 text-center font-mono text-xs text-faint">
          no runs match this filter
        </p>
      ) : (
        <ul
          ref={listRef}
          {...walk.listProps}
          className="flex flex-col divide-y divide-border/60 overflow-hidden rounded-lg border border-border/60 bg-card/40"
        >
          {capped.rows.map((row) => (
            <RowItem
              key={rowKey(row)}
              row={row}
              showRepo={isAll}
              showAuthor={shared}
              activity={activityByKey.get(`${row.repo}/${row.run.ticket}`)}
              now={now}
              rowProps={walk.rowProps(rowKey(row))}
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
