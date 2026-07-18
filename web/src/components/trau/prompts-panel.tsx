import { useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useBlocker } from '@tanstack/react-router'
import { Check, Pencil, RotateCcw, TriangleAlert, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { TerminalCard } from '@/components/trau/terminal-card'
import { cn } from '@/lib/utils'
import {
  globalSeed,
  matchesPrompt,
  promptsQueryOptions,
  repoPromptsQueryOptions,
  repoResetFallback,
  repoSeed,
  resetPrompt,
  resetRepoPrompt,
  writePrompt,
  writeRepoPrompt,
  type EffectiveSource,
  type Prompt,
  type PromptPlaceholder,
} from '@/lib/prompts'

export function PromptsSection({ query = '' }: { query?: string }) {
  const queryClient = useQueryClient()
  const { data, error, isPending, refetch } = useQuery(promptsQueryOptions)
  const [editing, setEditing] = useState<string | null>(null)

  const prompts = data?.prompts ?? []
  const visible = prompts.filter((p) => matchesPrompt(p, query))
  if (query !== '' && visible.length === 0) return null

  const done = () => {
    setEditing(null)
    void queryClient.invalidateQueries({ queryKey: ['prompts'] })
    void queryClient.invalidateQueries({ queryKey: ['repo-prompts'] })
  }

  return (
    <PromptsCard
      id="prompts"
      title="Prompts"
      description="Instruction templates trau sends to agents. A global override applies to every repo and takes effect from the next run."
      error={error}
      isPending={isPending}
      onRetry={refetch}
    >
      {visible.map((p) => (
        <PromptRow
          key={p.name}
          title={p.title}
          description={p.description}
          customized={p.override !== null}
          chips={p.override !== null && <CustomizedChip />}
          editing={editing === p.name}
          onToggle={() => setEditing(editing === p.name ? null : p.name)}
        >
          <PromptEditor
            prompt={p}
            seed={globalSeed(p)}
            showDefaultToggle={p.override !== null}
            reset={
              p.override !== null
                ? {
                    label: 'Reset to default',
                    title: `Reset “${p.title}” to the built-in default?`,
                    description:
                      'Removes the global override. Repos without their own override go back to the built-in prompt.',
                  }
                : null
            }
            write={(body) => writePrompt(p.name, body)}
            remove={() => resetPrompt(p.name)}
            onDone={done}
            onCancel={() => setEditing(null)}
          />
        </PromptRow>
      ))}
    </PromptsCard>
  )
}

export function RepoPromptsSection({
  repo,
  query = '',
}: {
  repo: string
  query?: string
}) {
  const queryClient = useQueryClient()
  const { data, error, isPending, refetch } = useQuery(
    repoPromptsQueryOptions(repo),
  )
  const [editing, setEditing] = useState<string | null>(null)

  const prompts = data?.prompts ?? []
  const visible = prompts.filter((p) => matchesPrompt(p, query))
  if (query !== '' && visible.length === 0) return null

  const done = () => {
    setEditing(null)
    void queryClient.invalidateQueries({ queryKey: ['repo-prompts', repo] })
  }

  return (
    <PromptsCard
      id="repo-prompts"
      title="Repo prompts"
      description="Prompt overrides for this repo only. A repo override beats the global one; resetting falls back to global, then the built-in default."
      error={error}
      isPending={isPending}
      onRetry={refetch}
    >
      {visible.map((p) => (
        <PromptRow
          key={p.name}
          title={p.title}
          description={p.description}
          customized={p.repo_override !== null}
          chips={<SourceChip source={p.effective} />}
          editing={editing === p.name}
          onToggle={() => setEditing(editing === p.name ? null : p.name)}
        >
          <PromptEditor
            prompt={p}
            seed={repoSeed(p)}
            showDefaultToggle={p.effective !== 'default'}
            reset={
              p.repo_override !== null
                ? {
                    label: 'Reset override',
                    title: `Remove this repo’s override for “${p.title}”?`,
                    description:
                      repoResetFallback(p) === 'global'
                        ? 'This repo goes back to the global override.'
                        : 'This repo goes back to the built-in default.',
                  }
                : null
            }
            write={(body) => writeRepoPrompt(repo, p.name, body)}
            remove={() => resetRepoPrompt(repo, p.name)}
            onDone={done}
            onCancel={() => setEditing(null)}
          />
        </PromptRow>
      ))}
    </PromptsCard>
  )
}

function PromptsCard({
  id,
  title,
  description,
  error,
  isPending,
  onRetry,
  children,
}: {
  id: string
  title: string
  description: string
  error: Error | null
  isPending: boolean
  onRetry: () => void
  children: ReactNode
}) {
  return (
    <section id={id} className="scroll-mt-6">
      <TerminalCard title={title} bodyClassName="p-0">
        <div className="flex flex-col">
          <p className="border-b border-border/60 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
            {description}
          </p>
          {error ? (
            <div className="flex flex-col items-start gap-2 px-4 py-3">
              <p
                className="inline-flex items-center gap-2 font-mono text-xs text-fail"
                role="alert"
              >
                <TriangleAlert className="size-3.5" aria-hidden="true" />
                {String(error.message)}
              </p>
              <Button
                variant="outline"
                size="sm"
                className="font-mono text-xs"
                onClick={onRetry}
              >
                retry
              </Button>
            </div>
          ) : isPending ? (
            <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
              loading prompts…
            </p>
          ) : (
            children
          )}
        </div>
      </TerminalCard>
    </section>
  )
}

function PromptRow({
  title,
  description,
  customized,
  chips,
  editing,
  onToggle,
  children,
}: {
  title: string
  description: string
  customized: boolean
  chips: ReactNode
  editing: boolean
  onToggle: () => void
  children: ReactNode
}) {
  return (
    <div
      className={cn(
        'group border-b border-border/60 px-4 py-2.5 last:border-0',
        customized && 'bg-warn/[0.04]',
        editing && 'bg-secondary/20',
      )}
    >
      <div className="flex items-center gap-2.5">
        <span
          aria-hidden="true"
          className={cn(
            'size-1.5 shrink-0 rounded-full',
            customized ? 'bg-warn' : 'bg-transparent',
          )}
          title={customized ? 'overridden' : undefined}
        />

        <span className="min-w-0 truncate font-mono text-xs text-foreground">
          {title}
        </span>

        {chips}

        <span className="ml-auto flex shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={editing}
            className="rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
            aria-label={`Edit ${title}`}
          >
            <Pencil className="size-3.5" aria-hidden="true" />
          </button>
        </span>
      </div>

      <p className="mt-1 pl-4 text-xs leading-relaxed text-muted-foreground">
        {description}
      </p>

      {editing && <div className="mt-2 pl-4">{children}</div>}
    </div>
  )
}

function CustomizedChip() {
  return (
    <span className="inline-flex shrink-0 items-center rounded border border-warn/50 bg-warn/12 px-1.5 py-0.5 font-mono text-[0.65rem] leading-none text-warn">
      customized
    </span>
  )
}

const SOURCE_STYLES: Record<EffectiveSource, string> = {
  repo: 'border-teal/50 bg-teal/12 text-teal',
  global: 'border-info/50 bg-info/12 text-info',
  default: 'border-faint/50 bg-faint/12 text-faint',
}

const SOURCE_LABELS: Record<EffectiveSource, string> = {
  repo: 'this repo',
  global: 'global',
  default: 'default',
}

function SourceChip({ source }: { source: EffectiveSource }) {
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center rounded border px-1.5 py-0.5 font-mono text-[0.65rem] leading-none',
        SOURCE_STYLES[source],
      )}
    >
      {SOURCE_LABELS[source]}
    </span>
  )
}

