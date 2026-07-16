import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { parseAsString, useQueryState } from 'nuqs'
import {
  Flame,
  Loader2,
  PanelRightClose,
  PanelRightOpen,
  SkipForward,
  Sparkles,
} from 'lucide-react'

import { AuthoringDrawer } from '@/components/grill-panel'
import { Markdown } from '@/components/markdown'
import { ErrorNote } from '@/components/grill/banners'
import {
  GrillConversation,
  type GrillStatus,
} from '@/components/grill/conversation'
import { useGrillSession } from '@/components/grill/session'
import { Button } from '@/components/ui/button'
import {
  AssigneeAvatar,
  PageHeader,
  ProjectScopeGate,
  RepoHealthGate,
  StatusPill,
  useActiveRepo,
} from '@/components/trau'
import {
  GRILLABLE_LABELS,
  latestOutcome,
  outcomePayload,
  pregrillIssues,
  type OutcomePayload,
} from '@/lib/grill'
import {
  contextRows,
  loadContextOpen,
  storeContextOpen,
} from '@/lib/inbox-context'
import { issueQueryOptions } from '@/lib/issues'
import {
  inboxPill,
  inboxPosition,
  nextIssueId,
  prevIssueId,
  skipTarget,
  summarisePregrill,
  useInboxQueue,
  type InboxGroup,
  type InboxGroupView,
  type InboxItem,
} from '@/lib/inbox'
import { hasOpenLayer, inboxKeyAction } from '@/lib/inbox-keys'
import {
  hasUnseenQuestion,
  loadSeen,
  markSeen,
  storeSeen,
  type SeenMarks,
} from '@/lib/inbox-seen'
import { standardTitle, usePageTitle } from '@/lib/page-title'
import { cn } from '@/lib/utils'

export const Route = createFileRoute('/inbox')({
  component: InboxPage,
})

const GROUP_COUNT_TONE: Record<InboxGroup, string> = {
  waiting: 'text-warn',
  review: 'text-teal',
  done: 'text-done',
}

