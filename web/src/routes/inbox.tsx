import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
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
import { ErrorNote } from '@/components/grill/banners'
import {
  GrillConversation,
  type GrillStatus,
} from '@/components/grill/conversation'
import { useGrillSession } from '@/components/grill/session'
import { Button } from '@/components/ui/button'
import {
  PageHeader,
  ProjectScopeGate,
  RepoHealthGate,
  StatusPill,
  useActiveRepo,
} from '@/components/trau'
import { GRILLABLE_LABELS, pregrillIssues } from '@/lib/grill'
import {
  inboxPill,
  inboxPosition,
  skipTarget,
  summarisePregrill,
  useInboxQueue,
  type InboxGroup,
  type InboxGroupView,
  type InboxItem,
} from '@/lib/inbox'
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

  const [contextOpen, setContextOpen] = useState(true)
  const [authoring, setAuthoring] = useState(false)
  const [passSummary, setPassSummary] = useState<string | null>(null)

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
                  contextOpen={contextOpen}
                  onToggleContext={() => setContextOpen((v) => !v)}
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

            {selected && contextOpen && <ContextPanel item={selected} />}
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
  contextOpen,
  onToggleContext,
  onSkip,
  onApplied,
}: {
  repo: string
  item: InboxItem
  position: number
  total: number
  contextOpen: boolean
  onToggleContext: () => void
  onSkip: () => void
  onApplied: () => void
}) {
  const { session, starting, error, retry } = useGrillSession(repo, item.id)
  const [status, setStatus] = useState<GrillStatus | null>(null)

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

      {session ? (
        <GrillConversation
          key={session.id}
          repo={repo}
          initial={session}
          onStatus={setStatus}
          onApplied={onApplied}
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
          className="hidden size-8 xl:inline-flex"
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
  selectedId,
  onSelect,
}: {
  repo: string
  groups: InboxGroupView[]
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
  selected,
  onSelect,
}: {
  repo: string
  item: InboxItem
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
          'flex w-full flex-col gap-0.5 rounded-md px-2.5 py-2 text-left transition-colors',
          selected ? 'bg-primary/10' : 'hover:bg-secondary',
        )}
      >
        {selected && (
          <span
            aria-hidden="true"
            className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-primary"
          />
        )}
        <span className="flex items-center gap-2">
          <span
            className={cn(
              'font-mono text-xs',
              selected ? 'text-primary' : 'text-muted-foreground',
            )}
          >
            {item.id}
          </span>
          {item.attention === 'answer' && (
            <span
              className="size-1.5 rounded-full bg-warn"
              aria-hidden="true"
              title="Waiting for your answer"
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

// ContextPanel reserves the workspace's third column. It anchors on what the queue
// already knows about the issue; the description, meta and proposed outcome land in
// the context-panel slice.
function ContextPanel({ item }: { item: InboxItem }) {
  const labels = (item.entry?.labels ?? []).filter((l) =>
    GRILLABLE_LABELS.includes(l),
  )

  return (
    <aside
      aria-label="Issue context"
      className="hidden min-h-0 flex-col gap-5 overflow-y-auto border-l border-border py-4 pl-4 xl:flex"
    >
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
    </aside>
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
