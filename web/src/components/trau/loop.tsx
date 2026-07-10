import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ExternalLink, Minus, Plus, Square } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { MakeStartableButton } from '@/components/make-startable-button'
import { useActiveRepo } from '@/components/trau/active-repo'
import { TargetRepoField } from '@/components/trau/target-repo-field'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { Eyebrow } from '@/components/trau/eyebrow'
import { PhaseStepper } from '@/components/trau/phase-stepper'
import { SegmentedControl } from '@/components/trau/segmented-control'
import { StatusPill } from '@/components/trau/status-pill'
import { TerminalCard } from '@/components/trau/terminal-card'
import { cn } from '@/lib/utils'
import { eligibleQueryOptions } from '@/lib/eligible'
import {
  epicPreviewQueryOptions,
  isTicketId,
  type EpicSubIssue,
} from '@/lib/epic'
import { useEventFeed } from '@/lib/events'
import {
  instancesQueryOptions,
  startInstance,
  stopInstance,
  type Instance,
} from '@/lib/instances'
import { deriveLoopHalt, type LoopHalt } from '@/lib/loop'
import { pauseKind, phaseLabel, runPhaseSteps } from '@/lib/runlive'

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), ms)
    return () => clearTimeout(id)
  }, [value, ms])
  return debounced
}

function elapsedSince(fromISO: string, now: number): string {
  const s = Math.max(0, Math.floor((now - new Date(fromISO).getTime()) / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (h > 0) return `${h}h ${String(m).padStart(2, '0')}m`
  return `${m}m ${String(sec).padStart(2, '0')}s`
}

type Scope = 'ready' | 'epic'

const SCOPE_OPTIONS: { value: Scope; label: string }[] = [
  { value: 'ready', label: 'Ready queue' },
  { value: 'epic', label: 'Epic' },
]

const SUB_GLYPH: Record<
  EpicSubIssue['state'],
  { glyph: string; className: string; label: string }
> = {
  done: { glyph: '✓', className: 'text-done', label: 'done' },
  epic: { glyph: '◆', className: 'text-info', label: 'epic' },
  todo: { glyph: '○', className: 'text-faint', label: 'todo' },
}

interface StartOptions {
  epic?: string
  max: number
  noResume: boolean
}

function MaxIterationsStepper({
  value,
  onChange,
}: {
  value: number
  onChange: (n: number) => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        max iterations
      </span>
      <div className="inline-flex w-fit items-center overflow-hidden rounded-md border border-border bg-input">
        <button
          type="button"
          onClick={() => onChange(Math.max(1, value - 1))}
          aria-label="Decrease max iterations"
          className="flex size-8 items-center justify-center text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
        >
          <Minus className="size-3.5" aria-hidden="true" />
        </button>
        <span className="w-10 text-center font-mono text-sm tabular-nums text-foreground">
          {value}
        </span>
        <button
          type="button"
          onClick={() => onChange(Math.min(99, value + 1))}
          aria-label="Increase max iterations"
          className="flex size-8 items-center justify-center text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
        >
          <Plus className="size-3.5" aria-hidden="true" />
        </button>
      </div>
    </div>
  )
}

function SkipResumeToggle({
  value,
  onChange,
}: {
  value: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center gap-3">
        <button
          type="button"
          role="switch"
          aria-checked={value}
          aria-label="Skip resume"
          onClick={() => onChange(!value)}
          className={cn(
            'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors',
            value ? 'border-primary bg-primary/30' : 'border-border bg-input',
          )}
        >
          <span
            aria-hidden="true"
            className={cn(
              'inline-block size-3.5 rounded-full transition-transform',
              value
                ? 'translate-x-4 bg-primary'
                : 'translate-x-0.5 bg-muted-foreground',
            )}
          />
        </button>
        <span className="font-mono text-sm text-foreground">skip resume</span>
      </div>
      <p className="font-sans text-xs leading-relaxed text-muted-foreground">
        Start fresh; ignore existing checkpoints.
      </p>
    </div>
  )
}

function TicketRows({
  rows,
  more,
}: {
  rows: { id: string; title: string; badge: React.ReactNode }[]
  more?: number
}) {
  return (
    <div className="overflow-hidden rounded-md border border-border">
      <table className="w-full border-collapse text-left">
        <thead>
          <tr className="border-b border-border bg-secondary/40">
            <th className="px-3 py-2 font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
              Ticket
            </th>
            <th className="px-3 py-2 font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
              Title
            </th>
            <th className="px-3 py-2 text-right font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
              State
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.id} className="border-b border-border/60 last:border-0">
              <td className="px-3 py-2 font-mono text-sm text-primary">
                {row.id}
              </td>
              <td className="px-3 py-2 font-sans text-sm text-foreground">
                {row.title}
              </td>
              <td className="px-3 py-2 text-right">{row.badge}</td>
            </tr>
          ))}
          {more && more > 0 ? (
            <tr>
              <td
                colSpan={3}
                className="px-3 py-2 font-mono text-xs text-muted-foreground"
              >
                …{more} more
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  )
}

