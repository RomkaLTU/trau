import { useMemo, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from '@/components/ui/select'
import { useActiveRepo } from '@/components/trau/active-repo'
import { EmptyState } from '@/components/trau/empty-state'
import { StatTile } from '@/components/trau/stat-tile'
import { TerminalCard } from '@/components/trau/terminal-card'
import {
  avgUsd,
  cohortLabel,
  cohortWindow,
  comparePhases,
  configCohortsQueryOptions,
  durationLabel,
  isLegacy,
  isLowSample,
  orderCohorts,
  pctLabel,
  ratePct,
  routingDiff,
  signed,
  type ConfigCohort,
  type Delta,
  type PhaseComparison,
  type RoutingChange,
} from '@/lib/cohorts'
import { money } from '@/lib/costs'
import { cn } from '@/lib/utils'

export function ConfigExperiments() {
  const { repo, isAll, autoScope, openSwitcher } = useActiveRepo()
  const { data, error, isPending } = useQuery(
    configCohortsQueryOptions(repo ?? ''),
  )
  const cohorts = useMemo(() => orderCohorts(data?.cohorts ?? []), [data])

  return (
    <section className="flex flex-col gap-4">
      <header className="flex flex-col gap-1">
        <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
          config experiments
        </span>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Every routing configuration this project has run under, and what
          changing it did to cost and speed.
        </p>
      </header>

      {isAll ? (
        <EmptyState
          message="Config experiments are per project."
          actions={
            <Button
              size="sm"
              className="font-mono"
              onClick={() => {
                if (!autoScope()) openSwitcher()
              }}
            >
              Pick a project
            </Button>
          }
        />
      ) : repo === null ? (
        <EmptyState
          message="No project registered yet — cohorts form once a project has metered runs."
          actions={
            <Button asChild size="sm" className="font-mono">
              <Link to="/projects/new">Add a project</Link>
            </Button>
          }
        />
      ) : error ? (
        <p className="font-mono text-sm text-destructive">{String(error)}</p>
      ) : isPending ? (
        <p className="font-mono text-sm text-muted-foreground">Loading…</p>
      ) : cohorts.length === 0 ? (
        <EmptyState message="No metered runs yet — there is nothing to group into cohorts." />
      ) : (
        <>
          <CohortList cohorts={cohorts} />
          <Comparison cohorts={cohorts} />
        </>
      )}
    </section>
  )
}

function CohortList({ cohorts }: { cohorts: ConfigCohort[] }) {
  return (
    <TerminalCard title="config.cohorts" bodyClassName="p-0">
      <ul className="flex flex-col">
        {cohorts.map((cohort, i) => (
          <CohortRow
            key={cohort.hash}
            cohort={cohort}
            previous={priorFingerprint(cohorts, i)}
            current={i === 0 && !isLegacy(cohort)}
          />
        ))}
      </ul>
    </TerminalCard>
  )
}

function CohortRow({
  cohort,
  previous,
  current,
}: {
  cohort: ConfigCohort
  previous?: ConfigCohort
  current: boolean
}) {
  const legacy = isLegacy(cohort)
  const changes = legacy ? null : routingDiff(cohort.routing, previous?.routing)

  return (
    <li
      className={cn(
        'flex flex-col gap-2 border-b border-border/60 px-4 py-3 last:border-0',
        legacy && 'text-muted-foreground opacity-60',
      )}
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span
          className={cn(
            'font-mono text-sm',
            legacy ? 'text-muted-foreground' : 'text-foreground',
          )}
        >
          {cohortLabel(cohort)}
        </span>
        {current && (
          <span className="rounded border border-primary/50 bg-primary/10 px-1.5 py-0.5 font-mono text-[0.6rem] uppercase tracking-wider text-primary">
            current
          </span>
        )}
        {isLowSample(cohort) && (
          <span className="rounded border border-warn/50 bg-warn/[0.08] px-1.5 py-0.5 font-mono text-[0.6rem] uppercase tracking-wider text-warn">
            low sample
          </span>
        )}
        <span className="font-mono text-xs text-muted-foreground">
          {cohortWindow(cohort)}
        </span>
        <span className="ml-auto font-mono text-xs text-muted-foreground">
          {cohort.tickets} {cohort.tickets === 1 ? 'ticket' : 'tickets'}
        </span>
        <span
          className={cn(
            'font-mono text-sm tabular-nums',
            legacy ? 'text-muted-foreground' : 'text-foreground',
          )}
        >
          {money(cohort.cost_per_ticket, cohort.metered)} / ticket
        </span>
      </div>
      <ChangeChips changes={changes} legacy={legacy} first={!previous} />
    </li>
  )
}

function ChangeChips({
  changes,
  legacy,
  first,
}: {
  changes: RoutingChange[] | null
  legacy: boolean
  first: boolean
}) {
  if (legacy) {
    return (
      <p className="font-mono text-xs text-muted-foreground">
        Ran before the ledger recorded a config fingerprint.
      </p>
    )
  }
  if (changes === null) {
    return (
      <p className="font-mono text-xs text-muted-foreground">
        {first
          ? 'First recorded configuration.'
          : 'Fingerprint no longer resolvable — the change it made is unknown.'}
      </p>
    )
  }
  if (changes.length === 0) {
    return (
      <p className="font-mono text-xs text-muted-foreground">
        No fingerprint keys changed.
      </p>
    )
  }
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {changes.map((change) => (
        <span
          key={change.key}
          className="inline-flex max-w-full flex-wrap items-center gap-x-1.5 rounded border border-border bg-muted/60 px-1.5 py-0.5 font-mono text-[0.65rem]"
        >
          <span className="text-muted-foreground">{change.key}</span>
          <span className="text-muted-foreground/80">{change.from || '—'}</span>
          <span aria-hidden="true" className="text-muted-foreground">
            →
          </span>
          <span className="text-foreground">{change.to || '—'}</span>
        </span>
      ))}
    </div>
  )
}

