import { RefreshCw } from 'lucide-react'

import { useReloadOnUpdate } from '@/lib/reload-on-update'

// ReloadOnUpdate reloads a page whose bundle no longer matches the hub. The
// notice is the fallback for a reload that did not take — the cached shell came
// back — where a second automatic attempt would only loop.
export function ReloadOnUpdate() {
  const stale = useReloadOnUpdate()

  if (!stale) return null

  return (
    <div
      role="status"
      className="animate-in fade-in slide-in-from-bottom-2 fixed bottom-6 left-1/2 z-50 flex -translate-x-1/2 items-center gap-3 rounded-lg border bg-card px-4 py-2.5 text-sm shadow-lg"
    >
      <RefreshCw className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <p className="text-muted-foreground">
        trau was updated — reload to get the new UI.
      </p>
      <button
        type="button"
        onClick={() => globalThis.location.reload()}
        className="rounded-md border px-2.5 py-1 text-xs font-medium transition-colors hover:bg-accent"
      >
        Reload
      </button>
    </div>
  )
}
