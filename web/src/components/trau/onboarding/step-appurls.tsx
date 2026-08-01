import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowRight, Plus, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { URLLink, WORKSPACE_HINT, WorkspaceBadge } from '@/components/trau/app-urls-panel'
import { appURLsQueryOptions } from '@/lib/app-urls'
import {
  appURLRowIssue,
  appURLRowsToWrite,
  appURLsCanContinue,
  blankAppURLAccount,
  blankAppURLRow,
  writeAppURLRows,
  type AppURLAccountFields,
  type AppURLRowFields,
} from '@/lib/onboarding'
import { Callout, FieldLabel, Hint, TextInput } from './ui'

export function StepAppURLs({
  repo,
  onBack,
  onContinue,
}: {
  repo: string
  onBack: () => void
  onContinue: () => void
}) {
  const queryClient = useQueryClient()
  const existing = useQuery(appURLsQueryOptions(repo))
  const [rows, setRows] = useState<AppURLRowFields[]>(() => [blankAppURLRow()])

  const patchRow = (index: number, patch: Partial<AppURLRowFields>) =>
    setRows((prev) => prev.map((r, i) => (i === index ? { ...r, ...patch } : r)))

  const save = useMutation({
    mutationFn: () => writeAppURLRows(repo, rows),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['app-urls', repo] })
      void queryClient.invalidateQueries({ queryKey: ['qa-accounts', repo] })
      onContinue()
    },
  })

  const pending = appURLRowsToWrite(rows).length
  const entries = existing.data ?? []

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">Where does this app run?</h2>
        <Hint>
          Browser verify drives these URLs when it checks the UI a run just changed. One
          URL is plenty for a single-app repo — a monorepo can name one per workspace.
        </Hint>
      </div>

      {entries.length > 0 && (
        <ul className="flex flex-col gap-1.5 rounded-md border border-border bg-secondary/20 px-3 py-2.5">
          {entries.map((entry) => (
            <li key={entry.id} className="flex flex-wrap items-center gap-2.5">
              <WorkspaceBadge workspace={entry.workspace} />
              <URLLink url={entry.url} />
            </li>
          ))}
        </ul>
      )}

      {rows.map((row, index) => (
        <URLRow
          key={index}
          index={index}
          row={row}
          issue={appURLRowIssue(rows, index)}
          removable={rows.length > 1}
          onChange={(patch) => patchRow(index, patch)}
          onRemove={() => setRows((prev) => prev.filter((_, i) => i !== index))}
        />
      ))}

      <button
        type="button"
        onClick={() => setRows((prev) => [...prev, blankAppURLRow()])}
        className="flex w-fit items-center gap-1.5 font-mono text-xs text-faint transition-colors hover:text-muted-foreground"
      >
        <Plus className="size-3.5" aria-hidden="true" />
        add another URL
      </button>

      {save.error && (
        <Callout tone="fail" title="Could not save the app URLs">
          {save.error.message} Fix the entry and try again, or skip — onboarding finishes
          either way.
        </Callout>
      )}

      <Callout
        tone="info"
        title="No app URL yet? Skip this step."
        actions={
          <Button type="button" variant="outline" size="sm" onClick={onContinue}>
            Skip for now
            <ArrowRight className="size-3.5" />
          </Button>
        }
      >
        Skipping writes nothing. Add URLs and their QA logins whenever you like from the
        app urls card on the Overview, or on the App URLs page.
      </Callout>

      <div className="flex items-center justify-between">
        <Button type="button" variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button
          type="button"
          onClick={() => save.mutate()}
          disabled={!appURLsCanContinue(rows) || save.isPending}
        >
          {save.isPending ? 'Saving…' : pending > 0 ? 'Save & continue' : 'Continue'}
          <ArrowRight className="size-4" />
        </Button>
      </div>
    </div>
  )
}

function URLRow({
  index,
  row,
  issue,
  removable,
  onChange,
  onRemove,
}: {
  index: number
  row: AppURLRowFields
  issue: string | null
  removable: boolean
  onChange: (patch: Partial<AppURLRowFields>) => void
  onRemove: () => void
}) {
  const patchAccount = (at: number, patch: Partial<AppURLAccountFields>) =>
    onChange({
      accounts: row.accounts.map((a, i) => (i === at ? { ...a, ...patch } : a)),
    })

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-secondary/20 p-3">
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="flex flex-col gap-1.5">
          <FieldLabel htmlFor={`app-url-${index}`}>url</FieldLabel>
          <TextInput
            id={`app-url-${index}`}
            value={row.url}
            onChange={(e) => onChange({ url: e.target.value })}
            placeholder="http://localhost:3000"
            invalid={issue === 'a URL is required'}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <FieldLabel htmlFor={`app-url-label-${index}`}>label</FieldLabel>
          <TextInput
            id={`app-url-label-${index}`}
            value={row.label}
            onChange={(e) => onChange({ label: e.target.value })}
            placeholder="storefront"
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <FieldLabel htmlFor={`app-url-workspace-${index}`}>workspace</FieldLabel>
          <TextInput
            id={`app-url-workspace-${index}`}
            value={row.workspace}
            onChange={(e) => onChange({ workspace: e.target.value })}
            placeholder="default"
          />
        </div>
      </div>

      <Hint>{WORKSPACE_HINT}</Hint>

      {row.accounts.map((account, at) => (
        <div key={at} className="flex flex-col gap-2 border-t border-border/60 pt-3">
          <div className="flex items-center justify-between">
            <FieldLabel>qa account</FieldLabel>
            <button
              type="button"
              onClick={() =>
                onChange({ accounts: row.accounts.filter((_, i) => i !== at) })
              }
              aria-label={`Remove QA account ${at + 1}`}
              className="rounded p-1 text-muted-foreground transition-colors hover:text-fail"
            >
              <X className="size-3.5" aria-hidden="true" />
            </button>
          </div>
          <div className="grid gap-2 sm:grid-cols-3">
            <TextInput
              value={account.label}
              onChange={(e) => patchAccount(at, { label: e.target.value })}
              placeholder="admin"
              aria-label={`QA account ${at + 1} label`}
            />
            <TextInput
              value={account.username}
              onChange={(e) => patchAccount(at, { username: e.target.value })}
              placeholder="qa@example.test"
              aria-label={`QA account ${at + 1} username`}
            />
            <TextInput
              type="password"
              autoComplete="new-password"
              value={account.secret}
              onChange={(e) => patchAccount(at, { secret: e.target.value })}
              placeholder="password"
              aria-label={`QA account ${at + 1} secret`}
            />
          </div>
          <TextInput
            value={account.description}
            onChange={(e) => patchAccount(at, { description: e.target.value })}
            placeholder="the cases and flows this account covers"
            aria-label={`QA account ${at + 1} coverage description`}
          />
        </div>
      ))}

      <div className="flex items-center justify-between">
        <button
          type="button"
          onClick={() => onChange({ accounts: [...row.accounts, blankAppURLAccount()] })}
          className="flex items-center gap-1.5 font-mono text-xs text-faint transition-colors hover:text-muted-foreground"
        >
          <Plus className="size-3.5" aria-hidden="true" />
          add QA account
        </button>
        {removable && (
          <button
            type="button"
            onClick={onRemove}
            aria-label={`Remove URL ${index + 1}`}
            className="rounded p-1 text-muted-foreground transition-colors hover:text-fail"
          >
            <X className="size-3.5" aria-hidden="true" />
          </button>
        )}
      </div>

      {issue && (
        <p className="font-mono text-xs text-fail" role="alert">
          {issue}
        </p>
      )}
    </div>
  )
}