function ReadyPreview({ repo }: { repo: string }) {
  const eligible = useQuery(eligibleQueryOptions(repo))
  const tickets = eligible.data?.tickets ?? []

  if (eligible.isLoading) {
    return (
      <p className="font-mono text-sm text-muted-foreground">
        Reading the ready queue…
      </p>
    )
  }
  if (eligible.error) {
    return (
      <p className="font-mono text-sm text-destructive">
        {actionError(eligible.error)}
      </p>
    )
  }
  if (tickets.length === 0) {
    return (
      <p className="font-sans text-sm text-muted-foreground">
        Queue is empty — mark tickets ready-for-agent to fill the range.
      </p>
    )
  }
  return (
    <TicketRows
      rows={tickets.slice(0, 8).map((t) => ({
        id: t.id,
        title: t.title,
        badge: <StatusPill state="todo" label="ready" />,
      }))}
      more={tickets.length - 8}
    />
  )
}

function EpicPreview({ repo, epic }: { repo: string; epic: string }) {
  const valid = isTicketId(epic)
  const preview = useQuery(epicPreviewQueryOptions(repo, epic))

  if (!valid) {
    return (
      <p className="font-sans text-sm text-muted-foreground">
        Enter an epic id to preview its sub-issues.
      </p>
    )
  }
  if (preview.isLoading) {
    return (
      <p className="font-mono text-sm text-muted-foreground">
        Listing {epic}…
      </p>
    )
  }
  if (preview.error) {
    return (
      <p className="font-mono text-sm text-destructive">
        {actionError(preview.error)}
      </p>
    )
  }
  const subs = preview.data?.sub_issues ?? []
  if (subs.length === 0) {
    return (
      <p className="font-sans text-sm text-muted-foreground">
        {epic} has no sub-issues.
      </p>
    )
  }
  return (
    <TicketRows
      rows={subs.slice(0, 12).map((sub) => {
        const styles = SUB_GLYPH[sub.state]
        return {
          id: sub.id,
          title: sub.title,
          badge: (
            <span
              className={cn(
                'inline-flex items-center gap-1.5 font-mono text-xs',
                styles.className,
              )}
            >
              <span aria-hidden="true">{styles.glyph}</span>
              {styles.label}
            </span>
          ),
        }
      })}
      more={subs.length - 12}
    />
  )
}

