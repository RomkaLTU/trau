import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import {
  AlertTriangle,
  Check,
  ChevronsUpDown,
  Circle,
  FolderGit2,
  FolderPlus,
  GitBranch,
} from 'lucide-react'

import { ALL_SCOPE, useActiveRepo } from '@/components/trau/active-repo'
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import { loadRepoUsage, sortRepos } from '@/lib/active-repo'
import { instancesQueryOptions, type RepoView } from '@/lib/instances'
import {
  repoBadgeState,
  toSessionState,
  type RepoBadgeState,
} from '@/lib/overview'
import { matchesQuery } from '@/lib/palette-filter'
import {
  isMacPlatform,
  isRepoPickerShortcut,
  shortcutLabel,
} from '@/lib/palette-keys'
import {
  filterRepoRows,
  groupRepos,
  projectsQueryOptions,
  type RepoRow,
} from '@/lib/projects'
import { loadRecents } from '@/lib/recents'
import { cn } from '@/lib/utils'

const GROUP_HEADING =
  '[&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[0.65rem] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-[0.2em] [&_[cmdk-group-heading]]:font-normal'

const RECENT_REPOS = 5

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

export function RepoSwitcher({ onOpen }: { onOpen: () => void }) {
  const { repo, repos, isAll } = useActiveRepo()
  const badges = useRepoBadgeStates()
  const active = repos.find((r) => r.name === repo)

  return (
    <button
      type="button"
      onClick={onOpen}
      aria-haspopup="dialog"
      title="Switch repo"
      className="flex w-full items-center gap-2.5 rounded-md border border-border bg-input px-2.5 py-2 text-left transition-colors hover:border-ring/50"
    >
      {isAll ? (
        <AllReposIcon />
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
      <kbd className="shrink-0 rounded border border-border px-1 py-0.5 font-mono text-[0.65rem] text-muted-foreground">
        {shortcutLabel(isMacPlatform(navigator), 'P')}
      </kbd>
      <ChevronsUpDown
        className="size-3.5 shrink-0 text-muted-foreground"
        aria-hidden="true"
      />
    </button>
  )
}

export function RepoSwitcherDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { repo, repos, isAll, setScope, switcherSignal } = useActiveRepo()
  const badges = useRepoBadgeStates()
  const { data: projectData } = useQuery(projectsQueryOptions)
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const trimmed = query.trim()

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (!isRepoPickerShortcut(e)) return
      e.preventDefault()
      onOpenChange(!open)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [open, onOpenChange])

  useEffect(() => {
    if (!open) setQuery('')
  }, [open])

  // A gated nav click opens the picker so the fix is one keystroke away instead
  // of a dead link. switcherSignal starts at 0, so mount never opens it.
  useEffect(() => {
    if (switcherSignal === 0) return
    onOpenChange(true)
  }, [switcherSignal, onOpenChange])

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

  const sections = useMemo(
    () => repoSections(filterRepoRows(rows, trimmed)),
    [rows, trimmed],
  )

  const recents = useMemo(() => {
    if (!open || trimmed !== '') return []
    const byName = new Map(repos.map((r) => [r.name, r]))
    return loadRecents()
      .flatMap((entry) =>
        entry.kind === 'project' && entry.label !== repo
          ? (byName.get(entry.label) ?? [])
          : [],
      )
      .slice(0, RECENT_REPOS)
  }, [open, trimmed, repo, repos])

  const scoped = isAll ? null : repo
  const showAllScope = repos.length > 1 && matchesQuery(trimmed, ['All repos'])
  const showNewProject = matchesQuery(trimmed, ['New project'])
  const showManageRepos = matchesQuery(trimmed, ['Manage repos'])

  function pick(scope: string) {
    setScope(scope)
    onOpenChange(false)
  }

  function goTo(to: '/projects/new' | '/instances') {
    onOpenChange(false)
    void navigate({ to })
  }

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Switch repo"
      description="Search the registered repos and pick the one to work in."
      className="font-mono"
      size="lg"
      shouldFilter={false}
    >
      <CommandInput
        placeholder="Switch repo…"
        value={query}
        onValueChange={setQuery}
      />
      <CommandList className="max-h-[65vh]">
        <CommandEmpty>No repo matches.</CommandEmpty>
        {showAllScope && (
          <CommandGroup heading="scope" className={GROUP_HEADING}>
            <CommandItem
              value="scope:all"
              onSelect={() => pick(ALL_SCOPE)}
              className="gap-2.5 px-2.5 py-1.5"
            >
              <AllReposIcon />
              <span className="flex min-w-0 flex-1 flex-col">
                <span
                  className={cn(
                    'truncate text-sm',
                    isAll ? 'text-primary' : 'text-foreground',
                  )}
                >
                  All repos
                </span>
                <span className="truncate text-[0.65rem] text-muted-foreground">
                  {repos.length} repos · operate pages ask you to pick one
                </span>
              </span>
              {isAll && (
                <Check
                  className="size-3.5 shrink-0 text-primary"
                  aria-hidden="true"
                />
              )}
            </CommandItem>
          </CommandGroup>
        )}
        {recents.length > 0 && (
          <CommandGroup heading="recent" className={GROUP_HEADING}>
            {recents.map((r) => (
              <RepoOption
                key={r.name}
                value={`recent:${r.name}`}
                repo={r}
                state={badges.get(r.name) ?? 'none'}
                active={r.name === scoped}
                onSelect={() => pick(r.name)}
              />
            ))}
          </CommandGroup>
        )}
        {sections.map((section) => (
          <CommandGroup
            key={section.key}
            heading={section.heading}
            className={GROUP_HEADING}
          >
            {section.repos.map((r) => (
              <RepoOption
                key={r.name}
                value={`repo:${r.name}`}
                repo={r}
                state={badges.get(r.name) ?? 'none'}
                active={r.name === scoped}
                onSelect={() => pick(r.name)}
              />
            ))}
          </CommandGroup>
        ))}
        {(showNewProject || showManageRepos) && (
          <>
            <CommandSeparator />
            <CommandGroup className={GROUP_HEADING}>
              {showNewProject && (
                <CommandItem
                  value="new-project"
                  onSelect={() => goTo('/projects/new')}
                  className="gap-2.5 px-2.5 py-1.5"
                >
                  <FolderPlus />
                  <span className="flex-1 truncate">New project…</span>
                </CommandItem>
              )}
              {showManageRepos && (
                <CommandItem
                  value="manage-repos"
                  onSelect={() => goTo('/instances')}
                  className="gap-2.5 px-2.5 py-1.5"
                >
                  <FolderGit2 />
                  <span className="flex-1 truncate">Manage repos</span>
                </CommandItem>
              )}
            </CommandGroup>
          </>
        )}
      </CommandList>
    </CommandDialog>
  )
}

