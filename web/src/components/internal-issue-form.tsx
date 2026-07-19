import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Plus, X } from 'lucide-react'

import { MarkdownEditor } from '@/components/markdown-editor'
import { cn } from '@/lib/utils'
import { configQueryOptions } from '@/lib/config'
import {
  INTERNAL_STATES,
  createInternalIssue,
  updateInternalIssue,
  type InternalIssue,
  type InternalIssueDraft,
} from '@/lib/issues'

function parseLabels(text: string): string[] {
  return text
    .split(',')
    .map((l) => l.trim())
    .filter((l) => l !== '')
}

// suggestedLabels surfaces the repo's managed labels (ready, quarantine) as
// one-click chips so a filed issue can be made pickable without typing the exact
// label name.
function useSuggestedLabels(repo: string): string[] {
  const { data } = useQuery(configQueryOptions(repo))
  return useMemo(() => {
    const wanted = ['READY_LABEL', 'QUARANTINE_LABEL']
    return (data?.keys ?? [])
      .filter((k) => wanted.includes(k.key) && k.value !== '')
      .map((k) => k.value)
  }, [data])
}

const inputClass =
  'h-9 w-full rounded-md border bg-transparent px-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50'

// InternalIssueForm creates a new internal issue or, when `issue` is given, edits
// an existing one. Internal issues live only in the hub store — no external
// tracker is touched. On success it invalidates the backlog so the board repaints
// and calls onDone.
export function InternalIssueForm({
  repo,
  issue,
  onDone,
  onCancel,
}: {
  repo: string
  issue?: InternalIssue
  onDone?: (issue: InternalIssue) => void
  onCancel?: () => void
}) {
  const queryClient = useQueryClient()
  const editing = issue !== undefined
  const [title, setTitle] = useState(issue?.title ?? '')
  const [description, setDescription] = useState(issue?.description ?? '')
  const [state, setState] = useState(issue?.state ?? 'backlog')
  const [labels, setLabels] = useState((issue?.labels ?? []).join(', '))
  const [parent, setParent] = useState(issue?.parent ?? '')
  const suggested = useSuggestedLabels(repo)

  const mutation = useMutation({
    mutationFn: (draft: InternalIssueDraft) =>
      editing
        ? updateInternalIssue(repo, issue.id, draft)
        : createInternalIssue(repo, draft),
    onSuccess: (saved) => {
      void queryClient.invalidateQueries({ queryKey: ['backlog', repo] })
      void queryClient.invalidateQueries({ queryKey: ['internal-issue', repo, saved.id] })
      onDone?.(saved)
    },
  })

  const addLabel = (label: string) => {
    const current = parseLabels(labels)
    if (current.includes(label)) return
    setLabels([...current, label].join(', '))
  }

  const submit = () => {
    const trimmed = title.trim()
    if (trimmed === '') return
    mutation.mutate({
      title: trimmed,
      description: description.trim() || undefined,
      state,
      labels: parseLabels(labels),
      parent: parent.trim() || undefined,
    })
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-card p-4">
      <input
        type="text"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Issue title"
        className={inputClass}
      />
      <MarkdownEditor
        placeholder="Description (markdown, optional)"
        defaultValue={issue?.description ?? ''}
        onChange={setDescription}
        editorClassName="min-h-20"
        contentClassName="max-h-64"
      />
      <div className="flex flex-wrap gap-3">
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          State
          <select
            value={state}
            onChange={(e) => setState(e.target.value)}
            className="h-9 rounded-md border bg-transparent px-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            {INTERNAL_STATES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-1 flex-col gap-1 text-xs text-muted-foreground">
          Parent epic (optional)
          <input
            type="text"
            value={parent}
            onChange={(e) => setParent(e.target.value)}
            placeholder="e.g. LOOP-4"
            className={cn(inputClass, 'font-mono')}
          />
        </label>
      </div>
      <input
        type="text"
        value={labels}
        onChange={(e) => setLabels(e.target.value)}
        placeholder="Labels (comma-separated)"
        className={cn(inputClass, 'font-mono')}
      />
      {suggested.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          {suggested.map((label) => (
            <button
              key={label}
              type="button"
              onClick={() => addLabel(label)}
              className="inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:border-ring/50 hover:text-foreground"
            >
              <Plus className="size-3" />
              {label}
            </button>
          ))}
        </div>
      )}

      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={submit}
          disabled={mutation.isPending || title.trim() === ''}
          className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
        >
          <Check className="size-4" />
          {mutation.isPending
            ? editing
              ? 'Saving…'
              : 'Creating…'
            : editing
              ? 'Save changes'
              : 'Create issue'}
        </button>
        {onCancel && (
          <button
            type="button"
            onClick={onCancel}
            className="inline-flex items-center gap-1.5 rounded-md border px-3 py-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
          >
            <X className="size-4" />
            Cancel
          </button>
        )}
        {mutation.error && (
          <p className="text-xs text-destructive">
            {String((mutation.error as Error).message)}
          </p>
        )}
      </div>
    </div>
  )
}