function InboxPage() {
  usePageTitle(standardTitle('Inbox'))
  const { repo: activeRepo } = useActiveRepo()
  const repo = activeRepo ?? ''
  const queryClient = useQueryClient()
  const { items, groups, isLoading, error } = useInboxQueue(repo)

  const [peek, setPeek] = useQueryState(
    'issue',
    parseAsString.withOptions({ history: 'push' }),
  )
  // The queue owns the selection: an ?issue= naming something that has left it — or
  // was never in it — falls back to the head rather than opening a session on a
  // stray id.
  const selected = items.find((item) => item.id === peek) ?? items[0] ?? null

  const [contextOpen, setContextOpen] = useState(loadContextOpen)
  const [authoring, setAuthoring] = useState(false)
  const [passSummary, setPassSummary] = useState<string | null>(null)
  const [status, setStatus] = useState<GrillStatus | null>(null)
  const [seen, setSeen] = useState<SeenMarks>(loadSeen)

  // The thread reports the session it is following, but the panel beside it must not
  // read the outgoing issue's status while a freshly selected one is still mounting.
  const live = status?.session.issue_id === selected?.id ? status : null

  // Whatever is on screen has been read, and the thread's session is the one being
  // read — it is followed live, where the rail's list trails a staleTime behind.
  const onScreen = live?.session ?? selected?.session

  useEffect(() => {
    if (!onScreen) return
    setSeen((marks) => markSeen(marks, onScreen.id, onScreen.updated_at))
  }, [onScreen?.id, onScreen?.updated_at])

  useEffect(() => {
    storeSeen(seen)
  }, [seen])

  function toggleContext() {
    const next = !contextOpen
    setContextOpen(next)
    storeContextOpen(next)
  }

  const untouchedIds = items
    .filter((item) => item.attention === 'open')
    .map((item) => item.id)

  const pregrillAll = useMutation({
    mutationFn: () => pregrillIssues(repo, untouchedIds),
    onSuccess: (res) => setPassSummary(summarisePregrill(res)),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: ['grill', repo] }),
  })

  // Skipping parks nothing: an untouched session settles server-side on idle, so the
  // item keeps its place in the queue and comes round again.
  function skip() {
    const next = skipTarget(items, selected?.id ?? null)
    if (next !== null && next !== selected?.id) void setPeek(next)
  }

  // The workspace owns j/k/s while it is on screen. The listener sits on the document
  // because the queue has nothing focused to hang a handler on — the chat does, and
  // the composer's Enter stays its own.
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      const action = inboxKeyAction({
        key: e.key,
        ctrlKey: e.ctrlKey,
        metaKey: e.metaKey,
        altKey: e.altKey,
        isComposing: e.isComposing,
        targetTag: (e.target as HTMLElement | null)?.tagName,
        layerOpen: hasOpenLayer(document),
      })
      if (action === null) return
      if (action === 'skip') {
        skip()
        return
      }
      if (!selected) return
      const to =
        action === 'next'
          ? nextIssueId(items, selected.id)
          : prevIssueId(items, selected.id)
      if (to !== null) void setPeek(to)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [items, selected])

  // An applied outcome drops the issue's triage labels on the tracker, so refreshing
  // the board is what retires the row; the applied list is what re-lists it under
  // Done today. The unfiltered grill list is deliberately left alone — refetching it
  // would read the settled session as "no session" and restart the grilling.
  function onApplied() {
    skip()
    void queryClient.invalidateQueries({ queryKey: ['backlog', repo] })
    void queryClient.invalidateQueries({ queryKey: ['grill', repo, 'applied'] })
  }

  return (
    <ProjectScopeGate className="min-h-0 flex-1" action="triage unclear issues">
      {/* Both gates already establish a positioned wrapper, so filling it is how this
          page claims the height the root column left it — the recap banner above is
          conditional, and a fixed viewport offset would be wrong whenever it shows. */}
      <div className="absolute inset-0 flex flex-col">
        <PageHeader
          className="shrink-0"
          eyebrow={repo || 'inbox'}
          title="Triage inbox"
          description="Work through unclear issues in one sitting — the ones with a question waiting on you come first."
          actions={
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => setAuthoring(true)}
                className="inline-flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-sm text-foreground transition-colors hover:bg-muted"
              >
                <Sparkles className="size-4" />
                New issue (grilled)
              </button>
              {untouchedIds.length > 0 && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => pregrillAll.mutate()}
                  disabled={pregrillAll.isPending}
                >
                  <Flame />
                  {pregrillAll.isPending
                    ? 'Pre-grilling…'
                    : `Pre-grill all (${untouchedIds.length})`}
                </Button>
              )}
            </div>
          }
        />

        {(passSummary || pregrillAll.error) && (
          <p
            className={cn(
              'shrink-0 border-b border-border px-8 py-2 text-sm',
              pregrillAll.error ? 'text-destructive' : 'text-muted-foreground',
            )}
          >
            {pregrillAll.error ? pregrillAll.error.message : passSummary}
          </p>
        )}

        <RepoHealthGate className="min-h-0 flex-1">
          <div
            className={cn(
              'absolute inset-0 flex flex-col px-8 pb-4 md:grid md:grid-cols-[260px_minmax(0,1fr)]',
              contextOpen && 'xl:[grid-template-columns:260px_minmax(0,1fr)_340px]',
            )}
          >
            <QueueSelect
              groups={groups}
              selectedId={selected?.id ?? null}
              onSelect={(id) => void setPeek(id)}
            />
            <QueueRail
              repo={repo}
              groups={groups}
              seen={seen}
              selectedId={selected?.id ?? null}
              onSelect={(id) => void setPeek(id)}
            />

            <section
              aria-label="Grilling session"
              className="flex min-h-0 min-w-0 flex-col"
            >
              {selected ? (
                <SessionColumn
                  key={selected.id}
                  repo={repo}
                  item={selected}
                  position={inboxPosition(items, selected.id)}
                  total={items.length}
                  status={live}
                  onStatus={setStatus}
                  contextOpen={contextOpen}
                  onToggleContext={toggleContext}
                  onSkip={skip}
                  onApplied={onApplied}
                />
              ) : (
                <div className="flex min-h-0 flex-1 items-center justify-center p-8">
                  {error ? (
                    <ErrorNote message={error.message} />
                  ) : isLoading ? (
                    <p className="inline-flex items-center gap-2 text-sm text-muted-foreground">
                      <Loader2 className="size-4 animate-spin" />
                      Loading inbox…
                    </p>
                  ) : (
                    <EmptyInbox />
                  )}
                </div>
              )}
            </section>

            {selected && contextOpen && (
              <ContextColumn repo={repo} item={selected} status={live} />
            )}
          </div>
        </RepoHealthGate>
      </div>

      <AuthoringDrawer
        repo={repo}
        open={authoring}
        onOpenChange={setAuthoring}
        onCreated={() => {
          void queryClient.invalidateQueries({ queryKey: ['backlog', repo] })
          void queryClient.invalidateQueries({ queryKey: ['grill', repo] })
        }}
      />
    </ProjectScopeGate>
  )
}

