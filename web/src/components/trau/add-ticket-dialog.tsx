import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Check, ChevronDown, ChevronRight, Search } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { cn } from '@/lib/utils'
import {
  addTickets,
  pickerList,
  planAddSelected,
  toggleSelected,
  type PickerEmpty,
} from '@/lib/add-ticket'
import { epicPreviewQueryOptions } from '@/lib/epic'
import { enqueue, type QueueItem, type QueueResponse } from '@/lib/queue'
import { issueSearchQueryOptions, type SearchResult } from '@/lib/search'

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

const EMPTY_TEXT: Record<PickerEmpty, string> = {
  'no-match': 'No tickets match',
  'all-queued': 'All tracker tickets are already queued',
}

function SourceTab({
  active,
  disabled,
  title,
  children,
}: {
  active: boolean
  disabled?: boolean
  title?: string
  children: string
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      disabled={disabled}
      title={title}
      className={cn(
        '-mb-px border-b-2 px-1 pb-2 font-mono text-xs transition-colors',
        active
          ? 'border-primary text-foreground'
          : 'border-transparent text-muted-foreground',
        disabled && 'cursor-not-allowed opacity-40',
      )}
    >
      {children}
    </button>
  )
}

function SubIssuePreview({ repo, epic }: { repo: string; epic: string }) {
  const preview = useQuery(epicPreviewQueryOptions(repo, epic))
  const subs = preview.data?.sub_issues ?? []

  return (
    <div className="flex flex-col gap-0.5 border-t border-border/40 bg-secondary/20 py-1.5 pl-10 pr-3">
      {preview.error ? (
        <span className="font-mono text-xs text-fail" role="alert">
          {actionError(preview.error)}
        </span>
      ) : preview.isPending ? (
        <span className="font-mono text-xs text-muted-foreground">
          Loading sub-items…
        </span>
      ) : subs.length === 0 ? (
        <span className="font-mono text-xs text-muted-foreground">
          No sub-items.
        </span>
      ) : (
        subs.map((sub) => (
          <span
            key={sub.id}
            className="flex items-center gap-2 font-mono text-xs text-muted-foreground"
          >
            <span className="text-primary/70" aria-hidden="true">
              ◆
            </span>
            <span className="text-primary/70">{sub.id}</span>
            <span className="min-w-0 truncate font-sans">{sub.title}</span>
          </span>
        ))
      )}
    </div>
  )
}

// TrackerRow is one search hit. An epic keeps its sub-issue count and preview
// behind the expander: the hub resolves those by shelling out to the tracker, so
// they are fetched per epic the user opens rather than for every hit a search
// returns.
function TrackerRow({
  repo,
  result,
  selected,
  expanded,
  onToggle,
  onExpand,
}: {
  repo: string
  result: SearchResult
  selected: boolean
  expanded: boolean
  onToggle: () => void
  onExpand: () => void
}) {
  const isEpic = result.has_children

  return (
    <li className="border-b border-border/60 last:border-0">
      <div className="flex items-start">
        <button
          type="button"
          role="checkbox"
          aria-checked={selected}
          onClick={onToggle}
          className={cn(
            'flex min-w-0 flex-1 items-start gap-3 px-3 py-2.5 text-left transition-colors hover:bg-secondary/40',
            selected && 'bg-secondary/60',
          )}
        >
          <span
            className={cn(
              'mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-sm border transition-colors',
              selected
                ? 'border-primary bg-primary text-primary-foreground'
                : 'border-border bg-input',
            )}
            aria-hidden="true"
          >
            {selected && <Check className="size-3" />}
          </span>
          <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
            <span className="shrink-0 font-mono text-sm text-primary">
              {result.id}
            </span>
            <span className="min-w-0 truncate font-sans text-sm text-foreground">
              {result.title}
            </span>
            {isEpic && (
              <span className="shrink-0 font-mono text-[0.65rem] uppercase tracking-[0.14em] text-teal">
                epic
              </span>
            )}
          </span>
        </button>
        {isEpic && (
          <button
            type="button"
            onClick={onExpand}
            aria-expanded={expanded}
            aria-label={
              expanded ? `Collapse ${result.id}` : `Expand ${result.id}`
            }
            className="flex size-9 shrink-0 items-center justify-center text-muted-foreground transition-colors hover:text-foreground"
          >
            {expanded ? (
              <ChevronDown className="size-3.5" aria-hidden="true" />
            ) : (
              <ChevronRight className="size-3.5" aria-hidden="true" />
            )}
          </button>
        )}
      </div>
      {expanded && <SubIssuePreview repo={repo} epic={result.id} />}
    </li>
  )
}