function LaunchForm({
  repo,
  onStart,
  starting,
  error,
}: {
  repo: string
  onStart: (opts: StartOptions) => void
  starting: boolean
  error: unknown
}) {
  const [scope, setScope] = useState<Scope>('ready')
  const [epicId, setEpicId] = useState('')
  const [maxIter, setMaxIter] = useState(10)
  const [skipResume, setSkipResume] = useState(false)

  const debouncedEpic = useDebounced(epicId.trim().toUpperCase(), 350)
  const epicReady = scope === 'ready' || isTicketId(debouncedEpic)

  return (
    <TerminalCard title="loop-launch" className="max-w-3xl">
      <form
        className="flex flex-col gap-6"
        onSubmit={(e) => {
          e.preventDefault()
          if (!epicReady || starting) return
          onStart({
            epic: scope === 'epic' ? debouncedEpic : undefined,
            max: maxIter,
            noResume: skipResume,
          })
        }}
      >
        <TargetRepoField repo={repo} />

        <div className="flex flex-col gap-1.5">
          <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
            scope
          </span>
          <SegmentedControl
            aria-label="Loop scope"
            options={SCOPE_OPTIONS}
            value={scope}
            onChange={setScope}
          />
        </div>

        {scope === 'epic' ? (
          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-1.5">
              <label
                htmlFor="epic-id"
                className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground"
              >
                epic id
              </label>
              <input
                id="epic-id"
                value={epicId}
                onChange={(e) => setEpicId(e.target.value)}
                placeholder="COD-###"
                className="w-56 rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-muted-foreground/60 focus-visible:border-ring focus-visible:outline-none"
              />
            </div>
            <EpicPreview repo={repo} epic={debouncedEpic} />
          </div>
        ) : (
          <ReadyPreview repo={repo} />
        )}

        <div className="flex flex-col gap-4 border-t border-border pt-4">
          <MaxIterationsStepper value={maxIter} onChange={setMaxIter} />
          <SkipResumeToggle value={skipResume} onChange={setSkipResume} />
        </div>

        <div className="flex flex-col gap-2 border-t border-border pt-4">
          <Button
            type="submit"
            size="sm"
            className="w-fit font-mono"
            disabled={!repo || !epicReady || starting}
          >
            {starting ? 'Starting…' : 'Start loop'}
          </Button>
          {error ? (
            <p className="font-mono text-xs text-destructive">
              {actionError(error)}
            </p>
          ) : null}
        </div>
      </form>
    </TerminalCard>
  )
}

function RunningView({
  repo,
  instance,
  onStop,
  stopping,
  stopError,
}: {
  repo: string
  instance: Instance
  onStop: () => void
  stopping: boolean
  stopError: unknown
}) {
  const now = useNow(1000)
  const eligible = useQuery(eligibleQueryOptions(repo))
  const current = instance.ticket
  const queue = (eligible.data?.tickets ?? []).filter((t) => t.id !== current)

  return (
    <div className="flex flex-col gap-6">
      <TerminalCard title="loop" className="max-w-3xl">
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="inline-flex items-center gap-2 font-mono text-sm text-muted-foreground">
              running in <span className="text-foreground">{repo}</span>
            </span>
            {instance.phase ? (
              <StatusPill state="active" label={phaseLabel(instance.phase)} />
            ) : (
              <StatusPill state="todo" label="idle" />
            )}
          </div>

          {current ? (
            <>
              <div className="flex flex-wrap items-center gap-3">
                <span className="font-mono text-sm text-primary">{current}</span>
                <Link
                  to="/live/$repo/$ticket"
                  params={{ repo, ticket: current }}
                  className="inline-flex items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline"
                >
                  <ExternalLink className="size-3.5" aria-hidden="true" />
                  View run
                </Link>
              </div>
              <div className="rounded-md border border-border bg-secondary/30 px-4 py-3">
                <PhaseStepper steps={runPhaseSteps(instance.phase ?? '', 'live')} />
              </div>
            </>
          ) : (
            <p className="font-sans text-sm text-muted-foreground">
              Idle — picking the next ticket from the queue.
            </p>
          )}

          <div className="flex flex-wrap items-center gap-6 font-mono text-xs text-muted-foreground">
            <span>
              elapsed{' '}
              <span className="text-foreground">
                {elapsedSince(instance.started_at, now)}
              </span>
            </span>
            {instance.state_since ? (
              <span>
                in phase{' '}
                <span className="text-foreground">
                  {elapsedSince(instance.state_since, now)}
                </span>
              </span>
            ) : null}
          </div>
        </div>
      </TerminalCard>

      <div className="flex max-w-3xl flex-col gap-2">
        <Eyebrow glyph="idle">REMAINING QUEUE</Eyebrow>
        <TerminalCard title="queue" bodyClassName="p-0">
          {queue.length === 0 ? (
            <p className="px-4 py-6 text-center font-mono text-xs text-muted-foreground">
              Queue empty — nothing else ready.
            </p>
          ) : (
            <ul className="flex flex-col">
              {queue.map((t) => (
                <li
                  key={t.id}
                  className="flex items-center justify-between gap-4 border-b border-border/60 px-4 py-2.5 last:border-0"
                >
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-sm text-primary">{t.id}</span>
                    <span className="font-sans text-sm text-foreground">
                      {t.title}
                    </span>
                  </span>
                  <StatusPill state="todo" label="todo" />
                </li>
              ))}
            </ul>
          )}
        </TerminalCard>
      </div>

      <div className="flex max-w-3xl flex-col items-end gap-2">
        {stopError ? (
          <p className="font-mono text-xs text-destructive">
            {actionError(stopError)}
          </p>
        ) : null}
        <ConfirmDialog
          windowTitle="confirm"
          trigger={
            <Button
              variant="destructive"
              size="sm"
              className="font-mono"
              disabled={stopping}
            >
              <Square className="size-4" aria-hidden="true" />
              {stopping ? 'Stopping…' : 'Stop loop'}
            </Button>
          }
          title={`Stop the loop on ${repo}?`}
          description="The current ticket finishes its checkpoint, then the loop stops. Work in progress is preserved."
          confirmLabel="Stop loop"
          destructive
          onConfirm={onStop}
        />
      </div>
    </div>
  )
}

