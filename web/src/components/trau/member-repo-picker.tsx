import { useEffect, useRef, useState, type ReactNode } from 'react'
import { ChevronDown, GitBranch } from 'lucide-react'

import type { RepoView } from '@/lib/instances'
import { useStartRepo } from '@/lib/start-repo'
import { cn } from '@/lib/utils'

// useDismiss closes an open popup on a click outside it or on Escape, the way
// the repo and provider pickers already do.
function useDismiss(open: boolean, onClose: () => void) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    function onPointerDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose()
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open, onClose])
  return ref
}

function MemberList({
  members,
  value,
  suggested,
  onPick,
  className,
}: {
  members: readonly RepoView[]
  value: string
  suggested: string
  onPick: (repo: string) => void
  className?: string
}) {
  return (
    <ul
      role="listbox"
      aria-label="member repo"
      className={cn(
        'absolute right-0 z-20 min-w-full overflow-hidden whitespace-nowrap rounded-md border border-border bg-popover py-1 shadow-lg',
        className,
      )}
    >
      {members.map((member) => (
        <li key={member.root} role="option" aria-selected={member.name === value}>
          <button
            type="button"
            onClick={() => onPick(member.name)}
            className={cn(
              'flex w-full items-center gap-2 px-2.5 py-1.5 text-left font-mono text-sm hover:bg-secondary',
              member.name === value ? 'text-primary' : 'text-foreground',
            )}
          >
            <span className="truncate">{member.name}</span>
            {member.name === suggested && <SuggestedTag />}
          </button>
        </li>
      ))}
    </ul>
  )
}

function SuggestedTag() {
  return (
    <span className="shrink-0 rounded border border-border bg-muted/60 px-1 font-mono text-[0.6rem] uppercase tracking-[0.12em] text-muted-foreground">
      suggested
    </span>
  )
}

/**
 * Target-repo picker for launch forms in a project with several member repos.
 * The run has to land in one member, so beside the shell's scoped repo the
 * ticket carries a target of its own — opened on the hub's suggestion, marked as
 * one until the user picks.
 */
export function MemberRepoField({
  members,
  value,
  suggested,
  picked,
  ticket,
  onPick,
  className,
}: {
  members: readonly RepoView[]
  value: string
  suggested: string
  picked: boolean
  ticket: string
  onPick: (repo: string) => void
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const ref = useDismiss(open, () => setOpen(false))

  return (
    <div className="flex flex-col gap-1.5">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        target repo · this ticket
      </span>
      <div className={cn('relative w-56', className)} ref={ref}>
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-haspopup="listbox"
          aria-expanded={open}
          className="flex w-full items-center justify-between gap-2 rounded-md border border-border bg-input px-2.5 py-1.5 hover:border-ring/50"
        >
          <span className="inline-flex min-w-0 items-center gap-2 font-mono text-sm text-foreground">
            <GitBranch
              className="size-3.5 shrink-0 text-primary"
              aria-hidden="true"
            />
            <span className="truncate">{value || '—'}</span>
          </span>
          <span className="inline-flex shrink-0 items-center gap-1.5">
            {!picked && value === suggested && <SuggestedTag />}
            <ChevronDown
              className="size-3.5 text-muted-foreground"
              aria-hidden="true"
            />
          </span>
        </button>
        {open && (
          <MemberList
            members={members}
            value={value}
            suggested={suggested}
            className="mt-1"
            onPick={(repo) => {
              onPick(repo)
              setOpen(false)
            }}
          />
        )}
      </div>
      <p className="font-sans text-xs leading-relaxed text-muted-foreground">
        This project has {members.length} repos — {ticket} lands in the queue of
        the one you pick.
      </p>
    </div>
  )
}

/**
 * Wraps a start action that has no room for a field. A repo standing on its own
 * starts straight away, rendering nothing but the caller's own button. A repo in
 * a project with several members asks which one first — and starts straight away
 * after all once the hint narrows those members down to the one the ticket lives
 * in, since the hint only arrives after the click that asked for the start.
 */
export function StartIn({
  repo,
  ticket,
  onStart,
  children,
}: {
  repo: string
  ticket: string
  onStart: (repo: string) => void
  children: (begin: () => void) => ReactNode
}) {
  const [asked, setAsked] = useState(false)
  const start = useStartRepo(repo, ticket, asked)
  const ref = useDismiss(asked, () => setAsked(false))

  useEffect(() => {
    if (!asked || start.choose) return
    setAsked(false)
    onStart(start.target)
  }, [asked, start.choose, start.target, onStart])

  if (!start.choose) return <>{children(() => onStart(start.target))}</>
  return (
    <div className="relative flex" ref={ref}>
      {children(() => setAsked((a) => !a))}
      {asked && (
        <MemberList
          members={start.members}
          value={start.target}
          suggested={start.suggested}
          className="bottom-full mb-1"
          onPick={(member) => {
            onStart(member)
            setAsked(false)
          }}
        />
      )}
    </div>
  )
}