interface RepoSection {
  key: string
  heading: string
  repos: RepoView[]
}

// repoSections turns the grouped rows into the dialog's headings. Every repo no
// project holds shares one "repos" heading, opened where the first of them ranks,
// so the usage order the rows arrive in survives the merge.
function repoSections(rows: readonly RepoRow[]): RepoSection[] {
  const sections: RepoSection[] = []
  let loose: RepoView[] | null = null
  for (const row of rows) {
    if (row.project) {
      sections.push({
        key: row.project.id,
        heading: row.project.name,
        repos: row.repos,
      })
      continue
    }
    if (!loose) {
      loose = []
      sections.push({ key: 'repos', heading: 'repos', repos: loose })
    }
    loose.push(...row.repos)
  }
  return sections
}

function RepoOption({
  value,
  repo,
  state,
  active,
  onSelect,
}: {
  value: string
  repo: RepoView
  state: RepoBadgeState
  active: boolean
  onSelect: () => void
}) {
  return (
    <CommandItem
      value={value}
      onSelect={onSelect}
      title={`${repo.name}\n${repoSubtitle(repo)}`}
      className="gap-2.5 px-2.5 py-1.5"
    >
      <RepoIcon state={state} />
      <span className="flex min-w-0 flex-1 flex-col">
        <span
          className={cn(
            'truncate text-sm',
            active ? 'text-primary' : 'text-foreground',
          )}
        >
          {repo.name}
        </span>
        <span className="truncate text-[0.65rem] text-muted-foreground">
          {repoSubtitle(repo)}
        </span>
      </span>
      {active && (
        <Check className="size-3.5 shrink-0 text-primary" aria-hidden="true" />
      )}
    </CommandItem>
  )
}

// A Folder repo is an ordinary Repo — one board, one queue, one row — so its
// folder-ness rides on the subtitle rather than a badge of its own. Every row is
// two lines and no more, so a repo under a long path cannot push the ones below
// it off the screen: the subtitle truncates, and the marker leads it so a clipped
// line still says what the row is. The whole of it is on the row's tooltip.
function repoSubtitle(repo: RepoView): string {
  if (repo.kind !== 'folder') return repo.root
  const count = repo.child_repos ?? 0
  return `folder repo, ${count} ${count === 1 ? 'repository' : 'repositories'} · ${repo.root}`
}

function AllReposIcon() {
  return (
    <span
      aria-hidden="true"
      className="flex size-7 shrink-0 items-center justify-center rounded-md border border-primary/40 bg-secondary text-primary"
    >
      <FolderGit2 className="size-3.5 text-current" />
    </span>
  )
}

// The icons carry text-current so the command row's own muted-icon rule cannot
// override the state colour the wrapper sets.
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
        <AlertTriangle className="size-3.5 text-current" />
      ) : state === 'idle' ? (
        <Circle className="size-2 fill-current text-current" />
      ) : (
        <GitBranch className="size-3.5 text-current" />
      )}
    </span>
  )
}
