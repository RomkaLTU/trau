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
  Search,
} from 'lucide-react'

import { ALL_SCOPE, useActiveRepo } from '@/components/trau/active-repo'
import { Input } from '@/components/ui/input'
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
    for (const [name, states] of byRepo) badges.set(name, repoBadgeState(states))
    return badges
  }, [data])
}

export function RepoSwitcher() {
  const { repo, repos, isAll, setScope, switcherSignal } = useActiveRepo()
  const badges = useRepoBadgeStates()
  const { data: projectData } = useQuery(projectsQueryOptions)
  const [open, setOpen] = useState(false)
  const [pulse, setPulse] = useState(false)
  const [query, setQuery] = useState('')
  const [groups, setGroups] = useState<GroupState>(loadGroupState)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    function onPointerDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    // Escape backs out one layer at a time: it drops the query first, so a search
    // that found nothing does not also cost the popover.
    function onKeyDown(e: KeyboardEvent) {
      if (e.key !== 'Escape') return
      if (query !== '') {
        setQuery('')
        return
      }
      setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open, query])

  useEffect(() => setQuery(''), [open])

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
  const scoped = isAll ? null : repo

  function pick(name: string) {
    setScope(name)
    setOpen(false)
  }

  function foldGroup(project: string, fold: 'open' | 'closed') {
    setGroups((prev) => {
      const next = { ...prev, [project]: fold }
      saveGroupState(next)
      return next
    })
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

      {/* The clamp reserve has to exceed the popover's own ~110px offset from the top of
          the viewport (sidebar header + this button), or the footer rows fall off it. */}
      {open && (
        <div
          role="listbox"
          className="absolute left-0 right-0 z-30 mt-1 flex max-h-[calc(100vh-8rem)] flex-col overflow-hidden rounded-md border border-border bg-popover py-1 shadow-lg"
        >
          {repos.length > 1 && (
            <div className="shrink-0">
              <div className="px-2 pb-1 pt-0.5">
                <div className="relative">
                  <Search
                    className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
                    aria-hidden="true"
                  />
                  <Input
                    type="search"
                    autoFocus
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key !== 'Enter' || e.nativeEvent.isComposing) return
                      const first = visible[0]?.repos[0]
                      if (first) pick(first.name)
                    }}
                    placeholder="filter repos…"
                    aria-label="Filter repos"
                    autoComplete="off"
                    spellCheck={false}
                    className="h-auto py-1.5 pl-8 pr-2.5 font-mono text-xs placeholder:text-faint"
                  />
                </div>
              </div>
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
          )}
          <p className="shrink-0 px-2.5 pb-1 pt-1.5 font-mono text-[0.6rem] uppercase tracking-[0.2em] text-muted-foreground">
            repos
          </p>
          <div className="min-h-0 flex-1 overflow-y-auto">
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
                if (!row.project) {
                  return (
                    <RepoOption
                      key={row.repos[0].name}
                      repo={row.repos[0]}
                      state={badges.get(row.repos[0].name) ?? 'none'}
                      active={row.repos[0].name === scoped}
                      onSelect={() => pick(row.repos[0].name)}
                    />
                  )
                }
                const id = row.project.id
                const stored = isGroupOpen(
                  groups,
                  id,
                  row.repos.map((r) => r.name),
                  scoped,
                )
                return (
                  <ProjectGroup
                    key={id}
                    project={row.project}
                    repos={row.repos}
                    badges={badges}
                    activeRepo={scoped}
                    expanded={filtering || stored}
                    onToggle={() => foldGroup(id, stored ? 'closed' : 'open')}
                    onSelect={pick}
                  />
                )
              })
            )}
          </div>

          <div className="shrink-0">
            <div className="my-1 h-px bg-border" aria-hidden="true" />

            <Link
              to="/projects/new"
              onClick={() => setOpen(false)}
              className="flex w-full items-center gap-2.5 px-2.5 py-1.5 text-left transition-colors hover:bg-secondary"
            >
              <span
                aria-hidden="true"
                className="flex size-7 shrink-0 items-center justify-center rounded-md border border-dashed border-border bg-secondary text-primary"
              >
                <FolderPlus className="size-3.5" />
              </span>
              <span className="font-mono text-sm text-foreground">
                New project…
              </span>
            </Link>

            <Link
              to="/instances"
              onClick={() => setOpen(false)}
              className="flex items-center gap-2 px-2.5 py-1.5 font-mono text-xs text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
            >
              <FolderGit2 className="size-3.5" aria-hidden="true" />
              Manage repos
            </Link>
          </div>
        </div>
      )}
    </div>
  )
}

// ProjectGroup renders a project holding more than one repo: a foldable header
// over its members. A project down to a single repo never reaches here. The header
// keeps its tint while folded, so the group holding the scoped repo stays findable
// with its members away.
function ProjectGroup({
  project,
  repos,
  badges,
  activeRepo,
  expanded,
  onToggle,
  onSelect,
}: {
  project: ProjectView
  repos: RepoView[]
  badges: Map<string, RepoBadgeState>
  activeRepo: string | null
  expanded: boolean
  onToggle: () => void
  onSelect: (name: string) => void
}) {
  const Chevron = expanded ? ChevronDown : ChevronRight
  const holdsActive = repos.some((r) => r.name === activeRepo)
  return (
    <div role="group" aria-label={project.name}>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={expanded}
        className={cn(
          'flex w-full items-center gap-1.5 px-2.5 pb-0.5 pt-1.5 text-left font-mono text-[0.65rem] uppercase tracking-[0.14em] transition-colors hover:text-foreground',
          holdsActive ? 'text-primary' : 'text-muted-foreground',
        )}
      >
        <Chevron className="size-3 shrink-0" aria-hidden="true" />
        <FolderGit2 className="size-3 shrink-0" aria-hidden="true" />
        <span className="truncate">{project.name}</span>
        <span className="ml-auto shrink-0 pl-1.5 tabular-nums">
          {repos.length}
        </span>
      </button>
      {expanded && (
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
      title={`${repo.name}\n${repoSubtitle(repo)}`}
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
          {repoSubtitle(repo)}
        </span>
      </span>
      {active && (
        <Check className="size-3.5 shrink-0 text-primary" aria-hidden="true" />
      )}
    </button>
  )
}

// A Folder repo is an ordinary Repo — one board, one queue, one row — so its
// folder-ness rides on the subtitle rather than a badge of its own. The count
// belongs here and not in the right-aligned slot the Project group header uses,
// which would read as a project. Every row is two lines and no more, so a repo
// under a long path cannot push the ones below it off the screen: the subtitle
// truncates, and the marker leads it so a clipped line still says what the row
// is. The whole of it is on the button's tooltip.
function repoSubtitle(repo: RepoView): string {
  if (repo.kind !== 'folder') return repo.root
  const count = repo.child_repos ?? 0
  return `folder repo, ${count} ${count === 1 ? 'repository' : 'repositories'} · ${repo.root}`
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
