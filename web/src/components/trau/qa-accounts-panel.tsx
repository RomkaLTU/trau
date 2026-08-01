import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Pencil, Plus, Trash2, TriangleAlert, X } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { TerminalCard } from '@/components/trau/terminal-card'
import { WorkspaceBadge } from '@/components/trau/app-urls-panel'
import { cn } from '@/lib/utils'
import { appURLsQueryOptions, workspaceLabel, type AppURL } from '@/lib/app-urls'
import {
  attachedEntry,
  createQAAccount,
  deleteQAAccount,
  draftFor,
  groupQAAccounts,
  qaAccountsQueryOptions,
  qaNotesQueryOptions,
  updateQAAccount,
  writeQANotes,
  type QAAccount,
  type QAAccountDraft,
} from '@/lib/qa'

const MASKED = '••••••••'

const ANY_URL = 'any URL'

// Radix rejects an empty option value, so the unattached choice carries a name.
const UNATTACHED = 'any'

export function QAAccountsSection({ repo }: { repo: string }) {
  const queryClient = useQueryClient()
  const accountsQuery = useQuery(qaAccountsQueryOptions(repo))
  const notesQuery = useQuery(qaNotesQueryOptions(repo))
  const entriesQuery = useQuery(appURLsQueryOptions(repo))
  const [editing, setEditing] = useState<number | 'new' | null>(null)
  const [notesEditing, setNotesEditing] = useState(false)

  const accounts = accountsQuery.data ?? []
  const entries = entriesQuery.data ?? []
  const notes = notesQuery.data?.notes ?? ''
  const groups = groupQAAccounts(accounts, entries)

  const error = accountsQuery.error ?? notesQuery.error ?? entriesQuery.error
  const isPending =
    accountsQuery.isPending || notesQuery.isPending || entriesQuery.isPending

  const done = () => {
    setEditing(null)
    void queryClient.invalidateQueries({ queryKey: ['qa-accounts', repo] })
  }

  return (
    <section id="qa-accounts" className="scroll-mt-6">
      <TerminalCard title="QA accounts" bodyClassName="p-0">
        <div className="flex flex-col">
          <p className="border-b border-border/60 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
            Login accounts the verify phase uses to sign into the app in the
            browser. An account attached to an app URL is offered only to the
            verify driving it; one left on {ANY_URL} is offered to all of them.
            Secrets are write-only: settable here, never shown back.
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
                onClick={() => {
                  void accountsQuery.refetch()
                  void notesQuery.refetch()
                  void entriesQuery.refetch()
                }}
              >
                retry
              </Button>
            </div>
          ) : isPending ? (
            <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
              loading qa accounts…
            </p>
          ) : (
            <>
              {accounts.length === 0 && entries.length === 0 && (
                <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
                  No QA accounts yet — add the logins the verify phase should
                  use to sign into the app in the browser.
                </p>
              )}
              {groups.map((group) => (
                <div key={group.entry?.id ?? 'unattached'}>
                  {entries.length > 0 && (
                    <GroupHeader
                      entry={group.entry}
                      empty={group.accounts.length === 0}
                    />
                  )}
                  {group.accounts.map((a) => (
                    <AccountRow
                      key={a.id}
                      repo={repo}
                      account={a}
                      entries={entries}
                      editing={editing === a.id}
                      onToggle={() => setEditing(editing === a.id ? null : a.id)}
                      onDone={done}
                    />
                  ))}
                </div>
              ))}
              {editing === 'new' && (
                <div className="border-b border-border/60 px-4 py-2.5">
                  <AccountEditor
                    account={null}
                    entries={entries}
                    write={(draft) => createQAAccount(repo, draft)}
                    onDone={done}
                    onCancel={() => setEditing(null)}
                  />
                </div>
              )}
              <button
                type="button"
                onClick={() => setEditing(editing === 'new' ? null : 'new')}
                aria-expanded={editing === 'new'}
                className="flex w-full items-center gap-2 border-b border-border/60 px-4 py-2 font-mono text-xs text-faint transition-colors hover:bg-secondary/40 hover:text-muted-foreground"
              >
                <Plus className="size-3.5" aria-hidden="true" />
                add account
              </button>
              <NotesRow
                repo={repo}
                notes={notes}
                editing={notesEditing}
                onToggle={() => setNotesEditing(!notesEditing)}
                onDone={() => {
                  setNotesEditing(false)
                  void queryClient.invalidateQueries({
                    queryKey: ['qa-notes', repo],
                  })
                }}
              />
            </>
          )}
        </div>
      </TerminalCard>
    </section>
  )
}

