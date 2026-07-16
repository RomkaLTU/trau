import { useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, createFileRoute } from '@tanstack/react-router'
import {
  Bar,
  CartesianGrid,
  ComposedChart,
  Line,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

import {
  Eyebrow,
  EmptyState,
  SegmentedControl,
  StatTile,
  TerminalCard,
  useActiveRepo,
} from '@/components/trau'
import { cn } from '@/lib/utils'
import {
  ATLAS_PHASE,
  GROUP_BY,
  OTHER_KEY,
  PREV_KEY,
  collapseSeries,
  compactUsd,
  costsQueryOptions,
  money,
  previousWindow,
  priorTotalsByIndex,
  seriesDelta,
  timeseriesQueryOptions,
  toChartRows,
  type CostAnomaly,
  type GroupBy,
  type TimeseriesGroup,
  type TimeseriesResponse,
} from '@/lib/costs'
import { standardTitle, usePageTitle } from '@/lib/page-title'

export const Route = createFileRoute('/costs')({
  component: Costs,
})

const WINDOWS: { value: number; label: string }[] = [
  { value: 1, label: '24h' },
  { value: 7, label: '7d' },
  { value: 30, label: '30d' },
  { value: 90, label: '90d' },
]

const PALETTE = [
  'var(--color-brand)',
  'var(--color-teal)',
  'var(--color-info)',
  'var(--color-done)',
  'oklch(0.63 0.24 304)',
  'oklch(0.72 0.18 70)',
  'oklch(0.64 0.22 16)',
  'oklch(0.56 0.14 285)',
]
const OTHER_COLOR = 'var(--color-muted-foreground)'
const MONO = 'var(--font-mono)'

function colorAt(index: number, key: string): string {
  return key === OTHER_KEY ? OTHER_COLOR : PALETTE[index % PALETTE.length]
}

function seriesLabel(key: string): string {
  return key === OTHER_KEY ? 'other' : key
}

function Costs() {
  usePageTitle(standardTitle('Costs'))
  const { repo } = useActiveRepo()
  const [days, setDays] = useState(7)
  const [groupBy, setGroupBy] = useState<GroupBy>('provider')
  const [compare, setCompare] = useState(false)

  const repos = repo ? [repo] : undefined
  const { data, error, isPending } = useQuery(
    timeseriesQueryOptions({ days, groupBy, repos }),
  )
  const prev = previousWindow(days)
  const prior = useQuery({
    ...timeseriesQueryOptions({ from: prev.from, to: prev.to, groupBy, repos }),
    enabled: compare,
  })
  const costs = useQuery(costsQueryOptions(days))
  const atlas = useQuery(
    timeseriesQueryOptions({ days, groupBy: 'phase', repos }),
  )

  const windowLabel = WINDOWS.find((w) => w.value === days)?.label ?? `${days}d`
  const anomalies = (costs.data?.anomalies ?? []).filter((a) => a.repo === repo)
  const atlasSpend =
    atlas.data?.series.find((s) => s.key === ATLAS_PHASE) ?? null

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="partial" className="text-primary">
          COSTS
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Costs
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          How much, where, and what&apos;s abnormal.
        </p>
      </header>

      <div className="flex flex-wrap items-end gap-4">
        <Field label="window">
          <SegmentedControl
            aria-label="Time window"
            options={WINDOWS}
            value={days}
            onChange={setDays}
          />
        </Field>
        <Field label="group by">
          <SegmentedControl
            aria-label="Group by"
            options={GROUP_BY}
            value={groupBy}
            onChange={setGroupBy}
          />
        </Field>
        <Field label="compare">
          <button
            type="button"
            onClick={() => setCompare((c) => !c)}
            aria-pressed={compare}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-md border px-3 py-1.5 font-mono text-xs transition-colors',
              compare
                ? 'border-primary/60 bg-primary/12 text-primary'
                : 'border-border bg-input text-muted-foreground hover:text-foreground',
            )}
          >
            <span aria-hidden="true">{compare ? '◉' : '○'}</span>
            prior {windowLabel}
          </button>
        </Field>
      </div>

      {error && (
        <p className="font-mono text-sm text-destructive">{String(error)}</p>
      )}
      {isPending && !error && (
        <p className="font-mono text-sm text-muted-foreground">Loading…</p>
      )}

      {data && data.series.length === 0 && (
        <EmptyState message="No spend in this window." />
      )}

      {data && data.series.length > 0 && (
        <>
          {compare && (
            <CompareBadge
              current={data}
              prior={prior.data}
              label={windowLabel}
            />
          )}
          <SpendChart
            data={data}
            prior={compare ? prior.data : undefined}
            label={windowLabel}
          />
          <StatTiles
            data={data}
            anomalies={anomalies}
            label={windowLabel}
            groupBy={groupBy}
          />
          {atlasSpend && atlasSpend.cost_usd > 0 && (
            <AtlasSpend spend={atlasSpend} repo={repo} label={windowLabel} />
          )}
          <Anomalies anomalies={anomalies} />
          <Breakdown data={data} />
          {compare && prior.data && (
            <CompareTable current={data} prior={prior.data} />
          )}
        </>
      )}
    </div>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </span>
      {children}
    </div>
  )
}

