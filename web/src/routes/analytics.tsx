import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { ArrowLeftRight, Boxes, Coins, DollarSign } from 'lucide-react'

import { cn } from '@/lib/utils'
import {
  previousWindow,
  timeseriesQueryOptions,
  type GroupBy,
  type TimeseriesGroup,
  type TimeseriesResponse,
} from '@/lib/analytics'

export const Route = createFileRoute('/analytics')({
  component: Analytics,
})

type Metric = 'cost' | 'tokens'

const WINDOWS = [7, 14, 30, 90]
const GROUPS: { value: GroupBy; label: string }[] = [
  { value: 'provider', label: 'Provider' },
  { value: 'repo', label: 'Repo' },
  { value: 'model', label: 'Model' },
  { value: 'phase', label: 'Phase' },
]

const PALETTE = [
  'oklch(0.62 0.19 260)',
  'oklch(0.68 0.16 162)',
  'oklch(0.72 0.18 70)',
  'oklch(0.63 0.24 304)',
  'oklch(0.64 0.22 16)',
  'oklch(0.6 0.12 200)',
  'oklch(0.66 0.18 40)',
  'oklch(0.56 0.14 285)',
]
const OTHER_KEY = '__other'
const OTHER_COLOR = 'var(--color-muted-foreground)'
const TOP_N = 8

interface Filters {
  repos: string[]
  providers: string[]
  phases: string[]
}

function Analytics() {
  const [days, setDays] = useState(30)
  const [groupBy, setGroupBy] = useState<GroupBy>('provider')
  const [metric, setMetric] = useState<Metric>('cost')
  const [compare, setCompare] = useState(false)
  const [filters, setFilters] = useState<Filters>({
    repos: [],
    providers: [],
    phases: [],
  })

  const params = {
    days,
    groupBy,
    repos: filters.repos,
    providers: filters.providers,
    phases: filters.phases,
  }
  const { data, error, isPending } = useQuery(timeseriesQueryOptions(params))

  const prev = previousWindow(days)
  const prior = useQuery({
    ...timeseriesQueryOptions({ ...params, days: undefined, from: prev.from, to: prev.to }),
    enabled: compare,
  })

  const toggle = (dim: keyof Filters, value: string) =>
    setFilters((f) => {
      const has = f[dim].includes(value)
      return {
        ...f,
        [dim]: has ? f[dim].filter((v) => v !== value) : [...f[dim], value],
      }
    })

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-lg font-semibold">Analytics</h1>
        <SegmentedButtons
          options={WINDOWS.map((w) => ({ value: w, label: `${w}d` }))}
          value={days}
          onChange={setDays}
        />
      </div>

      <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
        <Control label="Group by">
          <SegmentedButtons options={GROUPS} value={groupBy} onChange={setGroupBy} />
        </Control>
        <Control label="Metric">
          <SegmentedButtons
            options={[
              { value: 'cost' as Metric, label: 'Cost' },
              { value: 'tokens' as Metric, label: 'Tokens' },
            ]}
            value={metric}
            onChange={setMetric}
          />
        </Control>
        <button
          type="button"
          onClick={() => setCompare((c) => !c)}
          className={cn(
            'flex items-center gap-1.5 rounded-md border px-3 py-1.5 text-sm transition-colors',
            compare
              ? 'border-primary/50 bg-accent text-accent-foreground'
              : 'text-muted-foreground hover:text-foreground',
          )}
        >
          <ArrowLeftRight className="size-3.5" />
          Compare periods
        </button>
      </div>

      {data && (
        <FilterChips
          facets={data.facets}
          filters={filters}
          onToggle={toggle}
        />
      )}

      {error && <p className="text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}
      {data && (
        <>
          <Summary data={data} metric={metric} />
          <Chart data={data} metric={metric} />
          {compare && (
            <Comparison
              current={data}
              prior={prior.data}
              metric={metric}
              loading={prior.isPending}
            />
          )}
        </>
      )}
    </div>
  )
}

