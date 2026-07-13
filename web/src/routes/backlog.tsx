import { Fragment, useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useQueryStates } from 'nuqs'
import {
  Check,
  ChevronsUpDown,
  FilePlus,
  ListFilter,
  ListPlus,
  Pencil,
  Search,
  Tag,
} from 'lucide-react'

import { PageHeader, ProjectScopeGate, useActiveRepo } from '@/components/trau'
import {
  SegmentedControl,
  type SegmentOption,
} from '@/components/trau/segmented-control'
import { InternalIssueForm } from '@/components/internal-issue-form'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  backlogQueryOptions,
  backlogSections,
  hiddenStateGroups,
  STATE_GROUPS,
  type BacklogEntry,
} from '@/lib/backlog'
import {
  backlogFilterParsers,
  backlogParamsFromFilters,
  effectiveStateGroups,
  hasActiveFilters,
  toggleStateGroup,
} from '@/lib/backlog-filters'
import { internalIssueQueryOptions } from '@/lib/issues'
import { labelsQueryOptions } from '@/lib/labels'
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

  useEffect(() => setText(q), [q])

  useEffect(() => {
    const id = setTimeout(() => {
      const next = text.trim()
      if (next !== q) setFilters({ q: next, page: null }, { history: 'replace' })
    }, 150)
    return () => clearTimeout(id)
  }, [text, q, setFilters])

  const backlog = useQuery(
    backlogQueryOptions(repo, backlogParamsFromFilters(filters, PAGE_SIZE)),
  )
  const items = backlog.data?.items ?? []
  const counts = backlog.data?.counts ?? {}
  const total = backlog.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const hasFilters = hasActiveFilters(filters)
  const sections = backlogSections(
    items,
    counts,
    effectiveStateGroups(state),
    (page - 1) * PAGE_SIZE,
  )
  const hidden = hiddenStateGroups(counts, effectiveStateGroups(state))

  return (
    <ProjectScopeGate action="manage the backlog">
      <PageHeader
        eyebrow={repo || 'backlog'}
        title="Backlog"
        description="In-progress, todo and backlog work — done and canceled are hidden until you filter for them."
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
          <StateFilter
            value={state}
            onChange={(next) =>
              setFilters({ state: next.length ? next : null, page: null })
            }
          />
          <LabelFilter
            repo={repo}
            value={label}
            onChange={(next) => setFilters({ label: next || null, page: null })}
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
          <div className="flex flex-col gap-6">
            {sections.map((section) => (
              <section key={section.group} className="flex flex-col gap-2">
                {!section.continuation && (
                  <div className="flex items-baseline gap-1.5 px-1">
                    <h2 className="text-sm font-semibold text-foreground">
                      {section.label}
                    </h2>
                    <span aria-hidden className="text-muted-foreground/50">
                      ·
                    </span>
                    <span className="text-xs tabular-nums text-muted-foreground">
                      {section.count}
                    </span>
                  </div>
                )}
                <ul className="flex flex-col gap-2">
                  {section.items.map((entry) => (
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
                </ul>
              </section>
            ))}

            {items.length === 0 && (
              <p className="rounded-lg border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
                {hasFilters
                  ? 'No issues match these filters.'
                  : 'No issues yet — create one to get started.'}
              </p>
            )}

            {hidden.length > 0 && (
              <p className="px-1 text-xs text-muted-foreground">
                {hidden.map((h, i) => (
                  <Fragment key={h.group}>
                    {i > 0 && (
                      <span className="px-1 text-muted-foreground/50">·</span>
                    )}
                    <button
                      type="button"
                      onClick={() => setFilters({ state: [h.group], page: null })}
                      className="tabular-nums underline-offset-2 transition-colors hover:text-foreground hover:underline"
                    >
                      {h.count} {h.group}
                    </button>
                  </Fragment>
                ))}
                {' hidden'}
              </p>
            )}
          </div>
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

function StateFilter({
  value,
  onChange,
}: {
  value: string[]
  onChange: (next: string[]) => void
}) {
  const [open, setOpen] = useState(false)
  const selected = new Set(value)

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className="h-9" aria-label="State">
          <ListFilter className="text-muted-foreground" />
          State
          {value.length > 0 && (
            <Badge variant="secondary" className="ml-0.5 tabular-nums">
              {value.length}
            </Badge>
          )}
          <ChevronsUpDown className="text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-52 p-0">
        <Command>
          <CommandInput placeholder="Filter states…" />
          <CommandList>
            <CommandEmpty>No states.</CommandEmpty>
            <CommandGroup>
              {STATE_GROUPS.map((group) => {
                const active = selected.has(group)
                return (
                  <CommandItem
                    key={group}
                    value={group}
                    onSelect={() => onChange(toggleStateGroup(value, group))}
                  >
                    <span
                      className={cn(
                        'flex size-4 items-center justify-center rounded-[4px] border',
                        active
                          ? 'border-primary bg-primary text-primary-foreground'
                          : 'border-border',
                      )}
                    >
                      {active && <Check className="size-3 text-primary-foreground" />}
                    </span>
                    {group}
                  </CommandItem>
                )
              })}
            </CommandGroup>
            {value.length > 0 && (
              <>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem
                    onSelect={() => onChange([])}
                    className="justify-center text-center text-muted-foreground"
                  >
                    Clear states
                  </CommandItem>
                </CommandGroup>
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

function LabelFilter({
  repo,
  value,
  onChange,
}: {
  repo: string
  value: string
  onChange: (next: string) => void
}) {
  const [open, setOpen] = useState(false)
  const labels = useQuery(labelsQueryOptions(repo))
  const facets = labels.data?.labels ?? []

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className="h-9 w-48 justify-between"
          aria-label="Label"
        >
          <span className="flex min-w-0 items-center gap-1.5">
            <Tag className="text-muted-foreground" />
            <span className={cn('truncate', !value && 'text-muted-foreground')}>
              {value || 'Label'}
            </span>
          </span>
          <ChevronsUpDown className="text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-56 p-0">
        <Command>
          <CommandInput placeholder="Search labels…" />
          <CommandList>
            <CommandEmpty>
              {labels.isLoading ? 'Loading labels…' : 'No labels found.'}
            </CommandEmpty>
            <CommandGroup>
              {facets.map((facet) => (
                <CommandItem
                  key={facet.name}
                  value={facet.name}
                  onSelect={() => {
                    onChange(facet.name === value ? '' : facet.name)
                    setOpen(false)
                  }}
                >
                  <Check
                    className={cn(
                      'size-4',
                      value === facet.name ? 'opacity-100' : 'opacity-0',
                    )}
                  />
                  <span className="flex-1 truncate">{facet.name}</span>
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {facet.count}
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
            {value && (
              <>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem
                    onSelect={() => {
                      onChange('')
                      setOpen(false)
                    }}
                    className="justify-center text-center text-muted-foreground"
                  >
                    Clear label
                  </CommandItem>
                </CommandGroup>
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
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
