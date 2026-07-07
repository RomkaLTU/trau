import { useEffect, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Check, ChevronsUpDown, Plus } from 'lucide-react'

import { useActiveRepo } from '@/components/trau/active-repo'
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
    <div className="px-3 pb-3" ref={ref}>
      <p className="px-2 pb-1.5 font-mono text-[0.65rem] uppercase tracking-[0.2em] text-muted-foreground">
        Repo
      </p>
      <div className="relative">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-haspopup="listbox"
          aria-expanded={open}
          className="flex w-full items-center justify-between gap-2 rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground hover:border-ring/50"
        >
          <span className="flex min-w-0 items-center gap-2">
            {active?.live && (
              <span
                className="size-1.5 shrink-0 rounded-full bg-teal"
                aria-hidden="true"
              />
            )}
            <span className="truncate">{repo ?? 'no repo'}</span>
          </span>
          <ChevronsUpDown
            className="size-3.5 shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
        </button>

        {open && (
          <ul
            role="listbox"
            className="absolute z-30 mt-1 w-full overflow-hidden rounded-md border border-border bg-popover py-1 shadow-lg"
          >
            {repos.length === 0 ? (
              <li className="px-2.5 py-1.5 font-mono text-xs text-muted-foreground">
                no repos yet
              </li>
            ) : (
              repos.map((r) => (
                <li key={r.name} role="option" aria-selected={r.name === repo}>
                  <button
                    type="button"
                    onClick={() => {
                      setRepo(r.name)
                      setOpen(false)
                    }}
                    className={cn(
                      'flex w-full items-center gap-2 px-2.5 py-1.5 text-left font-mono text-sm hover:bg-secondary',
                      r.name === repo ? 'text-primary' : 'text-foreground',
                    )}
                  >
                    <span
                      className={cn(
                        'size-1.5 shrink-0 rounded-full',
                        r.live ? 'bg-teal' : 'border border-border',
                      )}
                      aria-hidden="true"
                    />
                    <span className="flex-1 truncate">{r.name}</span>
                    {r.name === repo && (
                      <Check className="size-3.5 shrink-0" aria-hidden="true" />
                    )}
                  </button>
                </li>
              ))
            )}
            <li className="mt-1 border-t border-border pt-1">
              <Link
                to="/instances"
                onClick={() => setOpen(false)}
                className="flex items-center gap-2 px-2.5 py-1.5 font-mono text-xs text-muted-foreground hover:bg-secondary hover:text-foreground"
              >
                <Plus className="size-3.5" aria-hidden="true" />
                Add / manage repos
              </Link>
            </li>
          </ul>
        )}
      </div>
    </div>
  )
}