function metricValue(
  s: { tokens: number; cost_usd: number },
  metric: Metric,
): number {
  return metric === 'cost' ? s.cost_usd : s.tokens
}

function fmtMetric(v: number, metric: Metric): string {
  if (metric === 'cost') return `$${v.toFixed(2)}`
  return v.toLocaleString()
}

function compact(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k`
  return String(v)
}

function seriesLabel(key: string): string {
  return key === OTHER_KEY ? 'other' : key
}

function colorAt(i: number, key: string): string {
  return key === OTHER_KEY ? OTHER_COLOR : PALETTE[i % PALETTE.length]
}

function Chart({ data, metric }: { data: TimeseriesResponse; metric: Metric }) {
  const { rows, keys } = toChartData(data.series, metric)

  if (data.series.length === 0) {
    return (
      <Empty>No token spend recorded in this window yet.</Empty>
    )
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-card p-4">
      <div className="h-72 w-full">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={rows} margin={{ top: 4, right: 8, left: 4, bottom: 0 }}>
            <defs>
              {keys.map((key, i) => (
                <linearGradient key={key} id={`fill-${i}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={colorAt(i, key)} stopOpacity={0.5} />
                  <stop offset="100%" stopColor={colorAt(i, key)} stopOpacity={0.05} />
                </linearGradient>
              ))}
            </defs>
            <CartesianGrid
              vertical={false}
              stroke="var(--color-border)"
              strokeDasharray="3 3"
            />
            <XAxis
              dataKey="date"
              tickFormatter={(d: string) => d.slice(5)}
              tick={{ fontSize: 11, fill: 'var(--color-muted-foreground)' }}
              tickLine={false}
              axisLine={false}
              minTickGap={24}
            />
            <YAxis
              width={48}
              tickFormatter={(v: number) =>
                metric === 'cost' ? `$${compact(v)}` : compact(v)
              }
              tick={{ fontSize: 11, fill: 'var(--color-muted-foreground)' }}
              tickLine={false}
              axisLine={false}
            />
            <Tooltip
              content={({ active, payload, label }) => (
                <ChartTooltip
                  active={active}
                  payload={payload as unknown as TooltipEntry[]}
                  label={typeof label === 'string' || typeof label === 'number' ? label : undefined}
                  metric={metric}
                />
              )}
            />
            {keys.map((key, i) => (
              <Area
                key={key}
                type="monotone"
                dataKey={key}
                name={seriesLabel(key)}
                stackId="1"
                stroke={colorAt(i, key)}
                fill={`url(#fill-${i})`}
                strokeWidth={1.5}
              />
            ))}
          </AreaChart>
        </ResponsiveContainer>
      </div>
      <Legend keys={keys} />
    </div>
  )
}

interface TooltipEntry {
  dataKey?: string | number
  name?: string | number
  value?: number
  color?: string
}