// SessionColumn is the chat zone: the session bar over the issue's conversation,
// opening a session for an issue that has none. Hosts key it on the issue so the
// auto-start and the thread reset together.
function SessionColumn({
  repo,
  item,
  position,
  total,
  status,
  onStatus,
  contextOpen,
  onToggleContext,
  onSkip,
  onApplied,
}: {
  repo: string
  item: InboxItem
  position: number
  total: number
  status: GrillStatus | null
  onStatus: (status: GrillStatus) => void
  contextOpen: boolean
  onToggleContext: () => void
  onSkip: () => void
  onApplied: () => void
}) {
  const { session, starting, error, retry } = useGrillSession(repo, item.id)

  // The stream's session outranks the list's: it is the one the thread is following.
  const live = status?.session ?? session

  return (
    <>
      <SessionBar
        item={item}
        position={position}
        total={total}
        pill={live ? inboxPill(live.state) : null}
        reconnecting={status?.stream === 'error'}
        contextOpen={contextOpen}
        onToggleContext={onToggleContext}
        onSkip={onSkip}
      />

      {/* The overlay frame floats over the thread, not the bar above it: the bar
          carries the toggle that dismisses it again. */}
      <div className="relative flex min-h-0 flex-1 flex-col">
        {session ? (
          <GrillConversation
            key={session.id}
            repo={repo}
            initial={session}
            onStatus={onStatus}
            onApplied={onApplied}
            onDiscarded={onSkip}
          />
        ) : (
          <div className="flex min-h-0 flex-1 items-center justify-center px-4">
            {error ? (
              <div className="flex flex-col items-center gap-3">
                <ErrorNote message={error.message} />
                {retry && (
                  <Button size="sm" variant="outline" onClick={retry}>
                    Try again
                  </Button>
                )}
              </div>
            ) : (
              <p className="inline-flex items-center gap-2 text-sm text-muted-foreground">
                <Loader2 className="size-4 animate-spin" />
                {starting ? 'Starting grilling session…' : 'Loading…'}
              </p>
            )}
          </div>
        )}

        {contextOpen && (
          <ContextOverlay repo={repo} item={item} status={status} />
        )}
      </div>
    </>
  )
}

