import { useEffect, useReducer, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  ArrowDown,
  ArrowUp,
  Check,
  ChevronDown,
  ChevronRight,
  ExternalLink,
  ListPlus,
  Plus,
  RefreshCw,
  Search,
  Square,
  TriangleAlert,
  X,
} from 'lucide-react'

import { Button } from '@/components/ui/button'
import { MakeStartableButton } from '@/components/make-startable-button'
import { useActiveRepo } from '@/components/trau/active-repo'
import { AddTicketDialog } from '@/components/trau/add-ticket-dialog'
import { TargetRepoField } from '@/components/trau/target-repo-field'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { Eyebrow } from '@/components/trau/eyebrow'
import { PhaseStepper } from '@/components/trau/phase-stepper'
import { SegmentedControl } from '@/components/trau/segmented-control'
import { StatusPill, type RunState } from '@/components/trau/status-pill'
import { TerminalCard } from '@/components/trau/terminal-card'
import { cn } from '@/lib/utils'
import { addAllLabel, eligibleQueryOptions, planAddAll } from '@/lib/eligible'
import { useEventFeed } from '@/lib/events'
import {
  instancesQueryOptions,
  type Instance,
  type RepoFreshness,
} from '@/lib/instances'
import { deriveLoopHalt, type LoopHalt } from '@/lib/loop'
import { loopTitle, usePageTitle, type LoopTitleState } from '@/lib/page-title'
import {
  dequeue,
  drain,
  enqueue,
  moveQueueItem,
  publishQueue,
  queueExecutable,
  queueQueryOptions,
  skipResumeApplies,
  type OnFault,
  type QueueItem,
  type QueueResponse,
} from '@/lib/queue'
import { pauseKind, runSteps } from '@/lib/runlive'
import { stepName } from '@/lib/steps'
import { runsQueryOptions } from '@/lib/runs'
import {
  buildTimeline,
  builderView,
  finishedReducer,
  finishedView,
  ticketPill,
  FINISHED_INITIAL,
  FINISHED_PAGE_SIZE,
  type PendingEntry,
  type Timeline,
  type TimelineTicket,
} from '@/lib/timeline'

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

