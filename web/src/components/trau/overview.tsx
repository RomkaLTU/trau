import { useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Eye, Play, RefreshCw, Square } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { useActiveRepo } from '@/components/trau/active-repo'
import { EmptyState } from '@/components/trau/empty-state'
import { StatTile } from '@/components/trau/stat-tile'
import { StatusPill, type RunState } from '@/components/trau/status-pill'
import { TerminalCard } from '@/components/trau/terminal-card'
import { cn } from '@/lib/utils'
import { useAttentionRuns } from '@/lib/attention'
import { stopInstance } from '@/lib/instances'
import {
  phasePill,
  phaseSteps,
  useLiveLoops,
  useTodaySpend,
  type LiveLoop,
  type PhaseState,
} from '@/lib/overview'
import type { FailureClass } from '@/lib/runs'

function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

function elapsed(fromISO: string, now: number): string {
  const s = Math.max(0, Math.floor((now - new Date(fromISO).getTime()) / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const rem = s % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${rem}s`
  return `${rem}s`
}

function money(usd: number): string {
  return `$${usd.toFixed(2)}`
}

export function StatTiles() {
  const { repo } = useActiveRepo()
  const spend = useTodaySpend(repo)
  const loops = useLiveLoops(repo)
  const attention = useAttentionRuns(repo)

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <StatTile
        label="spend today"
        value={
          <>
            {spend.metered ? '' : '≥ '}
            {money(spend.cost)}
            {spend.budget ? (
              <span className="text-sm text-muted-foreground">
                {' '}
                / {money(spend.budget)}
              </span>
            ) : null}
          </>
        }
        progress={spend.budget ? { value: spend.cost, max: spend.budget } : undefined}
        hint={spend.budget ? undefined : 'no daily cap set'}
      />
      <StatTile label="active loops" value={loops.length} hint="running now" />
      <StatTile
        label="needs attention"
        value={attention.length}
        valueClassName={attention.length > 0 ? 'text-warn' : undefined}
        hint="waiting on you"
      />
    </div>
  )
}

const PHASE_TEXT: Record<PhaseState, string> = {
  done: 'text-done',
  active: 'text-teal',
  todo: 'text-faint',
}

const PHASE_GLYPH: Record<PhaseState, string> = {
  done: '✓',
  active: '●',
  todo: '○',
}

function PhaseStepper({ phase }: { phase: string }) {
  const steps = phaseSteps(phase)
  return (
    <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1 font-mono text-xs">
      {steps.map((step, i) => (
        <span key={step.label} className="inline-flex items-center gap-1.5">
          <span className={cn('inline-flex items-center gap-1', PHASE_TEXT[step.state])}>
            <span aria-hidden="true">{PHASE_GLYPH[step.state]}</span>
            {step.label}
          </span>
          {i < steps.length - 1 && (
            <span className="text-faint" aria-hidden="true">
              →
            </span>
          )}
        </span>
      ))}
    </div>
  )
}

function MetaItem({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="font-mono text-[0.6rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </span>
      <span className="font-mono text-sm text-foreground">{value}</span>
    </div>
  )
}

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function StopButton({ pid, repo }: { pid: number; repo: string }) {
  const queryClient = useQueryClient()
  const stop = useMutation({
    mutationFn: () => stopInstance(pid),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['instances'] }),
  })
  return (
    <div className="flex flex-col items-start gap-1">
      <Button
        variant="ghost"
        size="sm"
        className="font-mono text-fail hover:text-fail"
        disabled={stop.isPending}
        onClick={() => stop.mutate()}
        title={`Stop the loop in ${repo}`}
      >
        <Square className="size-4" aria-hidden="true" />
        {stop.isPending ? 'Stopping…' : 'Stop'}
      </Button>
      {stop.error && (
        <p className="text-xs text-destructive">
          Couldn’t stop {repo}: {actionError(stop.error)}
        </p>
      )}
    </div>
  )
}

function LoopCard({ loop, now }: { loop: LiveLoop; now: number }) {
  const pill = phasePill(loop.phase)
  return (
    <TerminalCard title={loop.repo} scanlines>
      <div className="flex flex-col gap-4">
        {loop.ticket ? (
          <div className="flex flex-col gap-1">
            <div className="flex items-center gap-2">
              <Link
                to="/runs/$repo/$ticket"
                params={{ repo: loop.repo, ticket: loop.ticket }}
                className="font-mono text-sm text-primary hover:underline"
              >
                {loop.ticket}
              </Link>
              <StatusPill state={pill.state} label={pill.label} />
            </div>
            {loop.title && (
              <p className="text-pretty font-sans text-sm leading-relaxed text-foreground">
                {loop.title}
              </p>
            )}
          </div>
        ) : (
          <div className="flex items-center gap-2">
            <StatusPill state="active" label="grazing" />
            <span className="font-sans text-sm text-muted-foreground">
              Idle — picking the next ready ticket
            </span>
          </div>
        )}

        <div className="flex items-center gap-6">
          <MetaItem label="elapsed" value={elapsed(loop.startedAt, now)} />
          {loop.ticket && loop.phaseSince && (
            <MetaItem label="in phase" value={elapsed(loop.phaseSince, now)} />
          )}
        </div>

        {loop.ticket && (
          <div className="rounded-md border border-border bg-secondary/30 px-3 py-2.5">
            <PhaseStepper phase={loop.phase} />
          </div>
        )}

        <div className="flex items-start gap-2">
          {loop.ticket && (
            <Button asChild variant="outline" size="sm" className="font-mono">
              <Link
                to="/live/$repo/$ticket"
                params={{ repo: loop.repo, ticket: loop.ticket }}
              >
                <Eye className="size-4" aria-hidden="true" />
                Watch
              </Link>
            </Button>
          )}
          <StopButton pid={loop.pid} repo={loop.repo} />
        </div>
      </div>
    </TerminalCard>
  )
}

export function LiveLoops() {
  const { repo } = useActiveRepo()
  const loops = useLiveLoops(repo)
  const now = useNow(1000)

  if (loops.length === 0) {
    return (
      <EmptyState
        message="No loops running right now. Point trau at a ticket to watch it work."
        actions={
          <>
            <Button asChild size="sm" className="font-mono">
              <Link to="/run-once">
                <Play className="size-4" aria-hidden="true" />
                Run once
              </Link>
            </Button>
            <Button asChild variant="outline" size="sm" className="font-mono">
              <Link to="/loop">
                <RefreshCw className="size-4" aria-hidden="true" />
                Start loop
              </Link>
            </Button>
          </>
        }
      />
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {loops.map((loop) => (
        <LoopCard key={loop.pid} loop={loop} now={now} />
      ))}
    </div>
  )
}

const ATTENTION_META: Record<
  FailureClass,
  { pill: { state: RunState; label: string }; action: string }
> = {
  paused: { pill: { state: 'warn', label: 'paused' }, action: 'Resume' },
  faulted: { pill: { state: 'fail', label: 'fault' }, action: 'View run' },
  gave_up: { pill: { state: 'fail', label: 'quarantined' }, action: 'Reset' },
}

export function NeedsAttention() {
  const { repo } = useActiveRepo()
  const attention = useAttentionRuns(repo)

  if (attention.length === 0) {
    return (
      <TerminalCard title="needs-attention">
        <p className="font-sans text-sm text-muted-foreground">
          Nothing waiting on you — every loop is healthy.
        </p>
      </TerminalCard>
    )
  }

  return (
    <TerminalCard title="needs-attention" bodyClassName="p-0">
      <ul className="flex flex-col">
        {attention.map((run) => {
          const meta = ATTENTION_META[run.failure_class!]
          return (
            <li
              key={`${run.repo} ${run.ticket}`}
              className="flex flex-col gap-2 border-b border-border/60 px-4 py-3 last:border-0"
            >
              <div className="flex items-center gap-2">
                <StatusPill state={meta.pill.state} label={meta.pill.label} />
                <span className="font-mono text-xs text-primary">{run.ticket}</span>
              </div>
              <p className="text-pretty font-sans text-sm leading-relaxed text-muted-foreground">
                {run.failure_reason || run.title || run.repo}
              </p>
              <Link
                to="/live/$repo/$ticket"
                params={{ repo: run.repo, ticket: run.ticket }}
                className="w-fit font-mono text-xs text-teal underline-offset-4 hover:underline"
              >
                {meta.action} →
              </Link>
            </li>
          )
        })}
      </ul>
    </TerminalCard>
  )
}

export function QuickLaunch() {
  return (
    <TerminalCard title="quick-launch">
      <div className="flex flex-col gap-4 sm:flex-row">
        <div className="flex flex-1 flex-col gap-2">
          <Button asChild className="w-full font-mono">
            <Link to="/run-once">
              <Play className="size-4" aria-hidden="true" />
              Run once
            </Link>
          </Button>
          <span className="font-mono text-[0.65rem] text-muted-foreground">
            one ticket, full pipeline
          </span>
        </div>
        <div className="flex flex-1 flex-col gap-2">
          <Button asChild variant="outline" className="w-full font-mono">
            <Link to="/loop">
              <RefreshCw className="size-4" aria-hidden="true" />
              Start loop
            </Link>
          </Button>
          <span className="font-mono text-[0.65rem] text-muted-foreground">
            graze the ready queue
          </span>
        </div>
      </div>
    </TerminalCard>
  )
}