function CompareBadge({
  current,
  prior,
  label,
}: {
  current: TimeseriesResponse
  prior?: TimeseriesResponse
  label: string
}) {
  if (!prior) {
    return (
      <span className="w-fit font-mono text-xs text-muted-foreground">
        Loading prior {label}…
      </span>
    )
  }
  const cur = current.totals.cost_usd
  const was = prior.totals.cost_usd
  const pct = was > 0 ? ((cur - was) / was) * 100 : null
  const up = cur >= was
  return (
    <span
      className={cn(
        'inline-flex w-fit items-center gap-1.5 rounded-md border px-2.5 py-1 font-mono text-xs',
        up
          ? 'border-warn/50 bg-warn/[0.08] text-warn'
          : 'border-done/50 bg-done/[0.08] text-done',
      )}
    >
      <span aria-hidden="true">{up ? '▲' : '▼'}</span>
      {pct === null
        ? `${money(cur, current.totals.metered)} vs $0 prior ${label}`
        : `${pct >= 0 ? '+' : ''}${pct.toFixed(0)}% vs prior ${label}`}
    </span>
  )
}

function SpendChart({
  data,
  prior,
  label,
}: {
  data: TimeseriesResponse
  prior?: TimeseriesResponse
  label: string
}) {
  const { rows, keys } = toChartRows(data.series)
  const priorTotals = prior ? priorTotalsByIndex(prior, rows.length) : null
  const chartRows = priorTotals
    ? rows.map((row, i) => ({ ...row, [PREV_KEY]: priorTotals[i] }))
    : rows

  return (
    <TerminalCard title="spend.chart">
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-4">
          {keys.map((key, i) => (
            <LegendSwatch
              key={key}
              color={colorAt(i, key)}
              label={seriesLabel(key)}
            />
          ))}
          {prior && (
            <span className="inline-flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
              <span
                className="h-0 w-4 border-t border-dashed border-muted-foreground"
                aria-hidden="true"
              />
              prior {label}
            </span>
          )}
        </div>

        <div className="h-64 w-full">
          <ResponsiveContainer width="100%" height="100%">
            <ComposedChart
              data={chartRows}
              margin={{ top: 4, right: 8, left: 0, bottom: 0 }}
            >
              <CartesianGrid
                vertical={false}
                stroke="var(--color-border)"
                strokeOpacity={0.4}
              />
              <XAxis
                dataKey="date"
                tickFormatter={(d: string) => d.slice(5)}
                tick={{
                  fontSize: 10,
                  fill: 'var(--color-muted-foreground)',
                  fontFamily: MONO,
                }}
                tickLine={false}
                axisLine={false}
                minTickGap={16}
              />
              <YAxis
                width={44}
                tickFormatter={compactUsd}
                tick={{
                  fontSize: 10,
                  fill: 'var(--color-muted-foreground)',
                  fontFamily: MONO,
                }}
                tickLine={false}
                axisLine={false}
              />
              <Tooltip
                cursor={{ fill: 'var(--color-secondary)', fillOpacity: 0.4 }}
                content={(props) => (
                  <ChartTooltip {...(props as unknown as TooltipProps)} />
                )}
              />
              {keys.map((key, i) => (
                <Bar
                  key={key}
                  dataKey={key}
                  name={seriesLabel(key)}
                  stackId="spend"
                  fill={colorAt(i, key)}
                  fillOpacity={0.9}
                  activeBar={{ fillOpacity: 1 }}
                  radius={i === keys.length - 1 ? [3, 3, 0, 0] : undefined}
                  isAnimationActive={false}
                />
              ))}
              {prior && (
                <Line
                  dataKey={PREV_KEY}
                  name={`prior ${label}`}
                  type="monotone"
                  stroke="var(--color-muted-foreground)"
                  strokeDasharray="4 3"
                  strokeWidth={1.5}
                  dot={false}
                  isAnimationActive={false}
                />
              )}
            </ComposedChart>
          </ResponsiveContainer>
        </div>
      </div>
    </TerminalCard>
  )
}