function TrackerPicker({
  repo,
  queued,
  onClose,
  onQueue,
}: {
  repo: string
  queued: QueueItem[]
  onClose: () => void
  onQueue: (res: QueueResponse) => void
}) {
  const [term, setTerm] = useState('')
  const [debounced, setDebounced] = useState('')
  const [selected, setSelected] = useState<SearchResult[]>([])
  const [expandedIds, setExpandedIds] = useState<string[]>([])

  useEffect(() => {
    const id = setTimeout(() => setDebounced(term.trim()), 150)
    return () => clearTimeout(id)
  }, [term])

  const search = useQuery(issueSearchQueryOptions(repo, debounced))
  const { rows, empty } = pickerList(search.data?.results ?? [], queued)

  const add = useMutation({
    mutationFn: () =>
      addTickets(
        planAddSelected(selected),
        (it) => enqueue(repo, { id: it.id, kind: it.kind }),
        onQueue,
      ),
    onSuccess: onClose,
  })

  const toggleExpand = (id: string) =>
    setExpandedIds((prev) =>
      prev.includes(id) ? prev.filter((e) => e !== id) : [...prev, id],
    )

  return (
    <div className="flex flex-col gap-4">
      <div className="relative">
        <Search
          className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
          aria-hidden="true"
        />
        <input
          value={term}
          onChange={(e) => setTerm(e.target.value)}
          placeholder="Search tracker tickets…"
          aria-label="Search tracker tickets"
          autoFocus
          className="w-full rounded-md border border-border bg-input py-1.5 pl-9 pr-2.5 font-mono text-sm text-foreground placeholder:text-muted-foreground/60 focus-visible:border-ring focus-visible:outline-none"
        />
      </div>

      <ul className="flex max-h-72 flex-col overflow-y-auto rounded-md border border-border">
        {debounced === '' ? (
          <li className="px-4 py-6 text-center font-mono text-xs text-muted-foreground">
            Search the tracker by id or title
          </li>
        ) : search.error ? (
          <li
            className="px-4 py-6 text-center font-mono text-xs text-fail"
            role="alert"
          >
            {actionError(search.error)}
          </li>
        ) : empty ? (
          <li className="px-4 py-6 text-center font-mono text-xs text-muted-foreground">
            {search.isFetching ? 'Searching…' : EMPTY_TEXT[empty]}
          </li>
        ) : (
          rows.map((row) => (
            <TrackerRow
              key={row.id}
              repo={repo}
              result={row}
              selected={selected.some((s) => s.id === row.id)}
              expanded={expandedIds.includes(row.id)}
              onToggle={() => setSelected((prev) => toggleSelected(prev, row))}
              onExpand={() => toggleExpand(row.id)}
            />
          ))
        )}
      </ul>

      {add.error ? (
        <p className="whitespace-pre-line font-mono text-xs text-fail" role="alert">
          {actionError(add.error)}
        </p>
      ) : null}

      <div className="flex items-center justify-between gap-2 border-t border-border pt-4">
        <span className="font-mono text-xs text-muted-foreground">
          {selected.length === 0
            ? 'Select tickets to import'
            : `${selected.length} selected · sub-items included automatically`}
        </span>
        <div className="flex gap-2">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="font-mono"
            onClick={onClose}
          >
            Cancel
          </Button>
          <Button
            type="button"
            size="sm"
            className="font-mono"
            disabled={selected.length === 0 || add.isPending}
            onClick={() => add.mutate()}
          >
            {add.isPending
              ? 'Adding…'
              : `Add ${selected.length > 0 ? `${selected.length} ` : ''}to queue`}
          </Button>
        </div>
      </div>
    </div>
  )
}

// AddTicketDialog is the queue builder's browse path: search the repo's synced
// tracker and check off what to queue. The internal-ticket tab is the shell for a
// follow-up slice and stays disabled until it lands.
export function AddTicketDialog({
  repo,
  queued,
  open,
  onOpenChange,
  onQueue,
}: {
  repo: string
  queued: QueueItem[]
  open: boolean
  onOpenChange: (open: boolean) => void
  onQueue: (res: QueueResponse) => void
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent
        aria-describedby={undefined}
        onOpenAutoFocus={(e) => e.preventDefault()}
        className="gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-lg"
      >
        <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <AlertDialogTitle asChild>
            <span className="font-mono text-xs font-normal text-muted-foreground">
              add-ticket
            </span>
          </AlertDialogTitle>
        </div>

        <div
          className="flex items-center gap-5 border-b border-border px-4 pt-3"
          role="tablist"
          aria-label="Ticket source"
        >
          <SourceTab active>From tracker</SourceTab>
          <SourceTab active={false} disabled title="Coming soon">
            Internal ticket
          </SourceTab>
          <span className="ml-auto pb-2 font-mono text-[0.65rem] uppercase tracking-[0.14em] text-muted-foreground">
            tracker · synced
          </span>
        </div>

        <div className="min-w-0 p-4">
          <TrackerPicker
            repo={repo}
            queued={queued}
            onClose={() => onOpenChange(false)}
            onQueue={onQueue}
          />
        </div>
      </AlertDialogContent>
    </AlertDialog>
  )
}
