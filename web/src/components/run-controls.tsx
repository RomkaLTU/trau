import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { CirclePlay, FlaskConical } from 'lucide-react'

import { dryRun, startInstance, type StartRequest } from '@/lib/instances'

type Mode = 'ticket' | 'epic'

const modeCopy: Record<Mode, { label: string; placeholder: string }> = {
  ticket: { label: 'Single ticket', placeholder: 'COD-693' },
  epic: { label: 'Epic', placeholder: 'COD-530' },
}

export function RunControls({ repo }: { repo: string }) {
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<Mode>('ticket')
  const [id, setId] = useState('')
  const [provider, setProvider] = useState('')

  const start = useMutation({
    mutationFn: (req: StartRequest) => startInstance(req),
    onSuccess: () => {
      setId('')
      void queryClient.invalidateQueries({ queryKey: ['instances'] })
    },
  })

  const preview = useMutation({
    mutationFn: () => dryRun(repo),
  })

  const submit = () => {
    const trimmed = id.trim()
    if (trimmed === '') return
    const p = provider.trim() || undefined
    start.mutate(
      mode === 'ticket'
        ? { repo, ticket: trimmed, provider: p }
        : { repo, epic: trimmed, provider: p },
    )
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-card p-4">
      <div className="flex items-center gap-2 text-sm font-medium">
        <CirclePlay className="size-4 text-muted-foreground" />
        {repo}
      </div>

      <div className="inline-flex w-fit rounded-md border p-0.5">
        {(Object.keys(modeCopy) as Mode[]).map((m) => (
          <button
            key={m}
            type="button"
            onClick={() => setMode(m)}
            className={`rounded px-2.5 py-1 text-xs transition-colors ${
              mode === m
                ? 'bg-primary text-primary-foreground'
                : 'text-muted-foreground hover:text-foreground'
            }`}
          >
            {modeCopy[m].label}
          </button>
        ))}
      </div>

      <input
        type="text"
        value={id}
        onChange={(e) => setId(e.target.value)}
        onKeyDown={(e) => e.key === 'Enter' && submit()}
        placeholder={modeCopy[mode].placeholder}
        className="h-9 w-full rounded-md border bg-transparent px-3 font-mono text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      <input
        type="text"
        value={provider}
        onChange={(e) => setProvider(e.target.value)}
        placeholder="Provider override (optional)"
        className="h-9 w-full rounded-md border bg-transparent px-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
      />

      <div className="flex flex-wrap items-center gap-3">
        <button
          type="button"
          onClick={submit}
          disabled={start.isPending || id.trim() === ''}
          className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
        >
          <CirclePlay className="size-4" />
          {start.isPending ? 'Starting…' : 'Run'}
        </button>
        <button
          type="button"
          onClick={() => preview.mutate()}
          disabled={preview.isPending}
          className="inline-flex items-center gap-1.5 rounded-md border px-3 py-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
        >
          <FlaskConical className="size-4" />
          {preview.isPending ? 'Previewing…' : 'Preview next'}
        </button>
      </div>

      {start.data && (
        <p className="text-xs text-emerald-600 dark:text-emerald-400">
          Started run — PID {start.data.pid}. Watch it in the instances above.
        </p>
      )}
      {start.error && (
        <p className="text-xs text-destructive">
          {String((start.error as Error).message)}
        </p>
      )}
      {preview.data && (
        <p className="text-xs text-muted-foreground">
          {preview.data.ticket ? (
            <>
              Next up: <span className="font-mono text-foreground">{preview.data.ticket}</span>
            </>
          ) : (
            'Nothing eligible right now.'
          )}
        </p>
      )}
      {preview.error && (
        <p className="text-xs text-destructive">
          {String((preview.error as Error).message)}
        </p>
      )}
    </div>
  )
}
