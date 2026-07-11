import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { FilePlus, ListPlus, Pencil } from 'lucide-react'

import { PageHeader, ProjectScopeGate, useActiveRepo } from '@/components/trau'
import { InternalIssueForm } from '@/components/internal-issue-form'
import { backlogQueryOptions, type BacklogEntry } from '@/lib/backlog'
import { internalIssueQueryOptions } from '@/lib/issues'
import { enqueue } from '@/lib/queue'
import { cn } from '@/lib/utils'

export const Route = createFileRoute('/backlog')({
  component: BacklogPage,
})

function BacklogPage() {
  const { repo: activeRepo } = useActiveRepo()
  const repo = activeRepo ?? ''
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
  const backlog = useQuery(backlogQueryOptions(repo))
  const items = backlog.data?.items ?? []

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
                No issues yet — create one to get started.
              </li>
            )}
          </ul>
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
