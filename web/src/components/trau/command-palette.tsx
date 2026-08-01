import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useRouterState } from '@tanstack/react-router'
import {
  Check,
  ChevronRight,
  FolderGit2,
  GitBranch,
  History,
  ListChecks,
  Settings,
} from 'lucide-react'
import { toast } from 'sonner'

import { ALL_SCOPE, useActiveRepo } from '@/components/trau/active-repo'
import { NAV_GROUPS, type NavItem } from '@/components/trau/nav-items'
import { StatusPill } from '@/components/trau/status-pill'
import { useResolvedTheme, useTheme } from '@/components/trau/theme-toggle'
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import { autoScopeTarget, loadLastRepo } from '@/lib/active-repo'
import { archiveIssue, archiveToastMessage } from '@/lib/archive'
import { invalidateRepoBoard } from '@/lib/backlog'
import { configQueryOptions } from '@/lib/config'
import { useNow } from '@/lib/elapsed'
import {
  instancesQueryOptions,
  pullCounts,
  repoHealthQueryOptions,
  syncRepo,
} from '@/lib/instances'
import { issueQueryOptions } from '@/lib/issues'
import { formatAge } from '@/lib/ledger'
import { boardPill } from '@/lib/overview'
import {
  issueActions,
  matchActions,
  paletteActions,
  updateCheckMessage,
  type IssueActionId,
  type PaletteAction,
  type PaletteActionId,
} from '@/lib/palette-actions'
import { matchesQuery } from '@/lib/palette-filter'
import {
  isPaletteShortcut,
  leavesSubmenu,
  movesHighlight,
  opensSubmenu,
} from '@/lib/palette-keys'
import {
  drain,
  enqueueFresh,
  publishQueue,
  queueQueryOptions,
  stopQueue,
  type QueueResponse,
} from '@/lib/queue'
import { loadRecents, visibleRecents, type RecentEntry } from '@/lib/recents'
import { matchRuns } from '@/lib/run-search'
import { runsQueryOptions, type Run } from '@/lib/runs'
import {
  globalSearchQueryOptions,
  issueSearchQueryOptions,
  type SearchResult,
} from '@/lib/search'
import { displayValue, matchSettings } from '@/lib/settings'
import { suggestFor, type SuggestionEntry } from '@/lib/suggestions'
import { checkForUpdates, updateQueryOptions } from '@/lib/update'

const GROUP_HEADING =
  '[&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[0.65rem] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-[0.2em] [&_[cmdk-group-heading]]:font-normal'

const NAV_ITEMS = NAV_GROUPS.flatMap((group) => group.items)

type IssueRow = SearchResult & { repo?: string }
type RunRow = Run & { repo?: string }

interface SettingRow {
  key: string
  section: string
  value: string
  repo?: string
}

// Under "All projects" the same ticket id can come from two repos, and cmdk
// selects on the value, so a cross-repo row is keyed by both.
function rowValue(kind: string, id: string, repo?: string) {
  return repo ? `${kind}:${repo}:${id}` : `${kind}:${id}`
}

function RepoChip({ repo }: { repo?: string }) {
  if (!repo) return null
  return (
    <span className="shrink-0 truncate text-[0.65rem] text-muted-foreground">
      {repo}
    </span>
  )
}

function runAge(run: Run, now: number): string {
  if (!run.updated_at) return '—'
  return formatAge(Math.max(0, now - Date.parse(run.updated_at)))
}

function recentIcon(entry: RecentEntry) {
  if (entry.kind === 'project') return GitBranch
  if (entry.kind === 'run') return ListChecks
  return NAV_ITEMS.find((item) => item.to === entry.path)?.icon ?? History
}

const toastError = (err: Error) => toast.error(err.message)

function useLoopControl(
  control: (repo: string) => Promise<QueueResponse>,
  verb: string,
) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: control,
    onSuccess: (res, repo) => {
      publishQueue(queryClient, repo, res)
      toast.success(`Loop ${verb} in ${repo}`)
    },
    onError: toastError,
  })
}

interface Runner<T> {
  pending?: boolean
  run: (target: T) => void
}

