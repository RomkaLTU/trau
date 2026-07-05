import { useEffect, useRef, useState } from 'react'
import { ChevronDown } from 'lucide-react'

import { cn } from '@/lib/utils'

export function RepoPicker({
  repos,
  value,
  onChange,
  label = 'repo',
  className,
}: {
  repos: readonly string[]
  value: string
  onChange: (repo: string) => void
  label?: string
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    function onPointerDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node))
        setOpen(false)
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

  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      {label ? (
        <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
          {label}
        </span>
      ) : null}
      <div className="relative w-48" ref={ref}>
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-haspopup="listbox"
          aria-expanded={open}
          className="flex w-full items-center justify-between rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground hover:border-ring/50"
        >
          {value || '—'}
          <ChevronDown
            className="size-3.5 text-muted-foreground"
            aria-hidden="true"
          />
        </button>
        {open && (
          <ul
            role="listbox"
            className="absolute z-20 mt-1 w-full overflow-hidden rounded-md border border-border bg-popover py-1 shadow-lg"
          >
            {repos.map((repo) => (
              <li key={repo} role="option" aria-selected={repo === value}>
                <button
                  type="button"
                  onClick={() => {
                    onChange(repo)
                    setOpen(false)
                  }}
                  className={cn(
                    'flex w-full px-2.5 py-1.5 text-left font-mono text-sm hover:bg-secondary',
                    repo === value ? 'text-primary' : 'text-foreground',
                  )}
                >
                  {repo}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
