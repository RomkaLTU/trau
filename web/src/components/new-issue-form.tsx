import { useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Check, ExternalLink, Plus } from 'lucide-react'

import { configQueryOptions } from '@/lib/config'
import { createIssue, type CreatedIssue, type IssueDraft } from '@/lib/issues'

export interface IssueDefaults {
  title?: string
  description?: string
  labels?: string[]
}

function parseLabels(text: string): string[] {
  return text
    .split(',')
    .map((l) => l.trim())
    .filter((l) => l !== '')
}

// suggestedLabels surfaces the repo's managed labels (ready, quarantine) as
// one-click chips so a filed issue can be made pickable without typing the exact
// label name. It degrades to nothing while the config loads or is unavailable.
function useSuggestedLabels(repo: string): string[] {
  const { data } = useQuery(configQueryOptions(repo))
  return useMemo(() => {
    const wanted = ['READY_LABEL', 'QUARANTINE_LABEL']
    return (data?.keys ?? [])
      .filter((k) => wanted.includes(k.key) && k.value !== '')
      .map((k) => k.value)
  }, [data])
}

export function NewIssueForm({
  repo,
  defaults,
  onCreated,
}: {
  repo: string
  defaults?: IssueDefaults
  onCreated?: (issue: CreatedIssue) => void
}) {
  const [title, setTitle] = useState(defaults?.title ?? '')
  const [description, setDescription] = useState(defaults?.description ?? '')
  const [labels, setLabels] = useState((defaults?.labels ?? []).join(', '))
  const suggested = useSuggestedLabels(repo)

  const mutation = useMutation({
    mutationFn: (draft: IssueDraft) => createIssue(repo, draft),
    onSuccess: (issue) => onCreated?.(issue),
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
      labels: parseLabels(labels),
    })
  }

  if (mutation.data) {
    return (
      <div className="flex flex-col gap-2 rounded-lg border border-emerald-500/40 bg-emerald-500/5 p-4 text-sm">
        <span className="text-emerald-600 dark:text-emerald-400">
          Created {mutation.data.provider} issue
        </span>
        <a
          href={mutation.data.url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex w-fit items-center gap-1.5 font-mono font-medium hover:underline"
        >
          {mutation.data.identifier}
          <ExternalLink className="size-3.5" />
        </a>
        <button
          type="button"
          onClick={() => mutation.reset()}
          className="w-fit text-xs text-muted-foreground hover:text-foreground"
        >
          File another
        </button>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-card p-4">
      <input
        type="text"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Issue title"
        className="h-9 w-full rounded-md border bg-transparent px-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      <textarea
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        placeholder="Description (markdown, optional)"
        rows={4}
        className="w-full resize-y rounded-md border bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      <input
        type="text"
        value={labels}
        onChange={(e) => setLabels(e.target.value)}
        placeholder="Labels (comma-separated)"
        className="h-9 w-full rounded-md border bg-transparent px-3 font-mono text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
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
          {mutation.isPending ? 'Creating…' : 'Create issue'}
        </button>
        {mutation.error && (
          <p className="text-xs text-destructive">
            {String((mutation.error as Error).message)}
          </p>
        )}
      </div>
    </div>
  )
}
