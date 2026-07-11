import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Search, X } from 'lucide-react'

import { useActiveRepo } from '@/components/trau/active-repo'
import { issueSearchQueryOptions, type SearchResult } from '@/lib/search'
import { cn } from '@/lib/utils'

// GlobalSearch is the app-shell entry point for full-text issue search, scoped to
// the active repo. A result click lands on that ticket's run detail. Under "All
// projects" there is no single repo to query, so the input prompts for one.
export function GlobalSearch() {
  const { repo, isAll } = useActiveRepo()
  const [term, setTerm] = useState('')
  const [debounced, setDebounced] = useState('')
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const id = setTimeout(() => setDebounced(term.trim()), 150)
    return () => clearTimeout(id)
  }, [term])

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

  const { data, isFetching } = useQuery(
    issueSearchQueryOptions(repo ?? '', debounced),
  )
  const results = data?.results ?? []
  const disabled = isAll || repo === null
  const showResults = open && !disabled && debounced !== ''

  function reset() {
    setTerm('')
    setDebounced('')
    setOpen(false)
  }

  return (
    <div ref={ref} className="relative">
      <div className="flex items-center gap-2 rounded-md border border-border bg-input px-2.5 py-1.5">
        <Search
          className="size-3.5 shrink-0 text-muted-foreground"
          aria-hidden="true"
        />
        <input
          type="text"
          value={term}
          disabled={disabled}
          onChange={(e) => setTerm(e.target.value)}
          onFocus={() => setOpen(true)}
          placeholder={disabled ? 'Select a project to search' : 'Search issues…'}
          aria-label="Search issues"
          className="w-full bg-transparent font-mono text-sm text-foreground outline-none placeholder:text-muted-foreground disabled:cursor-not-allowed"
        />
        {term !== '' && (
          <button
            type="button"
            onClick={reset}
            aria-label="Clear search"
            className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
          >
            <X className="size-3.5" aria-hidden="true" />
          </button>
        )}
      </div>

      {showResults && (
        <div className="absolute inset-x-0 top-full z-20 mt-1 overflow-hidden rounded-md border border-border bg-card shadow-lg">
          {results.length > 0 ? (
            <ul className="max-h-80 overflow-y-auto py-1">
              {results.map((r) => (
                <li key={r.id}>
                  <ResultRow repo={repo ?? ''} result={r} onNavigate={reset} />
                </li>
              ))}
            </ul>
          ) : (
            <p className="px-3 py-2.5 font-mono text-xs text-muted-foreground">
              {isFetching ? 'Searching…' : 'No matching issues.'}
            </p>
          )}
        </div>
      )}
    </div>
  )
}

function ResultRow({
  repo,
  result,
  onNavigate,
}: {
  repo: string
  result: SearchResult
  onNavigate: () => void
}) {
  return (
    <Link
      to="/runs/$repo/$ticket"
      params={{ repo, ticket: result.id }}
      onClick={onNavigate}
      className="flex flex-col gap-0.5 px-3 py-2 transition-colors hover:bg-secondary"
    >
      <span className="flex items-center gap-2">
        <span className="font-mono text-xs text-primary">{result.id}</span>
        {result.status && (
          <span className="truncate font-mono text-[0.65rem] text-muted-foreground">
            {result.status}
          </span>
        )}
      </span>
      <span
        className={cn(
          'truncate font-sans text-sm text-foreground',
          !result.title && 'text-muted-foreground',
        )}
      >
        {result.title || 'Untitled'}
      </span>
    </Link>
  )
}
