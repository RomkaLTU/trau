import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { Check, Lock, Pencil, Search, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  EmptyState,
  Eyebrow,
  RepoPicker,
  SegmentedControl,
  TerminalCard,
} from '@/components/trau'
import { cn } from '@/lib/utils'
import { reposQueryOptions } from '@/lib/runs'
import {
  configQueryOptions,
  writeConfig,
  type ConfigKey,
  type ConfigWrite,
} from '@/lib/config'

export const Route = createFileRoute('/settings')({
  component: Settings,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(reposQueryOptions),
})

function Settings() {
  const { data, error, isPending } = useQuery(reposQueryOptions)
  const [selected, setSelected] = useState<string | null>(null)

  const repos = data?.repos ?? []
  const active =
    selected && repos.some((r) => r.name === selected)
      ? selected
      : (repos.find((r) => r.live)?.name ?? repos[0]?.name ?? null)

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="action" className="text-primary">
          CONFIGURE
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Settings
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Layered config resolved from project → user → default. Edit any key and
          choose which layer the change writes to.
        </p>
      </header>

      {error && (
        <p className="font-mono text-sm text-destructive">{String(error)}</p>
      )}
      {isPending && !error && (
        <p className="font-mono text-sm text-muted-foreground">Loading…</p>
      )}

      {data && repos.length === 0 && (
        <EmptyState
          className="min-h-[300px]"
          message="No repos yet. A repo's layered config appears here once a trau loop has run in it."
        />
      )}

      {active && (
        <ConfigList
          repo={active}
          repos={repos.map((r) => r.name)}
          onRepoChange={setSelected}
        />
      )}
    </div>
  )
}

function ConfigList({
  repo,
  repos,
  onRepoChange,
}: {
  repo: string
  repos: string[]
  onRepoChange: (repo: string) => void
}) {
  const { data, error, isPending } = useQuery(configQueryOptions(repo))
  const [query, setQuery] = useState('')
  const [showAdvanced, setShowAdvanced] = useState(false)

  const keys = data?.keys ?? []
  const layers = data?.layers ?? ['project', 'user']

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    return keys.filter((k) => {
      if (!showAdvanced && k.advanced) return false
      if (!q) return true
      return (
        k.key.toLowerCase().includes(q) ||
        (k.description ?? '').toLowerCase().includes(q)
      )
    })
  }, [keys, query, showAdvanced])

  return (
    <>
      <div className="flex flex-wrap items-end gap-6">
        <label className="flex flex-col gap-1.5">
          <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
            search
          </span>
          <div className="relative w-72 max-w-full">
            <Search
              className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
              aria-hidden="true"
            />
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="search settings…"
              aria-label="Search settings"
              autoComplete="off"
              spellCheck={false}
              className="w-full rounded-md border border-border bg-input py-1.5 pl-8 pr-2.5 font-mono text-sm text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
            />
          </div>
        </label>
        <RepoPicker repos={repos} value={repo} onChange={onRepoChange} label="repo" />
        <label className="flex cursor-pointer items-center gap-2 font-mono text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={showAdvanced}
            onChange={(e) => setShowAdvanced(e.target.checked)}
            className="size-3.5 accent-primary"
          />
          show advanced
        </label>
        {keys.length > 0 && (
          <span className="ml-auto font-mono text-xs tabular-nums text-muted-foreground">
            {filtered.length} / {keys.length}
          </span>
        )}
      </div>

      {error && (
        <p className="font-mono text-sm text-destructive">{String(error)}</p>
      )}
      {isPending && !error && (
        <p className="font-mono text-sm text-muted-foreground">Loading…</p>
      )}

      {!isPending && !error && (
        <TerminalCard title="trau.ini" bodyClassName="p-0">
          <div className="divide-y divide-border/60">
            {filtered.map((item) => (
              <ConfigRow key={item.key} repo={repo} item={item} layers={layers} />
            ))}
            {filtered.length === 0 && (
              <p className="px-4 py-6 font-mono text-sm text-muted-foreground">
                No settings match “{query}”.
              </p>
            )}
          </div>
        </TerminalCard>
      )}
    </>
  )
}

const LAYER_STYLES: Record<string, string> = {
  project: 'border-teal/50 bg-teal/12 text-teal',
  user: 'border-info/50 bg-info/12 text-info',
  default: 'border-faint/50 bg-faint/12 text-faint',
  'env var': 'border-warn/50 bg-warn/12 text-warn',
  local: 'border-done/50 bg-done/12 text-done',
  CLI: 'border-primary/50 bg-primary/12 text-primary',
}

