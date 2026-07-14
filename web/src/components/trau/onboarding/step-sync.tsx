import { useEffect, useRef } from 'react'
import { useMutation } from '@tanstack/react-query'
import { ArrowRight, RotateCw, Settings2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  seedSync,
  type SyncResponse,
  type TrackerProvider,
} from '@/lib/onboarding'
import { cn } from '@/lib/utils'
import { Callout, Hint } from './ui'

export function StepSync({
  repo,
  provider,
  onSynced,
  onBackToTracker,
  onContinue,
}: {
  repo: string
  provider: TrackerProvider
  onSynced: (result: SyncResponse | null) => void
  onBackToTracker: () => void
  onContinue: () => void
}) {
  const internal = provider === 'internal'

  const sync = useMutation({
    mutationFn: () => seedSync(repo),
    onSuccess: (res) => onSynced(res),
  })

  const started = useRef(false)
  useEffect(() => {
    if (started.current) return
    started.current = true
    if (internal) {
      onSynced(null)
      return
    }
    sync.mutate()
  }, [])

  const done = internal || sync.isSuccess

  const glyph = internal || sync.isSuccess ? '✓' : sync.isError ? '✗' : '●'
  const glyphColor =
    internal || sync.isSuccess ? 'text-done' : sync.isError ? 'text-fail' : 'text-teal'

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">
          {internal ? 'No backlog to seed' : 'Seeding the backlog'}
        </h2>
        <Hint>
          {internal
            ? 'This project uses the internal issue store — there is nothing to pull from an external tracker.'
            : 'Pulling every issue and comment from the tracker into the hub store.'}
        </Hint>
      </div>

      <div className="overflow-hidden rounded-md border border-border bg-card">
        <div className="flex items-center gap-2 border-b border-border px-3 py-2">
          <span aria-hidden="true" className={glyphColor}>
            {glyph}
          </span>
          <span className="font-mono text-xs text-muted-foreground">
            seed-sync · {repo} · {provider}
          </span>
        </div>
        <div className="px-3 py-3 font-mono text-sm" aria-live="polite">
          {internal ? (
            <span className="text-done">ready — internal store, no external sync</span>
          ) : sync.isPending ? (
            <span className="text-muted-foreground">
              seeding<span className="cursor-block text-teal">▍</span>
            </span>
          ) : sync.isSuccess ? (
            <span className="text-done">done — {sync.data.issues} issues synced</span>
          ) : sync.isError ? (
            <span className="text-fail">error — {(sync.error as Error).message}</span>
          ) : null}
        </div>
      </div>

      {sync.isSuccess && (
        <div className="flex flex-wrap gap-2">
          <SyncStat label="issues" value={sync.data.issues} />
          <SyncStat label="comments" value={sync.data.comments} />
        </div>
      )}

      {sync.isError && (
        <Callout
          tone="fail"
          title="Seed sync failed"
          actions={
            <>
              <Button type="button" variant="outline" size="sm" onClick={onBackToTracker}>
                <Settings2 className="size-3.5" />
                Back to tracker
              </Button>
              <Button type="button" variant="outline" size="sm" onClick={() => sync.mutate()}>
                <RotateCw className="size-3.5" />
                Retry sync
              </Button>
            </>
          }
        >
          {(sync.error as Error).message} Fix the tracker credentials or binding, then retry — the
          backlog stays empty until the seed sync succeeds.
        </Callout>
      )}

      <div className="flex items-center justify-end gap-3">
        {sync.isPending && (
          <span className="font-sans text-xs text-muted-foreground">
            You can leave — this finishes in the background.
          </span>
        )}
        <Button type="button" onClick={onContinue} disabled={!done}>
          Continue <ArrowRight className="size-4" />
        </Button>
      </div>
    </div>
  )
}

function SyncStat({ label, value }: { label: string; value: number }) {
  return (
    <div className={cn('flex flex-col rounded-md border border-border bg-secondary/20 px-3 py-2')}>
      <span className="font-mono text-lg text-foreground">{value}</span>
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.15em] text-muted-foreground">
        {label}
      </span>
    </div>
  )
}