function LegendSwatch({ color, label }: { color: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
      <span
        className="size-2.5 rounded-[2px]"
        style={{ backgroundColor: color }}
        aria-hidden="true"
      />
      {label}
    </span>
  )
}

interface TooltipEntry {
  dataKey?: string | number
  name?: string | number
  value?: number
  color?: string
}

interface TooltipProps {
  active?: boolean
  payload?: TooltipEntry[]
  label?: string | number
}

function ChartTooltip({ active, payload, label }: TooltipProps) {
  if (!active || !payload || payload.length === 0) return null
  const rows = payload
    .filter((p) => p.dataKey !== PREV_KEY && (p.value ?? 0) > 0)
    .sort((a, b) => (b.value ?? 0) - (a.value ?? 0))
  if (rows.length === 0) return null
  const total = rows.reduce((sum, p) => sum + (p.value ?? 0), 0)
  return (
    <div className="rounded-md border border-border bg-popover px-3 py-2 font-mono text-xs shadow-md">
      <div className="mb-1 tabular-nums text-muted-foreground">
        {String(label)}
      </div>
      <div className="flex flex-col gap-0.5">
        {rows.map((p) => (
          <div key={String(p.dataKey)} className="flex items-center gap-2">
            <span
              className="size-2 shrink-0 rounded-[2px]"
              style={{ backgroundColor: p.color }}
              aria-hidden="true"
            />
            <span className="text-muted-foreground">{String(p.name)}</span>
            <span className="ml-auto pl-4 tabular-nums text-foreground">
              ${(p.value ?? 0).toFixed(2)}
            </span>
          </div>
        ))}
        <div className="mt-1 flex items-center gap-2 border-t border-border pt-1">
          <span className="text-muted-foreground">total</span>
          <span className="ml-auto tabular-nums text-foreground">
            ${total.toFixed(2)}
          </span>
        </div>
      </div>
    </div>
  )
}

function StatTiles({
  data,
  anomalies,
  label,
  groupBy,
}: {
  data: TimeseriesResponse
  anomalies: CostAnomaly[]
  label: string
  groupBy: GroupBy
}) {
  const top = data.series[0]
  const worst = anomalies[0]
  const mostExpensive = worst
    ? {
        value: money(worst.cost_usd, true),
        hint: `${worst.ticket} · ${worst.phase}`,
      }
    : top
      ? {
          value: money(top.cost_usd, top.metered),
          hint: `${top.key} · ${groupBy}`,
        }
      : { value: '—', hint: undefined }

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <StatTile
        label={`total (${label})`}
        value={money(data.totals.cost_usd, data.totals.metered)}
        hint={`across ${data.facets.providers.length} providers`}
      />
      <StatTile
        label={`tokens (${label})`}
        value={data.totals.tokens.toLocaleString()}
        hint={`${data.series.length} ${groupBy} series`}
      />
      <StatTile
        label="most expensive"
        value={mostExpensive.value}
        hint={mostExpensive.hint}
      />
    </div>
  )
}

function AtlasSpend({
  spend,
  repo,
  label,
}: {
  spend: TimeseriesGroup
  repo: string | null
  label: string
}) {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <StatTile
        label={`atlas generation (${label})`}
        value={money(spend.cost_usd, spend.metered)}
        hint={repo ? `${repo} · architecture Views` : 'architecture Views'}
      />
    </div>
  )
}