function ChartTooltip({
  active,
  payload,
  label,
  metric,
}: {
  active?: boolean
  payload?: TooltipEntry[]
  label?: string | number
  metric: Metric
}) {
  if (!active || !payload || payload.length === 0) return null
  const rows = payload
    .filter((p) => (p.value ?? 0) > 0)
    .sort((a, b) => (b.value ?? 0) - (a.value ?? 0))
  if (rows.length === 0) return null
  return (
    <div className="rounded-md border bg-popover px-3 py-2 text-xs shadow-md">
      <div className="mb-1 font-medium tabular-nums">{String(label)}</div>
      <div className="flex flex-col gap-0.5">
        {rows.map((p) => (
          <div key={String(p.dataKey)} className="flex items-center gap-2">
            <span
              className="size-2 shrink-0 rounded-[2px]"
              style={{ backgroundColor: p.color }}
            />
            <span className="text-muted-foreground">{String(p.name)}</span>
            <span className="ml-auto tabular-nums">
              {fmtMetric(p.value ?? 0, metric)}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}

function Legend({ keys }: { keys: string[] }) {
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1.5">
      {keys.map((key, i) => (
        <span
          key={key}
          className="flex items-center gap-1.5 text-xs text-muted-foreground"
        >
          <span
            className="size-2.5 rounded-[2px]"
            style={{ backgroundColor: colorAt(i, key) }}
          />
          {seriesLabel(key)}
        </span>
      ))}
    </div>
  )
}

type ChartRow = Record<string, number | string>

function toChartData(
  series: TimeseriesGroup[],
  metric: Metric,
): { rows: ChartRow[]; keys: string[] } {
  if (series.length === 0) return { rows: [], keys: [] }
  const top = series.slice(0, TOP_N)
  const rest = series.slice(TOP_N)
  const dates = series[0].points.map((p) => p.date)
  const rows: ChartRow[] = dates.map((date, i) => {
    const row: ChartRow = { date }
    for (const s of top) row[s.key] = metricValue(s.points[i], metric)
    if (rest.length > 0) {
      row[OTHER_KEY] = rest.reduce((sum, s) => sum + metricValue(s.points[i], metric), 0)
    }
    return row
  })
  const keys = top.map((s) => s.key)
  if (rest.length > 0) keys.push(OTHER_KEY)
  return { rows, keys }
}

function Summary({
  data,
  metric,
}: {
  data: TimeseriesResponse
  metric: Metric
}) {
  const money = data.totals.metered
    ? `$${data.totals.cost_usd.toFixed(2)}`
    : `≥ $${data.totals.cost_usd.toFixed(2)}`
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
      <Tile icon={DollarSign} label="Spend" value={money} />
      <Tile icon={Coins} label="Tokens" value={data.totals.tokens.toLocaleString()} />
      <Tile
        icon={Boxes}
        label={`${GROUPS.find((g) => g.value === data.group_by)?.label ?? ''} series`}
        value={String(data.series.length)}
        hint={metric === 'cost' ? 'by spend' : 'by tokens'}
      />
    </div>
  )
}

function Tile({
  icon: Icon,
  label,
  value,
  hint,
}: {
  icon: typeof DollarSign
  label: string
  value: string
  hint?: string
}) {
  return (
    <div className="flex flex-col gap-1 rounded-lg border bg-card p-4">
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <Icon className="size-3.5" />
        {label}
      </span>
      <span className="text-xl font-semibold tabular-nums">{value}</span>
      {hint && <span className="text-[11px] text-muted-foreground">{hint}</span>}
    </div>
  )
}

function FilterChips({
  facets,
  filters,
  onToggle,
}: {
  facets: TimeseriesResponse['facets']
  filters: Filters
  onToggle: (dim: keyof Filters, value: string) => void
}) {
  const rows = (
    [
      { dim: 'repos', label: 'Repo', values: facets.repos },
      { dim: 'providers', label: 'Provider', values: facets.providers },
      { dim: 'phases', label: 'Phase', values: facets.phases },
    ] satisfies { dim: keyof Filters; label: string; values: string[] }[]
  ).filter((r) => r.values.length > 0)

  if (rows.length === 0) return null

  return (
    <div className="flex flex-col gap-2 rounded-lg border bg-card/50 p-3">
      {rows.map((r) => (
        <div key={r.dim} className="flex flex-wrap items-center gap-1.5">
          <span className="w-16 shrink-0 text-xs text-muted-foreground">
            {r.label}
          </span>
          {r.values.map((v) => {
            const active = filters[r.dim].includes(v)
            return (
              <button
                key={v}
                type="button"
                onClick={() => onToggle(r.dim, v)}
                className={cn(
                  'rounded-full border px-2.5 py-0.5 text-xs transition-colors',
                  active
                    ? 'border-primary/50 bg-accent text-accent-foreground'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                {v}
              </button>
            )
          })}
        </div>
      ))}
    </div>
  )
}

function Comparison({
  current,
  prior,
  metric,
  loading,
}: {
  current: TimeseriesResponse
  prior?: TimeseriesResponse
  metric: Metric
  loading: boolean
}) {
  if (!prior && loading) {
    return <Empty>Loading previous period…</Empty>
  }
  if (!prior) return null

  const rows = deltaRows(current, prior, metric)
  return (
    <section className="flex flex-col gap-3">
      <h2 className="flex items-center gap-2 text-sm font-semibold">
        <ArrowLeftRight className="size-4 text-muted-foreground" />
        This period vs previous
        <span className="font-normal text-muted-foreground">
          {prior.from} → {prior.to} vs {current.from} → {current.to}
        </span>
      </h2>
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full min-w-max text-sm">
          <thead>
            <tr className="border-b bg-muted/40 text-left text-xs text-muted-foreground">
              <th className="px-3 py-2 font-medium">
                {GROUPS.find((g) => g.value === current.group_by)?.label}
              </th>
              <th className="px-3 py-2 text-right font-medium">Previous</th>
              <th className="px-3 py-2 text-right font-medium">Current</th>
              <th className="px-3 py-2 text-right font-medium">Δ</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.key} className="border-b last:border-b-0">
                <td className="px-3 py-2 font-medium">{row.key}</td>
                <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                  {fmtMetric(row.prev, metric)}
                </td>
                <td className="px-3 py-2 text-right tabular-nums">
                  {fmtMetric(row.cur, metric)}
                </td>
                <td
                  className={cn(
                    'px-3 py-2 text-right tabular-nums',
                    row.delta > 0 && 'text-amber-600 dark:text-amber-400',
                    row.delta < 0 && 'text-emerald-600 dark:text-emerald-400',
                  )}
                >
                  {row.delta > 0 ? '+' : ''}
                  {fmtMetric(row.delta, metric)}
                  {row.pct !== null && (
                    <span className="ml-1 text-[11px] text-muted-foreground">
                      ({row.pct > 0 ? '+' : ''}
                      {row.pct.toFixed(0)}%)
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

interface DeltaRow {
  key: string
  prev: number
  cur: number
  delta: number
  pct: number | null
}

function deltaRows(
  current: TimeseriesResponse,
  prior: TimeseriesResponse,
  metric: Metric,
): DeltaRow[] {
  const cur = new Map(current.series.map((s) => [s.key, metricValue(s, metric)]))
  const prev = new Map(prior.series.map((s) => [s.key, metricValue(s, metric)]))
  const keys = new Set([...cur.keys(), ...prev.keys()])
  const rows: DeltaRow[] = []
  for (const key of keys) {
    const c = cur.get(key) ?? 0
    const p = prev.get(key) ?? 0
    rows.push({
      key,
      prev: p,
      cur: c,
      delta: c - p,
      pct: p > 0 ? ((c - p) / p) * 100 : null,
    })
  }
  rows.sort((a, b) => b.cur - a.cur || b.prev - a.prev)
  return rows
}

function SegmentedButtons<T extends string | number>({
  options,
  value,
  onChange,
}: {
  options: { value: T; label: string }[]
  value: T
  onChange: (v: T) => void
}) {
  return (
    <div className="flex items-center gap-1">
      {options.map((o) => (
        <button
          key={String(o.value)}
          type="button"
          onClick={() => onChange(o.value)}
          className={cn(
            'rounded-md px-3 py-1.5 text-sm transition-colors',
            o.value === value
              ? 'bg-accent text-accent-foreground'
              : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

function Control({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex items-center gap-2">
      <span className="text-xs text-muted-foreground">{label}</span>
      {children}
    </div>
  )
}

function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed px-4 py-6 text-sm text-muted-foreground">
      {children}
    </div>
  )
}
