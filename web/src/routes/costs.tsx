import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, createFileRoute } from '@tanstack/react-router'
import { Boxes, Coins, DollarSign, Flame, Layers } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import {
  costsQueryOptions,
  type CostAnomaly,
  type CostBudget,
  type CostSpend,
  type CostsResponse,
  type DailyCost,
  type PhaseSpend,
  type RepoCost,
} from '@/lib/costs'

export const Route = createFileRoute('/costs')({
  component: Costs,
})

const WINDOWS = [7, 14, 30, 90]

function Costs() {
  const [days, setDays] = useState(30)
  const { data, error, isPending } = useQuery(costsQueryOptions(days))

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-lg font-semibold">Costs</h1>
        <div className="flex items-center gap-1">
          {WINDOWS.map((w) => (
            <button
              key={w}
              type="button"
              onClick={() => setDays(w)}
              className={cn(
                'rounded-md px-3 py-1.5 text-sm transition-colors',
                w === days
                  ? 'bg-accent text-accent-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {w}d
            </button>
          ))}
        </div>
      </div>

      {error && <p className="text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}
      {data && <Report data={data} />}
    </div>
  )
}

function Report({ data }: { data: CostsResponse }) {
  return (
    <div className="flex flex-col gap-8">
      <Summary
        totals={data.totals}
        budget={data.budget}
        repos={data.repos.length}
      />
      <DailyChart
        daily={data.daily}
        budget={data.budget}
        from={data.from}
        to={data.to}
      />
      <Repos repos={data.repos} />
      <Phases phases={data.phases} />
      <Anomalies anomalies={data.anomalies} />
    </div>
  )
}

function num(n: number): string {
  return n.toLocaleString()
}

function money(usd: number, metered: boolean): string {
  const formatted = `$${usd.toFixed(2)}`
  return metered ? formatted : `≥ ${formatted}`
}

function Summary({
  totals,
  budget,
  repos,
}: {
  totals: CostSpend
  budget: CostBudget
  repos: number
}) {
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      <Tile
        icon={DollarSign}
        label="Spend"
        value={money(totals.cost_usd, totals.metered)}
      />
      <Tile icon={Coins} label="Tokens" value={num(totals.tokens)} />
      <Tile icon={Boxes} label="Repos" value={String(repos)} />
      <Tile
        icon={Flame}
        label="Daily cap"
        value={budget.daily_usd ? `$${budget.daily_usd.toFixed(2)}` : '—'}
      />
    </div>
  )
}

function Tile({
  icon: Icon,
  label,
  value,
}: {
  icon: typeof DollarSign
  label: string
  value: string
}) {
  return (
    <div className="flex flex-col gap-1 rounded-lg border bg-card p-4">
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <Icon className="size-3.5" />
        {label}
      </span>
      <span className="text-xl font-semibold tabular-nums">{value}</span>
    </div>
  )
}

function Section({
  icon: Icon,
  title,
  children,
}: {
  icon: typeof DollarSign
  title: string
  children: React.ReactNode
}) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="flex items-center gap-2 text-sm font-semibold text-foreground">
        <Icon className="size-4 text-muted-foreground" />
        {title}
      </h2>
      {children}
    </section>
  )
}

function DailyChart({
  daily,
  budget,
  from,
  to,
}: {
  daily: DailyCost[]
  budget: CostBudget
  from: string
  to: string
}) {
  const cap = budget.daily_usd ?? 0
  const maxCost = daily.reduce((m, d) => Math.max(m, d.cost_usd), 0)
  const scale = Math.max(maxCost, cap, 0.01)
  const capPct = cap > 0 ? Math.min((cap / scale) * 100, 100) : 0

  return (
    <Section icon={DollarSign} title="Daily spend">
      {maxCost === 0 ? (
        <Empty>No token spend recorded in this window yet.</Empty>
      ) : (
        <div className="flex flex-col gap-2 rounded-lg border bg-card p-4">
          <div className="relative flex h-40 items-end gap-px">
            {cap > 0 && (
              <div
                className="pointer-events-none absolute inset-x-0 border-t border-dashed border-amber-500/70"
                style={{ bottom: `${capPct}%` }}
              >
                <span className="absolute right-0 -top-4 text-[10px] tabular-nums text-amber-600 dark:text-amber-400">
                  ${cap.toFixed(0)}/day
                </span>
              </div>
            )}
            {daily.map((d) => {
              const over = cap > 0 && d.cost_usd > cap
              return (
                <div
                  key={d.date}
                  title={`${d.date}: ${money(d.cost_usd, d.metered)} · ${num(d.tokens)} tokens`}
                  className="flex h-full flex-1 items-end"
                >
                  <div
                    className={cn(
                      'w-full rounded-sm transition-colors',
                      over ? 'bg-amber-500' : 'bg-primary/70 hover:bg-primary',
                      d.cost_usd === 0 && 'bg-muted',
                    )}
                    style={{
                      height: `${Math.max((d.cost_usd / scale) * 100, d.cost_usd > 0 ? 2 : 0)}%`,
                    }}
                  />
                </div>
              )
            })}
          </div>
          <div className="flex justify-between text-xs tabular-nums text-muted-foreground">
            <span>{from}</span>
            <span>{to}</span>
          </div>
        </div>
      )}
    </Section>
  )
}

