import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useQueryStates } from 'nuqs'
import { FilePlus, ListPlus, Pencil, Search } from 'lucide-react'

import { PageHeader, ProjectScopeGate, useActiveRepo } from '@/components/trau'
import {
  SegmentedControl,
  type SegmentOption,
} from '@/components/trau/segmented-control'
import { InternalIssueForm } from '@/components/internal-issue-form'
import { Button } from '@/components/ui/button'
import { backlogQueryOptions, type BacklogEntry } from '@/lib/backlog'
import {
  backlogFilterParsers,
  backlogParamsFromFilters,
  hasActiveFilters,
} from '@/lib/backlog-filters'
import { INTERNAL_STATES, internalIssueQueryOptions } from '@/lib/issues'
import { enqueue } from '@/lib/queue'
import { cn } from '@/lib/utils'

export const Route = createFileRoute('/backlog')({
  component: BacklogPage,
})

const PAGE_SIZE = 50

type SourceFilter = 'all' | 'internal' | 'synced'

const SOURCE_OPTIONS: readonly SegmentOption<SourceFilter>[] = [
  { value: 'all', label: 'All' },
  { value: 'internal', label: 'Internal' },
  { value: 'synced', label: 'Synced' },
]

const selectClass =
  'h-9 rounded-md border bg-transparent px-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50'

function BacklogPage() {
  const { repo: activeRepo } = useActiveRepo()
  const repo = activeRepo ?? ''
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)

  const [filters, setFilters] = useQueryStates(backlogFilterParsers, {
    history: 'push',
  })
  const { q, state, label, source, page } = filters

  const [text, setText] = useState(q)
  const [labelText, setLabelText] = useState(label)

  useEffect(() => setText(q), [q])
  useEffect(() => setLabelText(label), [label])

  useEffect(() => {
    const id = setTimeout(() => {
      const next = text.trim()
      if (next !== q) setFilters({ q: next, page: null }, { history: 'replace' })
    }, 150)
    return () => clearTimeout(id)
  }, [text, q, setFilters])
  useEffect(() => {
    const id = setTimeout(() => {
      const next = labelText.trim()
      if (next !== label) setFilters({ label: next, page: null }, { history: 'replace' })
    }, 150)
    return () => clearTimeout(id)
  }, [labelText, label, setFilters])

  const backlog = useQuery(
    backlogQueryOptions(repo, backlogParamsFromFilters(filters, PAGE_SIZE)),
  )
  const items = backlog.data?.items ?? []
  const total = backlog.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const hasFilters = hasActiveFilters(filters)

  return (
    <ProjectScopeGate action="manage the backlog">
      <PageHeader
        eyebrow={repo || 'backlog'}
        title="Backlog"
        description="Every issue in this repo's store — synced tickets and issues created inside trau."
        actions={
          <button
            type="button"
            onClick={() => {
              setEditing(null)
              setCreating((v) => !v)
            }}
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground transition-opacity hover:opacity-90"
          >
            <FilePlus className="size-4" />
            New issue
          </button>
        }
      />

      <div className="flex flex-col gap-4 px-8 py-6">
        {creating && (
          <InternalIssueForm
            repo={repo}
            onDone={() => setCreating(false)}
            onCancel={() => setCreating(false)}
          />
        )}

        <div className="flex flex-wrap items-center gap-3">
          <div className="flex min-w-56 flex-1 items-center gap-2 rounded-md border border-border bg-input px-2.5 py-1.5">
            <Search className="size-4 shrink-0 text-muted-foreground" />
            <input
              type="text"
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder="Search id or title…"
              className="w-full bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
            />
          </div>
          <select
            value={state[0] ?? ''}
            onChange={(e) => {
              const v = e.target.value
              setFilters({ state: v ? [v] : null, page: null })
            }}
            aria-label="State"
            className={selectClass}
          >
            <option value="">All states</option>
            {INTERNAL_STATES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          <input
            type="text"
            value={labelText}
            onChange={(e) => setLabelText(e.target.value)}
            placeholder="Label…"
            aria-label="Label"
            className={cn(selectClass, 'w-40')}
          />
          <SegmentedControl
            aria-label="Source"
            options={SOURCE_OPTIONS}
            value={source ?? 'all'}
            onChange={(v) => setFilters({ source: v === 'all' ? null : v, page: null })}
          />
        </div>

        {backlog.isLoading && (
          <p className="text-sm text-muted-foreground">Loading backlog…</p>
        )}
        {backlog.error && (
          <p className="text-sm text-destructive">
            {String((backlog.error as Error).message)}
          </p>
        )}

        {backlog.data && (
          <ul className="flex flex-col gap-2">
            {items.map((entry) => (
              <BacklogRow
                key={entry.id}
                repo={repo}
                entry={entry}
                editing={editing === entry.id}
                onToggleEdit={() =>
                  setEditing((cur) => (cur === entry.id ? null : entry.id))
                }
                onEditDone={() => setEditing(null)}
              />
            ))}
            {items.length === 0 && (
              <li className="rounded-lg border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
                {hasFilters
                  ? 'No issues match these filters.'
                  : 'No issues yet — create one to get started.'}
              </li>
            )}
          </ul>
        )}

        {total > PAGE_SIZE && (
          <div className="flex items-center justify-between pt-1">
            <p className="text-xs text-muted-foreground">
              Showing {(page - 1) * PAGE_SIZE + 1}–{Math.min(page * PAGE_SIZE, total)} of{' '}
              {total}
            </p>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setFilters({ page: Math.max(1, page - 1) })}
                disabled={page <= 1}
              >
                Previous
              </Button>
              <span className="text-xs text-muted-foreground">
                Page {page} of {pageCount}
              </span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setFilters({ page: Math.min(pageCount, page + 1) })}
                disabled={page >= pageCount}
              >
                Next
              </Button>
            </div>
          </div>
        )}
      </div>
    </ProjectScopeGate>
  )
}