function SourceChip({ source }: { source: string }) {
  return (
    <span
      className={cn(
        'inline-flex items-center rounded border px-1.5 py-0.5 font-mono text-[0.65rem]',
        LAYER_STYLES[source] ?? LAYER_STYLES.default,
      )}
    >
      {source}
    </span>
  )
}

function displayValue(item: ConfigKey): string {
  if (item.secret) return item.set ? '••••••••' : '—'
  if (item.bool) return item.value === '1' ? 'on' : 'off'
  return item.value === '' ? '—' : item.value
}

function ConfigRow({
  repo,
  item,
  layers,
}: {
  repo: string
  item: ConfigKey
  layers: string[]
}) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(item.value)
  const [layer, setLayer] = useState(layers[0] ?? 'project')

  const mutation = useMutation({
    mutationFn: (body: ConfigWrite) => writeConfig(repo, body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['config', repo] })
      setEditing(false)
    },
  })

  const startEdit = () => {
    setDraft(item.value)
    setLayer(item.layer === 'user' ? 'user' : 'project')
    mutation.reset()
    setEditing(true)
  }

  return (
    <div className="flex flex-col gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span className="font-mono text-sm text-foreground">{item.key}</span>
        <SourceChip source={item.layer} />
        {item.secret && (
          <span className="inline-flex items-center gap-1 rounded border border-border bg-secondary/40 px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground">
            <Lock className="size-3" aria-hidden="true" />
            secret
          </span>
        )}
        {!editing && (
          <span
            className={cn(
              'ml-auto font-mono text-sm',
              item.value === '' && !item.set
                ? 'text-muted-foreground'
                : 'text-foreground',
            )}
          >
            {displayValue(item)}
          </span>
        )}
        {!editing && item.editable && (
          <Button
            variant="ghost"
            size="sm"
            className="font-mono text-muted-foreground hover:text-foreground"
            onClick={startEdit}
            aria-label={`Edit ${item.key}`}
          >
            <Pencil className="size-3.5" aria-hidden="true" />
            edit
          </Button>
        )}
        {!editing && !item.editable && (
          <Lock
            className="size-4 text-muted-foreground/60"
            aria-label="Read-only"
          />
        )}
      </div>

      {item.description && !editing && (
        <p className="text-xs leading-relaxed text-muted-foreground">
          {item.description}
        </p>
      )}

      {editing && (
        <div className="flex flex-col gap-3 rounded-md border border-border bg-input/40 p-3">
          {item.description && (
            <p className="text-xs leading-relaxed text-muted-foreground">
              {item.description}
            </p>
          )}
          <ValueEditor item={item} value={draft} onChange={setDraft} />

          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2">
              <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
                write to
              </span>
              <SegmentedControl
                aria-label="Write to layer"
                options={layers.map((l) => ({ value: l, label: l }))}
                value={layer}
                onChange={setLayer}
              />
            </div>

            <div className="ml-auto flex items-center gap-2">
              <Button
                variant="ghost"
                size="sm"
                className="font-mono"
                onClick={() => setEditing(false)}
                disabled={mutation.isPending}
              >
                <X className="size-3.5" aria-hidden="true" />
                Cancel
              </Button>
              <Button
                size="sm"
                className="font-mono"
                onClick={() =>
                  mutation.mutate({ key: item.key, value: draft, layer })
                }
                disabled={mutation.isPending}
              >
                <Check className="size-3.5" aria-hidden="true" />
                {mutation.isPending ? 'Saving…' : 'Save'}
              </Button>
            </div>
          </div>

          {mutation.error && (
            <p className="font-mono text-xs text-fail">
              {String((mutation.error as Error).message)}
            </p>
          )}
          {hasDefault(item) && (
            <p className="font-mono text-xs text-muted-foreground">
              default: <span className="text-foreground">{item.default}</span>
            </p>
          )}
        </div>
      )}
    </div>
  )
}

function hasDefault(item: ConfigKey): boolean {
  return (item.default ?? '') !== ''
}

function ValueEditor({
  item,
  value,
  onChange,
}: {
  item: ConfigKey
  value: string
  onChange: (v: string) => void
}) {
  if (item.bool) {
    return (
      <SegmentedControl
        aria-label={`${item.key} value`}
        options={[
          { value: '1', label: 'on' },
          { value: '0', label: 'off' },
        ]}
        value={value === '1' ? '1' : '0'}
        onChange={onChange}
      />
    )
  }

  if (item.options && item.options.length > 0) {
    return (
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="w-full max-w-xs rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground focus-visible:border-ring focus-visible:outline-none"
      >
        {item.options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    )
  }

  return (
    <input
      type="text"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={item.default ?? ''}
      className="w-full max-w-md rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
    />
  )
}
