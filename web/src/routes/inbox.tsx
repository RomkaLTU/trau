import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { parseAsString, useQueryState } from 'nuqs'
import { ChevronLeft, Flame, SkipForward, Sparkles } from 'lucide-react'

import { AuthoringDrawer, GrillPanel } from '@/components/grill-panel'
import { Markdown } from '@/components/markdown'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  EmptyState,
  PageHeader,
  ProjectScopeGate,
  StatusPill,
  useActiveRepo,
  type RunState,
} from '@/components/trau'
import { GRILLABLE_LABELS, pregrillIssues } from '@/lib/grill'
import {
  inboxPosition,
  inboxSections,
  nextIssueId,
  prevIssueId,
  summarisePregrill,
  useInbox,
  type InboxAttention,
  type InboxItem,
} from '@/lib/inbox'
import { issueQueryOptions } from '@/lib/issues'
import { standardTitle, usePageTitle } from '@/lib/page-title'
import { cn } from '@/lib/utils'

export const Route = createFileRoute('/inbox')({
  component: InboxPage,
})

const ATTENTION_META: Record<
  InboxAttention,
  { label: string; hint: string; pill: { state: RunState; label: string } }
> = {
  answer: {
    label: 'Waiting for your answer',
    hint: 'A grilling question is parked on you — answer to resume.',
    pill: { state: 'info', label: 'your turn' },
  },
  thinking: {
    label: 'In progress',
    hint: 'The agent is grilling this issue right now.',
    pill: { state: 'active', label: 'thinking' },
  },
  open: {
    label: 'Needs triage',
    hint: 'Unclear issues no grilling session has touched yet.',
    pill: { state: 'todo', label: 'untouched' },
  },
  review: {
    label: 'Ready to review',
    hint: 'A proposal is waiting for your approval before it is applied.',
    pill: { state: 'verify', label: 'review' },
  },
}

function InboxPage() {
  usePageTitle(standardTitle('Inbox'))
  const { repo: activeRepo } = useActiveRepo()
  const repo = activeRepo ?? ''
  const { items, isLoading, error } = useInbox(repo)
  const queryClient = useQueryClient()

  const [peek, setPeek] = useQueryState(
    'issue',
    parseAsString.withOptions({ history: 'push' }),
  )

  const sections = inboxSections(items)
  const untouchedIds = items
    .filter((item) => item.attention === 'open')
    .map((item) => item.entry.id)

  const [authoring, setAuthoring] = useState(false)
  const [passSummary, setPassSummary] = useState<string | null>(null)
  const pregrillAll = useMutation({
    mutationFn: () => pregrillIssues(repo, untouchedIds),
    onSuccess: (res) => setPassSummary(summarisePregrill(res)),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: ['grill', repo] }),
  })

  return (
    <ProjectScopeGate action="triage unclear issues">
      <PageHeader
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

      <div className="flex flex-col gap-6 px-8 py-6">
        {(passSummary || pregrillAll.error) && (
          <p
            className={cn(
              'text-sm',
              pregrillAll.error ? 'text-destructive' : 'text-muted-foreground',
            )}
          >
            {pregrillAll.error ? pregrillAll.error.message : passSummary}
          </p>
        )}

        {isLoading && items.length === 0 && (
          <p className="text-sm text-muted-foreground">Loading inbox…</p>
        )}
        {error && (
          <p className="text-sm text-destructive">{error.message}</p>
        )}

        {!isLoading && items.length === 0 && !error && (
          <EmptyState message="Inbox zero — no issues are labelled needs-triage, needs-info or needs-split right now." />
        )}

        {sections.map((section) => {
          const meta = ATTENTION_META[section.attention]
          return (
            <section key={section.attention} className="flex flex-col gap-2">
              <div className="flex items-baseline gap-1.5 px-1">
                <h2 className="text-sm font-semibold text-foreground">{meta.label}</h2>
                <span aria-hidden className="text-muted-foreground/50">
                  ·
                </span>
                <span className="text-xs tabular-nums text-muted-foreground">
                  {section.items.length}
                </span>
              </div>
              <ul className="flex flex-col gap-2">
                {section.items.map((item) => (
                  <InboxRow
                    key={item.entry.id}
                    repo={repo}
                    item={item}
                    active={peek === item.entry.id}
                    onOpen={() => void setPeek(item.entry.id)}
                  />
                ))}
              </ul>
            </section>
          )
        })}
      </div>

      <InboxDrawer
        repo={repo}
        items={items}
        issueId={peek}
        onNavigate={(id) => void setPeek(id)}
      />

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

function InboxRow({
  repo,
  item,
  active,
  onOpen,
}: {
  repo: string
  item: InboxItem
  active: boolean
  onOpen: () => void
}) {
  const { entry } = item
  const pill = ATTENTION_META[item.attention].pill
  const labels = entry.labels.filter((l) => GRILLABLE_LABELS.includes(l))

  return (
    <li
      className={cn(
        'flex items-center gap-1 rounded-lg border bg-card pr-2 transition-colors hover:border-ring/40',
        active && 'border-ring/60 bg-secondary/40',
      )}
    >
      <button
        type="button"
        onClick={onOpen}
        aria-label={`Open ${entry.id}`}
        className="flex min-w-0 flex-1 items-center gap-3 rounded-lg px-4 py-3 text-left"
      >
        <span className="font-mono text-sm font-medium text-foreground">{entry.id}</span>
        <span className="min-w-0 flex-1 truncate text-sm text-foreground">{entry.title}</span>
        {labels.map((label) => (
          <span
            key={label}
            className="hidden shrink-0 rounded-full border px-2 py-0.5 text-xs text-muted-foreground sm:inline"
          >
            {label}
          </span>
        ))}
        <StatusPill state={pill.state} label={pill.label} />
      </button>
      {item.attention === 'open' && <PregrillButton repo={repo} issueId={entry.id} />}
    </li>
  )
}

function PregrillButton({ repo, issueId }: { repo: string; issueId: string }) {
  const queryClient = useQueryClient()
  const pregrill = useMutation({
    mutationFn: () => pregrillIssues(repo, [issueId]),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: ['grill', repo] }),
  })

  return (
    <Button
      variant="ghost"
      size="icon"
      className="size-8 shrink-0"
      onClick={() => pregrill.mutate()}
      disabled={pregrill.isPending}
      aria-label={`Pre-grill ${issueId}`}
      title="Pre-grill — ask an opening question ahead of time"
    >
      <Flame className={cn(pregrill.isPending && 'animate-pulse')} />
    </Button>
  )
}

