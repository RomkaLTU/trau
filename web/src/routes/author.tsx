import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import MDEditor from '@uiw/react-md-editor'
import { Check, ExternalLink, Send } from 'lucide-react'

import '@uiw/react-md-editor/markdown-editor.css'

import { Button } from '@/components/ui/button'
import {
  EmptyState,
  Eyebrow,
  SegmentedControl,
  TerminalCard,
  useActiveRepo,
} from '@/components/trau'
import { cn } from '@/lib/utils'
import { reposQueryOptions } from '@/lib/runs'
import { configQueryOptions } from '@/lib/config'
import { createIssue, type IssueDraft } from '@/lib/issues'
import {
  clearDraft,
  draftIsEmpty,
  loadDraft,
  publishPRD,
  saveDraft,
  type PRDDraft,
} from '@/lib/prd'

type AuthorTab = 'issue' | 'prd'

const TAB_OPTIONS = [
  { value: 'issue' as const, label: 'New issue' },
  { value: 'prd' as const, label: 'PRD' },
]

export const Route = createFileRoute('/author')({
  validateSearch: (search: Record<string, unknown>): { tab: AuthorTab } => ({
    tab: search.tab === 'prd' ? 'prd' : 'issue',
  }),
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(reposQueryOptions),
  component: Author,
})

function Author() {
  const { tab } = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const { repo: active, repos } = useActiveRepo()

  const setTab = (next: AuthorTab) =>
    navigate({ search: (prev) => ({ ...prev, tab: next }) })

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="action" className="text-primary">
          AUTHOR
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Author work
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Draft a new issue or a full PRD, then hand it to the agent.
        </p>
      </header>

      <SegmentedControl
        aria-label="Author mode"
        options={TAB_OPTIONS}
        value={tab}
        onChange={setTab}
      />

      {repos.length === 0 && (
        <EmptyState
          className="min-h-[300px]"
          message="No repos yet. Issues and PRDs are filed to a repo's configured tracker, so one needs to appear here first."
        />
      )}

      {active && tab === 'issue' && <NewIssuePanel repo={active} />}
      {active && tab === 'prd' && <PrdPanel key={active} repo={active} />}
    </div>
  )
}

function useManagedLabels(repo: string): { ready?: string; suggested: string[] } {
  const { data } = useQuery(configQueryOptions(repo))
  return useMemo(() => {
    const valueOf = (key: string) =>
      data?.keys?.find((k) => k.key === key)?.value || undefined
    const ready = valueOf('READY_LABEL')
    const quarantine = valueOf('QUARANTINE_LABEL')
    const suggested = [ready, quarantine].filter((v): v is string => !!v)
    return { ready, suggested }
  }, [data])
}

const LABEL_FIELD =
  'font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground'

