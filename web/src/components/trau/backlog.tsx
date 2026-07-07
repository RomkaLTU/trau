import { useMemo } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { ListTree, Play, RefreshCw } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { EmptyState } from './empty-state'
import { StatusPill, type RunState } from './status-pill'
import { TerminalCard } from './terminal-card'
import { useActiveRepo } from './active-repo'
import { cn } from '@/lib/utils'
import {
  BacklogUnavailableError,
  backlogQueryOptions,
  groupBacklog,
  type BacklogEntry,
  type StatusGroupKey,
} from '@/lib/backlog'
import { startInstance } from '@/lib/instances'

const GROUP_PILL: Record<StatusGroupKey, RunState> = {
  started: 'active',
  unstarted: 'todo',
  backlog: 'info',
  done: 'success',
  canceled: 'fail',
  unknown: 'warn',
}

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

export function Backlog() {
  const navigate = useNavigate()
  const { repo: activeRepo, repos } = useActiveRepo()
  const repo = activeRepo ?? ''
  const startable = repos.filter((r) => r.allowed).map((r) => r.name)
  const canRun = repo !== '' && startable.includes(repo)

  const backlog = useQuery(backlogQueryOptions(repo))
  const items = backlog.data?.items ?? []
  const groups = useMemo(() => groupBacklog(items), [items])
  const titleById = useMemo(
    () => new Map(items.map((it) => [it.id, it.title])),
    [items],
  )

  const start = useMutation({
    mutationFn: (ticket: string) => startInstance({ repo, ticket }),
    onSuccess: (_res, ticket) => {
      void navigate({ to: '/live/$repo/$ticket', params: { repo, ticket } })
    },
  })

  if (repo === '') {
    return (
      <EmptyState
        message="No repo checked out yet. Register a repo to browse its backlog."
        actions={
          <Button asChild variant="outline" size="sm" className="font-mono">
            <Link to="/instances">Manage repos</Link>
          </Button>
        }
      />
    )
  }

  if (backlog.isLoading) return <BacklogSkeleton />

  if (backlog.error instanceof BacklogUnavailableError) {
    return <BacklogUnavailable message={backlog.error.message} />
  }

  if (backlog.error) {
    return (
      <TerminalCard title="backlog">
        <p className="font-mono text-sm text-destructive">
          {actionError(backlog.error)}
        </p>
      </TerminalCard>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="font-mono text-xs text-muted-foreground">
          {items.length} ticket{items.length === 1 ? '' : 's'}
          {backlog.data?.provider ? ` · ${backlog.data.provider}` : ''}
        </p>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="font-mono"
          onClick={() => void backlog.refetch()}
          disabled={backlog.isFetching}
        >
          <RefreshCw
            className={cn('size-3.5', backlog.isFetching && 'animate-spin')}
            aria-hidden="true"
          />
          {backlog.isFetching ? 'Refreshing…' : 'Refresh'}
        </Button>
      </div>

      {start.error && (
        <p className="font-mono text-xs text-destructive">
          {actionError(start.error)}
        </p>
      )}

      {groups.length === 0 ? (
        <EmptyState message={`No tickets in ${repo}'s Project backlog.`} />
      ) : (
        groups.map((group) => (
          <section key={group.key} className="flex flex-col gap-2">
            <div className="flex items-center gap-2">
              <StatusPill state={GROUP_PILL[group.key]} label={group.label} />
              <span className="font-mono text-xs text-muted-foreground">
                {group.items.length}
              </span>
            </div>
            <ul className="flex flex-col gap-2">
              {group.items.map((item) => (
                <BacklogRow
                  key={item.id}
                  item={item}
                  parentTitle={
                    item.parent ? titleById.get(item.parent) : undefined
                  }
                  canRun={canRun}
                  launching={start.isPending && start.variables === item.id}
                  onRun={() => start.mutate(item.id)}
                />
              ))}
            </ul>
          </section>
        ))
      )}
    </div>
  )
}

function BacklogRow({
  item,
  parentTitle,
  canRun,
  launching,
  onRun,
}: {
  item: BacklogEntry
  parentTitle?: string
  canRun: boolean
  launching: boolean
  onRun: () => void
}) {
  return (
    <li
      className={cn(
        'flex flex-col gap-2 rounded-md border bg-secondary/20 px-3 py-3 sm:flex-row sm:items-start sm:gap-4',
        item.ready ? 'border-l-2 border-l-teal border-border' : 'border-border',
      )}
    >
      <div className="flex flex-1 flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm text-primary">{item.id}</span>
          <span className="font-sans text-sm text-foreground">{item.title}</span>
          {item.has_children && (
            <span className="inline-flex items-center gap-1 rounded border border-info/50 bg-info/10 px-1.5 py-0.5 font-mono text-[0.65rem] text-info">
              <ListTree className="size-3" aria-hidden="true" />
              epic
            </span>
          )}
          {item.ready && (
            <span className="inline-flex items-center gap-1 rounded border border-teal/50 bg-teal/12 px-1.5 py-0.5 font-mono text-[0.65rem] text-teal">
              <span aria-hidden="true">●</span>
              ready
            </span>
          )}
        </div>

        {item.parent && (
          <span className="font-mono text-xs text-muted-foreground">
            ↳ {item.parent}
            {parentTitle ? ` · ${parentTitle}` : ''}
          </span>
        )}

        {item.labels.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {item.labels.map((label) => (
              <span
                key={label}
                className="w-fit rounded border border-border bg-muted/60 px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground"
              >
                {label}
              </span>
            ))}
          </div>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-2">
        <StatusPill
          state={GROUP_PILL[item.group as StatusGroupKey] ?? 'info'}
          label={item.status || item.group}
        />
        {canRun && !item.has_children && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="font-mono"
            onClick={onRun}
            disabled={launching}
          >
            <Play className="size-3.5" aria-hidden="true" />
            {launching ? 'Launching…' : 'Run once'}
          </Button>
        )}
      </div>
    </li>
  )
}

function BacklogUnavailable({ message }: { message: string }) {
  return (
    <TerminalCard title="backlog" className="max-w-3xl">
      <div className="flex flex-col items-start gap-4">
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          The backlog board reads the tracker directly, so it needs this repo's
          own API credentials. {message}
        </p>
        <Button asChild variant="outline" size="sm" className="font-mono">
          <Link to="/settings">Open settings</Link>
        </Button>
      </div>
    </TerminalCard>
  )
}

function BacklogSkeleton() {
  return (
    <ul className="flex flex-col gap-2" aria-busy="true">
      {[0, 1, 2, 3].map((i) => (
        <li
          key={i}
          className="flex items-center gap-3 rounded-md border border-border bg-secondary/30 px-3 py-4"
        >
          <span className="h-3 w-16 animate-pulse rounded bg-muted" />
          <span className="h-3 w-2/5 animate-pulse rounded bg-muted" />
          <span className="ml-auto h-5 w-20 animate-pulse rounded bg-muted" />
        </li>
      ))}
    </ul>
  )
}