function InboxDrawer({
  repo,
  items,
  issueId,
  onNavigate,
}: {
  repo: string
  items: InboxItem[]
  issueId: string | null
  onNavigate: (id: string | null) => void
}) {
  // shownId lags issueId so the panel keeps rendering the closing issue through the
  // sheet's exit animation; the keyed body resets per-issue state on change.
  const [shownId, setShownId] = useState(issueId)
  useEffect(() => {
    if (issueId !== null) setShownId(issueId)
  }, [issueId])

  return (
    <Sheet open={issueId !== null} onOpenChange={(open) => !open && onNavigate(null)}>
      <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-xl">
        {shownId !== null && (
          <InboxDrawerBody
            key={shownId}
            repo={repo}
            id={shownId}
            items={items}
            onNavigate={onNavigate}
          />
        )}
      </SheetContent>
    </Sheet>
  )
}

function InboxDrawerBody({
  repo,
  id,
  items,
  onNavigate,
}: {
  repo: string
  id: string
  items: InboxItem[]
  onNavigate: (id: string | null) => void
}) {
  const issue = useQuery(issueQueryOptions(repo, id))
  const index = inboxPosition(items, id)
  const total = items.length
  const labels = (issue.data?.labels ?? []).filter((l) => GRILLABLE_LABELS.includes(l))

  const goNext = () => onNavigate(nextIssueId(items, id))
  const goPrev = () => onNavigate(prevIssueId(items, id))

  return (
    <>
      <SheetHeader className="gap-3 border-b pr-12">
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="icon"
            className="size-7"
            onClick={goPrev}
            disabled={index <= 0}
            aria-label="Previous issue"
          >
            <ChevronLeft />
          </Button>
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {index >= 0 ? `${index + 1} of ${total}` : `${total} left`}
          </span>
          <div className="flex-1" />
          <Button variant="outline" size="sm" onClick={goNext} aria-label="Skip to next issue">
            <SkipForward />
            Skip
          </Button>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm font-medium text-foreground">{id}</span>
          {labels.map((label) => (
            <span
              key={label}
              className="rounded-full border px-2 py-0.5 text-xs text-muted-foreground"
            >
              {label}
            </span>
          ))}
        </div>
        <SheetTitle className="text-base leading-snug">
          {issue.data?.title ?? id}
        </SheetTitle>
        {issue.data?.description.trim() && (
          <details className="text-sm">
            <summary className="cursor-pointer text-xs text-muted-foreground">
              Issue description
            </summary>
            <div className="mt-2 max-h-48 overflow-y-auto rounded-md border bg-card px-3 py-2">
              <Markdown>{issue.data.description}</Markdown>
            </div>
          </details>
        )}
      </SheetHeader>

      <GrillPanel
        repo={repo}
        issueId={id}
        onClose={() => onNavigate(null)}
        onApplied={goNext}
      />
    </>
  )
}