function GroupHeader({
  entry,
  empty,
}: {
  entry: AppURL | null
  empty: boolean
}) {
  return (
    <div className="flex items-center gap-2.5 border-b border-border/60 bg-secondary/20 px-4 py-1.5">
      {entry ? (
        <>
          <WorkspaceBadge workspace={entry.workspace} />
          <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">
            {entryName(entry)}
          </span>
        </>
      ) : (
        <span className="font-mono text-xs text-muted-foreground">{ANY_URL}</span>
      )}
      {empty && (
        <span className="ml-auto shrink-0 font-mono text-[0.7rem] text-faint">
          no accounts
        </span>
      )}
    </div>
  )
}

function AttachmentSelect({
  entries,
  selected,
  onChange,
}: {
  entries: AppURL[]
  selected: AppURL | null
  onChange: (id: number | null) => void
}) {
  return (
    <Select
      value={selected === null ? UNATTACHED : String(selected.id)}
      onValueChange={(v) => onChange(v === UNATTACHED ? null : Number(v))}
    >
      <SelectTrigger
        size="sm"
        className="w-full font-mono text-xs"
        aria-label="QA account app URL"
      >
        {selected ? entryOption(selected) : ANY_URL}
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={UNATTACHED} className="font-mono text-xs">
          {ANY_URL}
        </SelectItem>
        {entries.map((entry) => (
          <SelectItem
            key={entry.id}
            value={String(entry.id)}
            className="font-mono text-xs"
          >
            {entryOption(entry)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function entryName(entry: AppURL): string {
  return entry.label !== '' ? entry.label : entry.url
}

function entryOption(entry: AppURL): string {
  return `${workspaceLabel(entry.workspace)} · ${entryName(entry)}`
}

function AccountRow({
  repo,
  account,
  entries,
  editing,
  onToggle,
  onDone,
}: {
  repo: string
  account: QAAccount
  entries: AppURL[]
  editing: boolean
  onToggle: () => void
  onDone: () => void
}) {
  const [confirmDelete, setConfirmDelete] = useState(false)
  const remove = useMutation({
    mutationFn: () => deleteQAAccount(repo, account.id),
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
        <span className="min-w-0 truncate font-mono text-xs text-foreground">
          {account.label}
        </span>
        {account.username !== '' && (
          <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">
            {account.username}
          </span>
        )}
        {account.source === 'agent' && (
          <Badge
            variant="secondary"
            className="shrink-0 font-mono text-[0.7rem] font-normal"
          >
            Captured by agent
          </Badge>
        )}
        <span className="ml-auto flex shrink-0 items-center gap-2">
          <span
            className={cn(
              'font-mono text-xs',
              account.secret_set ? 'text-foreground' : 'text-faint',
            )}
          >
            {account.secret_set ? MASKED : '—'}
          </span>
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={editing}
            className="rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
            aria-label={`Edit ${account.label}`}
          >
            <Pencil className="size-3.5" aria-hidden="true" />
          </button>
          <button
            type="button"
            onClick={() => setConfirmDelete(true)}
            disabled={remove.isPending}
            className="rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:text-fail focus-visible:opacity-100 group-hover:opacity-100"
            aria-label={`Delete ${account.label}`}
          >
            <Trash2 className="size-3.5" aria-hidden="true" />
          </button>
        </span>
      </div>

      {account.description !== '' && (
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          {account.description}
        </p>
      )}

      {remove.error && (
        <p className="mt-1 font-mono text-xs text-fail" role="alert">
          {String(remove.error.message)}
        </p>
      )}

      {editing && (
        <div className="mt-2">
          <AccountEditor
            account={account}
            entries={entries}
            write={(draft) => updateQAAccount(repo, account.id, draft)}
            onDone={onDone}
            onCancel={onToggle}
          />
        </div>
      )}

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        windowTitle="delete qa account"
        title={`Delete the QA account “${account.label}”?`}
        description="The verify phase loses this login. The stored secret is removed with it."
        confirmLabel="Delete"
        destructive
        onConfirm={() => remove.mutate()}
      />
    </div>
  )
}

function AccountEditor({
  account,
  entries,
  write,
  onDone,
  onCancel,
}: {
  account: QAAccount | null
  entries: AppURL[]
  write: (draft: QAAccountDraft) => Promise<void>
  onDone: () => void
  onCancel: () => void
}) {
  const [draft, setDraft] = useState(() => draftFor(account))
  const attached = attachedEntry(entries, draft.app_url_id)
  const save = useMutation({
    mutationFn: () => write({ ...draft, app_url_id: attached?.id ?? null }),
    onSuccess: onDone,
  })

  const set = (patch: Partial<QAAccountDraft>) =>
    setDraft((prev) => ({ ...prev, ...patch }))
  const valid = draft.label.trim() !== ''

  const fieldClass = 'h-auto px-3 py-1.5 font-mono text-xs placeholder:text-faint'

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-secondary/30 p-3">
      <div className="grid gap-3 sm:grid-cols-3">
        <FieldLabel text="label">
          <Input
            autoFocus
            value={draft.label}
            onChange={(e) => set({ label: e.target.value })}
            placeholder="admin"
            autoComplete="off"
            spellCheck={false}
            aria-label="QA account label"
            className={fieldClass}
          />
        </FieldLabel>
        <FieldLabel text="username">
          <Input
            value={draft.username}
            onChange={(e) => set({ username: e.target.value })}
            placeholder="qa@example.test"
            autoComplete="off"
            spellCheck={false}
            aria-label="QA account username"
            className={fieldClass}
          />
        </FieldLabel>
        <FieldLabel text="secret">
          <Input
            type="password"
            value={draft.secret}
            onChange={(e) => set({ secret: e.target.value })}
            placeholder={
              account?.secret_set ? 'unchanged — enter to replace' : 'enter secret'
            }
            autoComplete="new-password"
            aria-label="QA account secret"
            className={fieldClass}
          />
        </FieldLabel>
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        <FieldLabel text="app url">
          <AttachmentSelect
            entries={entries}
            selected={attached}
            onChange={(id) => set({ app_url_id: id })}
          />
        </FieldLabel>
      </div>
      <FieldLabel text="covers">
        <Textarea
          value={draft.description}
          onChange={(e) => set({ description: e.target.value })}
          rows={2}
          placeholder="the cases and flows this account covers"
          spellCheck={false}
          aria-label="QA account coverage description"
          className={cn(fieldClass, 'resize-y leading-relaxed')}
        />
      </FieldLabel>

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
          disabled={save.isPending || !valid}
        >
          <Check className="size-3.5" aria-hidden="true" />
          {save.isPending ? 'Saving…' : 'Save'}
        </Button>
      </div>

      {save.error && (
        <p className="font-mono text-xs text-fail" role="alert">
          {String(save.error.message)}
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

function NotesRow({
  repo,
  notes,
  editing,
  onToggle,
  onDone,
}: {
  repo: string
  notes: string
  editing: boolean
  onToggle: () => void
  onDone: () => void
}) {
  return (
    <div
      className={cn(
        'group px-4 py-2.5',
        notes !== '' && 'bg-warn/[0.04]',
        editing && 'bg-secondary/20',
      )}
    >
      <div className="flex items-center gap-2.5">
        <span
          aria-hidden="true"
          className={cn(
            'size-1.5 shrink-0 rounded-full',
            notes !== '' ? 'bg-warn' : 'bg-transparent',
          )}
          title={notes !== '' ? 'notes set' : undefined}
        />
        <span className="min-w-0 truncate font-mono text-xs text-foreground">
          QA notes
        </span>
        <span className="ml-auto flex shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={editing}
            className="rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
            aria-label="Edit QA notes"
          >
            <Pencil className="size-3.5" aria-hidden="true" />
          </button>
        </span>
      </div>

      <p className="mt-1 pl-4 text-xs leading-relaxed text-muted-foreground">
        Disposable-user recipes, login quirks, and cleanup rules the verify
        phase should know about.
      </p>

      {editing && (
        <div className="mt-2 pl-4">
          <NotesEditor repo={repo} notes={notes} onDone={onDone} onCancel={onToggle} />
        </div>
      )}
    </div>
  )
}

function NotesEditor({
  repo,
  notes,
  onDone,
  onCancel,
}: {
  repo: string
  notes: string
  onDone: () => void
  onCancel: () => void
}) {
  const [draft, setDraft] = useState(notes)
  const save = useMutation({
    mutationFn: () => writeQANotes(repo, draft),
    onSuccess: onDone,
  })
  const dirty = draft !== notes

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-secondary/30 p-3">
      <Textarea
        autoFocus
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') onCancel()
        }}
        rows={8}
        spellCheck={false}
        aria-label="QA notes"
        className="resize-y font-mono text-xs leading-relaxed"
      />
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
          disabled={save.isPending || !dirty}
        >
          <Check className="size-3.5" aria-hidden="true" />
          {save.isPending ? 'Saving…' : 'Save'}
        </Button>
      </div>
      {save.error && (
        <p className="font-mono text-xs text-fail" role="alert">
          {String(save.error.message)}
        </p>
      )}
    </div>
  )
}
