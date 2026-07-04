import { useState, type ComponentType } from 'react'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import {
  CircleCheck,
  CircleDot,
  GitPullRequest,
  Pause,
  ShieldAlert,
  TriangleAlert,
  type LucideProps,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { EventFeed } from '@/components/event-feed'
import { cn } from '@/lib/utils'
import {
  reposQueryOptions,
  runsQueryOptions,
  type FailureClass,
  type Run,
} from '@/lib/runs'

export const Route = createFileRoute('/runs')({
  component: Runs,
  loader: ({ context }) => context.queryClient.ensureQueryData(reposQueryOptions),
})

type Tone = 'flow' | 'success' | 'danger'

interface PhaseMeta {
  label: string
  icon: ComponentType<LucideProps>
  tone: Tone
}

const PHASE_META: Record<string, PhaseMeta> = {
  building: { label: 'Building', icon: CircleDot, tone: 'flow' },
  built: { label: 'Built', icon: CircleDot, tone: 'flow' },
  handed_off: { label: 'Handed off', icon: CircleDot, tone: 'flow' },
  verified: { label: 'Verified', icon: CircleDot, tone: 'flow' },
  pr_open: { label: 'PR open', icon: GitPullRequest, tone: 'flow' },
  merged: { label: 'Merged', icon: CircleCheck, tone: 'success' },
  quarantined: { label: 'Quarantined', icon: ShieldAlert, tone: 'danger' },
}

function phaseMeta(phase: string): PhaseMeta {
  return (
    PHASE_META[phase] ?? {
      label: phase === '' ? 'Queued' : phase.replace(/_/g, ' '),
      icon: CircleDot,
      tone: 'flow',
    }
  )
}

const headerTone: Record<Tone, string> = {
  flow: 'text-muted-foreground',
  success: 'text-emerald-600 dark:text-emerald-400',
  danger: 'text-destructive',
}

function Runs() {
  const { data, error, isPending } = useQuery(reposQueryOptions)
  const [selected, setSelected] = useState<string | null>(null)

  const repos = data?.repos ?? []
  const active =
    selected && repos.some((r) => r.name === selected)
      ? selected
      : repos.find((r) => r.live)?.name ?? repos[0]?.name ?? null

  return (
    <div className="flex flex-col gap-6">
      {error && <p className="text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}

      {data && repos.length === 0 && (
        <Card className="max-w-md">
          <CardHeader>
            <CardTitle>Runs</CardTitle>
            <CardDescription>
              No repos yet. Runs appear here once a trau loop runs in a repo on
              this machine.
            </CardDescription>
          </CardHeader>
        </Card>
      )}

      {repos.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          {repos.map((repo) => (
            <button
              key={repo.root}
              type="button"
              title={repo.root}
              onClick={() => setSelected(repo.name)}
              className={cn(
                'flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm transition-colors',
                repo.name === active
                  ? 'border-transparent bg-accent text-accent-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {repo.name}
              {repo.live && (
                <span className="size-1.5 rounded-full bg-emerald-500" />
              )}
            </button>
          ))}
        </div>
      )}

      {active && <Board repo={active} />}
      {active && <EventFeed repo={active} />}
    </div>
  )
}

function Board({ repo }: { repo: string }) {
  const { data, error, isPending } = useQuery(runsQueryOptions(repo))
  const runs = data?.runs ?? []

  if (error) return <p className="text-sm text-destructive">{String(error)}</p>
  if (isPending) return <p className="text-sm text-muted-foreground">Loading…</p>
  if (runs.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No runs recorded for {repo} yet.
      </p>
    )
  }

  const order: string[] = []
  const byPhase: Record<string, Run[]> = {}
  for (const run of runs) {
    if (!byPhase[run.phase]) {
      byPhase[run.phase] = []
      order.push(run.phase)
    }
    byPhase[run.phase].push(run)
  }

  return (
    <div className="overflow-x-auto pb-2">
      <div className="flex min-w-max gap-4">
        {order.map((phase) => (
          <PhaseColumn key={phase} phase={phase} runs={byPhase[phase]} />
        ))}
      </div>
    </div>
  )
}

function PhaseColumn({ phase, runs }: { phase: string; runs: Run[] }) {
  const meta = phaseMeta(phase)
  const Icon = meta.icon
  return (
    <section className="flex w-64 shrink-0 flex-col gap-2">
      <div className="flex items-center justify-between px-1">
        <span className={cn('flex items-center gap-2 text-sm font-medium', headerTone[meta.tone])}>
          <Icon className="size-4" />
          {meta.label}
        </span>
        <span className="text-xs tabular-nums text-muted-foreground">{runs.length}</span>
      </div>
      <div className="flex flex-col gap-2">
        {runs.map((run) => (
          <RunCard key={run.ticket} run={run} />
        ))}
      </div>
    </section>
  )
}

const failureStyle: Record<FailureClass, { badge: string; icon: ComponentType<LucideProps>; label: string }> = {
  paused: {
    badge: 'border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400',
    icon: Pause,
    label: 'paused',
  },
  faulted: {
    badge: 'border-destructive/40 bg-destructive/10 text-destructive',
    icon: TriangleAlert,
    label: 'faulted',
  },
  gave_up: {
    badge: 'border-destructive/40 bg-destructive/10 text-destructive',
    icon: ShieldAlert,
    label: 'quarantined',
  },
}

function RunCard({ run }: { run: Run }) {
  const fail = run.failure_class ? failureStyle[run.failure_class] : null
  return (
    <div
      className={cn(
        'rounded-lg border bg-card p-3 text-card-foreground shadow-xs',
        run.phase === 'merged' && 'border-l-2 border-l-emerald-500/60',
        run.phase === 'quarantined' && 'border-l-2 border-l-destructive',
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-sm font-medium">{run.ticket}</span>
        {fail && (
          <Badge variant="outline" className={fail.badge}>
            <fail.icon className="size-3" />
            {fail.label}
          </Badge>
        )}
      </div>

      {run.title && (
        <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">{run.title}</p>
      )}

      {run.failure_reason && (
        <p className="mt-2 text-xs text-muted-foreground">{run.failure_reason}</p>
      )}

      {(run.branch || run.pr_url) && (
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          {run.branch && <span className="truncate font-mono">{run.branch}</span>}
          {run.pr_url && (
            <a
              href={run.pr_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-foreground hover:underline"
            >
              <GitPullRequest className="size-3" />
              {run.pr ? `PR #${run.pr}` : 'PR'}
            </a>
          )}
        </div>
      )}
    </div>
  )
}