function Comparison({ cohorts }: { cohorts: ConfigCohort[] }) {
  const [picked, setPicked] = useState<{ current: string; baseline: string }>()
  const current =
    cohorts.find((c) => c.hash === picked?.current) ?? cohorts[0]
  const baseline =
    cohorts.find((c) => c.hash === picked?.baseline) ?? cohorts[1]

  if (!baseline) {
    return (
      <EmptyState message="Only one configuration recorded so far. Change a routing setting and the next runs form a cohort to compare against." />
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end gap-4">
        <CohortPicker
          label="compare"
          cohorts={cohorts}
          value={current.hash}
          onChange={(hash) =>
            setPicked({ current: hash, baseline: baseline.hash })
          }
        />
        <CohortPicker
          label="against"
          cohorts={cohorts}
          value={baseline.hash}
          onChange={(hash) =>
            setPicked({ current: current.hash, baseline: hash })
          }
        />
      </div>

      <Headline current={current} baseline={baseline} />
      <PhaseTable rows={comparePhases(current, baseline)} />
    </div>
  )
}

function CohortPicker({
  label,
  cohorts,
  value,
  onChange,
}: {
  label: string
  cohorts: ConfigCohort[]
  value: string
  onChange: (hash: string) => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </span>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger size="sm" className="font-mono text-xs" aria-label={label}>
          {cohortLabel(cohorts.find((c) => c.hash === value) ?? cohorts[0])}
        </SelectTrigger>
        <SelectContent>
          {cohorts.map((cohort) => (
            <SelectItem
              key={cohort.hash}
              value={cohort.hash}
              className="font-mono text-xs"
            >
              {cohortLabel(cohort)} · {cohortWindow(cohort)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

function Headline({
  current,
  baseline,
}: {
  current: ConfigCohort
  baseline: ConfigCohort
}) {
  const cost = current.cost_per_ticket - baseline.cost_per_ticket
  const retry = current.verify_retry_rate - baseline.verify_retry_rate
  const thin = isLowSample(current) || isLowSample(baseline)

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <StatTile
        label="cost / ticket"
        value={money(current.cost_per_ticket, current.metered)}
        hint={
          <DeltaHint delta={cost}>
            {signed(cost, (v) => `$${v.toFixed(2)}`)} vs{' '}
            {money(baseline.cost_per_ticket, baseline.metered)}
          </DeltaHint>
        }
      />
      <StatTile
        label="verify retry rate"
        value={ratePct(current.verify_retry_rate)}
        hint={
          <DeltaHint delta={retry}>
            {signed(retry * 100, (v) => `${v.toFixed(0)}pp`)} vs{' '}
            {ratePct(baseline.verify_retry_rate)}
          </DeltaHint>
        }
      />
      <StatTile
        label="sample"
        value={`${current.tickets} vs ${baseline.tickets}`}
        valueClassName={cn('text-2xl', thin && 'text-warn')}
        hint={
          thin
            ? 'low sample — read the deltas as a hint, not a result'
            : `${current.calls} vs ${baseline.calls} calls`
        }
      />
    </div>
  )
}

function DeltaHint({
  delta,
  children,
}: {
  delta: number
  children: ReactNode
}) {
  return (
    <span className={cn('tabular-nums', toneClass(delta))}>{children}</span>
  )
}

function PhaseTable({ rows }: { rows: PhaseComparison[] }) {
  return (
    <TerminalCard title="phase.deltas" bodyClassName="p-0">
      <div className="overflow-x-auto">
        <table className="w-full border-collapse font-mono text-xs">
          <thead>
            <tr className="border-b border-border text-left text-muted-foreground">
              <th className="px-4 py-2 font-normal uppercase tracking-wider">
                Phase
              </th>
              <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
                Avg cost
              </th>
              <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
                Δ
              </th>
              <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
                Avg duration
              </th>
              <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
                Δ
              </th>
              <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
                Avg turns
              </th>
              <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
                Δ
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr
                key={row.phase}
                className="border-b border-border/60 last:border-0 hover:bg-secondary/40"
              >
                <td className="px-4 py-2.5">
                  <span className="text-foreground">{row.phase}</span>
                  <PhaseRoute row={row} />
                </td>
                <Value>{avgUsd(row.cost.cur)}</Value>
                <DeltaCell delta={row.cost} format={avgUsd} />
                <Value>{durationLabel(row.duration.cur)}</Value>
                <DeltaCell delta={row.duration} format={durationLabel} />
                <Value>{row.turns.cur.toFixed(1)}</Value>
                <DeltaCell delta={row.turns} format={(v) => v.toFixed(1)} />
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </TerminalCard>
  )
}

function PhaseRoute({ row }: { row: PhaseComparison }) {
  if (!row.route && !row.baselineRoute) return null
  return (
    <span className="mt-0.5 block text-[0.65rem] text-muted-foreground">
      {row.baselineRoute && row.baselineRoute !== row.route
        ? `${row.baselineRoute} → ${row.route || '—'}`
        : row.route}
    </span>
  )
}

function Value({ children }: { children: ReactNode }) {
  return (
    <td className="px-4 py-2.5 text-right align-top tabular-nums text-foreground">
      {children}
    </td>
  )
}

function DeltaCell({
  delta,
  format,
}: {
  delta: Delta
  format: (v: number) => string
}) {
  return (
    <td
      className={cn(
        'px-4 py-2.5 text-right align-top tabular-nums',
        toneClass(delta.delta),
      )}
    >
      {signed(delta.delta, format)}
      {delta.pct !== null && delta.delta !== 0 && (
        <span className="ml-1 text-[0.65rem] text-muted-foreground">
          ({pctLabel(delta.pct)})
        </span>
      )}
    </td>
  )
}

// The legacy bucket carries no fingerprint to diff against.
function priorFingerprint(
  cohorts: ConfigCohort[],
  index: number,
): ConfigCohort | undefined {
  const previous = cohorts[index + 1]
  return previous && !isLegacy(previous) ? previous : undefined
}

// On every metric here — cost, duration, turns, retry rate — less is the win.
function toneClass(delta: number): string {
  if (delta > 0) return 'text-warn'
  if (delta < 0) return 'text-done'
  return 'text-muted-foreground'
}
