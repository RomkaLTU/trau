import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Check,
  ExternalLink,
  Pencil,
  Plus,
  Trash2,
  TriangleAlert,
  X,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { TerminalCard } from '@/components/trau/terminal-card'
import { cn } from '@/lib/utils'
import {
  createAppURL,
  deleteAppURL,
  draftFor,
  draftIssue,
  updateAppURL,
  useAppURLTargets,
  workspaceLabel,
  type AppURL,
  type AppURLDraft,
  type AppURLFallback,
} from '@/lib/app-urls'

const WORKSPACE_HINT =
  'Leave blank for the repo default. In a monorepo, name the workspace by its manifest package name, its directory path, or its directory name — the slice whose changed files live there gets this URL.'

export function AppURLsSection({ repo }: { repo: string }) {
  const queryClient = useQueryClient()
  const { entries, fallback, isPending, error, refetch } = useAppURLTargets(repo)
  const [editing, setEditing] = useState<number | 'new' | null>(null)

  const done = () => {
    setEditing(null)
    void queryClient.invalidateQueries({ queryKey: ['app-urls', repo] })
  }

  return (
    <TerminalCard title="app urls" bodyClassName="p-0">
      <div className="flex flex-col">
        <p className="border-b border-border/60 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
          The URLs browser verify drives when it checks this repo&apos;s UI. One
          entry per workspace, plus an optional default for everything else.
        </p>

        {error ? (
          <div className="flex flex-col items-start gap-2 px-4 py-3">
            <p
              className="inline-flex items-center gap-2 font-mono text-xs text-fail"
              role="alert"
            >
              <TriangleAlert className="size-3.5" aria-hidden="true" />
              {error.message}
            </p>
            <Button
              variant="outline"
              size="sm"
              className="font-mono text-xs"
              onClick={refetch}
            >
              retry
            </Button>
          </div>
        ) : isPending ? (
          <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
            loading app urls…
          </p>
        ) : (
          <>
            {entries.length === 0 && fallback.length === 0 && (
              <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
                No app URLs yet — add the URL your app serves locally so verify
                can drive it in a browser.
              </p>
            )}
            {fallback.length > 0 && <FallbackRows fallback={fallback} />}
            {entries.map((entry) => (
              <EntryRow
                key={entry.id}
                repo={repo}
                entry={entry}
                entries={entries}
                editing={editing === entry.id}
                onToggle={() =>
                  setEditing(editing === entry.id ? null : entry.id)
                }
                onDone={done}
              />
            ))}
            {editing === 'new' && (
              <div className="border-b border-border/60 px-4 py-2.5">
                <EntryEditor
                  entry={null}
                  entries={entries}
                  write={(draft) => createAppURL(repo, draft)}
                  onDone={done}
                  onCancel={() => setEditing(null)}
                />
              </div>
            )}
            <button
              type="button"
              onClick={() => setEditing(editing === 'new' ? null : 'new')}
              aria-expanded={editing === 'new'}
              className="flex w-full items-center gap-2 px-4 py-2 font-mono text-xs text-faint transition-colors hover:bg-secondary/40 hover:text-muted-foreground"
            >
              <Plus className="size-3.5" aria-hidden="true" />
              add app url
            </button>
          </>
        )}
      </div>
    </TerminalCard>
  )
}