function elapsedSince(fromISO: string, now: number): string {
  const s = Math.max(0, Math.floor((now - new Date(fromISO).getTime()) / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (h > 0) return `${h}h ${String(m).padStart(2, '0')}m`
  return `${m}m ${String(sec).padStart(2, '0')}s`
}

// SyncFreshness shows the issue store's synced-ness for the loop card: a spinner
// while a background sync runs, the last-synced age once it lands, or a warning
// when the last sync failed. It stays silent for a repo that has never synced, so
// a repo with no tracker shows nothing rather than a misleading state.
function SyncFreshness({ freshness }: { freshness?: RepoFreshness }) {
  const now = useNow(30_000)
  if (!freshness) return null
  if (freshness.syncing) {
    return (
      <span className="inline-flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
        <RefreshCw className="size-3 animate-spin" aria-hidden="true" />
        syncing…
      </span>
    )
  }
  if (freshness.last_error) {
    return (
      <span
        className="inline-flex items-center gap-1.5 font-mono text-xs text-warn"
        title={freshness.last_error}
      >
        <TriangleAlert className="size-3" aria-hidden="true" />
        sync failed
      </span>
    )
  }
  if (!freshness.last_synced_at) return null
  return (
    <span className="inline-flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
      <Check className="size-3 text-done" aria-hidden="true" />
      synced {elapsedSince(freshness.last_synced_at, now)} ago
    </span>
  )
}

const STATUS_STATE: Record<string, RunState> = {
  pending: 'todo',
  running: 'active',
  paused: 'warn',
  done: 'success',
  failed: 'fail',
  skipped: 'info',
}

function statusState(status: string): RunState {
  return STATUS_STATE[status] ?? 'info'
}

const SUB_GLYPH: Record<
  string,
  { glyph: string; className: string; label: string }
> = {
  done: { glyph: '✓', className: 'text-done', label: 'done' },
  epic: { glyph: '◆', className: 'text-info', label: 'epic' },
  todo: { glyph: '○', className: 'text-faint', label: 'todo' },
}

function subGlyph(state: string) {
  return SUB_GLYPH[state] ?? SUB_GLYPH.todo
}

// InternalTag marks a row the tracker knows nothing about, so a queue mixing both
// reads unambiguously. A synced row stays unmarked — it is the common case.
function InternalTag({ source }: { source?: string }) {
  if (source !== 'internal') return null
  return (
    <span className="shrink-0 rounded-sm border border-border bg-secondary/60 px-1.5 py-0.5 font-mono text-[0.6rem] uppercase tracking-[0.14em] text-muted-foreground">
      internal
    </span>
  )
}

function epicCounts(item: QueueItem): { done: number; total: number } {
  const subs = item.sub_issues ?? []
  return {
    done: subs.filter((s) => s.state === 'done').length,
    total: subs.length,
  }
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
        This queue has prior progress. Start fresh from the top; ignore stored
        checkpoints.
      </p>
    </div>
  )
}

const ON_FAULT_OPTIONS: { value: OnFault; label: string }[] = [
  { value: 'halt', label: 'Halt' },
  { value: 'skip', label: 'Skip & continue' },
]

function OnFaultToggle({
  value,
  onChange,
}: {
  value: OnFault
  onChange: (v: OnFault) => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        on fault
      </span>
      <SegmentedControl
        aria-label="On fault"
        options={ON_FAULT_OPTIONS}
        value={value}
        onChange={onChange}
      />
      <p className="font-sans text-xs leading-relaxed text-muted-foreground">
        {value === 'halt'
          ? 'A fault parks the queue for you to intervene.'
          : 'A fault settles the item failed and the queue drains on. Queue order is not dependency-aware: items that depend on a skipped ticket may fail.'}
      </p>
    </div>
  )
}

function QueueBuilderRow({
  item,
  index,
  count,
  expanded,
  busy,
  onToggle,
  onMove,
  onRemove,
}: {
  item: QueueItem
  index: number
  count: number
  expanded: boolean
  busy: boolean
  onToggle: () => void
  onMove: (dir: -1 | 1) => void
  onRemove: () => void
}) {
  const isEpic = item.kind === 'epic'
  const { done, total } = epicCounts(item)
  const subs = item.sub_issues ?? []

  return (
    <li className="border-b border-border/60 last:border-0">
      <div className="flex items-center gap-3 px-3 py-2.5">
        <span className="w-5 shrink-0 text-right font-mono text-xs text-faint">
          {index + 1}
        </span>

        {isEpic ? (
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={expanded}
            aria-label={expanded ? `Collapse ${item.id}` : `Expand ${item.id}`}
            className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
          >
            {expanded ? (
              <ChevronDown className="size-3.5" aria-hidden="true" />
            ) : (
              <ChevronRight className="size-3.5" aria-hidden="true" />
            )}
          </button>
        ) : (
          <span className="w-3.5 shrink-0" aria-hidden="true" />
        )}

        <span className="shrink-0 font-mono text-sm text-primary">
          {item.id}
        </span>
        <span className="min-w-0 flex-1 truncate font-sans text-sm text-foreground">
          {item.title || '—'}
        </span>
        <InternalTag source={item.source} />

        {isEpic ? (
          <StatusPill state="info" label={`epic · ${done}/${total}`} />
        ) : item.status !== 'pending' ? (
          <StatusPill state={statusState(item.status)} label={item.status} />
        ) : (
          <StatusPill state="todo" label="ticket" />
        )}

        <div className="flex shrink-0 items-center gap-0.5">
          <button
            type="button"
            onClick={() => onMove(-1)}
            disabled={index === 0 || busy}
            aria-label={`Move ${item.id} up`}
            className="flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:pointer-events-none disabled:opacity-30"
          >
            <ArrowUp className="size-3.5" aria-hidden="true" />
          </button>
          <button
            type="button"
            onClick={() => onMove(1)}
            disabled={index === count - 1 || busy}
            aria-label={`Move ${item.id} down`}
            className="flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:pointer-events-none disabled:opacity-30"
          >
            <ArrowDown className="size-3.5" aria-hidden="true" />
          </button>
          <button
            type="button"
            onClick={onRemove}
            disabled={busy}
            aria-label={`Remove ${item.id} from queue`}
            className="flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-fail disabled:pointer-events-none disabled:opacity-30"
          >
            <X className="size-3.5" aria-hidden="true" />
          </button>
        </div>
      </div>

      {isEpic && expanded && subs.length > 0 && (
        <ul className="border-t border-border/60 bg-secondary/20">
          {subs.map((sub) => {
            const styles = subGlyph(sub.state)
            return (
              <li
                key={sub.id}
                className="flex items-center gap-3 border-b border-border/40 py-1.5 pl-14 pr-3 last:border-0"
              >
                <span className="shrink-0 font-mono text-xs text-primary/80">
                  {sub.id}
                </span>
                <span className="min-w-0 flex-1 truncate font-sans text-xs text-muted-foreground">
                  {sub.title}
                </span>
                <span
                  className={cn(
                    'inline-flex shrink-0 items-center gap-1.5 font-mono text-xs',
                    styles.className,
                  )}
                >
                  <span aria-hidden="true">{styles.glyph}</span>
                  {styles.label}
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </li>
  )
}

function LaunchQueueCard({
  repo,
  freshness,
}: {
  repo: string
  freshness?: RepoFreshness
}) {
  const queryClient = useQueryClient()
  const queue = useQuery(queueQueryOptions(repo))
  const eligible = useQuery(eligibleQueryOptions(repo))
  const runs = useQuery(runsQueryOptions(repo))

  const items = queue.data?.items ?? []
  const builder = builderView(items, runs.data?.runs ?? [])
  const addAllPlan = planAddAll(eligible.data?.tickets ?? [], items)
  const skipResumeShown = skipResumeApplies(items, runs.data?.runs ?? [])
  const [draft, setDraft] = useState('')
  const [expandedIds, setExpandedIds] = useState<string[]>([])
  const [browseOpen, setBrowseOpen] = useState(false)
  const [skipResume, setSkipResume] = useState(false)
  const [onFault, setOnFault] = useState<OnFault>('halt')

  const setQueue = (res: QueueResponse) => publishQueue(queryClient, repo, res)

  const add = useMutation({
    mutationFn: (id: string) => enqueue(repo, { id }),
    onSuccess: (res) => {
      setQueue(res)
      setDraft('')
    },
  })

  const addAll = useMutation({
    mutationFn: async () => {
      const errors: string[] = []
      for (const it of addAllPlan.items) {
        try {
          setQueue(await enqueue(repo, { id: it.id, kind: it.kind }))
        } catch (err) {
          errors.push(`${it.id}: ${actionError(err)}`)
        }
      }
      if (errors.length > 0) throw new Error(errors.join('\n'))
    },
  })

  const move = useMutation({
    mutationFn: (vars: { id: string; dir: -1 | 1 }) =>
      moveQueueItem(repo, vars.id, vars.dir),
    onSuccess: setQueue,
  })

  const remove = useMutation({
    mutationFn: (id: string) => dequeue(repo, id),
    onSuccess: setQueue,
  })

  const start = useMutation({
    mutationFn: () =>
      drain(repo, true, {
        no_resume: skipResume && skipResumeShown,
        on_fault: onFault,
      }),
    onSuccess: setQueue,
  })

  const executable = queueExecutable(builder.queue)

  const busy =
    move.isPending || remove.isPending || add.isPending || addAll.isPending

  const submitAdd = () => {
    const id = draft.trim().toUpperCase()
    if (id) add.mutate(id)
  }

  const toggleExpand = (id: string) =>
    setExpandedIds((prev) =>
      prev.includes(id) ? prev.filter((e) => e !== id) : [...prev, id],
    )

  return (
    <div className="flex max-w-3xl flex-col gap-6">
      <TerminalCard title="loop-launch">
        <form
          className="flex flex-col gap-6"
          onSubmit={(e) => e.preventDefault()}
        >
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between gap-3">
              <label className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
                repo
              </label>
              <SyncFreshness freshness={freshness} />
            </div>
            <TargetRepoField repo={repo} />
          </div>

          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-1.5">
              <label
                htmlFor="queue-add"
                className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground"
              >
                queue
              </label>
              <div className="flex flex-wrap items-center gap-2">
                <input
                  id="queue-add"
                  value={draft}
                  onChange={(e) => {
                    setDraft(e.target.value)
                    if (add.error) add.reset()
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                      e.preventDefault()
                      submitAdd()
                    }
                  }}
                  placeholder="COD-### (ticket or epic)"
                  className="w-56 rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-muted-foreground/60 focus-visible:border-ring focus-visible:outline-none"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="font-mono"
                  onClick={submitAdd}
                  disabled={add.isPending || draft.trim() === ''}
                >
                  <Plus className="size-4" aria-hidden="true" />
                  {add.isPending ? 'Adding…' : 'Add'}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="font-mono"
                  onClick={() => setBrowseOpen(true)}
                >
                  <Search className="size-4" aria-hidden="true" />
                  Browse…
                </Button>
                {addAllPlan.items.length > 0 && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="font-mono"
                    onClick={() => addAll.mutate()}
                    disabled={addAll.isPending}
                  >
                    <ListPlus className="size-4" aria-hidden="true" />
                    {addAll.isPending ? 'Adding…' : addAllLabel(addAllPlan)}
                  </Button>
                )}
              </div>
              {add.error ? (
                <p className="font-mono text-xs text-fail" role="alert">
                  {actionError(add.error)}
                </p>
              ) : (
                <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                  Mix tickets and epics. Epics expand into their remaining
                  sub-issues at run time. Runs top to bottom.
                </p>
              )}
              {addAll.error ? (
                <p className="font-mono text-xs text-fail" role="alert">
                  {actionError(addAll.error)}
                </p>
              ) : null}
              {move.error ? (
                <p className="font-mono text-xs text-fail" role="alert">
                  {actionError(move.error)}
                </p>
              ) : null}
              {remove.error ? (
                <p className="font-mono text-xs text-fail" role="alert">
                  {actionError(remove.error)}
                </p>
              ) : null}
            </div>

            {builder.queue.length === 0 ? (
              <div className="rounded-md border border-dashed border-border px-4 py-8 text-center">
                <p className="font-sans text-sm text-muted-foreground">
                  Queue is empty — add a ticket or epic above.
                </p>
              </div>
            ) : (
              <div className="overflow-hidden rounded-md border border-border">
                <ul className="flex flex-col">
                  {builder.queue.map((item, index) => (
                    <QueueBuilderRow
                      key={item.id}
                      item={item}
                      index={index}
                      count={builder.queue.length}
                      expanded={expandedIds.includes(item.id)}
                      busy={busy}
                      onToggle={() => toggleExpand(item.id)}
                      onMove={(dir) => move.mutate({ id: item.id, dir })}
                      onRemove={() => remove.mutate(item.id)}
                    />
                  ))}
                </ul>
                <div className="border-t border-border bg-secondary/40 px-3 py-2 font-mono text-xs text-muted-foreground">
                  {builder.queue.length} queued · {executable} executable{' '}
                  {executable === 1 ? 'ticket' : 'tickets'} · runs top to bottom
                </div>
              </div>
            )}
          </div>

          <div className="flex flex-col gap-4 border-t border-border pt-4">
            <OnFaultToggle value={onFault} onChange={setOnFault} />
            {skipResumeShown ? (
              <SkipResumeToggle value={skipResume} onChange={setSkipResume} />
            ) : null}
          </div>

          <div className="flex flex-col gap-2 border-t border-border pt-4">
            <Button
              type="button"
              size="sm"
              className="w-fit font-mono"
              onClick={() => start.mutate()}
              disabled={executable === 0 || start.isPending}
            >
              {start.isPending ? 'Starting…' : 'Start queue'}
            </Button>
            {start.error ? (
              <p className="font-mono text-xs text-destructive">
                {actionError(start.error)}
              </p>
            ) : null}
          </div>
        </form>

        <AddTicketDialog
          repo={repo}
          queued={items}
          open={browseOpen}
          onOpenChange={setBrowseOpen}
          onQueue={setQueue}
        />
      </TerminalCard>

      {builder.settled.length > 0 ? (
        <FinishedSection repo={repo} settled={builder.settled} />
      ) : null}
    </div>
  )
}

function EpicTag({ id }: { id: string }) {
  return (
    <span className="inline-flex shrink-0 items-center gap-1 font-mono text-[0.7rem] text-info">
      <span aria-hidden="true">◆</span>
      {id}
    </span>
  )
}

function TicketReason({ children }: { children: string }) {
  return (
    <p className="text-pretty font-mono text-[0.7rem] leading-relaxed text-muted-foreground">
      {children}
    </p>
  )
}

function SettledRow({
  repo,
  ticket,
}: {
  repo: string
  ticket: TimelineTicket
}) {
  const pill = ticketPill(ticket)
  const head = (
    <div className="flex items-center gap-3">
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
        {ticket.epicId ? <EpicTag id={ticket.epicId} /> : null}
        <span className="font-mono text-sm text-primary">{ticket.id}</span>
        {ticket.title ? (
          <span className="min-w-0 truncate font-sans text-sm text-foreground">
            {ticket.title}
          </span>
        ) : null}
        <InternalTag source={ticket.source} />
      </div>
      <StatusPill state={pill.state} label={pill.label} className="shrink-0" />
    </div>
  )
  const reason = ticket.reason ? (
    <TicketReason>{ticket.reason}</TicketReason>
  ) : null

  if (ticket.hasRun) {
    return (
      <li className="border-b border-border/60 last:border-0">
        <Link
          to="/runs/$repo/$ticket"
          params={{ repo, ticket: ticket.id }}
          className="flex flex-col gap-1.5 px-4 py-2.5 transition-colors hover:bg-secondary/40"
        >
          {head}
          {reason}
        </Link>
      </li>
    )
  }
  return (
    <li className="flex flex-col gap-1.5 border-b border-border/60 px-4 py-2.5 last:border-0">
      {head}
      {reason}
    </li>
  )
}

function FinishedSection({
  repo,
  settled,
}: {
  repo: string
  settled: TimelineTicket[]
}) {
  const [state, dispatch] = useReducer(finishedReducer, FINISHED_INITIAL)
  const view = finishedView(settled, state.visible)

  return (
    <section className="flex flex-col gap-2">
      <Eyebrow glyph="done">FINISHED</Eyebrow>
      <div className="overflow-hidden rounded-md border border-border">
        <button
          type="button"
          onClick={() => dispatch({ type: 'toggle' })}
          aria-expanded={state.expanded}
          className={cn(
            'flex w-full items-center justify-between gap-4 px-4 py-2.5 text-left transition-colors hover:bg-secondary/40',
            state.expanded && 'border-b border-border',
          )}
        >
          <span className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
            {state.expanded ? (
              <ChevronDown
                className="size-4 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
            ) : (
              <ChevronRight
                className="size-4 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
            )}
            <span className="font-mono text-sm text-foreground">
              {view.total} finished
            </span>
            <span className="font-mono text-xs text-muted-foreground">
              <span className="text-done" aria-hidden="true">
                ✓
              </span>{' '}
              {view.tally.map((t) => `${t.count} ${t.label}`).join(' · ')}
            </span>
          </span>
          {!state.expanded && view.latest ? (
            <span className="hidden shrink-0 items-center gap-2 font-mono text-xs text-muted-foreground sm:inline-flex">
              latest <span className="text-primary">{view.latest.id}</span>
            </span>
          ) : null}
        </button>

        {state.expanded ? (
          <>
            <ul className="flex flex-col">
              {view.rows.map((ticket) => (
                <SettledRow key={ticket.id} repo={repo} ticket={ticket} />
              ))}
            </ul>
            {view.older > 0 ? (
              <div className="border-t border-border px-4 py-2.5">
                <button
                  type="button"
                  onClick={() => dispatch({ type: 'more' })}
                  className="font-mono text-xs text-teal underline-offset-4 hover:underline"
                >
                  Show {Math.min(view.older, FINISHED_PAGE_SIZE)} more (
                  {view.older} older)
                </button>
              </div>
            ) : null}
          </>
        ) : null}
      </div>
    </section>
  )
}

function RunningRow({
  repo,
  ticket,
  instance,
  now,
}: {
  repo: string
  ticket: TimelineTicket
  instance?: Instance
  now: number
}) {
  const live = instance?.ticket === ticket.id ? instance : undefined
  const phase = live?.phase ?? ticket.phase

  return (
    <div className="flex flex-col gap-3 rounded-md border border-teal/40 bg-teal/5 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        {ticket.epicId ? <EpicTag id={ticket.epicId} /> : null}
        <span className="font-mono text-sm text-primary">{ticket.id}</span>
        {ticket.title ? (
          <span className="font-sans text-base text-foreground">
            {ticket.title}
          </span>
        ) : null}
        <Link
          to="/live/$repo/$ticket"
          params={{ repo, ticket: ticket.id }}
          className="inline-flex items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline"
        >
          <ExternalLink className="size-3.5" aria-hidden="true" />
          View run
        </Link>
      </div>
      {phase || live?.activity ? (
        <PhaseStepper
          {...runSteps('live', phase ?? '', live?.activity, live?.detail)}
        />
      ) : (
        <p className="font-sans text-sm text-muted-foreground">
          Picking the next ticket…
        </p>
      )}
      {live ? (
        <div className="flex flex-wrap items-center gap-6 font-mono text-xs text-muted-foreground">
          <span>
            elapsed{' '}
            <span className="text-foreground">
              {elapsedSince(live.started_at, now)}
            </span>
          </span>
          {live.state_since ? (
            <span>
              in phase{' '}
              <span className="text-foreground">
                {elapsedSince(live.state_since, now)}
              </span>
            </span>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

function PendingTicketRow({ ticket }: { ticket: TimelineTicket }) {
  return (
    <li className="flex items-center gap-3 border-b border-border/60 px-4 py-2.5 last:border-0">
      {ticket.epicId ? <EpicTag id={ticket.epicId} /> : null}
      <span className="font-mono text-sm text-primary">{ticket.id}</span>
      <span className="min-w-0 flex-1 truncate font-sans text-sm text-muted-foreground">
        {ticket.title || '—'}
      </span>
      <InternalTag source={ticket.source} />
      <StatusPill state="todo" label="pending" className="shrink-0" />
    </li>
  )
}

function PendingEpicGroup({
  entry,
}: {
  entry: Extract<PendingEntry, { kind: 'epic' }>
}) {
  return (
    <li className="border-b border-border/60 last:border-0">
      <div className="flex items-center gap-3 px-4 py-2.5">
        <span className="inline-flex shrink-0 items-center gap-1 font-mono text-sm text-info">
          <span aria-hidden="true">◆</span>
          {entry.id}
        </span>
        <span className="min-w-0 flex-1 truncate font-sans text-sm text-foreground">
          {entry.title || '—'}
        </span>
        <InternalTag source={entry.source} />
        <StatusPill
          state="info"
          label={`epic · ${entry.done}/${entry.total}`}
          className="shrink-0"
        />
      </div>
      <ul className="border-t border-border/60 bg-secondary/20">
        {entry.children.map((child) => (
          <li
            key={child.id}
            className="flex items-center gap-3 border-b border-border/40 py-1.5 pl-12 pr-4 last:border-0"
          >
            <span className="font-mono text-xs text-primary/80">
              {child.id}
            </span>
            <span className="min-w-0 flex-1 truncate font-sans text-xs text-muted-foreground">
              {child.title || '—'}
            </span>
            <StatusPill state="todo" label="pending" className="shrink-0" />
          </li>
        ))}
      </ul>
    </li>
  )
}

function RunningQueueView({
  repo,
  queue,
  instance,
  halt,
  onStop,
  stopping,
  stopError,
}: {
  repo: string
  queue: QueueResponse
  instance?: Instance
  halt: LoopHalt | null
  onStop: () => void
  stopping: boolean
  stopError: unknown
}) {
  const now = useNow(1000)
  const queryClient = useQueryClient()
  const runs = useQuery(runsQueryOptions(repo))
  const timeline = buildTimeline(queue.items, runs.data?.runs ?? [], instance)
  const [addOpen, setAddOpen] = useState(false)

  return (
    <div className="flex flex-col gap-6">
      {halt ? <HaltBanner repo={repo} halt={halt} /> : null}

      <TerminalCard title="loop" className="max-w-3xl">
        <div className="flex flex-col gap-6">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="font-mono text-sm text-muted-foreground">
              <span className="text-foreground">
                {timeline.done}/{timeline.total}
              </span>{' '}
              tickets done
            </span>
            <div className="flex items-center gap-4">
              {timeline.elapsedAnchor ? (
                <span className="font-mono text-xs text-muted-foreground">
                  elapsed{' '}
                  <span className="text-foreground">
                    {elapsedSince(timeline.elapsedAnchor, now)}
                  </span>
                </span>
              ) : null}
              <StatusPill
                state="active"
                label={
                  timeline.running
                    ? stepName(
                        timeline.running.activity,
                        timeline.running.phase ?? '',
                      ).toLowerCase() || 'draining'
                    : 'draining'
                }
              />
            </div>
          </div>

          <section className="flex flex-col gap-2">
            <Eyebrow glyph="active">RUNNING</Eyebrow>
            {timeline.running ? (
              <RunningRow
                repo={repo}
                ticket={timeline.running}
                instance={instance}
                now={now}
              />
            ) : (
              <p className="font-sans text-sm text-muted-foreground">
                Idle — picking the next ticket from the queue.
              </p>
            )}
          </section>

          <section className="flex flex-col gap-2">
            <div className="flex items-center justify-between gap-3">
              <Eyebrow glyph="idle">REMAINING</Eyebrow>
              <button
                type="button"
                onClick={() => setAddOpen(true)}
                className="inline-flex items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline"
              >
                <Plus className="size-3.5" aria-hidden="true" />
                Add ticket
              </button>
            </div>
            {timeline.pending.length > 0 ? (
              <>
                <div className="overflow-hidden rounded-md border border-border">
                  <ul className="flex flex-col">
                    {timeline.pending.map((entry) =>
                      entry.kind === 'epic' ? (
                        <PendingEpicGroup key={entry.id} entry={entry} />
                      ) : (
                        <PendingTicketRow
                          key={entry.ticket.id}
                          ticket={entry.ticket}
                        />
                      ),
                    )}
                  </ul>
                </div>
                <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                  Remaining tickets — the pick order is decided at run time, not
                  promised here.
                </p>
              </>
            ) : (
              <div className="rounded-md border border-dashed border-border px-4 py-6 text-center">
                <p className="font-sans text-sm text-muted-foreground">
                  Nothing left in the queue — add a ticket and the drain picks
                  it up.
                </p>
              </div>
            )}
          </section>

          {timeline.settled.length > 0 ? (
            <FinishedSection repo={repo} settled={timeline.settled} />
          ) : null}
        </div>
      </TerminalCard>

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
              {stopping ? 'Stopping…' : 'Stop queue'}
            </Button>
          }
          title={`Stop the queue on ${repo}?`}
          description="The current ticket finishes its checkpoint, then the queue stops. Work in progress is preserved — Start again to resume where it left off."
          confirmLabel="Stop queue"
          destructive
          onConfirm={onStop}
        />
      </div>

      <AddTicketDialog
        repo={repo}
        queued={queue.items}
        open={addOpen}
        onOpenChange={setAddOpen}
        onQueue={(res) => publishQueue(queryClient, repo, res)}
      />
    </div>
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
            hint: 'This is not a failure. Re-login to the provider, then the queue resumes.',
          }
        : {
            tone: 'warn',
            glyph: '⚠',
            headline: 'paused — rate limit reached',
            hint: "This is not a failure. The queue resumes on its own once the provider's usage window clears.",
          }
    case 'budget':
      return {
        tone: 'warn',
        glyph: '⚠',
        headline: 'budget stop',
        hint: `${halt.reason || 'The budget cap was reached'}. The queue stops for the day — raise BUDGET in Settings to keep going.`,
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

// loopTitleState reads the loop's tab-title state from the same signals the card
// renders: the halt banner, the draining header's done/total and step pill, or a
// clean drain. It never re-derives a state the page does not already show.
function loopTitleState(
  canRun: boolean,
  halt: LoopHalt | null,
  draining: boolean,
  timeline: Timeline | null,
): LoopTitleState {
  if (!canRun) return { kind: 'idle' }
  if (halt) return { kind: 'halted', halt: halt.kind, ticket: halt.ticket }
  if (draining && timeline) {
    const running = timeline.running
    const step = running
      ? stepName(running.activity, running.phase ?? '').toLowerCase() ||
        'draining'
      : 'draining'
    return {
      kind: 'draining',
      done: timeline.done,
      total: timeline.total,
      ticket: running?.id ?? '',
      step,
    }
  }
  if (timeline && timeline.total > 0 && timeline.done === timeline.total) {
    return { kind: 'done', total: timeline.total }
  }
  return { kind: 'idle' }
}

export function Loop() {
  const queryClient = useQueryClient()
  const { repo: activeRepo, repos } = useActiveRepo()
  const repo = activeRepo ?? ''

  const startable = repos.filter((r) => r.allowed).map((r) => r.name)
  const canRun = repo !== '' && startable.includes(repo)

  const queue = useQuery({
    ...queueQueryOptions(repo),
    refetchInterval: (q) => (q.state.data?.draining ? 3000 : false),
  })
  const { data: instData } = useQuery(instancesQueryOptions)
  const liveInstance = instData?.instances.find((i) => i.repo === repo)
  const feed = useEventFeed(repo)
  const halt = deriveLoopHalt(feed.events)
  const runs = useQuery(runsQueryOptions(repo))

  const draining = queue.data?.draining ?? false
  const timeline = queue.data
    ? buildTimeline(queue.data.items, runs.data?.runs ?? [], liveInstance)
    : null
  usePageTitle(loopTitle(loopTitleState(canRun, halt, draining, timeline)))

  const stop = useMutation({
    mutationFn: () => drain(repo, false),
    onSuccess: (res) => publishQueue(queryClient, repo, res),
  })

  useEffect(() => {
    stop.reset()
  }, [repo])

  if (!canRun) {
    return (
      <NotStartableNotice
        repo={repo}
        root={repos.find((r) => r.name === repo)?.root}
      />
    )
  }

  if (draining && queue.data) {
    return (
      <RunningQueueView
        repo={repo}
        queue={queue.data}
        instance={liveInstance}
        halt={halt}
        onStop={() => stop.mutate()}
        stopping={stop.isPending}
        stopError={stop.error}
      />
    )
  }

  return (
    <div className="flex flex-col gap-6">
      {halt ? <HaltBanner repo={repo} halt={halt} /> : null}
      <LaunchQueueCard
        repo={repo}
        freshness={repos.find((r) => r.name === repo)?.freshness}
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
            <MakeStartableButton
              root={root}
              name={repo}
              className="font-mono"
            />
          )}
          <Button asChild variant="outline" size="sm" className="font-mono">
            <Link to="/instances">Manage repos</Link>
          </Button>
        </div>
      </div>
    </TerminalCard>
  )
}
