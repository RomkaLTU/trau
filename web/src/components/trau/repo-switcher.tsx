import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  AlertTriangle,
  Check,
  ChevronDown,
  ChevronRight,
  ChevronsUpDown,
  Circle,
  FolderGit2,
  FolderPlus,
  GitBranch,
  Plus,
} from 'lucide-react'

import { ALL_SCOPE, useActiveRepo } from '@/components/trau/active-repo'
import { NewProjectDialog } from '@/components/trau/new-project-dialog'
import { loadRepoUsage, sortRepos } from '@/lib/active-repo'
import { instancesQueryOptions, type RepoView } from '@/lib/instances'
import {
  repoBadgeState,
  toSessionState,
  type RepoBadgeState,
} from '@/lib/overview'
import {
  filterRepoRows,
  groupRepos,
  projectsQueryOptions,
  type ProjectView,
} from '@/lib/projects'
import {
  isGroupOpen,
  loadGroupState,
  saveGroupState,
  type GroupState,
} from '@/lib/repo-groups'
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
    for (const [name, states] of byRepo)
      badges.set(name, repoBadgeState(states))
    return badges
  }, [data])
}

export function RepoSwitcher() {
  const { repo, repos, isAll, setScope, switcherSignal } = useActiveRepo()
  const badges = useRepoBadgeStates()
  const { data: projectData } = useQuery(projectsQueryOptions)
  const [open, setOpen] = useState(false)
  const [pulse, setPulse] = useState(false)
  const [creating, setCreating] = useState(false)
  const [query, setQuery] = useState('')
  const [groups, setGroups] = useState<GroupState>(loadGroupState)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    function onPointerDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    // Escape backs out of the filter first — the typed query is the nearer thing
    // to escape, and the popover is one more press away.
    function onKeyDown(e: KeyboardEvent) {
      if (e.key !== 'Escape') return
      if (query === '') setOpen(false)
      else setQuery('')
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open, query])

  // A gated nav click pulses the switcher open so the fix is one click away
  // instead of a dead link. switcherSignal starts at 0, so mount never opens it.
  useEffect(() => {
    if (switcherSignal === 0) return
    setOpen(true)
    setPulse(true)
    const id = window.setTimeout(() => setPulse(false), 900)
    return () => window.clearTimeout(id)
  }, [switcherSignal])

  // The filter is transient: every open starts on the whole list.
  useEffect(() => setQuery(''), [open])

  const active = repos.find((r) => r.name === repo)
  const activeRepo = isAll ? null : repo
  // The usage stamps are read on open, so the repo picked here leads the list
  // the next time it is opened.
  const rows = useMemo(
    () =>
      open
        ? groupRepos(
            sortRepos(repos, badges, loadRepoUsage()),
            projectData?.projects ?? [],
          )
        : [],
    [open, repos, badges, projectData],
  )
  const visible = useMemo(() => filterRepoRows(rows, query), [rows, query])
  const filtering = query.trim() !== ''

  function pick(name: string) {
    setScope(name)
    setOpen(false)
  }

  function pickTop() {
    if (visible.length > 0) pick(visible[0].repos[0].name)
  }

  function toggleGroup(project: string, expanded: boolean) {
    const next: GroupState = {
      ...groups,
      [project]: expanded ? 'closed' : 'open',
    }
    setGroups(next)
    saveGroupState(next)
  }

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="listbox"
        aria-expanded={open}
        className={cn(
          'flex w-full items-center gap-2.5 rounded-md border bg-input px-2.5 py-2 text-left transition-colors hover:border-ring/50',
          pulse ? 'border-primary ring-2 ring-primary/40' : 'border-border',
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
          className="absolute left-0 right-0 z-30 mt-1 flex max-h-[calc(100vh-6rem)] flex-col overflow-hidden rounded-md border border-border bg-popover py-1 shadow-lg"
        >
          {repos.length > 1 && (
            <>
              <div className="shrink-0 px-2.5 py-0.5">
                <input
                  type="text"
                  value={query}
                  autoFocus
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(e) => setQuery(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                      pickTop()
                    }
                  }}
                  placeholder="filter repos…"
                  aria-label="Filter repos"
                  className="w-full rounded border border-border bg-input px-2 py-1 font-mono text-sm text-foreground outline-none placeholder:text-muted-foreground focus:border-ring/50"
                />
              </div>
              <div className="shrink-0">
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
              </div>
            </>
          )}
          <div className="min-h-0 flex-1 overflow-y-auto">
            <p className="px-2.5 pb-1 pt-1.5 font-mono text-[0.6rem] uppercase tracking-[0.2em] text-muted-foreground">
              repos
            </p>
            {repos.length === 0 ? (
              <p className="px-2.5 py-1.5 font-mono text-xs text-muted-foreground">
                no repos yet
              </p>
            ) : visible.length === 0 ? (
              <p className="px-2.5 py-1.5 font-mono text-xs text-muted-foreground">
                no match
              </p>
            ) : (
              visible.map((row) => {
                const project = row.project
                if (!project) {
                  return (
                    <RepoOption
                      key={row.repos[0].name}
                      repo={row.repos[0]}
                      state={badges.get(row.repos[0].name) ?? 'none'}
                      active={row.repos[0].name === activeRepo}
                      onSelect={() => pick(row.repos[0].name)}
                    />
                  )
                }
                const expanded = isGroupOpen(
                  groups,
                  project.id,
                  row.repos.map((r) => r.name),
                  activeRepo,
                )
                return (
                  <ProjectGroup
                    key={project.id}
                    project={project}
                    repos={row.repos}
                    badges={badges}
                    activeRepo={activeRepo}
                    open={filtering || expanded}
                    onToggle={() => toggleGroup(project.id, expanded)}
                    onSelect={pick}
                  />
                )
              })
            )}
          </div>

          <div className="shrink-0">
            <div className="my-1 h-px bg-border" aria-hidden="true" />

            <button
              type="button"
              onClick={() => {
                setCreating(true)
                setOpen(false)
              }}
              className="flex w-full items-center gap-2.5 px-2.5 py-1.5 text-left transition-colors hover:bg-secondary"
            >
              <span
                aria-hidden="true"
                className="flex size-7 shrink-0 items-center justify-center rounded-md border border-dashed border-border bg-secondary text-primary"
              >
                <FolderPlus className="size-3.5" />
              </span>
              <span className="font-mono text-sm text-foreground">
                New project
              </span>
            </button>

            <Link
              to="/projects/new"
              onClick={() => setOpen(false)}
              className="flex w-full items-center gap-2.5 px-2.5 py-1.5 text-left transition-colors hover:bg-secondary"
            >
              <span
                aria-hidden="true"
                className="flex size-7 shrink-0 items-center justify-center rounded-md border border-dashed border-border bg-secondary text-primary"
              >
                <Plus className="size-3.5" />
              </span>
              <span className="font-mono text-sm text-foreground">
                Register a repo
              </span>
            </Link>

            <Link
              to="/instances"
              onClick={() => setOpen(false)}
              className="flex items-center gap-2 px-2.5 py-1.5 font-mono text-xs text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
            >
              <Plus className="size-3.5" aria-hidden="true" />
              Add / manage repos
            </Link>
          </div>
        </div>
      )}

      <NewProjectDialog
        repos={repos}
        open={creating}
        onOpenChange={setCreating}
      />
    </div>
  )
}

