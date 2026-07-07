import { useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, ExternalLink, Send, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { SegmentedControl } from './segmented-control'
import { TerminalCard } from './terminal-card'
import { cn } from '@/lib/utils'
import { configQueryOptions } from '@/lib/config'
import { createIssue, type CreatedIssue, type IssueDraft } from '@/lib/issues'
import { publishPRD, type PublishedPRD } from '@/lib/prd'
import type { ParentOption } from '@/lib/backlog'

export type CreateMode = 'issue' | 'prd'

const MODE_OPTIONS = [
  { value: 'issue' as const, label: 'New issue' },
  { value: 'prd' as const, label: 'Publish PRD' },
]

const FIELD_LABEL =
  'font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground'

const INPUT =
  'w-full rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none'

function parseLabels(text: string): string[] {
  return text
    .split(',')
    .map((l) => l.trim())
    .filter((l) => l !== '')
}

function useReadyLabel(repo: string): string | undefined {
  const { data } = useQuery(configQueryOptions(repo))
  return useMemo(
    () => data?.keys?.find((k) => k.key === 'READY_LABEL')?.value || undefined,
    [data],
  )
}

export function BacklogCreate({
  repo,
  initialMode,
  seedParent,
  parents,
  onClose,
}: {
  repo: string
  initialMode: CreateMode
  seedParent?: string
  parents: ParentOption[]
  onClose: () => void
}) {
  const [mode, setMode] = useState<CreateMode>(initialMode)

  return (
    <TerminalCard title="create" className="max-w-3xl">
      <div className="flex flex-col gap-5">
        <div className="flex items-center justify-between gap-3">
          <SegmentedControl
            aria-label="Create mode"
            options={MODE_OPTIONS}
            value={mode}
            onChange={setMode}
          />
          <button
            type="button"
            onClick={onClose}
            aria-label="Close create panel"
            className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
          >
            <X className="size-4" aria-hidden="true" />
          </button>
        </div>

        {mode === 'issue' ? (
          <IssueForm repo={repo} seedParent={seedParent} parents={parents} />
        ) : (
          <PrdForm repo={repo} />
        )}
      </div>
    </TerminalCard>
  )
}

function IssueForm({
  repo,
  seedParent,
  parents,
}: {
  repo: string
  seedParent?: string
  parents: ParentOption[]
}) {
  const queryClient = useQueryClient()
  const readyLabel = useReadyLabel(repo)
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [labels, setLabels] = useState('')
  const [ready, setReady] = useState(false)
  const [parent, setParent] = useState(seedParent ?? '')

  const mutation = useMutation({
    mutationFn: (draft: IssueDraft) => createIssue(repo, draft),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['backlog', repo] })
    },
  })

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const trimmed = title.trim()
    if (trimmed === '') return
    const set = parseLabels(labels)
    if (ready && readyLabel && !set.includes(readyLabel)) set.push(readyLabel)
    mutation.mutate({
      title: trimmed,
      description: description.trim() || undefined,
      labels: set,
      parent: parent.trim() || undefined,
    })
  }

  if (mutation.data) {
    const created: CreatedIssue = mutation.data
    return (
      <div className="flex flex-col gap-3 font-mono text-sm">
        <span className="inline-flex items-center gap-2 text-done">
          <Check className="size-4" aria-hidden="true" />
          Filed to {created.provider}
          {parent ? ` under ${parent}` : ''}
        </span>
        <a
          href={created.url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex w-fit items-center gap-1.5 text-foreground hover:text-primary hover:underline"
        >
          {created.identifier}
          <ExternalLink className="size-3.5" aria-hidden="true" />
        </a>
        <button
          type="button"
          onClick={() => {
            mutation.reset()
            setTitle('')
            setDescription('')
            setLabels('')
            setReady(false)
          }}
          className="w-fit text-xs text-muted-foreground transition-colors hover:text-foreground"
        >
          {parent ? 'File another under this epic' : 'File another'}
        </button>
      </div>
    )
  }

  return (
    <form className="flex flex-col gap-5" onSubmit={submit}>
      <div className="flex flex-col gap-1.5">
        <label htmlFor="backlog-issue-title" className={FIELD_LABEL}>
          title
        </label>
        <input
          id="backlog-issue-title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Short, imperative summary"
          className={INPUT}
        />
      </div>

      <div className="flex flex-col gap-1.5">
        <label htmlFor="backlog-issue-description" className={FIELD_LABEL}>
          description (markdown)
        </label>
        <textarea
          id="backlog-issue-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={6}
          placeholder={'## Context\n\n## Acceptance criteria\n- [ ] ...'}
          className={cn(INPUT, 'resize-y leading-relaxed')}
        />
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="flex flex-col gap-1.5">
          <label htmlFor="backlog-issue-parent" className={FIELD_LABEL}>
            parent (epic)
          </label>
          <select
            id="backlog-issue-parent"
            value={parent}
            onChange={(e) => setParent(e.target.value)}
            className={cn(INPUT, 'appearance-none')}
          >
            <option value="">— none (top-level issue) —</option>
            {parents.map((p) => (
              <option key={p.id} value={p.id}>
                {p.isEpic ? '◆ ' : ''}
                {p.id} · {p.title}
              </option>
            ))}
          </select>
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor="backlog-issue-labels" className={FIELD_LABEL}>
            labels (comma-separated)
          </label>
          <input
            id="backlog-issue-labels"
            value={labels}
            onChange={(e) => setLabels(e.target.value)}
            placeholder="backend, spike"
            className={INPUT}
          />
        </div>
      </div>

      {readyLabel && (
        <label className="inline-flex w-fit cursor-pointer items-center gap-2 font-mono text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={ready}
            onChange={(e) => setReady(e.target.checked)}
            className="size-3.5 accent-primary"
          />
          Mark ready for the agent
          <span className="text-primary">({readyLabel})</span>
        </label>
      )}

      <div className="flex flex-wrap items-center gap-3 border-t border-border pt-4">
        <Button
          type="submit"
          size="sm"
          className="font-mono"
          disabled={mutation.isPending || title.trim() === ''}
        >
          <Check className="size-3.5" aria-hidden="true" />
          {mutation.isPending ? 'Filing…' : 'File issue'}
        </Button>
        {mutation.error && (
          <p className="font-mono text-xs text-fail">
            {String((mutation.error as Error).message)}
          </p>
        )}
      </div>
    </form>
  )
}