function Anomalies({ anomalies }: { anomalies: CostAnomaly[] }) {
  return (
    <div className="flex flex-col gap-2">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        anomalies
      </span>
      <TerminalCard title="anomalies" bodyClassName="p-0">
        {anomalies.length === 0 ? (
          <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
            No cost anomalies flagged in this window.
          </p>
        ) : (
          <ul className="flex flex-col">
            {anomalies.map((a, i) => (
              <li key={`${a.repo}/${a.ticket}/${a.phase}/${i}`}>
                <Link
                  to="/runs/$repo/$ticket"
                  params={{ repo: a.repo, ticket: a.ticket }}
                  className={cn(
                    'flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-l-2 px-4 py-3 transition-colors last:border-b-0',
                    i === 0
                      ? 'border-l-fail/70 bg-fail/[0.08] text-fail hover:bg-fail/[0.12]'
                      : 'border-l-warn/70 bg-warn/[0.06] text-warn hover:bg-warn/[0.1]',
                  )}
                >
                  <span aria-hidden="true" className="font-mono text-sm">
                    {i === 0 ? '✗' : '⚠'}
                  </span>
                  <span className="font-mono text-sm">{a.ticket}</span>
                  <span className="font-mono text-xs text-muted-foreground">
                    {a.repo}
                  </span>
                  <span className="font-mono text-xs text-muted-foreground">
                    · {a.phase}
                  </span>
                  <span className="font-mono text-sm tabular-nums">
                    {money(a.cost_usd, true)}
                  </span>
                  {a.reasons.length > 0 && (
                    <span className="font-mono text-xs text-muted-foreground">
                      — {a.reasons.join(', ')}
                    </span>
                  )}
                </Link>
              </li>
            ))}
          </ul>
        )}
      </TerminalCard>
    </div>
  )
}

function Breakdown({ data }: { data: TimeseriesResponse }) {
  const rows = collapseSeries(data.series)
  return (
    <TerminalCard title="breakdown" bodyClassName="p-0">
      <table className="w-full border-collapse font-mono text-xs">
        <thead>
          <tr className="border-b border-border text-left text-muted-foreground">
            <th className="px-4 py-2 font-normal uppercase tracking-wider">
              {data.group_by}
            </th>
            <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
              Tokens
            </th>
            <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
              Spend
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr
              key={row.key}
              className="border-b border-border/60 last:border-0 hover:bg-secondary/40"
            >
              <td className="px-4 py-2.5">
                <span className="inline-flex items-center gap-2 text-foreground">
                  <span
                    className="size-2.5 rounded-[2px]"
                    style={{ backgroundColor: colorAt(i, row.key) }}
                    aria-hidden="true"
                  />
                  {seriesLabel(row.key)}
                </span>
              </td>
              <td className="px-4 py-2.5 text-right tabular-nums text-muted-foreground">
                {row.tokens.toLocaleString()}
              </td>
              <td className="px-4 py-2.5 text-right tabular-nums text-foreground">
                {money(row.cost_usd, row.metered)}
              </td>
            </tr>
          ))}
        </tbody>
        <tfoot>
          <tr className="border-t border-border">
            <td className="px-4 py-2.5 text-muted-foreground">total</td>
            <td className="px-4 py-2.5 text-right tabular-nums text-muted-foreground">
              {data.totals.tokens.toLocaleString()}
            </td>
            <td className="px-4 py-2.5 text-right tabular-nums text-foreground">
              {money(data.totals.cost_usd, data.totals.metered)}
            </td>
          </tr>
        </tfoot>
      </table>
    </TerminalCard>
  )
}

function CompareTable({
  current,
  prior,
}: {
  current: TimeseriesResponse
  prior: TimeseriesResponse
}) {
  const rows = seriesDelta(current, prior)
  return (
    <TerminalCard title="compare" bodyClassName="p-0">
      <table className="w-full border-collapse font-mono text-xs">
        <thead>
          <tr className="border-b border-border text-left text-muted-foreground">
            <th className="px-4 py-2 font-normal uppercase tracking-wider">
              {current.group_by}
            </th>
            <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
              Prior
            </th>
            <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
              Current
            </th>
            <th className="px-4 py-2 text-right font-normal uppercase tracking-wider">
              Δ
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr
              key={row.key}
              className="border-b border-border/60 last:border-0"
            >
              <td className="px-4 py-2.5 text-foreground">
                {seriesLabel(row.key)}
              </td>
              <td className="px-4 py-2.5 text-right tabular-nums text-muted-foreground">
                ${row.prev.toFixed(2)}
              </td>
              <td className="px-4 py-2.5 text-right tabular-nums text-foreground">
                ${row.cur.toFixed(2)}
              </td>
              <td
                className={cn(
                  'px-4 py-2.5 text-right tabular-nums',
                  row.delta > 0 && 'text-warn',
                  row.delta < 0 && 'text-done',
                  row.delta === 0 && 'text-muted-foreground',
                )}
              >
                {row.delta > 0 ? '+' : ''}
                {row.delta.toFixed(2)}
                {row.pct !== null && (
                  <span className="ml-1 text-[0.65rem] text-muted-foreground">
                    ({row.pct > 0 ? '+' : ''}
                    {row.pct.toFixed(0)}%)
                  </span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </TerminalCard>
  )
}