export function CommandPalette({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { repo, repos, isAll, setScope, autoScope, openSwitcher } =
    useActiveRepo()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const [query, setQuery] = useState('')
  const [debounced, setDebounced] = useState('')
  const [selected, setSelected] = useState('')
  // The submenu filters on its own query so the results keep theirs, restored on
  // the way back out.
  const [submenu, setSubmenu] = useState<IssueRow | null>(null)
  const [submenuQuery, setSubmenuQuery] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const highlightMoved = useRef(false)
  const trimmed = query.trim()

  useEffect(() => {
    const id = setTimeout(() => setDebounced(trimmed), 150)
    return () => clearTimeout(id)
  }, [trimmed])

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (!isPaletteShortcut(e)) return
      e.preventDefault()
      onOpenChange(!open)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [open, onOpenChange])

  useEffect(() => {
    if (open) return
    setQuery('')
    setSelected('')
    setSubmenu(null)
    setSubmenuQuery('')
    highlightMoved.current = false
  }, [open])

  const { data: instancesData } = useQuery(instancesQueryOptions)
  const suggestions = useMemo(
    () =>
      open
        ? suggestFor({
            pathname,
            scope: repo,
            instances: instancesData?.instances ?? [],
          })
        : [],
    [open, pathname, repo, instancesData],
  )

  const recents = useMemo(
    () =>
      open
        ? visibleRecents(loadRecents(), {
            path: pathname,
            repo,
            repos: repos.map((r) => r.name),
          })
        : [],
    [open, pathname, repo, repos],
  )

  // Under one repo the palette queries that repo's paths; under "All projects"
  // a single hub-side query fans the same three groups out over every repo.
  const scopedRepo = isAll ? '' : (repo ?? '')
  const scopedSearch = scopedRepo !== '' && trimmed !== ''
  const globalSearch = isAll && trimmed !== ''
  const searching = scopedSearch || globalSearch

  // Holding the previous hits mounted while the next query flies keeps the
  // highlight on the row the user picked instead of churning it through cmdk.
  const issues = useQuery({
    ...issueSearchQueryOptions(scopedRepo, debounced),
    placeholderData: (previous) => previous,
  })
  const global = useQuery({
    ...globalSearchQueryOptions(debounced),
    enabled: open && isAll && debounced !== '',
    placeholderData: (previous) => previous,
  })
  const globalData = global.data

  const issueRows: IssueRow[] = globalSearch
    ? (globalData?.results.issues ?? [])
    : scopedSearch
      ? (issues.data?.results ?? [])
      : []
  const issuesPending =
    searching &&
    (debounced !== trimmed ||
      (globalSearch ? global.isFetching : issues.isFetching))

  const config = useQuery({
    ...configQueryOptions(scopedRepo),
    enabled: open && scopedSearch,
  })
  const settingRows = useMemo<SettingRow[]>(() => {
    if (globalSearch) {
      return (globalData?.results.settings ?? []).map((row) => ({
        key: row.key,
        section: row.group,
        value: row.value,
        repo: row.repo,
      }))
    }
    if (!scopedSearch) return []
    return matchSettings(config.data?.keys ?? [], trimmed).map(
      ({ item, section }) => ({
        key: item.key,
        section,
        value: displayValue(item),
      }),
    )
  }, [globalSearch, globalData, scopedSearch, config.data, trimmed])

  const runs = useQuery({
    ...runsQueryOptions(scopedRepo),
    enabled: open && scopedSearch,
  })
  const runRows = useMemo<RunRow[]>(() => {
    if (globalSearch) return globalData?.results.runs ?? []
    return scopedSearch ? matchRuns(runs.data?.runs ?? [], trimmed) : []
  }, [globalSearch, globalData, scopedSearch, runs.data, trimmed])
  const now = useNow(30_000)

  // The Actions group describes the repo it would act on, which under "All
  // projects" is the one the repo handoff auto-scopes to — reading its queue and
  // health there is what keeps Start/Stop and the internal-tracker rule honest.
  const actionRepo = useMemo(
    () => (open ? (repo ?? autoScopeTarget(repos, loadLastRepo()) ?? '') : ''),
    [open, repo, repos],
  )
  const queue = useQuery({
    ...queueQueryOptions(actionRepo),
    enabled: open && actionRepo !== '',
  })
  const health = useQuery({
    ...repoHealthQueryOptions(actionRepo),
    enabled: open && actionRepo !== '',
  })
  const theme = useResolvedTheme()
  const { setMode } = useTheme()
  const actionRows = useMemo(
    () =>
      matchActions(
        paletteActions({
          repo: actionRepo,
          draining: queue.data?.draining,
          provider: health.data?.provider,
          theme,
        }),
        trimmed,
      ),
    [actionRepo, queue.data, health.data, theme, trimmed],
  )

  // cmdk only auto-selects when nothing is selected, so late-arriving issue rows
  // would leave the highlight on a static row: move it to the top hit ourselves,
  // unless the user has already moved the highlight.
  const topIssue = issueRows[0]
  const topIssueValue = topIssue
    ? rowValue('issue', topIssue.id, topIssue.repo)
    : ''
  useEffect(() => {
    if (topIssueValue && !highlightMoved.current) setSelected(topIssueValue)
  }, [topIssueValue])

  // Rows that unmount can leave cmdk's controlled value pointing at nothing,
  // which silently kills Enter: hand the highlight back to the first row.
  useEffect(() => {
    const list = listRef.current
    if (!list) return
    const values = Array.from(list.querySelectorAll('[cmdk-item]')).map((el) =>
      el.getAttribute('data-value'),
    )
    if (values.includes(selected)) return
    if (values[0]) setSelected(values[0])
  })

  const highlightedIssue = issueRows.find(
    (row) => rowValue('issue', row.id, row.repo) === selected,
  )

  const projectRows = repos.filter((r) => matchesQuery(trimmed, [r.name, r.root]))
  const showAllScope = repos.length > 1 && matchesQuery(trimmed, ['All repos'])
  const showProjects = showAllScope || projectRows.length > 0
  const navRows = NAV_ITEMS.filter((item) =>
    matchesQuery(trimmed, [item.label, item.to]),
  )

  function pickScope(scope: string) {
    setScope(scope)
    onOpenChange(false)
  }

  // A gated destination under "All repos" mirrors the sidebar: auto-scope to a
  // sensible repo and follow the link, or hand off to the pulsing switcher.
  function goTo(item: NavItem) {
    onOpenChange(false)
    if (isAll && item.requiresProject && !autoScope()) {
      openSwitcher()
      return
    }
    void navigate({ to: item.to, search: item.search })
  }

  function pickSuggestion(entry: SuggestionEntry) {
    if (entry.kind === 'page') {
      goTo(entry.item)
      return
    }
    onOpenChange(false)
    void navigate({ to: entry.path })
  }

  const rowRepo = (row: IssueRow) => row.repo ?? scopedRepo

  // The run route carries its own repo, so a cross-repo hit lands without
  // moving the active scope.
  function pickIssue(result: IssueRow) {
    onOpenChange(false)
    void navigate({
      to: '/runs/$repo/$ticket',
      params: { repo: rowRepo(result), ticket: result.id },
    })
  }

  function pickRun(run: RunRow) {
    onOpenChange(false)
    void navigate({
      to: '/runs/$repo/$ticket',
      params: { repo: run.repo ?? scopedRepo, ticket: run.ticket },
    })
  }

  // Settings is repo-scoped, so a cross-repo key has to move the scope with it.
  function pickSetting(row: SettingRow) {
    onOpenChange(false)
    if (row.repo) setScope(row.repo)
    void navigate({ to: '/settings', search: { q: row.key } })
  }

  const queryClient = useQueryClient()

  const startLoop = useLoopControl((target) => drain(target, true), 'started')
  const stopLoop = useLoopControl(stopQueue, 'stopped')

  const syncBacklog = useMutation({
    mutationFn: syncRepo,
    onSuccess: (result, target) =>
      toast.success(
        result.status === 'pulled'
          ? `${target}: ${pullCounts(result.response)}`
          : `${target} is already syncing`,
      ),
    onError: toastError,
    onSettled: (_result, _err, target) => {
      void queryClient.invalidateQueries({ queryKey: ['repo-health', target] })
      invalidateRepoBoard(queryClient, target)
    },
  })

  const addToQueue = useMutation({
    mutationFn: (row: IssueRow) => enqueueFresh(rowRepo(row), { id: row.id }),
    onSuccess: (res, row) => {
      publishQueue(queryClient, rowRepo(row), res)
      onOpenChange(false)
      toast.success(`Queued ${row.id} in ${rowRepo(row)}`)
    },
    onError: toastError,
  })

  const archiveRow = useMutation({
    mutationFn: (row: IssueRow) => archiveIssue(rowRepo(row), row.id, true),
    onSuccess: (result, row) => {
      const target = rowRepo(row)
      queryClient.setQueryData(issueQueryOptions(target, row.id).queryKey, result)
      invalidateRepoBoard(queryClient, target)
      onOpenChange(false)
      toast.success(archiveToastMessage(row.id, true, result.queue_removed))
    },
    onError: toastError,
  })

  const checkUpdates = useMutation({
    mutationFn: checkForUpdates,
    onSuccess: (status) => {
      queryClient.setQueryData(updateQueryOptions.queryKey, status)
      toast.success(updateCheckMessage(status))
      void navigate({ to: '/settings', hash: 'updates' })
    },
    onError: toastError,
  })

  // Every action id needs an entry here, so a new mutating action cannot reach
  // the list without declaring what makes it pending.
  const runners: Record<PaletteActionId, Runner<string>> = {
    'start-loop': { pending: startLoop.isPending, run: startLoop.mutate },
    'stop-loop': { pending: stopLoop.isPending, run: stopLoop.mutate },
    'sync-backlog': { pending: syncBacklog.isPending, run: syncBacklog.mutate },
    // ADR 0015 retired the Run once page: the Loop card owns picking the ticket
    // and the Run next gesture that launches it.
    'run-next': { run: () => void navigate({ to: '/loop' }) },
    'toggle-theme': { run: () => setMode(theme === 'dark' ? 'light' : 'dark') },
    'check-updates': {
      pending: checkUpdates.isPending,
      run: () => checkUpdates.mutate(),
    },
  }

  const issueRunners: Record<IssueActionId, Runner<IssueRow>> = {
    open: { run: pickIssue },
    queue: { pending: addToQueue.isPending, run: addToQueue.mutate },
    archive: { pending: archiveRow.isPending, run: archiveRow.mutate },
  }

  function exitSubmenu(row: IssueRow) {
    setSelected(rowValue('issue', row.id, row.repo))
    setSubmenu(null)
    setSubmenuQuery('')
  }

  function enterSubmenu(row: IssueRow) {
    setSubmenu(row)
    setSubmenuQuery('')
    // The hint button leaves with the results, so the input takes the keys back.
    inputRef.current?.focus()
  }

  // The dialog dismisses on an Escape it catches while the event descends the
  // document, so an open submenu claims the key one step earlier — on the window
  // — and steps back to the results instead of closing the palette.
  useEffect(() => {
    const row = submenu
    if (!row) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      exitSubmenu(row)
    }
    window.addEventListener('keydown', onKeyDown, { capture: true })
    return () =>
      window.removeEventListener('keydown', onKeyDown, { capture: true })
  }, [submenu])

  // A repo-scoped action acts on the very repo its row describes, so under "All
  // repos" the scope follows it there — or the pulsing switcher takes over when
  // there was no sensible repo to describe in the first place.
  function runAction(action: PaletteAction) {
    onOpenChange(false)
    const { run } = runners[action.id]
    if (!action.requiresProject) {
      run('')
      return
    }
    if (!actionRepo) {
      openSwitcher()
      return
    }
    if (isAll) autoScope()
    run(actionRepo)
  }

  function pickRecent(entry: RecentEntry) {
    if (entry.kind === 'project') {
      pickScope(entry.label)
      return
    }
    if (entry.kind === 'page') {
      const item = NAV_ITEMS.find((i) => i.to === entry.path)
      if (item) {
        goTo(item)
        return
      }
    }
    onOpenChange(false)
    void navigate({ to: entry.path })
  }

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      className="font-mono"
      size="lg"
      shouldFilter={false}
      value={selected}
      onValueChange={setSelected}
    >
      <CommandInput
        ref={inputRef}
        detachSearch
        placeholder={
          submenu ? 'Search actions…' : 'Search issues, projects and pages…'
        }
        leading={
          submenu && (
            <span className="flex shrink-0 items-center gap-1 rounded border border-border bg-muted/60 px-1.5 py-0.5 text-[0.65rem] text-primary">
              {submenu.id}
              <ChevronRight className="size-3 text-muted-foreground" />
            </span>
          )
        }
        value={submenu ? submenuQuery : query}
        onValueChange={submenu ? setSubmenuQuery : setQuery}
        onKeyDown={(e) => {
          if (movesHighlight(e.nativeEvent)) highlightMoved.current = true
          if (submenu) {
            if (!leavesSubmenu(e.nativeEvent, submenuQuery)) return
            // Without this the restored results query is what the browser's own
            // Backspace erases a character from, on the way back out.
            e.preventDefault()
            exitSubmenu(submenu)
            return
          }
          if (opensSubmenu(e.nativeEvent) && highlightedIssue) {
            e.preventDefault()
            enterSubmenu(highlightedIssue)
          }
        }}
      />
      <CommandList ref={listRef} className="max-h-[65vh]">
        {!issuesPending && <CommandEmpty>No results.</CommandEmpty>}
        {submenu ? (
          <CommandGroup heading="Actions" className={GROUP_HEADING}>
            {matchActions(issueActions, submenuQuery.trim()).map((action) => {
              const runner = issueRunners[action.id]
              return (
                <CommandItem
                  key={action.id}
                  value={`issue-action:${action.id}`}
                  disabled={runner.pending}
                  onSelect={() => runner.run(submenu)}
                >
                  <action.icon />
                  <span className="flex-1 truncate">{action.label}</span>
                </CommandItem>
              )
            })}
          </CommandGroup>
        ) : (
          <>
            {(issueRows.length > 0 || issuesPending) && (
              <>
                <CommandGroup heading="Issues" className={GROUP_HEADING}>
                  {issuesPending && (
                    <p className="px-2 py-1.5 text-xs text-muted-foreground">
                      Searching…
                    </p>
                  )}
                  {issueRows.map((result) => (
                    <CommandItem
                      key={rowValue('issue', result.id, result.repo)}
                      value={rowValue('issue', result.id, result.repo)}
                      className="group"
                      onSelect={() => pickIssue(result)}
                    >
                      <span className="shrink-0 text-primary">{result.id}</span>
                      <span className="min-w-0 flex-1 truncate font-sans">
                        {result.title || 'Untitled'}
                      </span>
                      {result.status && (
                        <span className="shrink-0 text-[0.65rem] text-muted-foreground">
                          {result.status}
                        </span>
                      )}
                      {result.labels.map((label) => (
                        <span
                          key={label}
                          className="shrink-0 rounded border border-border bg-muted/60 px-1.5 text-[0.6rem] text-muted-foreground"
                        >
                          {label}
                        </span>
                      ))}
                      <RepoChip repo={result.repo} />
                      <button
                        type="button"
                        aria-label={`Actions for ${result.id}`}
                        onClick={(e) => {
                          e.stopPropagation()
                          enterSubmenu(result)
                        }}
                        className="hidden shrink-0 items-center gap-1 rounded border border-border px-1.5 text-[0.6rem] text-muted-foreground hover:text-foreground group-data-[selected=true]:inline-flex"
                      >
                        Actions ⇥
                      </button>
                    </CommandItem>
                  ))}
                </CommandGroup>
                {(runRows.length > 0 ||
                  actionRows.length > 0 ||
                  showProjects ||
                  navRows.length > 0 ||
                  settingRows.length > 0) && <CommandSeparator />}
              </>
            )}
            {runRows.length > 0 && (
              <>
                <CommandGroup heading="Runs" className={GROUP_HEADING}>
                  {runRows.map((run) => {
                    const pill = boardPill(run)
                    return (
                      <CommandItem
                        key={rowValue('run', run.ticket, run.repo)}
                        value={rowValue('run', run.ticket, run.repo)}
                        onSelect={() => pickRun(run)}
                      >
                        <span className="shrink-0 text-primary">{run.ticket}</span>
                        <span className="min-w-0 flex-1 truncate font-sans">
                          {run.title ?? run.ticket}
                        </span>
                        <StatusPill state={pill.state} label={pill.label} />
                        <span className="shrink-0 text-[0.65rem] text-muted-foreground">
                          {runAge(run, now)}
                        </span>
                        <RepoChip repo={run.repo} />
                      </CommandItem>
                    )
                  })}
                </CommandGroup>
                {(actionRows.length > 0 ||
                  showProjects ||
                  navRows.length > 0 ||
                  settingRows.length > 0) && <CommandSeparator />}
              </>
            )}
            {trimmed === '' && suggestions.length > 0 && (
              <>
                <CommandGroup heading="Suggested" className={GROUP_HEADING}>
                  {suggestions.map((entry) => (
                    <CommandItem
                      key={entry.key}
                      value={`suggest:${entry.key}`}
                      onSelect={() => pickSuggestion(entry)}
                    >
                      {entry.kind === 'page' ? (
                        <>
                          <entry.item.icon />
                          <span className="flex-1 truncate">{entry.item.label}</span>
                        </>
                      ) : (
                        <>
                          {entry.kind === 'live' ? (
                            <GitBranch className="text-teal" />
                          ) : (
                            <ListChecks />
                          )}
                          <span className="flex-1 truncate">{entry.label}</span>
                          <span
                            className={
                              entry.kind === 'live'
                                ? 'text-[0.65rem] text-teal'
                                : 'text-[0.65rem] text-muted-foreground'
                            }
                          >
                            {entry.kind === 'live' ? 'live' : 'run'}
                          </span>
                        </>
                      )}
                    </CommandItem>
                  ))}
                </CommandGroup>
                <CommandSeparator />
              </>
            )}
            {trimmed === '' && recents.length > 0 && (
              <>
                <CommandGroup heading="Recent" className={GROUP_HEADING}>
                  {recents.map((entry) => {
                    const Icon = recentIcon(entry)
                    return (
                      <CommandItem
                        key={entry.key}
                        value={entry.key}
                        onSelect={() => pickRecent(entry)}
                      >
                        <Icon />
                        <span className="flex-1 truncate">{entry.label}</span>
                        {entry.sublabel && (
                          <span className="truncate text-[0.65rem] text-muted-foreground">
                            {entry.sublabel}
                          </span>
                        )}
                      </CommandItem>
                    )
                  })}
                </CommandGroup>
                <CommandSeparator />
              </>
            )}
            {actionRows.length > 0 && (
              <>
                <CommandGroup heading="Actions" className={GROUP_HEADING}>
                  {actionRows.map((action) => (
                    <CommandItem
                      key={action.id}
                      value={`action:${action.id}`}
                      disabled={runners[action.id].pending}
                      onSelect={() => runAction(action)}
                    >
                      <action.icon />
                      <span className="flex-1 truncate">{action.label}</span>
                      <ChevronRight />
                    </CommandItem>
                  ))}
                </CommandGroup>
                {(showProjects || navRows.length > 0 || settingRows.length > 0) && (
                  <CommandSeparator />
                )}
              </>
            )}
            {showProjects && (
              <>
                <CommandGroup heading="Projects" className={GROUP_HEADING}>
                  {showAllScope && (
                    <CommandItem
                      value="All repos"
                      onSelect={() => pickScope(ALL_SCOPE)}
                    >
                      <FolderGit2 />
                      <span className="flex-1 truncate">All repos</span>
                      {isAll && <Check className="text-primary" />}
                    </CommandItem>
                  )}
                  {projectRows.map((r) => (
                    <CommandItem
                      key={r.root}
                      value={r.root}
                      onSelect={() => pickScope(r.name)}
                    >
                      <GitBranch />
                      <span className="flex min-w-0 flex-1 flex-col">
                        <span className="truncate">{r.name}</span>
                        <span className="truncate text-[0.65rem] text-muted-foreground">
                          {r.root}
                        </span>
                      </span>
                      {!isAll && r.name === repo && (
                        <Check className="text-primary" />
                      )}
                    </CommandItem>
                  ))}
                </CommandGroup>
                {(navRows.length > 0 || settingRows.length > 0) && (
                  <CommandSeparator />
                )}
              </>
            )}
            {navRows.length > 0 && (
              <>
                <CommandGroup heading="Navigation" className={GROUP_HEADING}>
                  {navRows.map((item) => (
                    <CommandItem
                      key={item.to}
                      value={item.label}
                      onSelect={() => goTo(item)}
                    >
                      <item.icon />
                      <span className="flex-1 truncate">{item.label}</span>
                    </CommandItem>
                  ))}
                </CommandGroup>
                {settingRows.length > 0 && <CommandSeparator />}
              </>
            )}
            {settingRows.length > 0 && (
              <CommandGroup heading="Settings" className={GROUP_HEADING}>
                {settingRows.map((row) => (
                  <CommandItem
                    key={rowValue('setting', row.key, row.repo)}
                    value={rowValue('setting', row.key, row.repo)}
                    onSelect={() => pickSetting(row)}
                  >
                    <Settings />
                    <span className="min-w-0 flex-1 truncate">{row.key}</span>
                    <span className="shrink-0 text-[0.65rem] text-muted-foreground">
                      {row.section}
                    </span>
                    <span className="max-w-[10rem] shrink-0 truncate text-[0.65rem]">
                      {row.value}
                    </span>
                    <RepoChip repo={row.repo} />
                  </CommandItem>
                ))}
              </CommandGroup>
            )}
          </>
        )}
      </CommandList>
    </CommandDialog>
  )
}