function SessionBar({
  item,
  position,
  total,
  pill,
  reconnecting,
  contextOpen,
  onToggleContext,
  onSkip,
}: {
  item: InboxItem
  position: number
  total: number
  pill: { tone: 'warn' | 'active' | 'verify' | 'success' | 'todo'; label: string } | null
  reconnecting: boolean
  contextOpen: boolean
  onToggleContext: () => void
  onSkip: () => void
}) {
  return (
    <div className="flex shrink-0 items-center justify-between gap-3 border-b border-border py-3 pl-5 pr-1">
      <div className="flex min-w-0 items-center gap-3">
        <span className="shrink-0 font-mono text-xs tabular-nums text-muted-foreground">
          {position + 1} of {total}
        </span>
        <span className="truncate text-sm font-medium text-foreground">
          <span className="font-mono text-muted-foreground">{item.id}</span>{' '}
          {item.title}
        </span>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {reconnecting && (
          <span className="inline-flex items-center gap-1 text-xs text-warn">
            <span aria-hidden="true">⚠</span>
            reconnecting…
          </span>
        )}
        {pill && <StatusPill state={pill.tone} label={pill.label} />}
        <Button
          variant="outline"
          size="sm"
          onClick={onSkip}
          aria-label="Skip to next issue"
        >
          <SkipForward />
          Skip
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className="size-8"
          onClick={onToggleContext}
          aria-pressed={contextOpen}
          title={contextOpen ? 'Hide issue context' : 'Show issue context'}
        >
          {contextOpen ? <PanelRightClose /> : <PanelRightOpen />}
          <span className="sr-only">
            {contextOpen ? 'Hide issue context' : 'Show issue context'}
          </span>
        </Button>
      </div>
    </div>
  )
}

function QueueRail({
  repo,
  groups,
  seen,
  selectedId,
  onSelect,
}: {
  repo: string
  groups: InboxGroupView[]
  seen: SeenMarks
  selectedId: string | null
  onSelect: (id: string) => void
}) {
  return (
    <nav
      aria-label="Triage queue"
      className="hidden min-h-0 flex-col gap-5 overflow-y-auto border-r border-border py-4 pr-3 md:flex"
    >
      {groups.map((group) => (
        <div key={group.group} className="flex flex-col gap-1.5">
          <div className="flex items-center justify-between px-2.5">
            <SectionLabel>{group.label}</SectionLabel>
            <span
              className={cn(
                'font-mono text-[0.65rem] tabular-nums',
                GROUP_COUNT_TONE[group.group],
              )}
            >
              {group.items.length}
            </span>
          </div>
          <ul className="flex flex-col gap-0.5">
            {group.items.map((item) =>
              item.attention === 'done' ? (
                <DoneRow key={item.id} item={item} />
              ) : (
                <QueueRow
                  key={item.id}
                  repo={repo}
                  item={item}
                  unread={hasUnseenQuestion(seen, item)}
                  selected={selectedId === item.id}
                  onSelect={() => onSelect(item.id)}
                />
              ),
            )}
            {group.items.length === 0 && (
              <li className="px-2.5 py-1 font-mono text-xs text-faint">none</li>
            )}
          </ul>
        </div>
      ))}

      <div className="mt-auto flex flex-col gap-1 px-2.5 pt-4">
        <SectionLabel>Keys</SectionLabel>
        <p className="font-mono text-[0.65rem] leading-relaxed text-faint">
          j / k — next / prev · s — skip · enter — send
        </p>
      </div>
    </nav>
  )
}

function QueueRow({
  repo,
  item,
  unread,
  selected,
  onSelect,
}: {
  repo: string
  item: InboxItem
  unread: boolean
  selected: boolean
  onSelect: () => void
}) {
  return (
    <li className="group/row relative">
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? 'true' : undefined}
        aria-label={`Open ${item.id}`}
        className={cn(
          'flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left transition-colors',
          selected ? 'bg-primary/10' : 'hover:bg-secondary',
          item.attention === 'open' && 'pr-9',
        )}
      >
        {selected && (
          <span
            aria-hidden="true"
            className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-primary"
          />
        )}
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex items-center gap-2">
            <span
              className={cn(
                'font-mono text-xs',
                selected ? 'text-primary' : 'text-muted-foreground',
              )}
            >
              {item.id}
            </span>
            {unread && (
              <span
                className="size-1.5 rounded-full bg-warn"
                aria-hidden="true"
                title="A question you haven't read yet"
              />
            )}
          </span>
          <span
            className={cn(
              'line-clamp-2 text-xs leading-relaxed',
              selected ? 'text-foreground' : 'text-muted-foreground',
            )}
          >
            {item.title}
          </span>
        </span>
        {item.assignee && (
          <AssigneeAvatar
            assignee={item.assignee}
            className="size-5 self-center text-[0.55rem]"
          />
        )}
      </button>
      {item.attention === 'open' && (
        <PregrillButton
          repo={repo}
          issueId={item.id}
          className="absolute right-1 top-1 opacity-0 focus-visible:opacity-100 group-hover/row:opacity-100"
        />
      )}
    </li>
  )
}

