import { GitBranch, Lock } from 'lucide-react'

import { cn } from '@/lib/utils'

/**
 * Read-only target-repo indicator for launch forms. Runs always execute in the
 * shell's Active repo, so the target is fixed — this makes that binding
 * explicit. Ported from the trau-cli-web design's locked "scoped" state; switch
 * repos from the sidebar switcher.
 */
export function TargetRepoField({
  repo,
  className,
}: {
  repo: string
  className?: string
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        target
      </span>
      <div
        className={cn(
          'flex w-56 items-center justify-between rounded-md border border-border bg-secondary/40 px-2.5 py-1.5',
          className,
        )}
      >
        <span className="inline-flex min-w-0 items-center gap-2 font-mono text-sm text-foreground">
          <GitBranch
            className="size-3.5 shrink-0 text-primary"
            aria-hidden="true"
          />
          <span className="truncate">{repo || '—'}</span>
        </span>
        <span
          className="inline-flex shrink-0 items-center gap-1 font-mono text-[0.65rem] text-muted-foreground"
          title="Scoped to the active repo — switch repos from the sidebar"
        >
          <Lock className="size-3" aria-hidden="true" />
          scoped
        </span>
      </div>
    </div>
  )
}