// ProjectGroup renders a project holding more than one repo: the name over its
// members, foldable so a folder-added project of forty cannot bury the list. A
// project down to a single repo never reaches here.
function ProjectGroup({
  project,
  repos,
  badges,
  activeRepo,
  open,
  onToggle,
  onSelect,
}: {
  project: ProjectView
  repos: RepoView[]
  badges: Map<string, RepoBadgeState>
  activeRepo: string | null
  open: boolean
  onToggle: () => void
  onSelect: (name: string) => void
}) {
  const holdsActive = repos.some((r) => r.name === activeRepo)
  const Chevron = open ? ChevronDown : ChevronRight
  return (
    <div role="group" aria-label={project.name}>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className={cn(
          'flex w-full items-center gap-1.5 px-2.5 pb-0.5 pt-1.5 text-left font-mono text-[0.65rem] uppercase tracking-[0.14em] transition-colors hover:text-foreground',
          !open && holdsActive ? 'text-primary' : 'text-muted-foreground',
        )}
      >
        <Chevron className="size-3 shrink-0" aria-hidden="true" />
        <FolderGit2 className="size-3 shrink-0" aria-hidden="true" />
        <span className="truncate">{project.name}</span>
        <span className="ml-auto shrink-0 tabular-nums">{repos.length}</span>
      </button>
      {open && (
        <div className="ml-4 border-l border-border">
          {repos.map((r) => (
            <RepoOption
              key={r.name}
              repo={r}
              state={badges.get(r.name) ?? 'none'}
              active={r.name === activeRepo}
              onSelect={() => onSelect(r.name)}
            />
          ))}
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