function DoneRow({ item }: { item: InboxItem }) {
  return (
    <li className="flex flex-col gap-0.5 rounded-md px-2.5 py-2 opacity-60">
      <span className="inline-flex items-center gap-2 font-mono text-xs text-done">
        <span aria-hidden="true">✓</span>
        {item.id}
      </span>
      <span className="line-clamp-1 text-xs leading-relaxed text-muted-foreground">
        {item.title}
      </span>
    </li>
  )
}

// QueueSelect is the rail's fallback under md, where 260px of chrome would crowd out
// the chat. Only the two working groups are offered — Done today is a record, not a
// destination.
function QueueSelect({
  groups,
  selectedId,
  onSelect,
}: {
  groups: InboxGroupView[]
  selectedId: string | null
  onSelect: (id: string) => void
}) {
  return (
    <label className="flex shrink-0 flex-col gap-1 py-3 md:hidden">
      <span className="sr-only">Triage queue</span>
      <select
        value={selectedId ?? ''}
        onChange={(e) => onSelect(e.target.value)}
        className="h-9 w-full rounded-md border bg-transparent px-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        {groups
          .filter((group) => group.group !== 'done')
          .map((group) => (
            <optgroup key={group.group} label={`${group.label} (${group.items.length})`}>
              {group.items.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.id} — {item.title}
                </option>
              ))}
            </optgroup>
          ))}
      </select>
    </label>
  )
}

// The context frames are what the triager keeps an eye on while grilling: the issue
// as it stands today, and the proposal building up to replace it. Only one is ever
// on screen — the workspace's third column where there is room for it, an overlay
// over the chat where there is not.
function ContextColumn({
  repo,
  item,
  status,
}: {
  repo: string
  item: InboxItem
  status: GrillStatus | null
}) {
  return (
    <aside
      aria-label="Issue context"
      className="hidden min-h-0 flex-col gap-5 overflow-y-auto border-l border-border py-4 pl-4 xl:flex"
    >
      <ContextBody repo={repo} item={item} status={status} />
    </aside>
  )
}

function ContextOverlay({
  repo,
  item,
  status,
}: {
  repo: string
  item: InboxItem
  status: GrillStatus | null
}) {
  return (
    <aside
      aria-label="Issue context"
      className="absolute inset-y-0 right-0 z-10 flex w-[340px] max-w-full flex-col gap-5 overflow-y-auto rounded-lg border border-border bg-card p-4 shadow-lg xl:hidden"
    >
      <ContextBody repo={repo} item={item} status={status} />
    </aside>
  )
}

