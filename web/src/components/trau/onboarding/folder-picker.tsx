import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronRight, CornerLeftUp, Folder, FolderGit2, Monitor } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { breadcrumbs, browseQueryOptions } from '@/lib/fs-browse'
import { cn } from '@/lib/utils'
import { Hint } from './ui'

export function FolderPicker({
  busy,
  disabled,
  onSelect,
}: {
  busy: boolean
  disabled?: boolean
  onSelect: (path: string) => void
}) {
  const [dir, setDir] = useState('')
  const [showHidden, setShowHidden] = useState(false)
  const browse = useQuery(browseQueryOptions(dir))
  const trail = useRef<HTMLElement>(null)

  useEffect(() => {
    const el = trail.current
    if (el) el.scrollLeft = el.scrollWidth
  }, [dir, browse.data])

  const listing = browse.data
  const crumbs = listing ? breadcrumbs(listing.path, listing.separator) : []
  const entries = (listing?.entries ?? []).filter(
    (e) => showHidden || !e.name.startsWith('.'),
  )
  const hiddenCount = (listing?.entries.length ?? 0) - entries.length
  const action = listing?.is_repo ? 'Inspect this folder' : 'Scan this folder'
  const pending = listing?.is_repo ? 'Inspecting…' : 'Scanning…'

  return (
    <div className="flex flex-col gap-2">
      <div className="overflow-hidden rounded-md border border-border bg-card">
        <div className="flex items-center gap-1 border-b border-border bg-muted/30 px-1.5 py-1.5">
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="Go up one folder"
            disabled={dir === ''}
            onClick={() => setDir(listing?.parent ?? '')}
          >
            <CornerLeftUp className="size-3.5" />
          </Button>
          <nav
            ref={trail}
            aria-label="Folder path"
            className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto font-mono text-xs"
          >
            <button
              type="button"
              onClick={() => setDir('')}
              className="flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              <Monitor className="size-3" aria-hidden="true" />
              this machine
            </button>
            {crumbs.map((crumb) => (
              <span key={crumb.path} className="flex shrink-0 items-center">
                <ChevronRight
                  className="size-3 text-muted-foreground/60"
                  aria-hidden="true"
                />
                <button
                  type="button"
                  onClick={() => setDir(crumb.path)}
                  className="rounded px-1 py-0.5 text-foreground hover:bg-muted"
                >
                  {crumb.label}
                </button>
              </span>
            ))}
          </nav>
        </div>

        <div className="max-h-64 min-h-32 overflow-y-auto">
          {browse.isPending && <PickerNote>Reading the hub's folders…</PickerNote>}
          {browse.error && <PickerNote>{browse.error.message}</PickerNote>}
          {listing && entries.length === 0 && (
            <PickerNote>
              {hiddenCount > 0
                ? `Nothing here but ${hiddenCount} hidden folder${hiddenCount === 1 ? '' : 's'}.`
                : 'No folders here.'}
            </PickerNote>
          )}
          {entries.map((entry) => (
            <div
              key={entry.path}
              className="flex items-center border-b border-border/40 last:border-b-0 hover:bg-muted/50"
            >
              <button
                type="button"
                onClick={() => setDir(entry.path)}
                className="flex min-w-0 flex-1 items-center gap-2 px-2.5 py-1.5 text-left"
              >
                {entry.is_repo ? (
                  <FolderGit2 className="size-3.5 shrink-0 text-primary" aria-hidden="true" />
                ) : (
                  <Folder
                    className="size-3.5 shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                )}
                <span
                  className={cn(
                    'truncate font-mono text-xs',
                    entry.is_repo ? 'text-foreground' : 'text-muted-foreground',
                  )}
                >
                  {entry.name}
                </span>
                {entry.is_repo && (
                  <span className="shrink-0 rounded border border-primary/40 bg-primary/10 px-1 py-px font-mono text-[0.6rem] text-primary">
                    git
                  </span>
                )}
              </button>
              {entry.is_repo && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="mr-1.5 font-mono text-xs"
                  disabled={busy || disabled}
                  onClick={() => onSelect(entry.path)}
                >
                  select
                </Button>
              )}
            </div>
          ))}
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        {(hiddenCount > 0 || showHidden) && (
          <button
            type="button"
            onClick={() => setShowHidden(!showHidden)}
            className="font-mono text-[0.65rem] text-muted-foreground underline underline-offset-2 hover:text-foreground"
          >
            {showHidden ? 'hide dotfolders' : `show ${hiddenCount} hidden`}
          </button>
        )}
        <Button
          type="button"
          className="ml-auto"
          disabled={!listing || listing.path === '' || busy || disabled}
          onClick={() => listing && onSelect(listing.path)}
        >
          <FolderGit2 className="size-4" />
          {busy ? pending : action}
        </Button>
      </div>

      {listing && listing.path !== '' && !listing.is_repo && (
        <Hint>
          No <span className="font-mono">.git</span> here — scanning looks one or two levels
          down for repositories, and offers to start one if there are none.
        </Hint>
      )}
    </div>
  )
}

function PickerNote({ children }: { children: ReactNode }) {
  return (
    <p className="px-2.5 py-3 font-sans text-xs text-muted-foreground">{children}</p>
  )
}