function NewIssuePanel({ repo }: { repo: string }) {
  const { ready, suggested } = useManagedLabels(repo)
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [labels, setLabels] = useState<string[]>([])
  const [labelInput, setLabelInput] = useState('')
  const [error, setError] = useState<string | null>(null)

  const mutation = useMutation({
    mutationFn: (draft: IssueDraft) => createIssue(repo, draft),
  })

  const chips = useMemo(() => {
    const set = [...suggested]
    for (const l of labels) if (!set.includes(l)) set.push(l)
    return set
  }, [suggested, labels])

  const toggleLabel = (label: string) =>
    setLabels((prev) =>
      prev.includes(label) ? prev.filter((l) => l !== label) : [...prev, label],
    )

  const addLabel = () => {
    const label = labelInput.trim()
    if (label && !labels.includes(label)) setLabels((prev) => [...prev, label])
    setLabelInput('')
  }

  function submit(e: FormEvent) {
    e.preventDefault()
    const trimmed = title.trim()
    if (!trimmed) {
      setError('Title is required.')
      return
    }
    setError(null)
    mutation.mutate({
      title: trimmed,
      description: description.trim() || undefined,
      labels,
    })
  }

  return (
    <div className="grid grid-cols-1 gap-6 lg:grid-cols-11">
      <div className="lg:col-span-6">
        <TerminalCard title="new-issue">
          <form className="flex flex-col gap-6" onSubmit={submit}>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="issue-title" className={LABEL_FIELD}>
                title
              </label>
              <input
                id="issue-title"
                value={title}
                onChange={(e) => {
                  setTitle(e.target.value)
                  if (error) setError(null)
                }}
                placeholder="Short, imperative summary"
                aria-invalid={!!error}
                className={cn(
                  'w-full rounded-md border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-faint focus-visible:outline-none',
                  error
                    ? 'border-fail focus-visible:border-fail'
                    : 'border-border focus-visible:border-ring',
                )}
              />
              {error && (
                <p
                  className="inline-flex items-center gap-1.5 font-mono text-xs text-fail"
                  role="alert"
                >
                  <span aria-hidden="true">✗</span>
                  {error}
                </p>
              )}
            </div>

            <div className="flex flex-col gap-1.5">
              <label htmlFor="issue-description" className={LABEL_FIELD}>
                description (markdown)
              </label>
              <textarea
                id="issue-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={7}
                placeholder={'## Context\n\n## Acceptance criteria\n- [ ] ...'}
                className="w-full resize-y rounded-md border border-border bg-input px-2.5 py-2 font-mono text-sm leading-relaxed text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
              />
            </div>

            <div className="flex flex-col gap-1.5">
              <span className={LABEL_FIELD}>labels</span>
              <div className="flex flex-wrap items-center gap-2">
                {chips.map((label) => {
                  const active = labels.includes(label)
                  return (
                    <button
                      key={label}
                      type="button"
                      onClick={() => toggleLabel(label)}
                      aria-pressed={active}
                      className={cn(
                        'inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 font-mono text-xs transition-colors',
                        active
                          ? 'border-primary bg-primary/10 text-primary'
                          : 'border-border bg-secondary/30 text-muted-foreground hover:text-foreground',
                      )}
                    >
                      {active && <Check className="size-3" aria-hidden="true" />}
                      {label}
                    </button>
                  )
                })}
                <input
                  value={labelInput}
                  onChange={(e) => setLabelInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ',') {
                      e.preventDefault()
                      addLabel()
                    }
                  }}
                  placeholder="add label"
                  aria-label="Add label"
                  className="w-28 rounded-md border border-dashed border-border bg-transparent px-2 py-0.5 font-mono text-xs text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
                />
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-3 border-t border-border pt-4">
              <Button
                type="submit"
                size="sm"
                className="font-mono"
                disabled={mutation.isPending}
              >
                {mutation.isPending ? 'Creating…' : 'Create issue'}
              </Button>
              {mutation.data && (
                <a
                  href={mutation.data.url}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1.5 font-mono text-xs text-done hover:underline"
                >
                  <Check className="size-3.5" aria-hidden="true" />
                  {mutation.data.identifier} created
                  <ExternalLink className="size-3 opacity-70" aria-hidden="true" />
                </a>
              )}
              {mutation.error && (
                <p className="font-mono text-xs text-fail">
                  {String((mutation.error as Error).message)}
                </p>
              )}
            </div>
          </form>
        </TerminalCard>
      </div>

      <div className="flex flex-col gap-6 lg:col-span-5">
        <TerminalCard title="preview" scanlines>
          <div className="flex flex-col gap-3 font-mono text-sm">
            <p className="text-teal">
              <span aria-hidden="true">▸</span> {repo}
            </p>
            <p className="text-foreground">
              {title.trim() || (
                <span className="text-muted-foreground">untitled issue</span>
              )}
            </p>
            <div className="flex flex-wrap gap-1.5">
              {labels.length ? (
                labels.map((l) => (
                  <span
                    key={l}
                    className="rounded border border-border bg-muted/60 px-1.5 py-0.5 text-[0.65rem] text-muted-foreground"
                  >
                    {l}
                  </span>
                ))
              ) : (
                <span className="text-[0.65rem] text-muted-foreground">
                  no labels
                </span>
              )}
            </div>
          </div>
        </TerminalCard>

        <TerminalCard title="tip">
          <p className="font-sans text-sm leading-relaxed text-muted-foreground">
            Issues tagged{' '}
            <span className="font-mono text-primary">
              {ready ?? 'ready-for-agent'}
            </span>{' '}
            show up in the run pickers automatically.
          </p>
        </TerminalCard>
      </div>
    </div>
  )
}

function PrdPanel({ repo }: { repo: string }) {
  const [draft, setDraft] = useState<PRDDraft>(
    () => loadDraft(repo) ?? { title: '', markdown: '' },
  )

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
      <div className="flex flex-col gap-6">
        <TerminalCard title="published">
          <div className="flex flex-col gap-3 font-mono text-sm">
            <span className="inline-flex items-center gap-2 text-done">
              <Check className="size-4" aria-hidden="true" />
              Published to {mutation.data.provider} as a {kindLabel}
            </span>
            <a
              href={mutation.data.url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex w-fit items-center gap-1.5 text-foreground hover:text-primary hover:underline"
            >
              {mutation.data.identifier ?? mutation.data.url}
              <ExternalLink className="size-3.5" aria-hidden="true" />
            </a>
            <button
              type="button"
              onClick={() => {
                mutation.reset()
                setDraft({ title: '', markdown: '' })
              }}
              className="w-fit text-xs text-muted-foreground transition-colors hover:text-foreground"
            >
              Write another
            </button>
          </div>
        </TerminalCard>
      </div>
    )
  }

  const canPublish =
    draft.title.trim() !== '' && draft.markdown.trim() !== ''

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-end gap-3">
        <Button
          size="sm"
          className="font-mono"
          onClick={() => mutation.mutate()}
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

      <input
        type="text"
        value={draft.title}
        onChange={(e) => setDraft((d) => ({ ...d, title: e.target.value }))}
        placeholder="PRD title"
        className="w-full rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
      />

      <TerminalCard title="prd.md" bodyClassName="p-0">
        <div data-color-mode="dark" className="overflow-hidden">
          <MDEditor
            value={draft.markdown}
            onChange={(v) => setDraft((d) => ({ ...d, markdown: v ?? '' }))}
            height={460}
            preview="live"
          />
        </div>
      </TerminalCard>
    </div>
  )
}
