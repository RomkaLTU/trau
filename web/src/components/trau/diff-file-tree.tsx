import { useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, Search } from 'lucide-react'

import { DiffCounts } from '@/components/trau/diff-counts'
import { cn } from '@/lib/utils'
import {
  buildDiffTree,
  diffCardId,
  filterDiffTree,
  type DiffTreeNode,
} from '@/lib/difftree'
import type { RunDiffFile, RunDiffStatus } from '@/lib/rundiff'

const STATUS: Record<RunDiffStatus, { glyph: string; className: string }> = {
  added: { glyph: 'A', className: 'text-done' },
  modified: { glyph: 'M', className: 'text-warn' },
  deleted: { glyph: 'D', className: 'text-fail' },
  renamed: { glyph: 'R', className: 'text-muted-foreground' },
}

const ROW =
  'flex w-full items-center gap-1.5 rounded-md py-1 pr-1.5 text-left font-mono text-xs transition-colors hover:bg-accent/60 focus-visible:outline-none focus-visible:bg-accent/60'

// A filter renders the folders against this empty set so every match shows, leaving
// the collapsed one untouched to restore the shape the tree had once the box clears.
const NONE_COLLAPSED: ReadonlySet<string> = new Set()

const CHASE_RATIO = 0.2
const CHASE_SETTLED = 12
const CHASE_LIMIT = 240
const CHASE_INTERRUPTS = ['wheel', 'touchstart', 'keydown'] as const

let stopChase = () => {}

function indent(depth: number) {
  return { paddingLeft: `${0.375 + depth * 0.75}rem` }
}

export function DiffFileTree({
  files,
  className,
}: {
  files: RunDiffFile[]
  className?: string
}) {
  const [query, setQuery] = useState('')
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set())

  const tree = useMemo(() => buildDiffTree(files), [files])
  const shown = useMemo(() => filterDiffTree(tree, query), [tree, query])
  const filtering = query.trim() !== ''

  const toggle = (path: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (!next.delete(path)) next.add(path)
      return next
    })

  return (
    <aside
      className={cn(
        'flex flex-col gap-2 rounded-lg border border-border bg-card p-2',
        className,
      )}
    >
      <div className="relative">
        <Search
          className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
          aria-hidden="true"
        />
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="filter files…"
          aria-label="Filter changed files"
          autoComplete="off"
          spellCheck={false}
          className="w-full rounded-md border border-border bg-input py-1.5 pl-8 pr-2.5 font-mono text-xs text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
        />
      </div>
      <div className="max-h-64 overflow-y-auto lg:max-h-[calc(100vh-14rem)]">
        {shown.length === 0 ? (
          <p className="px-1.5 py-1 font-mono text-xs text-muted-foreground">
            No file matches that filter.
          </p>
        ) : (
          <TreeRows
            nodes={shown}
            depth={0}
            collapsed={filtering ? NONE_COLLAPSED : collapsed}
            onToggle={toggle}
          />
        )}
      </div>
    </aside>
  )
}

function TreeRows({
  nodes,
  depth,
  collapsed,
  onToggle,
}: {
  nodes: DiffTreeNode[]
  depth: number
  collapsed: ReadonlySet<string>
  onToggle: (path: string) => void
}) {
  return (
    <ul className="flex flex-col">
      {nodes.map((node) => {
        if (node.kind === 'file') {
          const status = STATUS[node.file.status]
          return (
            <li key={node.path}>
              <button
                type="button"
                onClick={() => scrollToCard(node.path)}
                title={node.path}
                style={indent(depth)}
                className={cn(ROW, 'text-foreground')}
              >
                <span
                  className={cn('w-3 shrink-0 text-center', status.className)}
                  title={node.file.status}
                >
                  {status.glyph}
                </span>
                <span className="truncate">{node.name}</span>
                <DiffCounts
                  additions={node.file.additions}
                  deletions={node.file.deletions}
                  className="ml-auto pl-2"
                />
              </button>
            </li>
          )
        }

        const open = !collapsed.has(node.path)
        const Chevron = open ? ChevronDown : ChevronRight
        return (
          <li key={node.path}>
            <button
              type="button"
              onClick={() => onToggle(node.path)}
              aria-expanded={open}
              title={node.path}
              style={indent(depth)}
              className={cn(ROW, 'text-muted-foreground hover:text-foreground')}
            >
              <Chevron className="size-3.5 shrink-0" aria-hidden="true" />
              <span className="truncate">{node.name}</span>
            </button>
            {open && (
              <TreeRows
                nodes={node.children}
                depth={depth + 1}
                collapsed={collapsed}
                onToggle={onToggle}
              />
            )}
          </li>
        )
      })}
    </ul>
  )
}

// Every card the scroll passes swaps its placeholder for the real patch on the way, so
// the document grows mid-flight and a one-shot scrollIntoView lands far short. Chase the
// card instead: close a share of the gap each frame, and hold the aim while it settles.
function scrollToCard(path: string) {
  const card = document.getElementById(diffCardId(path))
  if (!card) return

  stopChase()

  const margin = parseFloat(getComputedStyle(card).scrollMarginTop)
  let frames = CHASE_LIMIT
  let settled = 0
  let handle = 0

  const stop = () => {
    cancelAnimationFrame(handle)
    for (const event of CHASE_INTERRUPTS) window.removeEventListener(event, stop)
  }

  const aim = () => {
    const gap = card.getBoundingClientRect().top - margin
    const step = gap * CHASE_RATIO
    const from = window.scrollY
    window.scrollBy(0, Math.abs(step) < 1 ? gap : step)
    // The tail of a diff can never reach the margin, so treat a window that has stopped
    // moving as arrived — otherwise the chase shoves at the bottom for its whole budget.
    settled = Math.abs(gap) < 1 || window.scrollY === from ? settled + 1 : 0
    if (settled < CHASE_SETTLED && frames-- > 0) {
      handle = requestAnimationFrame(aim)
      return
    }
    stop()
  }

  handle = requestAnimationFrame(aim)
  for (const event of CHASE_INTERRUPTS) {
    window.addEventListener(event, stop, { passive: true })
  }
  stopChase = stop
}