function Repos({ repos }: { repos: RepoCost[] }) {
  return (
    <Section icon={Boxes} title="By repo">
      {repos.length === 0 ? (
        <Empty>No repo spend in this window.</Empty>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full min-w-max text-sm">
            <thead>
              <tr className="border-b bg-muted/40 text-left text-xs text-muted-foreground">
                <th className="px-3 py-2 font-medium">Repo</th>
                <th className="px-3 py-2 text-right font-medium">Tokens</th>
                <th className="px-3 py-2 text-right font-medium">Spend</th>
                <th className="px-3 py-2 text-right font-medium">Daily cap</th>
              </tr>
            </thead>
            <tbody>
              {repos.map((r) => (
                <tr key={r.repo} className="border-b last:border-b-0">
                  <td className="px-3 py-2 font-medium">{r.repo}</td>
                  <td className="px-3 py-2 text-right tabular-nums">
                    {num(r.tokens)}
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums">
                    {money(r.cost_usd, r.metered)}
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                    {r.daily_budget_usd
                      ? `$${r.daily_budget_usd.toFixed(2)}`
                      : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  )
}

function Phases({ phases }: { phases: PhaseSpend[] }) {
  const max = phases.reduce((m, p) => Math.max(m, p.cost_usd), 0)
  return (
    <Section icon={Layers} title="By phase">
      {phases.length === 0 ? (
        <Empty>No phase spend in this window.</Empty>
      ) : (
        <div className="flex flex-col gap-2 rounded-lg border bg-card p-4">
          {phases.map((p) => (
            <div key={p.phase} className="flex items-center gap-3">
              <span className="w-20 shrink-0 font-mono text-xs">{p.phase}</span>
              <div className="h-2 flex-1 overflow-hidden rounded-full bg-muted">
                <div
                  className="h-full rounded-full bg-primary/70"
                  style={{
                    width: `${max > 0 ? (p.cost_usd / max) * 100 : 0}%`,
                  }}
                />
              </div>
              <span className="w-28 shrink-0 text-right text-xs tabular-nums text-muted-foreground">
                {money(p.cost_usd, p.metered)} · {num(p.tokens)}
              </span>
            </div>
          ))}
        </div>
      )}
    </Section>
  )
}

function Anomalies({ anomalies }: { anomalies: CostAnomaly[] }) {
  return (
    <Section icon={Flame} title="Cost anomalies">
      {anomalies.length === 0 ? (
        <Empty>No cost anomalies flagged in this window.</Empty>
      ) : (
        <div className="flex flex-col gap-2">
          {anomalies.map((a, i) => (
            <Link
              key={`${a.repo}/${a.ticket}/${a.phase}/${i}`}
              to="/runs/$repo/$ticket"
              params={{ repo: a.repo, ticket: a.ticket }}
              className="flex flex-col gap-1.5 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 transition-colors hover:border-amber-500/70"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-xs font-medium">
                  {a.ticket}
                </span>
                <span className="text-xs text-muted-foreground">{a.repo}</span>
                <span className="font-mono text-xs text-muted-foreground">
                  · {a.phase}
                </span>
                <span className="ml-auto text-xs tabular-nums text-amber-700 dark:text-amber-300">
                  {money(a.cost_usd, true)}
                </span>
              </div>
              <div className="flex flex-wrap gap-1.5">
                {a.reasons.map((r, j) => (
                  <Badge
                    key={j}
                    variant="outline"
                    className="border-amber-500/40 text-amber-700 dark:text-amber-300"
                  >
                    {r}
                  </Badge>
                ))}
              </div>
            </Link>
          ))}
        </div>
      )}
    </Section>
  )
}

function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed px-4 py-6 text-sm text-muted-foreground">
      {children}
    </div>
  )
}
