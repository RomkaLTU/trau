import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import MDEditor from '@uiw/react-md-editor'
import { Check, ExternalLink, FileText, Send } from 'lucide-react'

import '@uiw/react-md-editor/markdown-editor.css'

import { cn } from '@/lib/utils'
import { reposQueryOptions } from '@/lib/runs'
import {
  clearDraft,
  draftIsEmpty,
  loadDraft,
  publishPRD,
  saveDraft,
  type PRDDraft,
} from '@/lib/prd'

export const Route = createFileRoute('/prd')({
  component: PRD,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(reposQueryOptions),
})

function useColorMode(): 'light' | 'dark' {
  const [mode, setMode] = useState<'light' | 'dark'>(() =>
    typeof window !== 'undefined' &&
    window.matchMedia('(prefers-color-scheme: dark)').matches
      ? 'dark'
      : 'light',
  )
  useEffect(() => {
    const m = window.matchMedia('(prefers-color-scheme: dark)')
    const apply = () => setMode(m.matches ? 'dark' : 'light')
    m.addEventListener('change', apply)
    return () => m.removeEventListener('change', apply)
  }, [])
  return mode
}

function PRD() {
  const { data, error, isPending } = useQuery(reposQueryOptions)
  const [selected, setSelected] = useState<string | null>(null)

  const repos = data?.repos ?? []
  const active =
    selected && repos.some((r) => r.name === selected)
      ? selected
      : repos[0]?.name ?? null

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="flex items-center gap-2 text-lg font-semibold">
          <FileText className="size-5 text-muted-foreground" />
          PRD
        </h1>
        <p className="text-sm text-muted-foreground">
          Draft a product requirements doc and publish it to the repo's tracker.
          Drafts are saved in this browser until you publish.
        </p>
      </div>

      {error && <p className="text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}
      {data && repos.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No repos yet. A PRD is published to a repo's configured tracker, so one
          needs to appear here first.
        </p>
      )}

      {repos.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          {repos.map((repo) => (
            <button
              key={repo.root}
              type="button"
              title={repo.root}
              onClick={() => setSelected(repo.name)}
              className={cn(
                'flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm transition-colors',
                repo.name === active
                  ? 'border-transparent bg-accent text-accent-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {repo.name}
            </button>
          ))}
        </div>
      )}

      {active && <Editor key={active} repo={active} />}
    </div>
  )
}

function Editor({ repo }: { repo: string }) {
  const [draft, setDraft] = useState<PRDDraft>(
    () => loadDraft(repo) ?? { title: '', markdown: '' },
  )
  const mode = useColorMode()

  useEffect(() => {
    if (draftIsEmpty(draft)) {
      clearDraft(repo)
    } else {
      saveDraft(repo, draft)
    }
  }, [repo, draft])

  const mutation = useMutation({
    mutationFn: () =>
      publishPRD(repo, { title: draft.title.trim(), markdown: draft.markdown }),
    onSuccess: () => clearDraft(repo),
  })

  if (mutation.data) {
    const kindLabel =
      mutation.data.kind === 'issue' ? 'issue' : 'project document'
    return (
      <div className="flex flex-col gap-2 rounded-lg border border-emerald-500/40 bg-emerald-500/5 p-4 text-sm">
        <span className="text-emerald-600 dark:text-emerald-400">
          Published to {mutation.data.provider} as a {kindLabel}
        </span>
        <a
          href={mutation.data.url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex w-fit items-center gap-1.5 font-medium hover:underline"
        >
          {mutation.data.identifier ?? mutation.data.url}
          <ExternalLink className="size-3.5" />
        </a>
        <button
          type="button"
          onClick={() => {
            mutation.reset()
            setDraft({ title: '', markdown: '' })
          }}
          className="w-fit text-xs text-muted-foreground hover:text-foreground"
        >
          Write another
        </button>
      </div>
    )
  }

  const canPublish = draft.title.trim() !== '' && draft.markdown.trim() !== ''

  return (
    <div className="flex flex-col gap-3">
      <input
        type="text"
        value={draft.title}
        onChange={(e) => setDraft((d) => ({ ...d, title: e.target.value }))}
        placeholder="PRD title"
        className="h-10 w-full rounded-md border bg-transparent px-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      <div data-color-mode={mode} className="overflow-hidden rounded-md border">
        <MDEditor
          value={draft.markdown}
          onChange={(v) => setDraft((d) => ({ ...d, markdown: v ?? '' }))}
          height={480}
          preview="live"
        />
      </div>
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={() => mutation.mutate()}
          disabled={mutation.isPending || !canPublish}
          className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
        >
          {mutation.isPending ? (
            <Send className="size-4" />
          ) : (
            <Check className="size-4" />
          )}
          {mutation.isPending ? 'Publishing…' : 'Publish'}
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