interface ResetAction {
  label: string
  title: string
  description: string
}

function PromptEditor({
  prompt,
  seed,
  showDefaultToggle,
  reset,
  write,
  remove,
  onDone,
  onCancel,
}: {
  prompt: Prompt
  seed: string
  showDefaultToggle: boolean
  reset: ResetAction | null
  write: (body: string) => Promise<void>
  remove: () => Promise<void>
  onDone: () => void
  onCancel: () => void
}) {
  const [draft, setDraft] = useState(seed)
  const [showDefault, setShowDefault] = useState(false)
  const [confirmReset, setConfirmReset] = useState(false)

  const dirty = draft !== seed

  const blocker = useBlocker({
    shouldBlockFn: () => dirty,
    withResolver: true,
    disabled: !dirty,
    enableBeforeUnload: () => dirty,
  })

  const save = useMutation({
    mutationFn: () => write(draft),
    onSuccess: onDone,
  })
  const resetMut = useMutation({
    mutationFn: remove,
    onSuccess: onDone,
  })

  const busy = save.isPending || resetMut.isPending
  const failure = save.error ?? resetMut.error

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-secondary/30 p-3">
      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_15rem]">
        <textarea
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape') onCancel()
          }}
          rows={16}
          spellCheck={false}
          aria-label={`${prompt.title} template`}
          className="w-full resize-y rounded-md border border-border bg-input px-3 py-2 font-mono text-xs leading-relaxed text-foreground focus-visible:border-ring focus-visible:outline-none"
        />
        <PlaceholderReference placeholders={prompt.placeholders} />
      </div>

      {showDefault && (
        <div className="flex flex-col gap-1">
          <span className="font-mono text-[0.7rem] uppercase tracking-wider text-faint">
            built-in default
          </span>
          <pre className="max-h-64 overflow-y-auto whitespace-pre-wrap rounded-md border border-border/60 bg-input/50 px-3 py-2 font-mono text-xs leading-relaxed text-muted-foreground">
            {prompt.default}
          </pre>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        {showDefaultToggle && (
          <button
            type="button"
            onClick={() => setShowDefault((v) => !v)}
            className="font-mono text-xs text-muted-foreground underline-offset-2 hover:underline"
          >
            {showDefault ? 'hide built-in default' : 'view built-in default'}
          </button>
        )}
        <span className="ml-auto flex items-center gap-2">
          {reset && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 font-mono text-xs text-muted-foreground hover:text-warn"
              onClick={() => setConfirmReset(true)}
              disabled={busy}
            >
              <RotateCcw className="size-3.5" aria-hidden="true" />
              {reset.label}
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            className="h-7 font-mono text-xs"
            onClick={onCancel}
            disabled={busy}
          >
            <X className="size-3.5" aria-hidden="true" />
            Cancel
          </Button>
          <Button
            size="sm"
            className="h-7 font-mono text-xs"
            onClick={() => save.mutate()}
            disabled={busy || !dirty}
          >
            <Check className="size-3.5" aria-hidden="true" />
            {save.isPending ? 'Saving…' : 'Save'}
          </Button>
        </span>
      </div>

      {failure && (
        <p className="font-mono text-xs text-fail" role="alert">
          {String((failure as Error).message)}
        </p>
      )}

      {reset && (
        <ConfirmDialog
          open={confirmReset}
          onOpenChange={setConfirmReset}
          windowTitle="reset prompt"
          title={reset.title}
          description={reset.description}
          confirmLabel="Reset"
          destructive
          onConfirm={() => resetMut.mutate()}
        />
      )}

      {blocker.status === 'blocked' && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) blocker.reset()
          }}
          windowTitle="unsaved changes"
          title="Discard unsaved prompt edits?"
          description={`“${prompt.title}” has changes that are not saved.`}
          confirmLabel="Discard"
          destructive
          onConfirm={blocker.proceed}
        />
      )}
    </div>
  )
}

function PlaceholderReference({
  placeholders,
}: {
  placeholders: PromptPlaceholder[]
}) {
  return (
    <aside className="flex min-w-0 flex-col gap-2">
      <span className="font-mono text-[0.7rem] uppercase tracking-wider text-faint">
        placeholders
      </span>
      {placeholders.length === 0 ? (
        <p className="text-xs leading-relaxed text-muted-foreground">
          This prompt takes no placeholders.
        </p>
      ) : (
        <ul className="flex flex-col gap-1.5">
          {placeholders.map((ph) => (
            <li key={ph.name} className="flex flex-col gap-0.5">
              <span className="inline-flex items-center gap-1.5">
                <code className="rounded bg-secondary/60 px-1 py-0.5 font-mono text-[0.7rem] text-foreground">
                  {`{{.${ph.name}}}`}
                </code>
                {ph.required && (
                  <span className="font-mono text-[0.65rem] text-warn">
                    required
                  </span>
                )}
              </span>
              {ph.description && (
                <span className="text-[0.7rem] leading-relaxed text-muted-foreground">
                  {ph.description}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </aside>
  )
}