function BacklogRow({
  repo,
  entry,
  editing,
  onToggleEdit,
  onEditDone,
}: {
  repo: string
  entry: BacklogEntry
  editing: boolean
  onToggleEdit: () => void
  onEditDone: () => void
}) {
  const queryClient = useQueryClient()
  const internal = entry.source === 'internal'
  const issueQuery = useQuery({
    ...internalIssueQueryOptions(repo, entry.id),
    enabled: editing && internal,
  })
  const addToQueue = useMutation({
    mutationFn: () => enqueue(repo, { id: entry.id }),
    onSuccess: (res) => queryClient.setQueryData(['queue', repo], res),
  })

  return (
    <li className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center gap-3 px-4 py-3">
        <span className="font-mono text-sm font-medium text-foreground">{entry.id}</span>
        <span className="min-w-0 flex-1 truncate text-sm text-foreground">{entry.title}</span>
        {entry.ready && (
          <span className="rounded-full border border-emerald-500/40 bg-emerald-500/5 px-2 py-0.5 text-xs text-emerald-600 dark:text-emerald-400">
            ready
          </span>
        )}
        <span className="rounded-full border px-2 py-0.5 text-xs text-muted-foreground">
          {entry.group}
        </span>
        <span
          className={cn(
            'rounded-full px-2 py-0.5 font-mono text-xs',
            internal
              ? 'border border-primary/40 bg-primary/5 text-primary'
              : 'border text-muted-foreground',
          )}
        >
          {entry.source}
        </span>
        {internal && (
          <button
            type="button"
            onClick={onToggleEdit}
            className="inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            <Pencil className="size-3.5" />
            Edit
          </button>
        )}
        <button
          type="button"
          onClick={() => addToQueue.mutate()}
          disabled={addToQueue.isPending || addToQueue.isSuccess}
          className="inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
        >
          <ListPlus className="size-3.5" />
          {addToQueue.isSuccess ? 'Queued' : 'Add to queue'}
        </button>
      </div>
      {addToQueue.error && (
        <p className="px-4 pb-2 text-xs text-destructive">
          {String((addToQueue.error as Error).message)}
        </p>
      )}
      {editing && internal && issueQuery.data && (
        <div className="border-t px-4 py-3">
          <InternalIssueForm
            repo={repo}
            issue={issueQuery.data}
            onDone={onEditDone}
            onCancel={onEditDone}
          />
        </div>
      )}
    </li>
  )
}
