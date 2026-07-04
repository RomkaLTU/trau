import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { Check, Lock, Pencil, Search, X } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
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
      {error && <p className="text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}

      {data && repos.length === 0 && (
        <Card className="max-w-md">
          <CardHeader>
            <CardTitle>Settings</CardTitle>
            <CardDescription>
              No repos yet. A repo's layered config appears here once a trau
              loop has run in it.
            </CardDescription>
          </CardHeader>
        </Card>
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
              {repo.live && (
                <span className="size-1.5 rounded-full bg-emerald-500" />
              )}
            </button>
          ))}
        </div>
      )}

      {active && <ConfigList repo={active} />}
    </div>
  )
}

function ConfigList({ repo }: { repo: string }) {
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

  if (error) return <p className="text-sm text-destructive">{String(error)}</p>
  if (isPending)
    return <p className="text-sm text-muted-foreground">Loading…</p>

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-full max-w-sm">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search settings…"
            className="h-9 w-full rounded-md border bg-transparent pl-9 pr-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
          />
        </div>
        <label className="flex cursor-pointer items-center gap-2 text-sm text-muted-foreground">
          <input
            type="checkbox"
            checked={showAdvanced}
            onChange={(e) => setShowAdvanced(e.target.checked)}
            className="size-4 accent-primary"
          />
          Show advanced
        </label>
        <span className="ml-auto text-xs tabular-nums text-muted-foreground">
          {filtered.length} / {keys.length}
        </span>
      </div>

      <div className="divide-y rounded-lg border bg-card">
        {filtered.map((item) => (
          <ConfigRow key={item.key} repo={repo} item={item} layers={layers} />
        ))}
        {filtered.length === 0 && (
          <p className="px-4 py-6 text-sm text-muted-foreground">
            No settings match “{query}”.
          </p>
        )}
      </div>
    </div>
  )
}

const layerStyle: Record<string, string> = {
  project: 'border-sky-500/40 bg-sky-500/10 text-sky-600 dark:text-sky-400',
  user: 'border-violet-500/40 bg-violet-500/10 text-violet-600 dark:text-violet-400',
  'env var':
    'border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400',
  local: 'border-teal-500/40 bg-teal-500/10 text-teal-600 dark:text-teal-400',
  CLI: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
}

function LayerBadge({ layer }: { layer: string }) {
  return (
    <Badge variant="outline" className={cn('font-normal', layerStyle[layer])}>
      {layer}
    </Badge>
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
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="font-mono text-sm font-medium">{item.key}</span>
        <LayerBadge layer={item.layer} />
        {item.secret && (
          <Badge
            variant="outline"
            className="gap-1 font-normal text-muted-foreground"
          >
            <Lock className="size-3" />
            secret
          </Badge>
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
          <button
            type="button"
            onClick={startEdit}
            className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            aria-label={`Edit ${item.key}`}
          >
            <Pencil className="size-4" />
          </button>
        )}
        {!editing && !item.editable && (
          <Lock
            className="size-4 text-muted-foreground/60"
            aria-label="Read-only"
          />
        )}
      </div>

      {item.description && !editing && (
        <p className="text-xs text-muted-foreground">{item.description}</p>
      )}

      {editing && (
        <div className="flex flex-col gap-3 rounded-md border bg-background p-3">
          {item.description && (
            <p className="text-xs text-muted-foreground">{item.description}</p>
          )}
          <ValueEditor item={item} value={draft} onChange={setDraft} />

          <div className="flex flex-wrap items-center gap-3">
            <label className="flex items-center gap-2 text-xs text-muted-foreground">
              Write to
              <select
                value={layer}
                onChange={(e) => setLayer(e.target.value)}
                className="h-8 rounded-md border bg-transparent px-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
              >
                {layers.map((l) => (
                  <option key={l} value={l}>
                    {l === 'project'
                      ? 'project (repo)'
                      : l === 'user'
                        ? 'user (~/)'
                        : l}
                  </option>
                ))}
              </select>
            </label>

            <div className="ml-auto flex items-center gap-2">
              <button
                type="button"
                onClick={() => setEditing(false)}
                disabled={mutation.isPending}
                className="flex items-center gap-1 rounded-md border px-2.5 py-1 text-sm text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
              >
                <X className="size-4" />
                Cancel
              </button>
              <button
                type="button"
                onClick={() =>
                  mutation.mutate({ key: item.key, value: draft, layer })
                }
                disabled={mutation.isPending}
                className="flex items-center gap-1 rounded-md bg-primary px-2.5 py-1 text-sm text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
              >
                <Check className="size-4" />
                {mutation.isPending ? 'Saving…' : 'Save'}
              </button>
            </div>
          </div>

          {mutation.error && (
            <p className="text-xs text-destructive">
              {String((mutation.error as Error).message)}
            </p>
          )}
          {hasDefault(item) && (
            <p className="text-xs text-muted-foreground">
              Default: <span className="font-mono">{item.default}</span>
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
      <div className="flex gap-2">
        {(['1', '0'] as const).map((v) => (
          <button
            key={v}
            type="button"
            onClick={() => onChange(v)}
            className={cn(
              'rounded-md border px-3 py-1 text-sm transition-colors',
              value === v
                ? 'border-transparent bg-accent text-accent-foreground'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {v === '1' ? 'on' : 'off'}
          </button>
        ))}
      </div>
    )
  }

  if (item.options && item.options.length > 0) {
    return (
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-9 w-full max-w-xs rounded-md border bg-transparent px-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
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
      className="h-9 w-full max-w-md rounded-md border bg-transparent px-3 font-mono text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
    />
  )
}