function ContextBody({
  repo,
  item,
  status,
}: {
  repo: string
  item: InboxItem
  status: GrillStatus | null
}) {
  const issue = useQuery(issueQueryOptions(repo, item.id))
  const labels = (item.entry?.labels ?? []).filter((l) =>
    GRILLABLE_LABELS.includes(l),
  )
  const messages = status?.messages ?? []
  const outcome =
    status?.session.state === 'finished' ? latestOutcome(messages) : null
  const rows = contextRows({
    created: issue.data?.created_at,
    source: item.entry?.source,
    messages,
    now: new Date(),
  })

  return (
    <>
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm text-foreground">{item.id}</span>
          {labels.map((label) => (
            <span
              key={label}
              className="inline-flex items-center rounded-full border border-warn/50 bg-warn/12 px-2 py-0.5 font-mono text-[0.65rem] text-warn"
            >
              {label}
            </span>
          ))}
        </div>
        <h2 className="text-balance text-sm font-semibold leading-relaxed text-foreground">
          {item.title}
        </h2>
      </div>

      <details open>
        <summary className="w-fit cursor-pointer font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground transition-colors hover:text-foreground">
          Description
        </summary>
        <div className="mt-2">
          <Description
            markdown={issue.data?.description.trim() ?? ''}
            loading={issue.isLoading}
            error={(issue.error as Error) ?? null}
          />
        </div>
      </details>

      <dl className="flex flex-col gap-2 border-t border-border pt-4">
        {rows.map((row) => (
          <div
            key={row.label}
            className="flex items-baseline justify-between gap-3"
          >
            <dt className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
              {row.label}
            </dt>
            <dd className="text-right font-mono text-xs text-foreground">
              {row.value}
            </dd>
          </div>
        ))}
      </dl>

      <div className="flex flex-col gap-1.5 border-t border-border pt-4">
        <SectionLabel>Proposed outcome</SectionLabel>
        {outcome ? (
          <OutcomeMirror outcome={outcomePayload(outcome)} />
        ) : (
          <p className="text-xs leading-relaxed text-faint">
            Builds up as the session progresses — the updated ticket and draft
            sub-issues will appear here before anything is written back.
          </p>
        )}
      </div>
    </>
  )
}

function Description({
  markdown,
  loading,
  error,
}: {
  markdown: string
  loading: boolean
  error: Error | null
}) {
  if (loading) {
    return (
      <p className="inline-flex items-center gap-2 text-xs text-muted-foreground">
        <Loader2 className="size-3 animate-spin" />
        Loading…
      </p>
    )
  }
  if (error) return <ErrorNote message={error.message} />
  if (!markdown) return <p className="text-xs text-faint">No description.</p>
  return <Markdown className="text-xs leading-relaxed">{markdown}</Markdown>
}

// OutcomeMirror is read-only on purpose: the proposal is edited and approved in the
// chat column, and two live editors over one outcome is a conflict, not a feature.
function OutcomeMirror({ outcome }: { outcome: OutcomePayload }) {
  return (
    <ul className="flex flex-col gap-1.5">
      <li className="text-xs leading-relaxed text-foreground">
        {outcome.summary}
      </li>
      {outcome.sub_issues?.map((sub) => (
        <li
          key={sub.title}
          className="flex items-start gap-2 text-xs leading-relaxed text-muted-foreground"
        >
          <span className="text-faint" aria-hidden="true">
            ○
          </span>
          {sub.title}
        </li>
      ))}
    </ul>
  )
}

function EmptyInbox() {
  return (
    <div className="flex flex-col items-center gap-3">
      <span className="font-mono text-2xl text-done" aria-hidden="true">
        ✓
      </span>
      <p className="text-sm font-medium text-foreground">Nothing needs triage</p>
      <p className="max-w-sm text-center text-xs leading-relaxed text-muted-foreground">
        New issues labelled <span className="font-mono text-warn">needs-triage</span>{' '}
        land here automatically. Come back when the picker flags something unclear.
      </p>
    </div>
  )
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <p className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
      {children}
    </p>
  )
}

function PregrillButton({
  repo,
  issueId,
  className,
}: {
  repo: string
  issueId: string
  className?: string
}) {
  const queryClient = useQueryClient()
  const pregrill = useMutation({
    mutationFn: () => pregrillIssues(repo, [issueId]),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: ['grill', repo] }),
  })

  return (
    <Button
      variant="ghost"
      size="icon"
      className={cn('size-7 shrink-0', className)}
      onClick={() => pregrill.mutate()}
      disabled={pregrill.isPending}
      aria-label={`Pre-grill ${issueId}`}
      title="Pre-grill — ask an opening question ahead of time"
    >
      <Flame className={cn(pregrill.isPending && 'animate-pulse')} />
    </Button>
  )
}
