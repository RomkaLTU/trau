import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ListTree, Pause, Play, RefreshCw, Trash2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { EmptyState } from './empty-state'
import { StatusPill, type RunState } from './status-pill'
import { TerminalCard } from './terminal-card'
import { useActiveRepo } from './active-repo'
import { cn } from '@/lib/utils'
import {
  dequeue,
  drain,
  queueCounts,
  queueQueryOptions,
  type QueueItem,
  type QueueResponse,
} from '@/lib/queue'

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

const STATUS_STATE: Record<string, RunState> = {
  pending: 'todo',
  running: 'active',
  done: 'success',
  failed: 'fail',
}

function statusState(status: string): RunState {
  return STATUS_STATE[status] ?? 'info'
}

export function Queue() {
  const { repo: activeRepo } = useActiveRepo()
  const repo = activeRepo ?? ''
  const queryClient = useQueryClient()

  const queue = useQuery(queueQueryOptions(repo))
  const items = queue.data?.items ?? []
  const counts = queueCounts(items)
  const draining = queue.data?.draining ?? false

  const remove = useMutation({
    mutationFn: (id: string) => dequeue(repo, id),
    onSuccess: (res) => queryClient.setQueryData<QueueResponse>(['queue', repo], res),
  })

  const toggleDrain = useMutation({
    mutationFn: (next: boolean) => drain(repo, next),
    onSuccess: (res) => queryClient.setQueryData<QueueResponse>(['queue', repo], res),
  })

  if (repo === '') {
    return (
      <EmptyState
        message="No repo checked out yet. Register a repo to queue work for it."
        actions={
          <Button asChild variant="outline" size="sm" className="font-mono">
            <Link to="/instances">Manage repos</Link>
          </Button>
        }
      />
    )
  }

  if (queue.isLoading) return <QueueSkeleton />

  if (queue.error) {
    return (
      <TerminalCard title="queue">
        <p className="font-mono text-sm text-destructive">
          {actionError(queue.error)}
        </p>
      </TerminalCard>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <p className="font-mono text-xs text-muted-foreground">
            {counts.total} item{counts.total === 1 ? '' : 's'}
            {counts.epics > 0
              ? ` · ${counts.tickets} ticket${counts.tickets === 1 ? '' : 's'}, ${counts.epics} epic${counts.epics === 1 ? '' : 's'}`
              : ''}
          </p>
          {draining && (
            <StatusPill state="active" label="draining" className="text-[0.65rem]" />
          )}
        </div>
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant={draining ? 'outline' : 'default'}
            size="sm"
            className="font-mono"
            onClick={() => toggleDrain.mutate(!draining)}
            disabled={toggleDrain.isPending || (!draining && counts.total === 0)}
          >
            {draining ? (
              <Pause className="size-3.5" aria-hidden="true" />
            ) : (
              <Play className="size-3.5" aria-hidden="true" />
            )}
            {draining ? 'Pause' : 'Start'}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="font-mono"
            onClick={() => void queue.refetch()}
            disabled={queue.isFetching}
          >
            <RefreshCw
              className={cn('size-3.5', queue.isFetching && 'animate-spin')}
              aria-hidden="true"
            />
            {queue.isFetching ? 'Refreshing…' : 'Refresh'}
          </Button>
        </div>
      </div>

      {toggleDrain.error && (
        <p className="font-mono text-xs text-destructive">
          {actionError(toggleDrain.error)}
        </p>
      )}

      {remove.error && (
        <p className="font-mono text-xs text-destructive">
          {actionError(remove.error)}
        </p>
      )}

      {items.length === 0 ? (
        <EmptyState
          message="Nothing queued yet. Add a ticket or epic from the Backlog board to register it for execution."
          actions={
            <Button asChild variant="outline" size="sm" className="font-mono">
              <Link to="/backlog">Open the backlog</Link>
            </Button>
          }
        />
      ) : (
        <ol className="flex flex-col gap-2">
          {items.map((item) => (
            <QueueRow
              key={item.id}
              repo={repo}
              item={item}
              removing={remove.isPending && remove.variables === item.id}
              onRemove={() => remove.mutate(item.id)}
            />
          ))}
        </ol>
      )}
    </div>
  )
}

function QueueRow({
  repo,
  item,
  removing,
  onRemove,
}: {
  repo: string
  item: QueueItem
  removing: boolean
  onRemove: () => void
}) {
  const isEpic = item.kind === 'epic'
  const isRunning = item.status === 'running'
  return (
    <li className="flex flex-col gap-2 rounded-md border border-border bg-secondary/20 px-3 py-3 sm:flex-row sm:items-start sm:gap-4">
      <span className="w-6 shrink-0 pt-0.5 font-mono text-sm text-muted-foreground tabular-nums">
        {item.position}
      </span>

      <div className="flex flex-1 flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2">
          {isRunning ? (
            <Link
              to="/runs/$repo/$ticket"
              params={{ repo, ticket: item.id }}
              className="font-mono text-sm text-primary underline-offset-2 hover:underline"
            >
              {item.id}
            </Link>
          ) : (
            <span className="font-mono text-sm text-primary">{item.id}</span>
          )}
          {item.title && (
            <span className="font-sans text-sm text-foreground">
              {item.title}
            </span>
          )}
          <span
            className={cn(
              'inline-flex items-center gap-1 rounded border px-1.5 py-0.5 font-mono text-[0.65rem]',
              isEpic
                ? 'border-info/50 bg-info/10 text-info'
                : 'border-border bg-muted/60 text-muted-foreground',
            )}
          >
            {isEpic && <ListTree className="size-3" aria-hidden="true" />}
            {item.kind}
          </span>
        </div>

        {isEpic && item.sub_issues && item.sub_issues.length > 0 && (
          <ul className="flex flex-col gap-1 border-l border-border pl-3">
            {item.sub_issues.map((sub) => (
              <li
                key={sub.id}
                className="flex flex-wrap items-center gap-2 font-mono text-xs text-muted-foreground"
              >
                <span className="text-foreground/80">{sub.id}</span>
                {sub.title && <span className="font-sans">{sub.title}</span>}
                <span className="rounded border border-border bg-muted/60 px-1 py-0.5 text-[0.6rem]">
                  {sub.state}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-2">
        <StatusPill state={statusState(item.status)} label={item.status} />
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="font-mono"
          onClick={onRemove}
          disabled={removing || isRunning}
        >
          <Trash2 className="size-3.5" aria-hidden="true" />
          {removing ? 'Removing…' : 'Remove'}
        </Button>
      </div>
    </li>
  )
}

function QueueSkeleton() {
  return (
    <ul className="flex flex-col gap-2" aria-busy="true">
      {[0, 1, 2].map((i) => (
        <li
          key={i}
          className="flex items-center gap-3 rounded-md border border-border bg-secondary/30 px-3 py-4"
        >
          <span className="h-3 w-6 animate-pulse rounded bg-muted" />
          <span className="h-3 w-16 animate-pulse rounded bg-muted" />
          <span className="h-3 w-2/5 animate-pulse rounded bg-muted" />
          <span className="ml-auto h-5 w-20 animate-pulse rounded bg-muted" />
        </li>
      ))}
    </ul>
  )
}
