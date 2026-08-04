import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import {
  filterWorkspaces,
  workspaceSuggestion,
  workspaceUnrouted,
  workspacesQueryOptions,
} from '@/lib/app-urls'

/**
 * The workspace field of an app URL: free text, with the repo's own detected
 * workspaces suggested so a routable name is one keystroke away. A name
 * matching none of them earns a hint but never blocks the save.
 */
export function WorkspaceCombobox({
  id,
  repo,
  value,
  onChange,
  label,
  className,
}: {
  id: string
  repo: string
  value: string
  onChange: (value: string) => void
  label: string
  className?: string
}) {
  const { data } = useQuery(workspacesQueryOptions(repo))
  const workspaces = data ?? []
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const ref = useRef<HTMLDivElement>(null)

  const matches = filterWorkspaces(workspaces, value)
  const listID = `${id}-suggestions`
  const expanded = open && matches.length > 0

  useEffect(() => {
    if (!open) return
    function onPointerDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open])

  const commit = (next: string) => {
    onChange(next)
    setOpen(false)
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape') {
      setOpen(false)
      return
    }
    if (matches.length === 0) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive(expanded ? (active + 1) % matches.length : 0)
      setOpen(true)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive(expanded ? (active + matches.length - 1) % matches.length : 0)
      setOpen(true)
    } else if (e.key === 'Enter' && expanded) {
      e.preventDefault()
      commit(workspaceSuggestion(matches[active]))
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="relative" ref={ref}>
        <Input
          id={id}
          role="combobox"
          aria-expanded={expanded}
          aria-controls={expanded ? listID : undefined}
          aria-autocomplete="list"
          aria-label={label}
          autoComplete="off"
          spellCheck={false}
          value={value}
          placeholder="default"
          onChange={(e) => {
            onChange(e.target.value)
            setActive(0)
            setOpen(true)
          }}
          onFocus={() => setOpen(true)}
          onKeyDown={onKeyDown}
          className={className}
        />
        {expanded && (
          <ul
            id={listID}
            role="listbox"
            className="absolute z-20 mt-1 max-h-56 w-full overflow-y-auto rounded-md border border-border bg-popover py-1 shadow-lg"
          >
            {matches.map((ws, index) => {
              const suggestion = workspaceSuggestion(ws)
              return (
                <li key={ws.path} role="option" aria-selected={index === active}>
                  <button
                    type="button"
                    onMouseEnter={() => setActive(index)}
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => commit(suggestion)}
                    className={cn(
                      'flex w-full flex-col items-start px-2.5 py-1.5 text-left',
                      index === active && 'bg-secondary',
                    )}
                  >
                    <span className="w-full truncate font-mono text-xs text-foreground">
                      {suggestion}
                    </span>
                    {ws.path !== suggestion && (
                      <span className="w-full truncate font-mono text-[0.7rem] text-faint">
                        {ws.path}
                      </span>
                    )}
                  </button>
                </li>
              )
            })}
          </ul>
        )}
      </div>
      {workspaceUnrouted(value, workspaces) && (
        <p className="font-sans text-xs leading-relaxed text-warn">
          Not a detected workspace — this URL will fall back to the repo default.
        </p>
      )}
    </div>
  )
}
