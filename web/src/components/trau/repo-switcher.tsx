import { useEffect, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Check, ChevronsUpDown, GitBranch, Plus } from 'lucide-react'

import { useActiveRepo } from '@/components/trau/active-repo'
import type { RepoView } from '@/lib/instances'
import { cn } from '@/lib/utils'

export function RepoSwitcher() {
  const { repo, repos, setRepo } = useActiveRepo()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    function onPointerDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  const active = repos.find((r) => r.name === repo)

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="listbox"
        aria-expanded={open}
        className="flex w-full items-center gap-2.5 rounded-md border border-border bg-input px-2.5 py-2 text-left transition-colors hover:border-ring/50"
      >
        <RepoIcon live={active?.live ?? false} />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate font-mono text-sm text-foreground">
            {repo ?? 'no repo'}
          </span>
          <span className="truncate font-mono text-[0.65rem] text-muted-foreground">
            {active ? active.root : `${repos.length} registered`}
          </span>
        </span>
        <ChevronsUpDown
          className="size-3.5 shrink-0 text-muted-foreground"
          aria-hidden="true"
        />
      </button>

      {open && (
        <div
          role="listbox"
          className="absolute left-0 right-0 z-30 mt-1 overflow-hidden rounded-md border border-border bg-popover py-1 shadow-lg"
        >
          <p className="px-2.5 pb-1 pt-1.5 font-mono text-[0.6rem] uppercase tracking-[0.2em] text-muted-foreground">
            repos
          </p>
          {repos.length === 0 ? (
            <p className="px-2.5 py-1.5 font-mono text-xs text-muted-foreground">
              no repos yet
            </p>
          ) : (
            repos.map((r) => (
              <RepoOption
                key={r.name}
                repo={r}
                active={r.name === repo}
                onSelect={() => {
                  setRepo(r.name)
                  setOpen(false)
                }}
              />
            ))
          )}

          <div className="my-1 h-px bg-border" aria-hidden="true" />

          <Link
            to="/instances"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 px-2.5 py-1.5 font-mono text-xs text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
          >
            <Plus className="size-3.5" aria-hidden="true" />
            Add / manage repos
          </Link>
        </div>
      )}
    </div>
  )
}

function RepoOption({
  repo,
  active,
  onSelect,
}: {
  repo: RepoView
  active: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={active}
      onClick={onSelect}
      className={cn(
        'flex w-full items-center gap-2.5 px-2.5 py-1.5 text-left transition-colors hover:bg-secondary',
        active && 'bg-secondary/60',
      )}
    >
      <RepoIcon live={repo.live} />
      <span className="flex min-w-0 flex-1 flex-col">
        <span
          className={cn(
            'truncate font-mono text-sm',
            active ? 'text-primary' : 'text-foreground',
          )}
        >
          {repo.name}
        </span>
        <span className="truncate font-mono text-[0.65rem] text-muted-foreground">
          {repo.root}
        </span>
      </span>
      {active && (
        <Check className="size-3.5 shrink-0 text-primary" aria-hidden="true" />
      )}
    </button>
  )
}

function RepoIcon({ live }: { live: boolean }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        'flex size-7 shrink-0 items-center justify-center rounded-md border bg-secondary',
        live ? 'border-teal/50 text-teal' : 'border-border text-primary',
      )}
    >
      <GitBranch className="size-3.5" />
    </span>
  )
}