function LaunchingCard({ repo }: { repo: string }) {
  return (
    <TerminalCard title="loop" className="max-w-3xl">
      <div className="flex items-center gap-2 font-mono text-sm text-muted-foreground">
        <span aria-hidden="true">○</span>
        <span>
          starting the loop on <span className="text-foreground">{repo}</span>…
        </span>
        <span className="cursor-block text-primary" aria-hidden="true">
          ▍
        </span>
      </div>
    </TerminalCard>
  )
}

interface HaltNotice {
  tone: 'warn' | 'fail'
  glyph: string
  headline: string
  hint: string
}

function haltNotice(halt: LoopHalt): HaltNotice {
  const ticket = halt.ticket || 'the ticket'
  switch (halt.kind) {
    case 'paused':
      return pauseKind(halt.reason) === 'reauth'
        ? {
            tone: 'warn',
            glyph: '⚠',
            headline: 'paused — re-authentication needed',
            hint: 'This is not a failure. Re-login to the provider, then the loop resumes.',
          }
        : {
            tone: 'warn',
            glyph: '⚠',
            headline: 'paused — rate limit reached',
            hint: "This is not a failure. The loop resumes on its own once the provider's usage window clears.",
          }
    case 'budget':
      return {
        tone: 'warn',
        glyph: '⚠',
        headline: 'budget stop',
        hint: `${halt.reason || 'The budget cap was reached'}. The loop stops for the day — raise BUDGET in Settings to keep going.`,
      }
    case 'fault':
      return {
        tone: 'fail',
        glyph: '✗',
        headline: 'fault',
        hint: `${ticket} left the pipeline in an unexpected state. Work in progress is preserved — open the run to intervene.`,
      }
    default:
      return {
        tone: 'fail',
        glyph: '✗',
        headline: 'quarantined',
        hint: `${ticket} needs a human — open the run to see why.`,
      }
  }
}

