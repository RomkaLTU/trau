import type { ReactNode } from 'react'
import { FolderGit2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { useActiveRepo } from '@/components/trau/active-repo'
import { cn } from '@/lib/utils'

/**
 * Gates page content that acts on a single repo. Under "All projects" the
 * children are dimmed and made inert (no focus, no clicks) and a single overlay
 * asks the user to pick a project. "Pick a project" auto-scopes to a lone or
 * last-used repo, and only falls back to opening the switcher when there's a real
 * choice to make — so the gate points at the fix instead of dead-ending.
 */
export function ProjectScopeGate({
  children,
  action = 'continue',
  className,
}: {
  children: ReactNode
  /** Short description of the action that needs a project, e.g. "run a ticket". */
  action?: string
  className?: string
}) {
  const { isAll, autoScope, openSwitcher } = useActiveRepo()

  function pick() {
    if (!autoScope()) openSwitcher()
  }

  return (
    <div className={cn('relative', className)}>
      <div
        inert={isAll || undefined}
        aria-hidden={isAll || undefined}
        className={cn(
          'transition-opacity duration-200',
          isAll && 'pointer-events-none select-none opacity-40 blur-[1px]',
        )}
      >
        {children}
      </div>

      {isAll && (
        <div
          role="status"
          className="absolute inset-0 z-10 flex items-start justify-center pt-16"
        >
          <div className="flex max-w-sm flex-col items-center gap-3 rounded-md border border-border bg-popover px-6 py-5 text-center shadow-lg">
            <span className="flex size-9 items-center justify-center rounded-md border border-border bg-secondary/60">
              <FolderGit2 className="size-4 text-primary" aria-hidden="true" />
            </span>
            <p className="font-mono text-sm text-foreground">
              Select a project to {action}
            </p>
            <p className="font-sans text-xs leading-relaxed text-muted-foreground">
              This page acts on a single repo. Pick a project — the target here
              follows that scope.
            </p>
            <Button size="sm" className="font-mono" onClick={pick}>
              Pick a project
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