function PrdForm({ repo }: { repo: string }) {
  const queryClient = useQueryClient()
  const [title, setTitle] = useState('')
  const [markdown, setMarkdown] = useState('')

  const mutation = useMutation({
    mutationFn: () => publishPRD(repo, { title: title.trim(), markdown }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['backlog', repo] })
    },
  })

  if (mutation.data) {
    const published: PublishedPRD = mutation.data
    const kindLabel = published.kind === 'issue' ? 'issue' : 'project document'
    return (
      <div className="flex flex-col gap-3 font-mono text-sm">
        <span className="inline-flex items-center gap-2 text-done">
          <Check className="size-4" aria-hidden="true" />
          Published to {published.provider} as a {kindLabel}
        </span>
        <a
          href={published.url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex w-fit items-center gap-1.5 text-foreground hover:text-primary hover:underline"
        >
          {published.identifier ?? published.url}
          <ExternalLink className="size-3.5" aria-hidden="true" />
        </a>
        <button
          type="button"
          onClick={() => {
            mutation.reset()
            setTitle('')
            setMarkdown('')
          }}
          className="w-fit text-xs text-muted-foreground transition-colors hover:text-foreground"
        >
          Write another
        </button>
      </div>
    )
  }

  const canPublish = title.trim() !== '' && markdown.trim() !== ''

  return (
    <form
      className="flex flex-col gap-5"
      onSubmit={(e) => {
        e.preventDefault()
        if (canPublish) mutation.mutate()
      }}
    >
      <div className="flex flex-col gap-1.5">
        <label htmlFor="backlog-prd-title" className={FIELD_LABEL}>
          title
        </label>
        <input
          id="backlog-prd-title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="PRD title"
          className={INPUT}
        />
      </div>

      <div className="flex flex-col gap-1.5">
        <label htmlFor="backlog-prd-body" className={FIELD_LABEL}>
          prd.md
        </label>
        <textarea
          id="backlog-prd-body"
          value={markdown}
          onChange={(e) => setMarkdown(e.target.value)}
          rows={12}
          placeholder={'# Title\n\n## Problem\n\n## Goals\n\n## Slices'}
          className={cn(INPUT, 'resize-y leading-relaxed')}
        />
      </div>

      <div className="flex flex-wrap items-center gap-3 border-t border-border pt-4">
        <Button
          type="submit"
          size="sm"
          className="font-mono"
          disabled={mutation.isPending || !canPublish}
        >
          <Send className="size-3.5" aria-hidden="true" />
          {mutation.isPending ? 'Publishing…' : 'Publish PRD'}
        </Button>
        {mutation.error && (
          <p className="font-mono text-xs text-fail">
            {String((mutation.error as Error).message)}
          </p>
        )}
      </div>
    </form>
  )
}