function HaltBanner({ repo, halt }: { repo: string; halt: LoopHalt }) {
  const notice = haltNotice(halt)
  const border = notice.tone === 'fail' ? 'border-fail/40' : 'border-warn/40'
  const bg = notice.tone === 'fail' ? 'bg-fail/10' : 'bg-warn/10'
  const glyphColor = notice.tone === 'fail' ? 'text-fail' : 'text-warn'
  return (
    <div
      className={cn(
        'flex max-w-3xl items-start gap-2.5 rounded-lg border px-4 py-3',
        border,
        bg,
      )}
    >
      <span
        className={cn('mt-0.5 shrink-0 font-mono text-sm', glyphColor)}
        aria-hidden="true"
      >
        {notice.glyph}
      </span>
      <div className="flex flex-col gap-1">
        <span className={cn('font-mono text-sm', glyphColor)}>
          {notice.headline}
        </span>
        <p className="font-sans text-sm leading-relaxed text-foreground">
          {notice.hint}
        </p>
        {halt.ticket ? (
          <Link
            to="/live/$repo/$ticket"
            params={{ repo, ticket: halt.ticket }}
            className="mt-1 inline-flex w-fit items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline"
          >
            <ExternalLink className="size-3.5" aria-hidden="true" />
            Open {halt.ticket}
          </Link>
        ) : null}
      </div>
    </div>
  )
}

export function Loop() {
  const queryClient = useQueryClient()
  const { repo: activeRepo, repos: allRepos } = useActiveRepo()
  const { data: instData } = useQuery(instancesQueryOptions)

  const startable = allRepos.filter((r) => r.allowed).map((r) => r.name)
  const repo = activeRepo ?? ''
  const canRun = repo !== '' && startable.includes(repo)

  const liveInstance = instData?.instances.find((i) => i.repo === repo)
  const feed = useEventFeed(repo)
  const halt = deriveLoopHalt(feed.events)

  const [justStarted, setJustStarted] = useState(false)
  useEffect(() => {
    if (liveInstance) setJustStarted(false)
  }, [liveInstance])

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['instances'] })
    void queryClient.invalidateQueries({ queryKey: ['repos'] })
  }

  const start = useMutation({
    mutationFn: (opts: StartOptions) =>
      startInstance({
        repo,
        epic: opts.epic,
        max: opts.max,
        no_resume: opts.noResume,
      }),
    onSuccess: () => {
      invalidate()
      setJustStarted(true)
      window.setTimeout(() => setJustStarted(false), 15_000)
    },
  })

  const stop = useMutation({
    mutationFn: () => stopInstance(liveInstance!.pid),
    onSuccess: invalidate,
  })

  useEffect(() => {
    start.reset()
    stop.reset()
    setJustStarted(false)
  }, [repo])

  const running = liveInstance !== undefined || justStarted

  if (running) {
    if (!liveInstance) return <LaunchingCard repo={repo} />
    return (
      <RunningView
        repo={repo}
        instance={liveInstance}
        onStop={() => stop.mutate()}
        stopping={stop.isPending}
        stopError={stop.error}
      />
    )
  }

  if (!canRun)
    return (
      <NotStartableNotice
        repo={repo}
        root={allRepos.find((r) => r.name === repo)?.root}
      />
    )

  return (
    <div className="flex flex-col gap-6">
      {halt ? <HaltBanner repo={repo} halt={halt} /> : null}
      <LaunchForm
        repo={repo}
        onStart={(opts) => start.mutate(opts)}
        starting={start.isPending}
        error={start.error}
      />
    </div>
  )
}

function NotStartableNotice({ repo, root }: { repo: string; root?: string }) {
  return (
    <TerminalCard title="loop" className="max-w-3xl">
      <div className="flex flex-col items-start gap-4">
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          {repo
            ? `${repo} is observe-only — the hub can browse its runs but isn't cleared to start loops here yet.`
            : 'No repo checked out yet. Register a repo to start a loop.'}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          {root && (
            <MakeStartableButton root={root} name={repo} className="font-mono" />
          )}
          <Button asChild variant="outline" size="sm" className="font-mono">
            <Link to="/instances">Manage repos</Link>
          </Button>
        </div>
      </div>
    </TerminalCard>
  )
}