function FallbackRows({ fallback }: { fallback: AppURLFallback[] }) {
  return (
    <div className="border-b border-border/60 bg-warn/[0.04] px-4 py-2.5">
      <p className="text-xs leading-relaxed text-muted-foreground">
        No entries stored for this repo, so verify falls back to the{' '}
        <span className="font-mono text-foreground">APP_URL</span> and{' '}
        <span className="font-mono text-foreground">APP_URLS</span> keys in the
        ini. The first entry added here replaces both of them for every run.
      </p>
      <ul className="mt-2 flex flex-col gap-1.5">
        {fallback.map((f) => (
          <li key={f.workspace} className="flex flex-wrap items-center gap-2.5">
            <WorkspaceBadge workspace={f.workspace} />
            <URLLink url={f.url} />
            <Badge
              variant="outline"
              className="shrink-0 font-mono text-[0.7rem] font-normal text-muted-foreground"
            >
              from config
            </Badge>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function WorkspaceBadge({ workspace }: { workspace: string }) {
  return (
    <Badge
      variant="secondary"
      className={cn(
        'shrink-0 font-mono text-[0.7rem] font-normal',
        workspace === '' && 'text-muted-foreground',
      )}
    >
      {workspaceLabel(workspace)}
    </Badge>
  )
}

export function URLLink({ url }: { url: string }) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noreferrer"
      className="inline-flex min-w-0 items-center gap-1.5 font-mono text-xs text-primary underline-offset-4 hover:underline"
    >
      <span className="truncate">{url}</span>
      <ExternalLink className="size-3 shrink-0" aria-hidden="true" />
    </a>
  )
}

function EntryRow({
  repo,
  entry,
  entries,
  editing,
  onToggle,
  onDone,
}: {
  repo: string
  entry: AppURL
  entries: AppURL[]
  editing: boolean
  onToggle: () => void
  onDone: () => void
}) {
  const [confirmDelete, setConfirmDelete] = useState(false)
  const remove = useMutation({
    mutationFn: () => deleteAppURL(repo, entry.id),
    onSuccess: onDone,
  })

  return (
    <div
      className={cn(
        'group border-b border-border/60 px-4 py-2.5',
        editing && 'bg-secondary/20',
      )}
    >
      <div className="flex items-center gap-2.5">
        <WorkspaceBadge workspace={entry.workspace} />
        {entry.label !== '' && (
          <span className="min-w-0 truncate font-mono text-xs text-foreground">
            {entry.label}
          </span>
        )}
        <span className="ml-auto flex min-w-0 shrink items-center gap-2">
          <URLLink url={entry.url} />
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={editing}
            className="rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
            aria-label={`Edit ${workspaceLabel(entry.workspace)} app URL`}
          >
            <Pencil className="size-3.5" aria-hidden="true" />
          </button>
          <button
            type="button"
            onClick={() => setConfirmDelete(true)}
            disabled={remove.isPending}
            className="rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:text-fail focus-visible:opacity-100 group-hover:opacity-100"
            aria-label={`Delete ${workspaceLabel(entry.workspace)} app URL`}
          >
            <Trash2 className="size-3.5" aria-hidden="true" />
          </button>
        </span>
      </div>

      {remove.error && (
        <p className="mt-1 font-mono text-xs text-fail" role="alert">
          {remove.error.message}
        </p>
      )}

      {editing && (
        <div className="mt-2">
          <EntryEditor
            entry={entry}
            entries={entries}
            write={(draft) => updateAppURL(repo, entry.id, draft)}
            onDone={onDone}
            onCancel={onToggle}
          />
        </div>
      )}

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        windowTitle="delete app url"
        title={`Delete the ${workspaceLabel(entry.workspace)} app URL?`}
        description="Browser verify loses this target. If it was the last entry, the ini APP_URL and APP_URLS take over again."
        confirmLabel="Delete"
        destructive
        onConfirm={() => remove.mutate()}
      />
    </div>
  )
}

function EntryEditor({
  entry,
  entries,
  write,
  onDone,
  onCancel,
}: {
  entry: AppURL | null
  entries: AppURL[]
  write: (draft: AppURLDraft) => Promise<void>
  onDone: () => void
  onCancel: () => void
}) {
  const [draft, setDraft] = useState(() => draftFor(entry))
  const save = useMutation({
    mutationFn: () => write(draft),
    onSuccess: onDone,
  })

  const set = (patch: Partial<AppURLDraft>) =>
    setDraft((prev) => ({ ...prev, ...patch }))
  const issue = draftIssue(draft, entries, entry?.id ?? null)
  // A blank URL only greys out Save; the clash messages are worth spelling out.
  const message =
    (draft.url.trim() === '' ? null : issue) ??
    (save.error ? save.error.message : null)

  const fieldClass = 'h-auto px-3 py-1.5 font-mono text-xs placeholder:text-faint'

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-secondary/30 p-3">
      <div className="grid gap-3 sm:grid-cols-3">
        <FieldLabel text="label">
          <Input
            autoFocus
            value={draft.label}
            onChange={(e) => set({ label: e.target.value })}
            placeholder="storefront"
            autoComplete="off"
            spellCheck={false}
            aria-label="App URL label"
            className={fieldClass}
          />
        </FieldLabel>
        <FieldLabel text="url">
          <Input
            value={draft.url}
            onChange={(e) => set({ url: e.target.value })}
            placeholder="http://localhost:3000"
            autoComplete="off"
            spellCheck={false}
            aria-label="App URL"
            className={fieldClass}
          />
        </FieldLabel>
        <FieldLabel text="workspace">
          <Input
            value={draft.workspace}
            onChange={(e) => set({ workspace: e.target.value })}
            placeholder="default"
            autoComplete="off"
            spellCheck={false}
            aria-label="App URL workspace"
            className={fieldClass}
          />
        </FieldLabel>
      </div>

      <p className="text-xs leading-relaxed text-muted-foreground">
        {WORKSPACE_HINT}
      </p>

      <div className="flex items-center justify-end gap-2">
        <Button
          variant="ghost"
          size="sm"
          className="h-7 font-mono text-xs"
          onClick={onCancel}
          disabled={save.isPending}
        >
          <X className="size-3.5" aria-hidden="true" />
          Cancel
        </Button>
        <Button
          size="sm"
          className="h-7 font-mono text-xs"
          onClick={() => save.mutate()}
          disabled={save.isPending || issue !== null}
        >
          <Check className="size-3.5" aria-hidden="true" />
          {save.isPending ? 'Saving…' : 'Save'}
        </Button>
      </div>

      {message && (
        <p className="font-mono text-xs text-fail" role="alert">
          {message}
        </p>
      )}
    </div>
  )
}

function FieldLabel({
  text,
  children,
}: {
  text: string
  children: React.ReactNode
}) {
  return (
    <label className="flex min-w-0 flex-col gap-1">
      <span className="font-mono text-[0.7rem] uppercase tracking-wider text-faint">
        {text}
      </span>
      {children}
    </label>
  )
}
