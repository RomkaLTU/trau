import { Download, X } from 'lucide-react'

import { usePwaInstall } from '@/lib/pwa-install'

export function InstallBanner() {
  const { available, install, dismiss } = usePwaInstall()

  if (!available) return null

  return (
    <div className="mb-6 flex items-center gap-3 rounded-lg border bg-card px-4 py-2.5 text-sm">
      <Download className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <p className="min-w-0 truncate text-muted-foreground">
        trau can be installed as an app — it gets its own window and works
        offline.
      </p>
      <div className="ml-auto flex shrink-0 items-center gap-1">
        <button
          type="button"
          onClick={() => void install()}
          className="rounded-md border px-2.5 py-1 text-xs font-medium transition-colors hover:bg-accent"
        >
          Install
        </button>
        <button
          type="button"
          onClick={dismiss}
          aria-label="Dismiss the install banner"
          className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <X className="size-4" />
        </button>
      </div>
    </div>
  )
}
