import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  AlertTriangle,
  Check,
  ChevronsUpDown,
  Circle,
  FolderGit2,
  GitBranch,
  Plus,
} from 'lucide-react'

import { ALL_SCOPE, useActiveRepo } from '@/components/trau/active-repo'
import { instancesQueryOptions, type RepoView } from '@/lib/instances'
import {
  repoBadgeState,
  toSessionState,
  type RepoBadgeState,
} from '@/lib/overview'
import { cn } from '@/lib/utils'

function useRepoBadgeStates(): Map<string, RepoBadgeState> {
  const { data } = useQuery(instancesQueryOptions)
  return useMemo(() => {
    const byRepo = new Map<string, ReturnType<typeof toSessionState>[]>()
    for (const inst of data?.instances ?? []) {
      const states = byRepo.get(inst.repo) ?? []
      states.push(toSessionState(inst.session_state))
      byRepo.set(inst.repo, states)
    }
    const badges = new Map<string, RepoBadgeState>()
    for (const [name, states] of byRepo) badges.set(name, repoBadgeState(states))
    return badges
  }, [data])
}

export function RepoSwitcher() {
  const { repo, repos, isAll, setScope, switcherSignal } = useActiveRepo()
  const badges = useRepoBadgeStates()
  const [open, setOpen] = useState(false)
  const [pulse, setPulse] = useState(false)
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

  // A gated nav click pulses the switcher open so the fix is one click away
  // instead of a dead link. switcherSignal starts at 0, so mount never opens it.
  useEffect(() => {
    if (switcherSignal === 0) return
    setOpen(true)
    setPulse(true)
    const id = window.setTimeout(() => setPulse(false), 900)
    return () => window.clearTimeout(id)
  }, [switcherSignal])

  const active = repos.find((r) => r.name === repo)

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="listbox"
        aria-expanded={open}
        className={cn(
          'flex w-full items-center gap-2.5 rounded-md border bg-input px-2.5 py-2 text-left transition-colors hover:border-ring/50',
          pulse
            ? 'border-primary ring-2 ring-primary/40'
            : 'border-border',
        )}
      >
        {isAll ? (
          <span
            aria-hidden="true"
            className="flex size-7 shrink-0 items-center justify-center rounded-md border border-primary/40 bg-secondary text-primary"
          >
            <FolderGit2 className="size-3.5" />
          </span>
        ) : (
          <RepoIcon state={repo ? (badges.get(repo) ?? 'none') : 'none'} />
        )}
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate font-mono text-sm text-foreground">
            {isAll ? 'All repos' : (repo ?? 'no repo')}
          </span>
          <span className="truncate font-mono text-[0.65rem] text-muted-foreground">
            {isAll
              ? `${repos.length} repos`
              : active
                ? active.root
                : `${repos.length} registered`}
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
          {repos.length > 1 && (
            <>
              <p className="px-2.5 pb-1 pt-1.5 font-mono text-[0.6rem] uppercase tracking-[0.2em] text-muted-foreground">
                scope
              </p>
              <AllReposOption
                count={repos.length}
                active={isAll}
                onSelect={() => {
                  setScope(ALL_SCOPE)
                  setOpen(false)
                }}
              />
              <div className="my-1 h-px bg-border" aria-hidden="true" />
            </>
          )}
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
                state={badges.get(r.name) ?? 'none'}
                active={!isAll && r.name === repo}
                onSelect={() => {
                  setScope(r.name)
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

function AllReposOption({
  count,
  active,
  onSelect,
}: {
  count: number
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
      <span
        aria-hidden="true"
        className="flex size-7 shrink-0 items-center justify-center rounded-md border border-primary/40 bg-secondary text-primary"
      >
        <FolderGit2 className="size-3.5" />
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span
          className={cn(
            'truncate font-mono text-sm',
            active ? 'text-primary' : 'text-foreground',
          )}
        >
          All repos
        </span>
        <span className="truncate font-mono text-[0.65rem] text-muted-foreground">
          {count} repos · operate pages ask you to pick one
        </span>
      </span>
      {active && (
        <Check className="size-3.5 shrink-0 text-primary" aria-hidden="true" />
      )}
    </button>
  )
}

function RepoOption({
  repo,
  state,
  active,
  onSelect,
}: {
  repo: RepoView
  state: RepoBadgeState
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
      <RepoIcon state={state} />
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

function RepoIcon({ state }: { state: RepoBadgeState }) {
  const box =
    state === 'active'
      ? 'border-teal/50 text-teal'
      : state === 'parked'
        ? 'border-warn/50 text-warn'
        : state === 'idle'
          ? 'border-border text-muted-foreground'
          : 'border-border text-primary'
  return (
    <span
      aria-hidden="true"
      className={cn(
        'flex size-7 shrink-0 items-center justify-center rounded-md border bg-secondary',
        box,
      )}
    >
      {state === 'parked' ? (
        <AlertTriangle className="size-3.5" />
      ) : state === 'idle' ? (
        <Circle className="size-2 fill-current" />
      ) : (
        <GitBranch className="size-3.5" />
      )}
    </span>
  )
}
